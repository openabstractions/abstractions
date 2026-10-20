package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type restartedHeadersProbe struct {
	*messagesProbe
	revisions []string
}

func (p *restartedHeadersProbe) Wait(i, c string, v wire.View, a, r string, wait int64) (wire.Page, error) {
	p.revisions = append(p.revisions, r)
	if r != "" {
		return wire.Page{Outcome: wire.OutcomeGap}, nil
	}
	return p.messagesProbe.Wait(i, c, v, a, r, wait)
}

func TestMessagesWatchRestartsStaleRevisionInExactInboxAcrossHosts(t *testing.T) {
	for _, application := range []string{"oa-claude-integration", "oa-codex-integration"} {
		t.Run(application, func(t *testing.T) {
			p := &restartedHeadersProbe{messagesProbe: &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "fresh", Wake: "0001"}}}
			b := &messagesBackend{application: application, instance: "own", contextName: "chat-" + strings.Repeat("1", 48), native: func(context.Context) (nativeMessages, error) {
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
			}}
			out, err := b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{Revision: "old-owner", WaitMs: 25000})
			if err != nil || out.Outcome.String() != "page" || out.Revision != "fresh" || out.Context != b.contextName || out.Wake != "0001" {
				t.Fatal(out, err)
			}
			if strings.Join(p.revisions, ",") != "old-owner," || p.contextName != b.contextName || p.instance != "" || p.after != "" || p.view != wire.ViewHeaders {
				t.Fatal(p)
			}
		})
	}
}

func TestMessagesWatchForwardsAuthoritativeSummaryPresence(t *testing.T) {
	p := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "summary", Unread: new(int64(50)), PendingQuestions: new(int64(0)), OldestUnreadUnixMs: new(int64(1000)), NewestUnreadUnixMs: new(int64(4000))}}
	b := &messagesBackend{application: "oa-claude-integration", instance: "own", contextName: "chat-" + strings.Repeat("1", 48), native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}}
	out, err := b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
	if err != nil || out.Unread == nil || *out.Unread != 50 || out.PendingQuestions == nil || *out.PendingQuestions != 0 || out.OldestUnreadUnixMs == nil || *out.OldestUnreadUnixMs != 1000 || out.NewestUnreadUnixMs == nil || *out.NewestUnreadUnixMs != 4000 {
		t.Fatal(out, err)
	}
	p.page.Unread, p.page.PendingQuestions, p.page.OldestUnreadUnixMs, p.page.NewestUnreadUnixMs = nil, nil, nil, nil
	out, err = b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
	if err != nil || out.Unread != nil || out.PendingQuestions != nil || out.OldestUnreadUnixMs != nil || out.NewestUnreadUnixMs != nil {
		t.Fatal("old helper absence invented a summary", out, err)
	}
}

func TestMessagesNativeWatchRetainsOwnHeadersAndNoBody(t *testing.T) {
	now := time.Now().UnixMilli()
	submitted := wire.TaskStateSubmitted
	p := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "headers-r", PendingUntilUnixMs: now + 60000, Headers: []wire.Header{{MessageID: "m", From: "app:peer", FromContext: "other", To: "app:oa-claude-integration", Context: "native-11111111111111111111111111111111", Kind: wire.KindQuestion, Subject: "peer body must never enter projection", ContextID: "thread", ExpiresUnixMs: now + 120000, AnswerByUnixMs: now + 60000, TaskState: &submitted}}}}
	b := &messagesBackend{application: "oa-claude-integration", instance: "native-instance", contextName: p.page.Headers[0].Context, native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}}
	out, err := b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{Revision: "previous", WaitMs: 0})
	if err != nil || out.Outcome.String() != "page" || out.Context != b.contextName || out.Revision != "headers-r" || out.PendingUntilUnixMs != now+60000 {
		t.Fatalf("%+v %v", out, err)
	}
	if p.calls != 1 || p.instance != "" || p.contextName != b.contextName || p.view != wire.ViewHeaders || p.after != "" || p.revision != "previous" {
		t.Fatalf("headers arguments %+v", p)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "peer") || strings.Contains(string(raw), "thread") {
		t.Fatal("peer data exposed", string(raw))
	}
	p.page.Headers[0].Kind = wire.KindNote
	p.page.PendingUntilUnixMs = 0
	out, _ = b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
	if out.PendingUntilUnixMs != 0 {
		t.Fatal("note woke")
	}
	p.page.Headers[0].Kind = wire.KindQuestion
	p.page.Headers[0].Context = "other"
	out, _ = b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
	if out.PendingUntilUnixMs != 0 {
		t.Fatal("other context woke")
	}
	// The native owner can report an eligible reply outside the bounded rows.
	p.page.Headers = nil
	p.page.ReplyUntilUnixMs = now + 120000
	out, _ = b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
	if out.PendingUntilUnixMs != 0 || out.ReplyUntilUnixMs != p.page.ReplyUntilUnixMs {
		t.Fatal("native hint not projected", out)
	}
	p.page.Headers = []wire.Header{{MessageID: "m"}}
	p.page.Messages = []wire.Message{{Header: p.page.Headers[0], IdempotencyKey: "key", Body: "body"}}
	out, _ = b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
	if out.Outcome.String() != "invalid" || out.Context != "" {
		t.Fatal("malformed headers page admitted", out)
	}
}

