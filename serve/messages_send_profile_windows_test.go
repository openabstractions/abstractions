//go:build windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	inference "github.com/openabstractions/abstraction-inference/go"
	iw "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
)

func TestMessagesHeldChatSendProfile(t *testing.T) {
	if os.Getenv("OA_MAILBOX_SEND_PROFILE") != "1" {
		t.Skip("set OA_MAILBOX_SEND_PROFILE=1")
	}
	entered, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fmt.Fprint(w, `{"data":[{"id":"fixture-chat"}]}`)
			return
		}
		close(entered)
		defer close(ended)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	defer finish()
	provider, err := inference.New(inference.Config{Router: router.New(router.OpenAICompatible("fixture", upstream.URL, "/v1", []string{"fixture-chat"})),
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "applied"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	})
	r := newRoutingRun(t)
	t.Cleanup(r.f.reader.host.followMessageChanges(r.f.run.ctx))
	_, target := r.open("profile-target", "Profile target")
	other := messagesObserveFixture(t)
	t.Cleanup(other.reader.host.followMessageChanges(other.run.ctx))
	waits := append(mailboxBaselineWaits(t, r.f, 20, msg.ViewHeaders), mailboxBaselineWaits(t, other, 20, msg.ViewHeaders)...)
	var commit time.Time
	r.f.store.boundary = func(point string) error {
		if point == "index_published" {
			commit = time.Now()
		}
		return nil
	}
	var serial int
	accepted := make(map[string]bool)
	measure := func(label string) {
		// Three cases share one program pair, with 20 fresh Sends per hour.
		const samples = messageBudgetPerHour / 3
		var prep, send, before, after []time.Duration
		cpu, start := mailboxBaselineCPU(t), time.Now()
		for range samples {
			serial++
			request := msg.PrepareRequest{
				Receiver: "app:" + routingClaude, Context: target,
				Body: fmt.Sprintf("Profile message %d", serial),
			}
			started := time.Now()
			out, err := r.sender.Prepare(r.si, "source", request)
			prep = append(prep, time.Since(started))
			if err != nil || out.Outcome != msg.OutcomePage || out.SendRequest == nil {
				t.Fatal(out, err)
			}
			commit = time.Time{}
			started = time.Now()
			sent := r.send(*out.SendRequest)
			finished := time.Now()
			if sent.Outcome != msg.OutcomeSent || sent.Context != target || commit.IsZero() {
				t.Fatal(sent, commit)
			}
			if sent.MessageID == "" || accepted[sent.MessageID] {
				t.Fatalf("sample %d did not create a fresh acceptance: %+v", serial, sent)
			}
			accepted[sent.MessageID] = true
			send = append(send, finished.Sub(started))
			before = append(before, commit.Sub(started))
			after = append(after, finished.Sub(commit))
		}
		stats := func(v []time.Duration) string {
			slices.Sort(v)
			return fmt.Sprintf("median=%s max=%s", v[len(v)/2], v[len(v)-1])
		}
		t.Logf("%s n=%d wall=%s CPU=%s prepare %s send %s precommit %s postcommit %s", label, samples, time.Since(start), mailboxBaselineCPU(t)-cpu, stats(prep), stats(send), stats(before), stats(after))
	}
	measure("no waits; no chat")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pending := make([]<-chan mailboxBaselineResult, 0, len(waits))
	for i := range waits {
		waits[i].snapshot(t)
		pending = append(pending, waits[i].start(ctx))
	}
	mailboxBaselineAdmitted(t, r.f, 20)
	mailboxBaselineAdmitted(t, other, 20)
	cpu, start := mailboxBaselineCPU(t), time.Now()
	time.Sleep(4 * time.Second)
	t.Logf("40 idle waits wall=%s CPU=%s", time.Since(start), mailboxBaselineCPU(t)-cpu)
	measure("40 waits; no chat")
	caller := inference.Subject{Account: r.f.self.Account, Program: r.f.selfProgram}
	chat := provider.Start(t.Context(), caller, iw.Request{Model: "fixture-chat", Messages: []iw.Message{{Role: iw.RoleUser, Parts: []iw.Part{{Kind: iw.PartKindText, Text: "profile"}}}}})
	if chat.Outcome != iw.StartOutcomeAccepted {
		t.Fatal(chat)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream chat not entered")
	}
	measure("40 waits; held upstream chat")
	select {
	case <-ended:
		t.Fatal("chat finished before Send measurement")
	default:
	}
	finish()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not leave")
	}
	cancel()
	for _, done := range pending {
		mailboxBaselineReceive(t, done)
	}
}
