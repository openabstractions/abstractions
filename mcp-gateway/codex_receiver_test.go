package main

import (
	"context"
	"errors"
	watchwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"reflect"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type codexReceiverTestCLI struct{ err error }

func (c *codexReceiverTestCLI) Program() string             { return "held-selected-codex" }
func (c *codexReceiverTestCLI) Check(context.Context) error { return c.err }

type codexReceiverTestWatch func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error)

func (f codexReceiverTestWatch) MessagesWatch(ctx context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
	return f(ctx, in)
}
func codexReceiverTestBinding(t *testing.T) codexReceiverBinding {
	return codexReceiverBinding{context: "native-11111111111111111111111111111111", thread: "01a10a6d-d9f4-7c61-9e33-4fe29defa7c6", directory: t.TempDir(), cli: &codexReceiverTestCLI{}, current: func(context.Context) error { return nil }}
}

func TestCodexReceiverFixedQueueAndBindingRecheck(t *testing.T) {
	b := codexReceiverTestBinding(t)
	calls := 0
	run := func(ctx context.Context, program, directory string, args []string) (codexCLIResult, error) {
		calls++
		if program != b.cli.Program() || directory != b.directory || !reflect.DeepEqual(args, []string{"queue", "--thread", b.thread, "--message", messagesHookPointer, "--resume"}) {
			t.Fatal(program, directory, args)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("unbounded queue")
		}
		return codexCLIResult{}, nil
	}
	if err := b.dispatch(context.Background(), run); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	b.current = func(context.Context) error { return errors.New("connection ended or paused") }
	if err := b.dispatch(context.Background(), run); err == nil || calls != 1 {
		t.Fatal("lost binding dispatched", err, calls)
	}
}

func TestCodexReceiverRestartKeepsPendingMessageWakeAcrossEpoch(t *testing.T) {
	b := codexReceiverTestBinding(t)
	p := new(codexReceiverProgress)
	at := time.Now()
	queues := 0
	stop := errors.New("receiver stopped")
	for _, revision := range []string{"epoch1:1", "epoch2:1"} {
		watch := codexReceiverTestWatch(func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
			return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: revision, Wake: "0001", PendingUntilUnixMs: at.Add(time.Minute).UnixMilli()}, WatchMetadata: gateway.WatchMetadata{Context: b.context}}, nil
		})
		err := runCodexInboxReceiverProgress(context.Background(), b, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
			queues++
			return codexCLIResult{}, nil
		}, func() time.Time { return at }, func(context.Context, time.Duration) error { return stop }, p)
		if !errors.Is(err, stop) {
			t.Fatal(err)
		}
		at = at.Add(time.Minute) // the restarted receiver is outside cooldown
	}
	if queues != 1 {
		t.Fatalf("same pending message woke %d times across runtime epochs", queues)
	}
}

func TestCodexReceiverReplyAcknowledgmentLeavesNoScheduledWake(t *testing.T) {
	for _, acknowledgeDuringCooldown := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted-pointer", true: "cooldown"}[acknowledgeDuringCooldown], func(t *testing.T) {
			binding := codexReceiverTestBinding(t)
			at := time.Now()
			reply := wire.Header{MessageID: "reply-140", From: "app:peer", FromContext: "peer", To: "app:oa-codex-integration", Context: binding.context, ContextID: "thread", Kind: wire.KindReply, ExpiresUnixMs: at.Add(time.Hour).UnixMilli()}
			note := reply
			note.MessageID, note.Kind = "note-141", wire.KindNote
			note.From, note.To = reply.To, reply.From
			note.FromContext, note.Context = binding.context, reply.FromContext
			probe := new(messagesProbe)
			backend := &messagesBackend{application: "oa-codex-integration", instance: "native-instance", contextName: binding.context, native: func(context.Context) (nativeMessages, error) {
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: probe}), nil
			}}
			progress := new(codexReceiverProgress)
			if acknowledgeDuringCooldown {
				progress.lastWake = at
			}
			reads, queues, observed := 0, 0, 0
			binding.observe = func(event string) {
				if event == "observed" {
					observed++
				}
			}
			watch := codexReceiverTestWatch(func(ctx context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
				reads++
				probe.page = wire.Page{Outcome: wire.OutcomePage, Revision: "reply", Wake: "0001", ReplyUntilUnixMs: reply.ExpiresUnixMs, Headers: []wire.Header{reply}}
				switch reads {
				case 2:
					// The explicit body cursor acknowledged the reply. An
					// outgoing note then changes the header revision.
					probe.page = wire.Page{Outcome: wire.OutcomePage, Revision: "ack-and-note", Headers: []wire.Header{note}}
				case 3:
					probe.page = wire.Page{Outcome: wire.OutcomePage, Revision: "empty"}
				}
				return backend.MessagesWatch(ctx, in)
			})
			stop := errors.New("sequence complete")
			err := runCodexInboxReceiverProgress(context.Background(), binding, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
				queues++
				return codexCLIResult{}, nil
			}, func() time.Time { return at }, func(context.Context, time.Duration) error {
				at = at.Add(10 * time.Second)
				if reads == 3 {
					return stop
				}
				return nil
			}, progress)
			wantQueues := 1
			if acknowledgeDuringCooldown {
				wantQueues = 0
			}
			if !errors.Is(err, stop) || queues != wantQueues || reads != 3 || observed != 3 {
				t.Fatal("acknowledged reply scheduled another pointer", err, queues, reads, observed)
			}
		})
	}
}

