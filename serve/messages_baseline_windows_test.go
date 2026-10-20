//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"golang.org/x/sys/windows"
)

type mailboxBaselineWait struct {
	receiver *messagesReceiver
	instance string
	name     string
	revision string
	view     msg.View
}

type mailboxBaselineResult struct {
	page msg.Page
	at   time.Time
	err  error
}

func (w mailboxBaselineWait) start(ctx context.Context) <-chan mailboxBaselineResult {
	return w.startWithWait(ctx, 30*time.Second)
}

func (w mailboxBaselineWait) startWithWait(ctx context.Context, wait time.Duration) <-chan mailboxBaselineResult {
	done := make(chan mailboxBaselineResult, 1)
	receiver := *w.receiver
	receiver.ctx = ctx
	go func() {
		page, err := receiver.Wait(w.instance, w.name, w.view, "", w.revision, wait.Milliseconds())
		done <- mailboxBaselineResult{page: page, at: time.Now(), err: err}
	}()
	return done
}

func mailboxBaselineWaits(t *testing.T, f *messagesObserveRun, n int, view msg.View) []mailboxBaselineWait {
	t.Helper()
	waits := make([]mailboxBaselineWait, n)
	for i := range waits {
		waits[i] = mailboxBaselineWait{receiver: f.reader, view: view, name: fmt.Sprintf("baseline-%02d", i)}
	}
	pin, err := f.reader.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	contexts := make([]wire.ApplicationContext, len(waits))
	for i, w := range waits {
		contexts[i].Name = w.name
	}
	presence := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{
		Application: "native-agent", LeaseMs: 60000, Contexts: contexts,
	})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	for i := range waits {
		w := &waits[i]
		if w.view == msg.ViewBody {
			w.instance = presence.Instance
			opened, err := f.reader.InboxOpen(w.instance, w.name, "ready")
			if err != nil || opened.Outcome != msg.OutcomeOpened {
				t.Fatal(opened, err)
			}
		}
	}
	return waits
}

func (w *mailboxBaselineWait) snapshot(t *testing.T) {
	t.Helper()
	page, err := w.receiver.Wait(w.instance, w.name, w.view, "", "", 0)
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Headers) != 0 || len(page.Messages) != 0 {
		t.Fatal("expected an empty snapshot", page, err)
	}
	w.revision = page.Revision
}

func mailboxBaselineAdmitted(t *testing.T, f *messagesObserveRun, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		f.reader.host.messageWatchMu.Lock()
		active := len(f.reader.host.messageWatches)
		f.reader.host.messageWatchMu.Unlock()
		if active == n && f.store.gate.Waiting() == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expected %d live admitted watchers, gate has %d", n, f.store.gate.Waiting())
}

func mailboxBaselineReceive(t *testing.T, done <-chan mailboxBaselineResult) mailboxBaselineResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
		return mailboxBaselineResult{}
	}
}

func TestMessagesMailboxBaselineLatency(t *testing.T) {
	if os.Getenv("OA_MAILBOX_BASELINE") != "1" {
		t.Skip("set OA_MAILBOX_BASELINE=1 to measure mailbox latency")
	}
	for _, view := range []msg.View{msg.ViewHeaders, msg.ViewBody} {
		t.Run(fmt.Sprint(view), func(t *testing.T) {
			const batches, contexts = 5, 20
			durations := make([]time.Duration, 0, batches*contexts)
			for batch := range batches {
				t.Run(fmt.Sprint(batch), func(t *testing.T) {
					durations = append(durations, mailboxBaselineLatencyBatch(t, view, contexts)...)
				})
			}
			if len(durations) != batches*contexts {
				t.Fatalf("only %d latency samples", len(durations))
			}
			slices.Sort(durations)
			t.Logf("view=%v samples=%d p50=%v p99=%v min=%v max=%v", view, len(durations),
				durations[49], durations[98], durations[0], durations[99])
		})
	}
}