func TestMessagesNativeHintsReplyCompletionAndAcknowledgmentAcrossHosts(t *testing.T) {
	for _, application := range []string{"oa-claude-integration", "oa-codex-integration"} {
		t.Run(application, func(t *testing.T) {
			binding := codexReceiverTestBinding(t)
			at := time.Now()
			completed := wire.TaskStateCompleted
			reply := wire.Header{MessageID: "reply-140", From: "app:peer", FromContext: "source", ContextID: "thread", Subject: "reply", To: "app:" + application, Context: binding.context, Kind: wire.KindReply, ExpiresUnixMs: at.Add(time.Hour).UnixMilli()}
			terminal := reply
			terminal.MessageID, terminal.Kind, terminal.TaskState = "completed-question-139", wire.KindQuestion, &completed
			submitted := wire.TaskStateSubmitted
			outgoing := reply
			outgoing.MessageID, outgoing.Kind, outgoing.TaskState, outgoing.To, outgoing.AnswerByUnixMs = "own-outgoing-question-158", wire.KindQuestion, &submitted, "app:peer", reply.ExpiresUnixMs
			note := reply
			note.MessageID, note.Kind, note.To = "outgoing-note-141", wire.KindNote, "app:peer"
			probe := new(messagesProbe)
			native := func(context.Context) (nativeMessages, error) {
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: probe}), nil
			}
			backend := &messagesBackend{application: application, instance: "native-instance", contextName: binding.context, native: native}
			pages := []wire.Page{
				{Outcome: wire.OutcomePage, Revision: "reply-completes-question", Wake: "0001", ReplyUntilUnixMs: reply.ExpiresUnixMs, Headers: []wire.Header{reply, terminal}},
				{Outcome: wire.OutcomePage, Revision: "completed-question-event", Headers: []wire.Header{terminal}},
				{Outcome: wire.OutcomePage, Revision: "acknowledged-empty"},
				{Outcome: wire.OutcomePage, Revision: "own-outgoing-question", Headers: []wire.Header{outgoing}},
				{Outcome: wire.OutcomePage, Revision: "outgoing-note", Headers: []wire.Header{terminal, note}},
				{Outcome: wire.OutcomePage, Revision: "outgoing-note", Headers: []wire.Header{terminal, note}},
			}
			for n, page := range pages {
				probe.page = page
				out, err := backend.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
				if err != nil || out.Outcome.String() != "page" || out.PendingUntilUnixMs != page.PendingUntilUnixMs || out.ReplyUntilUnixMs != page.ReplyUntilUnixMs || out.Revision != page.Revision {
					t.Fatal("adapter changed native eligibility", n, out, err)
				}
				if out.Wake != page.Wake {
					t.Fatal("adapter changed authoritative wake", n, out)
				}
				pointer, err := waitMessagesHook(context.Background(), native, application, binding.context, "", 0, func() time.Time { return at })
				if err != nil || (pointer != "") != (n == 0) {
					t.Fatal("hook inferred eligibility from event headers", n, pointer, err)
				}
			}
			if application != "oa-codex-integration" {
				return // The Claude host submission path is exercised by register.test.mjs.
			}
			reads, queues := 0, 0
			watch := codexReceiverTestWatch(func(ctx context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
				probe.page = pages[reads]
				reads++
				return backend.MessagesWatch(ctx, in)
			})
			stop := errors.New("corpus complete")
			err := runCodexInboxReceiver(context.Background(), binding, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
				queues++ // Host acceptance can outlive the reply being acknowledged.
				return codexCLIResult{}, nil
			}, func() time.Time { return at }, func(context.Context, time.Duration) error {
				at = at.Add(time.Minute)
				if reads == len(pages) {
					return stop
				}
				return nil
			})
			if !errors.Is(err, stop) || queues != 1 || reads != len(pages) {
				t.Fatal("acknowledgment or terminal/outgoing revision submitted another pointer", err, queues, reads)
			}
		})
	}
}