func TestCodexReceiverSuspensionAtWatchAndQueueBoundaries(t *testing.T) {
	for _, phase := range []string{"watch-return", "unknown-watch", "queue-preflight", "forbidden-preflight", "accepted", "unknown-queue", "warning-queue"} {
		t.Run(phase, func(t *testing.T) {
			a := new(agentToolAdmission)
			a.confirm(time.Now().Add(time.Minute))
			p := new(statusMessagesProbe)
			b := &messagesBackend{application: "oa-codex-integration", instance: "original", contextName: "native-11111111111111111111111111111111", status: &messagesInboxStatus{admission: a}, native: func(context.Context) (nativeMessages, error) { return p, nil }}
			binding := codexReceiverTestBinding(t)
			binding.current = func(ctx context.Context) error { return codexReceivingPresence(ctx, b) }
			reads, queues, pauses := 0, 0, 0
			stop := errors.New("fixture complete")
			watch := codexReceiverTestWatch(func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
				reads++
				if reads == 1 {
					if phase == "watch-return" || phase == "unknown-watch" {
						a.setReady(false)
					}
					if phase == "unknown-watch" {
						return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomeUnknown}}, nil
					}
					if phase == "queue-preflight" {
						p.outcome = wire.OutcomeUnknown
					}
					if phase == "forbidden-preflight" {
						p.outcome = wire.OutcomeForbidden
					}
				}
				return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: "same", Wake: "0001", PendingUntilUnixMs: time.Now().Add(time.Minute).UnixMilli()}, WatchMetadata: gateway.WatchMetadata{Context: binding.context}}, nil
			})
			run := codexReceivingQueue(context.Background(), b, binding.cli, binding.thread, func(context.Context, string, string, []string) (codexCLIResult, error) {
				queues++
				if phase == "unknown-queue" {
					return codexCLIResult{}, errors.New("driver reply lost")
				}
				if phase == "warning-queue" {
					return codexCLIResult{Diagnostic: []byte("resume failed")}, nil
				}
				if phase == "accepted" {
					a.setReady(false)
				}
				return codexCLIResult{}, nil
			})
			err := runCodexInboxReceiver(context.Background(), binding, watch, run, time.Now, func(context.Context, time.Duration) error {
				pauses++
				if reads >= 2 {
					return stop
				}
				want := 0
				if phase == "accepted" {
					want = 1
				}
				if queues != want {
					t.Fatal("wrong pre-resume driver effects", queues, want)
				}
				p.outcome = wire.OutcomeOpened
				generation := a.version()
				out, e := b.status.openContext(context.Background(), p, "original", b.contextName, nil)
				if e != nil || out.Outcome != wire.OutcomeOpened || !a.confirmVersion(time.Now().Add(time.Minute), generation) {
					t.Fatal(out, e)
				}
				return nil
			})
			if phase == "unknown-queue" || phase == "warning-queue" {
				var uncertain *codexReceiverQueueError
				if !errors.As(err, &uncertain) || queues != 1 || reads != 1 || pauses != 0 {
					t.Fatal("uncertain queue retried", err, queues, reads, pauses)
				}
			} else if !errors.Is(err, stop) || queues != 1 || reads != 2 || pauses != 2 {
				// A forbidden preflight is a known pre-dispatch refusal: it
				// suspends like an unknown one, never claims queue
				// uncertainty, and resumes only after a fresh confirmation.
				t.Fatal(err, queues, reads, pauses)
			}
		})
	}
}