func mailboxBaselineLatencyBatch(t *testing.T, view msg.View, samples int) []time.Duration {
	f := messagesObserveFixture(t)
	t.Cleanup(f.reader.host.followMessageChanges(f.run.ctx))
	waits := mailboxBaselineWaits(t, f, samples, view)
	actor := messageActor{
		Account: f.otherSubject.Account, Program: f.otherProgram,
		ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source",
	}
	var committed time.Time
	f.store.boundary = func(point string) error {
		// The acceptance indexes are durable here. The next step in
		// accept advances the shared gate.
		if point == "index_published" {
			committed = time.Now()
		}
		return nil
	}
	durations := make([]time.Duration, 0, samples)
	for i := range samples {
		w := &waits[i]
		w.snapshot(t)
		done := w.start(f.run.ctx)
		mailboxBaselineAdmitted(t, f, 1)
		// Stagger the send within the headers reread interval.
		time.Sleep(time.Duration(i*37%100) * time.Millisecond)
		committed = time.Time{}
		request := msg.SendRequest{
			To: "app:native-agent", Context: w.name, Kind: msg.KindNote,
			Body: "baseline", IdempotencyKey: fmt.Sprintf("baseline-%d", i),
			ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(),
		}
		sent := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
		if sent.Outcome != msg.OutcomeSent || committed.IsZero() {
			t.Fatal(i, sent, committed)
		}
		result := mailboxBaselineReceive(t, done)
		page := result.page
		gotMessage := view == msg.ViewHeaders && len(page.Headers) == 1 && page.Headers[0].MessageID == sent.MessageID ||
			view == msg.ViewBody && len(page.Messages) == 1 && page.Messages[0].Header.MessageID == sent.MessageID
		if result.err != nil || page.Outcome != msg.OutcomePage || !gotMessage {
			t.Fatal(i, result)
		}
		durations = append(durations, result.at.Sub(committed))
	}
	return durations
}

func mailboxBaselineCPU(t *testing.T) time.Duration {
	t.Helper()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	ticks := func(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	return time.Duration(ticks(kernel)+ticks(user)) * 100
}

func TestMessagesMailboxBaselineIdleCPU(t *testing.T) {
	if os.Getenv("OA_MAILBOX_BASELINE") != "1" {
		t.Skip("set OA_MAILBOX_BASELINE=1 to measure mailbox CPU")
	}
	const perStore = 20
	const window = 8 * time.Second
	for _, view := range []msg.View{msg.ViewHeaders, msg.ViewBody} {
		t.Run(fmt.Sprint(view), func(t *testing.T) {
			f, g := messagesObserveFixture(t), messagesObserveFixture(t)
			t.Cleanup(f.reader.host.followMessageChanges(f.run.ctx))
			t.Cleanup(g.reader.host.followMessageChanges(g.run.ctx))
			waitGroups := [][]mailboxBaselineWait{
				mailboxBaselineWaits(t, f, perStore, view),
				mailboxBaselineWaits(t, g, perStore, view),
			}
			measure := func(count int) {
				cpu, start := mailboxBaselineCPU(t), time.Now()
				time.Sleep(window)
				wall, used := time.Since(start), mailboxBaselineCPU(t)-cpu
				t.Logf("view=%v waiters=%d wall=%v cpu=%v cores=%.4f", view, count, wall, used, float64(used)/float64(wall))
			}
			measure(0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var pending []<-chan mailboxBaselineResult
			for _, waits := range waitGroups {
				for i := range waits {
					waits[i].snapshot(t)
					pending = append(pending, waits[i].start(ctx))
				}
			}
			mailboxBaselineAdmitted(t, f, perStore)
			mailboxBaselineAdmitted(t, g, perStore)
			time.Sleep(time.Second)
			measure(len(pending))
			mailboxBaselineAdmitted(t, f, perStore)
			mailboxBaselineAdmitted(t, g, perStore)
			cancel()
			for _, done := range pending {
				result := mailboxBaselineReceive(t, done)
				// Cancellation may land inside the body's native owner check.
				if result.err != nil || len(result.page.Headers)+len(result.page.Messages) != 0 {
					t.Fatal(result)
				}
			}
			mailboxBaselineAdmitted(t, f, 0)
			mailboxBaselineAdmitted(t, g, 0)
		})
	}
}