func TestCodexNativeWakeOutsideBoundedHeadersIgnoresRevisionChanges(t *testing.T) {
	binding := codexReceiverTestBinding(t)
	at := time.Now()
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "eligible-outside-rows", Wake: "0001", ReplyUntilUnixMs: at.Add(time.Hour).UnixMilli()}}
	for n := 0; n < 50; n++ {
		probe.page.Headers = append(probe.page.Headers, wire.Header{MessageID: strings.Repeat("n", n+1), From: "app:oa-codex-integration", FromContext: binding.context, To: "app:peer", Context: "source", ContextID: "thread", Subject: "outgoing note", Kind: wire.KindNote, ExpiresUnixMs: probe.page.ReplyUntilUnixMs})
	}
	backend := &messagesBackend{application: "oa-codex-integration", instance: "native-instance", contextName: binding.context, native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: probe}), nil
	}}
	reads, queues := 0, 0
	watch := codexReceiverTestWatch(func(ctx context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
		reads++
		if reads == 3 {
			// Aggregate hints cannot identify whether the unseen reply changed.
			probe.page.Revision = "unrelated-outgoing-revision"
		}
		if reads == 4 {
			probe.page.ReplyUntilUnixMs = 0
		}
		out, err := backend.MessagesWatch(ctx, in)
		if out.Wake != "0001" {
			t.Fatal("authoritative wake lost outside bounded projection", out)
		}
		return out, err
	})
	stop := errors.New("bounded corpus complete")
	err := runCodexInboxReceiver(context.Background(), binding, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
		queues++
		return codexCLIResult{}, nil
	}, func() time.Time { return at }, func(context.Context, time.Duration) error {
		if reads == 2 && queues != 1 {
			t.Fatal("unchanged revision repeated or eligible hint was suppressed", queues)
		}
		at = at.Add(time.Minute)
		if reads == 4 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || queues != 1 {
		t.Fatal("unrelated revision repeated authoritative wake", err, queues)
	}
}

// Notes and terminal events can change a header revision while one question
// remains pending. That question gets one wake through completion and ack.
func TestMessagesNativeWatchOneQuestionIgnoresUnrelatedRevisionChurn(t *testing.T) {
	binding := codexReceiverTestBinding(t)
	at := time.Now()
	submitted, completed := wire.TaskStateSubmitted, wire.TaskStateCompleted
	question := wire.Header{MessageID: "question-91", From: "app:peer", FromContext: "other", To: "app:oa-codex-integration", Context: binding.context, ContextID: "thread", Kind: wire.KindQuestion, TaskState: &submitted, ExpiresUnixMs: at.Add(time.Hour).UnixMilli(), AnswerByUnixMs: at.Add(time.Hour).UnixMilli()}
	note := question
	note.MessageID, note.Kind, note.TaskState = "unrelated-note", wire.KindNote, nil
	terminal := question
	terminal.MessageID, terminal.TaskState = "unrelated-completed-question", &completed
	probe := new(messagesProbe)
	backend := &messagesBackend{application: "oa-codex-integration", instance: "native-instance", contextName: binding.context, native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: probe}), nil
	}}
	reads, queues := 0, 0
	watch := codexReceiverTestWatch(func(ctx context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
		reads++
		probe.page = wire.Page{Outcome: wire.OutcomePage, Revision: strings.Repeat("r", reads), Wake: "0001", Headers: []wire.Header{question}, PendingUntilUnixMs: question.AnswerByUnixMs}
		switch reads {
		case 2, 4:
			probe.page.Headers = append(probe.page.Headers, note)
		case 3:
			probe.page.Headers = append(probe.page.Headers, terminal)
		case 5:
			probe.page.Headers[0].TaskState = &completed
			probe.page.PendingUntilUnixMs = 0
			probe.page.Wake = ""
		case 6, 7:
			probe.page.Headers, probe.page.PendingUntilUnixMs = nil, 0 // acknowledged, then unrelated revision
			probe.page.Wake = ""
		}
		out, err := backend.MessagesWatch(ctx, in)
		if err != nil || out.Outcome.String() != "page" {
			t.Fatalf("fixture watch failed: %+v %v", out, err)
		}
		return out, err
	})
	stop := errors.New("sequence complete")
	err := runCodexInboxReceiver(context.Background(), binding, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
		queues++
		return codexCLIResult{}, nil
	}, func() time.Time { return at }, func(context.Context, time.Duration) error {
		at = at.Add(10 * time.Second) // every revision is outside the wake cooldown
		if reads == 7 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || reads != 7 || queues != 1 {
		t.Fatal("unrelated header changes re-woke the question", err, reads, queues)
	}
}