func TestCodexReceiverSuspensionNeverCrossesLeaseRevocationOrCLIIdentity(t *testing.T) {
	for _, mode := range []string{"expiry", "revoked", "cli-changed"} {
		t.Run(mode, func(t *testing.T) {
			a := new(agentToolAdmission)
			a.confirm(time.Now().Add(time.Minute))
			a.setReady(false)
			b := &messagesBackend{application: "oa-codex-integration", status: &messagesInboxStatus{admission: a}}
			binding := codexReceiverTestBinding(t)
			binding.current = func(ctx context.Context) error { return codexReceivingPresence(ctx, b) }
			reads, queues, pauses := 0, 0, 0
			err := runCodexInboxReceiver(context.Background(), binding, codexReceiverTestWatch(func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
				reads++
				return gateway.MessagesWatchResult{}, nil
			}), func(context.Context, string, string, []string) (codexCLIResult, error) {
				queues++
				return codexCLIResult{}, nil
			}, time.Now, func(context.Context, time.Duration) error {
				pauses++
				if pauses > 1 {
					t.Fatal("terminal binding kept waiting")
				}
				switch mode {
				case "expiry":
					a.confirm(time.Now().Add(-time.Second))
				case "revoked":
					a.retire()
				case "cli-changed":
					a.confirm(time.Now().Add(time.Minute))
					binding.cli.(*codexReceiverTestCLI).err = errors.New("held CLI changed")
				}
				return nil
			})
			if err == nil || errors.Is(err, errCodexReceivingSuspended) || reads != 0 || queues != 0 || pauses != 1 {
				t.Fatal(err, reads, queues, pauses)
			}
		})
	}
}

func TestCodexReceiverOwnContextRevisionCooldownAndNoRetry(t *testing.T) {
	for _, failure := range []string{"", "queue", "foreign", "invalid", "forbidden"} {
		t.Run(failure, func(t *testing.T) {
			b := codexReceiverTestBinding(t)
			at := time.Unix(100, 0)
			reads, queued := 0, 0
			var queuedAt []time.Time
			stop := errors.New("test finished")
			watch := codexReceiverTestWatch(func(_ context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
				reads++
				wantWait := int64(25000)
				if reads >= 3 && reads <= 6 {
					wantWait = 0
				}
				if in.WaitMs != wantWait {
					t.Fatal(in)
				}
				if reads == 1 && in.Revision != "" || reads == 2 && in.Revision != "first" || reads > 2 && in.Revision != "second" {
					t.Fatal("revision not retained", reads, in)
				}
				page := gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: "first", Wake: "0001", PendingUntilUnixMs: at.Add(time.Minute).UnixMilli()}, WatchMetadata: gateway.WatchMetadata{Context: b.context}}
				if reads > 1 {
					page.Revision, page.Wake = "second", "0002"
				}
				if failure == "foreign" {
					page.Context = "native-22222222222222222222222222222222"
				}
				if failure == "invalid" || failure == "forbidden" {
					page.Outcome, _ = watchwire.ParseOutcome(failure)
				}
				return page, nil
			})
			run := func(context.Context, string, string, []string) (codexCLIResult, error) {
				queued++
				queuedAt = append(queuedAt, at)
				if failure == "queue" {
					return codexCLIResult{Diagnostic: []byte("secret output")}, errors.New("secret cause")
				}
				return codexCLIResult{}, nil
			}
			pause := func(context.Context, time.Duration) error {
				if reads == 8 {
					return stop
				}
				at = at.Add(time.Second)
				return nil
			}
			err := runCodexInboxReceiver(context.Background(), b, watch, run, func() time.Time { return at }, pause)
			if failure == "" {
				if !errors.Is(err, stop) || queued != 2 {
					t.Fatal(err, queued)
				}
				if elapsed := queuedAt[1].Sub(queuedAt[0]); elapsed < codexWakeCooldown || elapsed > 10*time.Second {
					t.Fatal("consecutive eligible question delayed", elapsed)
				}
			} else if failure == "queue" {
				if err == nil || queued != 1 || reads != 1 || err.Error() != "Codex queue failed; outcome unknown; no automatic retry" {
					t.Fatal(err, queued, reads)
				}
			} else if err == nil || queued != 0 || reads != 1 {
				t.Fatal(err, queued, reads)
			}
		})
	}
}

