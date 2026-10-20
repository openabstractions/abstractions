//go:build windows

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	watch "github.com/openabstractions/abstraction-watch/go"
)

// A probe with no Deadline establishes which edits signal the store gate
// independently of the production Wait's periodic fallback.
func mailboxGateSignal(t *testing.T, f *messagesObserveRun) (publish func(), done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.run.ctx, 5*time.Second)
	t.Cleanup(cancel)
	read := make(chan struct{})
	published := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		first := true
		outcome, err := f.store.gate.Wait(ctx, 30*time.Second, watch.Probe{
			Read: func() (bool, error) {
				if first {
					first = false
					close(read)
					return false, nil
				}
				select {
				case <-published:
					return true, nil
				default:
					return false, nil
				}
			},
			Recheck: func() error { return nil },
		})
		if outcome != watch.Answered && err == nil {
			err = errors.New("mailbox gate did not signal")
		}
		result <- err
	}()
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal("mailbox gate did not arm")
	}
	return func() { close(published) }, result
}

func mailboxTestNote(f *messagesObserveRun, name string) msg.SendResult {
	actor := messageActor{
		Account: f.otherSubject.Account, Program: f.otherProgram,
		ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source",
	}
	return f.store.accept(f.run.ctx, actor, f.selfProgram, msg.SendRequest{
		To: "app:native-agent", Context: name, Kind: msg.KindNote,
		Body: "gate test", IdempotencyKey: name, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(),
	}, false, "")
}

func TestMessagesWaitCommitAndFallback(t *testing.T) {
	const fallbackWait = 5 * time.Second
	for _, fallback := range []bool{false, true} {
		name := "commit"
		if fallback {
			name = "fallback"
		}
		t.Run(name, func(t *testing.T) {
			f := messagesObserveFixture(t)
			w := mailboxBaselineWaits(t, f, 1, msg.ViewBody)[0]
			w.snapshot(t)
			started := time.Now()
			var result <-chan mailboxBaselineResult
			if fallback {
				result = w.startWithWait(f.run.ctx, fallbackWait)
			} else {
				result = w.start(f.run.ctx)
			}
			mailboxBaselineAdmitted(t, f, 1)
			want := msg.OutcomeSent
			var signal <-chan error
			if fallback {
				want = msg.OutcomeUnknown
				f.store.boundary = func(point string) error {
					if point == "index_published" {
						return errors.New("interrupt notification after durable commit")
					}
					return nil
				}
			} else {
				var publish func()
				publish, signal = mailboxGateSignal(t, f)
				publish()
			}
			if sent := mailboxTestNote(f, w.name); sent.Outcome != want {
				t.Fatal(sent)
			}
			if signal != nil {
				if err := <-signal; err != nil {
					t.Fatal(err)
				}
			}
			var got mailboxBaselineResult
			if fallback {
				select {
				case got = <-result:
					if elapsed := got.at.Sub(started); elapsed < fallbackWait {
						t.Fatalf("missed commit signal returned after %v, before the final bounded read", elapsed)
					}
					t.Logf("missed commit signal recovered after %v", got.at.Sub(started))
				case <-time.After(time.Until(started.Add(fallbackWait + 5*time.Second))):
					t.Fatal("durable message did not appear on the final bounded read")
				}
			} else {
				got = mailboxBaselineReceive(t, result)
			}
			page := got.page
			if got.err != nil || page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Messages[0].Body != "gate test" {
				t.Fatal(page, got.err)
			}
		})
	}
}

func TestMessagesWaitSignalsRightsAndInboxChanges(t *testing.T) {
	f := messagesObserveFixture(t)
	w := mailboxBaselineWaits(t, f, 1, msg.ViewBody)[0]
	t.Cleanup(f.reader.host.followMessageChanges(f.run.ctx))
	publish, signal := mailboxGateSignal(t, f)
	publish()
	if err := f.reader.host.messageRights.policy.Set(f.otherSubject, ActionApplicationMessage, "app:native-agent", false); err != nil {
		t.Fatal(err)
	}
	if err := <-signal; err != nil {
		t.Fatal(err)
	}

	w.snapshot(t)
	result := w.start(f.run.ctx)
	mailboxBaselineAdmitted(t, f, 1)
	publish, signal = mailboxGateSignal(t, f)
	publish()
	if changed := f.run.d.withdrawInSession(f.run.ctx, f.self, nativeBaselineSession(t, f), "native-agent", w.instance); changed.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(changed)
	}
	if err := <-signal; err != nil {
		t.Fatal(err)
	}
	page := mailboxBaselineReceive(t, result).page
	if page.Outcome != msg.OutcomeForbidden || len(page.Messages) != 0 {
		t.Fatal(page)
	}
}

func nativeBaselineSession(t *testing.T, f *messagesObserveRun) string {
	t.Helper()
	pin, err := f.reader.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	_, _, session, _, err := nativeInboxPeer(pin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return session
}