func TestMessagesNativeWatchSuspensionCancelsReadAndBlocksNewReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a := new(agentToolAdmission)
	a.confirm(time.Now().Add(time.Minute))
	entered := make(chan struct{})
	calls := 0
	b := &messagesBackend{application: "oa-codex-integration", instance: "original", contextName: "native-11111111111111111111111111111111", status: &messagesInboxStatus{admission: a}, native: func(call context.Context) (nativeMessages, error) {
		calls++
		return &codexConcurrentNativeProbe{messagesProbe: &messagesProbe{}, ctx: call, entered: entered}, nil
	}}
	done := make(chan gateway.MessagesWatchResult, 1)
	go func() { out, _ := b.MessagesWatch(ctx, gateway.MessagesWatchInput{WaitMs: 25000}); done <- out }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("read did not start")
	}
	a.setReady(false)
	select {
	case out := <-done:
		if out.Outcome.String() != "unavailable" {
			t.Fatal("owned suspension did not preserve read recovery", out)
		}
	case <-ctx.Done():
		t.Fatal("suspension did not cancel active native Watch")
	}
	out, err := b.MessagesWatch(ctx, gateway.MessagesWatchInput{})
	if err != nil || out.Outcome.String() != "unavailable" || calls != 1 {
		t.Fatal("suspended gate started a new read", out, err, calls)
	}
}

func TestMessagesNativeWatchNestedAdmissionRetainsActualCallerCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(map[bool]string{false: "nested-owned", true: "caller-cancel-after-suspension"}[cancelCaller], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			a := new(agentToolAdmission)
			a.confirm(time.Now().Add(30 * time.Second))
			outer, finish, ok := a.begin(ctx)
			if !ok {
				t.Fatal("outer admission refused")
			}
			defer finish()
			entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var reads atomic.Int32
			b := &messagesBackend{application: "oa-claude-integration", instance: "original", contextName: "native-11111111111111111111111111111111", status: &messagesInboxStatus{admission: a}, native: func(call context.Context) (nativeMessages, error) {
				return &codexSuspensionWatchPeer{statusMessagesProbe: new(statusMessagesProbe), ctx: call, reads: &reads, entered: entered, cancelled: cancelled, release: release}, nil
			}}
			done := make(chan gateway.MessagesWatchResult, 1)
			go func() { out, _ := b.MessagesWatch(outer, gateway.MessagesWatchInput{WaitMs: 25000}); done <- out }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			a.setReady(false)
			select {
			case <-cancelled:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			a.confirm(time.Now().Add(30 * time.Second)) // Restore before the cancelled read returns.
			if cancelCaller {
				cancel()
			}
			close(release)
			select {
			case out := <-done:
				want := "unavailable"
				if cancelCaller {
					want = "invalid"
				}
				if out.Outcome.String() != want {
					t.Fatal("nested admission lost caller/suspension distinction", out, want)
				}
			case <-time.After(time.Second):
				t.Fatal("read did not join")
			}
		})
	}
}

func TestMessagesNativeWatchReservesOnlyCurrentOwnedWakeForBothHosts(t *testing.T) {
	for _, application := range []string{"oa-codex-integration", "oa-claude-integration"} {
		t.Run(application, func(t *testing.T) {
			p := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Wake: "0002"}}
			calls := 0
			ticket := strings.Repeat("a", 64)
			b := &messagesBackend{application: application, instance: "own", contextName: "chat-" + strings.Repeat("1", 48), native: func(context.Context) (nativeMessages, error) { return p, nil }}
			b.wakeReserve = func(_ context.Context, wake, previous string) (string, error) {
				calls++
				if wake != "0002" || previous != "" && previous != ticket {
					t.Fatal(wake, previous)
				}
				return ticket, nil
			}
			for _, stale := range []string{"", "0001", "0003"} {
				out, err := b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{ReserveWake: stale})
				if err != nil || out.Outcome.String() != "page" || out.WakeTicket != "" || calls != 0 {
					t.Fatal(out, err, calls)
				}
			}
			for _, previous := range []string{"", ticket} {
				out, err := b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{ReserveWake: "0002", WakeTicket: previous})
				if err != nil || out.WakeTicket != ticket || out.Context != b.contextName {
					t.Fatal(out, err)
				}
			}
			p.page.Outcome = wire.OutcomeForbidden
			out, err := b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{ReserveWake: "0002"})
			if err != nil || out.Outcome.String() != "forbidden" || calls != 2 || out.WakeTicket != "" {
				t.Fatal(out, err, calls)
			}
		})
	}
}