func TestCodexReceiverTransientReadRecoversOriginalRevision(t *testing.T) {
	for _, outcome := range []string{"unavailable", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			b := codexReceiverTestBinding(t)
			at := time.Unix(100, 0)
			reads, queued, checked := 0, 0, 0
			b.current = func(context.Context) error { checked++; return nil }
			stop := errors.New("finished")
			watch := codexReceiverTestWatch(func(_ context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
				reads++
				if reads > 1 && in.Revision != "first" {
					t.Fatal("lost original revision", in)
				}
				if reads == 2 {
					watchOutcome, _ := watchwire.ParseOutcome(outcome)
					return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchOutcome}}, nil
				}
				revision := "first"
				if reads == 3 {
					revision = "second"
				}
				return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: revision, Wake: revision, PendingUntilUnixMs: at.Add(time.Minute).UnixMilli()}, WatchMetadata: gateway.WatchMetadata{Context: b.context}}, nil
			})
			err := runCodexInboxReceiver(context.Background(), b, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
				queued++
				return codexCLIResult{}, nil
			}, func() time.Time { return at }, func(context.Context, time.Duration) error {
				if reads == 3 {
					return stop
				}
				at = at.Add(6 * time.Second)
				return nil
			})
			if !errors.Is(err, stop) || reads != 3 || queued != 2 || checked < 6 {
				t.Fatal(err, reads, queued, checked)
			}
		})
	}
}

func TestCodexReceiverRecoveryRefusesLossAndExpires(t *testing.T) {
	for _, mode := range []string{"cancel", "authority", "expiry", "foreign", "malformed", "error"} {
		t.Run(mode, func(t *testing.T) {
			b := codexReceiverTestBinding(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			at := time.Unix(100, 0)
			reads, queued := 0, 0
			lost := false
			b.current = func(context.Context) error {
				if lost {
					return errors.New("authority revoked")
				}
				return nil
			}
			watch := codexReceiverTestWatch(func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
				reads++
				page := gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomeUnknown}}
				if mode == "foreign" {
					page.Context = "native-22222222222222222222222222222222"
				}
				if mode == "malformed" {
					page.PendingUntilUnixMs = -1
				}
				if mode == "error" {
					return page, errors.New("unclassified failure")
				}
				return page, nil
			})
			err := runCodexInboxReceiver(ctx, b, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
				queued++
				return codexCLIResult{}, nil
			}, func() time.Time { return at }, func(context.Context, time.Duration) error {
				if mode == "cancel" {
					cancel()
				}
				if mode == "authority" {
					lost = true
				}
				at = at.Add(10 * time.Second)
				return nil
			})
			if err == nil || queued != 0 || reads > 4 {
				t.Fatal(err, reads, queued)
			}
			if mode != "expiry" && reads != 1 {
				t.Fatal("retried refused observation", reads)
			}
		})
	}
}

type codexRecoveryNative struct {
	*messagesProbe
	wait       func(string, wire.View, string, string, int64) (wire.Page, error)
	preflights int
}

type codexRecoveryFrame func([]byte) ([]byte, error)

func (f codexRecoveryFrame) ExchangeFrame(frame []byte) ([]byte, error) { return f(frame) }

func TestCodexReceiverMalformedNativeReplyNeverRetries(t *testing.T) {
	binding := codexReceiverTestBinding(t)
	frames, queued := 0, 0
	good := &wire.MessagesDispatcher{Handler: &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "good", Wake: "0001", PendingUntilUnixMs: time.Now().Add(time.Minute).UnixMilli()}}}
	b := &messagesBackend{application: "oa-codex-integration", instance: "original", contextName: binding.context, native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(codexRecoveryFrame(func(frame []byte) ([]byte, error) {
			frames++
			if frames == 1 {
				return []byte("malformed native frame"), nil
			}
			return good.ExchangeFrame(frame)
		})), nil
	}}
	err := runCodexInboxReceiver(context.Background(), binding, b, func(context.Context, string, string, []string) (codexCLIResult, error) {
		queued++
		return codexCLIResult{}, nil
	}, time.Now, func(context.Context, time.Duration) error { return nil })
	if err == nil || frames != 1 || queued != 0 {
		t.Fatal(err, frames, queued)
	}
}

func (p *codexRecoveryNative) Wait(_, contextName string, view wire.View, after, revision string, waitMs int64) (wire.Page, error) {
	return p.wait(contextName, view, after, revision, waitMs)
}
func (p *codexRecoveryNative) InboxOpen(_, _, _ string) (wire.InboxChange, error) {
	p.preflights++
	return wire.InboxChange{Outcome: wire.OutcomeOpened}, nil
}