func TestMessagesNativeWatchBoundsAndRefusals(t *testing.T) {
	p := &messagesProbe{page: wire.Page{Outcome: wire.OutcomeForbidden}}
	b := &messagesBackend{application: "own", instance: "i", contextName: "native-11111111111111111111111111111111", native: func(context.Context) (nativeMessages, error) { return p, nil }}
	for _, in := range []gateway.MessagesWatchInput{{WaitMs: -1}, {WaitMs: 25001}, {Revision: strings.Repeat("a", 257)}, {Revision: strings.Repeat("a", 129)}, {Revision: strings.Repeat("é", 65)}} {
		out, _ := b.MessagesWatch(context.Background(), in)
		if out.Outcome.String() != "invalid" || p.calls != 0 {
			t.Fatal("invalid dispatched", out)
		}
	}
	out, _ := b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{WaitMs: 25000})
	if out.Outcome.String() != "forbidden" || out.Context != "" || out.PendingUntilUnixMs != 0 {
		t.Fatal(out)
	}
	p.err = errors.New("reply lost")
	out, _ = b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
	if out.Outcome.String() != "unknown" {
		t.Fatal(out)
	}
	b.native = func(context.Context) (nativeMessages, error) { return nil, errors.New("no resolution") }
	out, _ = b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
	if out.Outcome.String() != "unavailable" {
		t.Fatal(out)
	}
}

func TestMessagesNativeWatchTypedFailuresRefuseBothConsumers(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"transport", errors.New("lost response"), ""},
		{"deadline", context.DeadlineExceeded, ""},
		{"cancel", context.Canceled, "invalid"},
		{"trust", listen.ErrServerUntrusted, "invalid"},
		{"wrapped-trust", &client.ResolutionError{Status: client.RuntimeUnavailable, Err: listen.ErrServerUntrusted}, "invalid"},
		{"runtime", &client.ResolutionError{Status: client.RuntimeUnavailable}, ""},
		{"upgrade", &client.ResolutionError{Status: client.UpgradeInProgress}, ""},
		{"unavailable", &client.ResolutionError{Status: client.ResolutionErrorStatus(facade.ResolutionStatusUnavailable.String())}, ""},
		{"forbidden", &client.ResolutionError{Status: client.ResolutionErrorStatus(facade.ResolutionStatusForbidden.String())}, "forbidden"},
		{"invalid-resolution", &client.ResolutionError{Status: client.InvalidResolution}, "invalid"},
		{"unknown-resolution", &client.ResolutionError{Status: "future_status"}, "invalid"},
		{"wire-refusal", &wire.Refusal{Word: "malformed"}, "invalid"},
		{"wire-dispatch", wire.DispatchError("mismatched_response"), "invalid"},
		{"wire-service", &wire.ServiceError{Code: wire.ServiceErrorCodeInvalidResult}, "invalid"},
		{"resolver-refusal", &facade.Refusal{Word: "bad_enum"}, "invalid"},
		{"resolver-dispatch", facade.DispatchError("mismatched_response"), "invalid"},
		{"resolver-service", &facade.ServiceError{Code: facade.ServiceErrorCodeInvalidResult}, "invalid"},
	} {
		for _, factory := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/wait", true: "/factory"}[factory], func(t *testing.T) {
				p := &messagesProbe{err: tc.err}
				b := &messagesBackend{application: "own", instance: "original", contextName: "native-11111111111111111111111111111111", native: func(context.Context) (nativeMessages, error) {
					if factory {
						return nil, tc.err
					}
					return p, nil
				}}
				out, err := b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
				want := tc.want
				if want == "" {
					want = "unknown"
					if factory {
						want = "unavailable"
					}
				}
				if err != nil || out.Outcome.String() != want || out.Context != "" || out.Revision != "" || out.PendingUntilUnixMs != 0 || out.ReplyUntilUnixMs != 0 {
					t.Fatal(out, err)
				}
			})
		}
	}
}