func TestCodexReceiverNativeWatchTransportRecovery(t *testing.T) {
	binding := codexReceiverTestBinding(t)
	at := time.Unix(100, 0)
	reads, queued := 0, 0
	admission := &agentToolAdmission{}
	admission.confirm(time.Now().Add(time.Minute))
	p := &codexRecoveryNative{messagesProbe: &messagesProbe{}}
	p.wait = func(contextName string, view wire.View, after, revision string, wait int64) (wire.Page, error) {
		reads++
		if contextName != binding.context || view != wire.ViewHeaders || after != "" {
			t.Fatal("wrong header scope", contextName, view, after)
		}
		if reads > 1 && revision != "first" {
			t.Fatal("lost revision", revision)
		}
		page := wire.Page{Outcome: wire.OutcomePage, Revision: "first"}
		if reads == 3 {
			if wait != 0 {
				t.Fatal("recovery long poll", wait)
			}
			page.Revision, page.Wake = "second", "0002"
			page.PendingUntilUnixMs = at.Add(time.Minute).UnixMilli()
		}
		return page, nil
	}
	b := &messagesBackend{application: "oa-codex-integration", instance: "original", contextName: binding.context, status: &messagesInboxStatus{admission: admission}, native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(codexRecoveryFrame(func(frame []byte) ([]byte, error) {
			if reads == 1 {
				reads++
				return nil, errors.New("inert transport lost response")
			}
			return (&wire.MessagesDispatcher{Handler: p}).ExchangeFrame(frame)
		})), nil
	}}
	binding.current = func(ctx context.Context) error { return codexReceivingPresence(ctx, b) }
	stop := errors.New("finished")
	err := runCodexInboxReceiver(context.Background(), binding, b, func(ctx context.Context, _, _ string, _ []string) (codexCLIResult, error) {
		call, done, ok := b.BeginNativeTool(ctx, "oa_messages_watch")
		defer done()
		if !ok || call.Err() != nil {
			return codexCLIResult{}, errors.New("admission failed")
		}
		queued++
		return codexCLIResult{}, nil
	}, func() time.Time { return at }, func(context.Context, time.Duration) error {
		if reads == 3 {
			return stop
		}
		at = at.Add(time.Second)
		return nil
	})
	if !errors.Is(err, stop) || reads != 3 || queued != 1 || p.preflights != 1 {
		t.Fatal(err, reads, queued, p.preflights)
	}
}

func TestCodexReceiverRecoveryRejectsLatePageAndSuspendedAdmission(t *testing.T) {
	for _, mode := range []string{"late", "suspended"} {
		t.Run(mode, func(t *testing.T) {
			binding := codexReceiverTestBinding(t)
			a := &agentToolAdmission{}
			a.confirm(time.Now().Add(time.Minute))
			b := &messagesBackend{application: "oa-codex-integration", status: &messagesInboxStatus{admission: a}}
			binding.current = func(ctx context.Context) error { return codexReceivingPresence(ctx, b) }
			at := time.Unix(100, 0)
			reads, queued, pauses := 0, 0, 0
			watch := codexReceiverTestWatch(func(ctx context.Context, _ gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
				reads++
				if reads == 1 {
					return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomeUnknown}}, nil
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > codexObservationRecovery {
					t.Fatal("unbounded recovery read")
				}
				at = at.Add(codexObservationRecovery)
				return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: "late", Wake: "0001", PendingUntilUnixMs: at.Add(time.Minute).UnixMilli()}, WatchMetadata: gateway.WatchMetadata{Context: binding.context}}, nil
			})
			err := runCodexInboxReceiver(context.Background(), binding, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
				queued++
				return codexCLIResult{}, nil
			}, func() time.Time { return at }, func(context.Context, time.Duration) error {
				pauses++
				if mode == "suspended" {
					if pauses == 1 {
						a.setReady(false)
					} else {
						a.confirm(time.Now().Add(-time.Second))
					}
				}
				return nil
			})
			want := 2
			if mode == "suspended" {
				want = 1
			}
			if err == nil || queued != 0 || reads != want {
				t.Fatal(err, reads, queued)
			}
		})
	}
}

func TestCodexReceiverCancellationJoinsSingleDispatch(t *testing.T) {
	b := codexReceiverTestBinding(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, done := make(chan struct{}), make(chan error, 1)
	watch := codexReceiverTestWatch(func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
		return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: "one", Wake: "0001", PendingUntilUnixMs: time.Now().Add(time.Minute).UnixMilli()}, WatchMetadata: gateway.WatchMetadata{Context: b.context}}, nil
	})
	go func() {
		done <- runCodexInboxReceiver(ctx, b, watch, func(call context.Context, _ string, _ string, _ []string) (codexCLIResult, error) {
			close(entered)
			<-call.Done()
			return codexCLIResult{}, call.Err()
		}, time.Now, func(ctx context.Context, _ time.Duration) error { return ctx.Err() })
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled dispatch succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("receiver did not join")
	}
}
