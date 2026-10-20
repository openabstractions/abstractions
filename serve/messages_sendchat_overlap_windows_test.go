//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestNativeSendChatPublishesAcrossIndependentProgramPairs(t *testing.T) {
	testNativeSendChatOverlap(t, messagesObserveFixture(t), false, false)
}

func TestNativeSendChatPublishesUnderV3AgentMessagesDefault(t *testing.T) {
	f := messagesObserveFixture(t, true)
	agentMessagesOn(t, f)
	testNativeSendChatOverlap(t, f, true, false)
}

func TestNativeSendChatRetainsTwoDisjointWarmPairs(t *testing.T) {
	testNativeSendChatOverlap(t, messagesObserveFixture(t), false, true)
}

func nativeSendChatAdditionalAgent(t *testing.T, f *messagesObserveRun, name string) *messagesReceiver {
	t.Helper()
	raw, err := os.ReadFile(f.otherProgram)
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), name+".exe")
	if err := os.WriteFile(image, raw, 0700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, f.run, name, identity.NormalizeSubjectProgram(image))
	agent1478Set(t, f.run, name, "", true)
	question := agent1478Question(t, f.run, name)
	answer(t, f.run.f, question.ID, "keep")
	agent1478Wait(t, f.run, name, "enrolled")
	call, _ := messages1608CallImage(t, image)
	return &messagesReceiver{host: f.reader.host, call: call, ctx: f.run.ctx}
}

func testNativeSendChatOverlap(t *testing.T, f *messagesObserveRun, defaultPolicy, disjoint bool) {
	t.Helper()
	first := *f.reader
	second := *f.other
	secondApplication, secondTarget := "other-agent", "native-agent"
	secondReceiver := f.reader
	if disjoint {
		secondApplication, secondTarget = "third-agent", "fourth-agent"
		second = *nativeSendChatAdditionalAgent(t, f, secondApplication)
		secondReceiver = nativeSendChatAdditionalAgent(t, f, secondTarget)
		pin, err := second.call.RetainProcess()
		if err != nil {
			t.Fatal(err)
		}
		_, subject, _, _, err := nativeInboxPeer(pin, f.run.d.now())
		pin.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := first.host.messageRights.policy.Set(subject, ActionApplicationMessage, "app:"+secondTarget, true); err != nil {
			t.Fatal(err)
		}
	}
	ai, aRoute := messagesTypedChatAt(t, f, &first, "native-agent", "chat-a", "", "source-a")
	bi, bRoute := messagesTypedChatAt(t, f, &second, secondApplication, "chat-b", "", "source-b")
	ar, aInbox := messagesStateOpen(t, f, secondReceiver, secondTarget, "target-a")
	br, bInbox := messagesStateOpen(t, f, f.other, "other-agent", "target-b")

	request := func(to, target, route, key string) msg.ChatSendRequest {
		return msg.ChatSendRequest{ReturnContext: route, Request: msg.SendRequest{
			To: "app:" + to, Context: target, Kind: msg.KindNote,
			Body: key, IdempotencyKey: key, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(),
		}}
	}
	warm := func(sender *messagesReceiver, instance, source string, req msg.ChatSendRequest, inbox *msg.MessagesClient, targetInstance string) {
		t.Helper()
		sent, err := sender.SendChat(instance, source, req)
		if err != nil || sent.Outcome != msg.OutcomeSent {
			t.Fatal("warm send", sent, err)
		}
		page, err := inbox.Wait(targetInstance, req.Request.Context, msg.ViewBody, "", "", 0)
		if err != nil || len(page.Messages) != 1 || page.Messages[0].Header.MessageID != sent.MessageID || page.Cursor == "" {
			t.Fatal("warm delivery", page, err)
		}
		if ack, err := inbox.Wait(targetInstance, req.Request.Context, msg.ViewBody, page.Cursor, "", 0); err != nil || ack.Outcome != msg.OutcomePage {
			t.Fatal("warm acknowledgement", ack, err)
		}
	}
	warm(&first, ai, "source-a", request("other-agent", "target-b", aRoute, "warm-a"), bInbox, br)
	warm(&second, bi, "source-b", request(secondTarget, "target-a", bRoute, "warm-b"), aInbox, ar)

	if defaultPolicy {
		if roles := f.run.d.roles.Load(); roles == nil || roles.messaging == nil {
			t.Fatal("warm sends did not publish the v3 messaging class")
		}
	}
	ctx, cancel := context.WithTimeout(f.run.ctx, 10*time.Second)
	defer cancel()
	entered, secondPublished, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var stalled, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	f.store.boundary = func(point string) error {
		if point == "before_record" {
			first := false
			stalled.Do(func() { first = true })
			if first {
				close(entered)
				<-release
			}
		}
		if point == "record_published" {
			select {
			case <-release:
			default:
				close(secondPublished)
			}
		}
		return nil
	}
	type result struct {
		out msg.SendResult
		err error
	}
	aDone, bDone := make(chan result, 1), make(chan result, 1)
	aReq := request("other-agent", "target-b", aRoute, "note-a")
	bReq := request(secondTarget, "target-a", bRoute, "note-b")
	go func() {
		out, err := first.SendChat(ai, "source-a", aReq)
		aDone <- result{out, err}
	}()
	select {
	case <-entered:
	case a := <-aDone:
		t.Fatal("first send returned before durable publication barrier", a)
	case <-ctx.Done():
		t.Fatal("first send did not reach durable publication barrier", ctx.Err())
	}

	// Exercise the real watch pass while the first Send holds publication
	// custody. A busy registration check must defer without queuing a writer.
	d := f.run.d
	if !d.lockRead(ctx) {
		t.Fatal(ctx.Err())
	}
	book := d.nativeInboxes
	d.mu.RUnlock()
	enrollment := make(chan bool, 1)
	go func() { enrollment <- book.checkEnrollment(ctx) }()
	select {
	case completed := <-enrollment:
		if completed {
			t.Fatal("enrollment maintenance did not defer during publication")
		}
	case <-ctx.Done():
		t.Fatal("enrollment maintenance queued a writer during publication")
	}

	go func() {
		out, err := second.SendChat(bi, "source-b", bReq)
		bDone <- result{out, err}
	}()
	overlapped := false
	select {
	case <-secondPublished:
		overlapped = true
	case <-ctx.Done():
	}
	unblock()
	b := <-bDone
	a := <-aDone
	if a.err != nil || b.err != nil || a.out.Outcome != msg.OutcomeSent || b.out.Outcome != msg.OutcomeSent || a.out.MessageID == "" || b.out.MessageID == "" || a.out.MessageID == b.out.MessageID {
		t.Fatal("native receipts", a, b)
	}
	for _, item := range []struct {
		client   *msg.MessagesClient
		instance string
		request  msg.ChatSendRequest
		id       string
	}{{bInbox, br, aReq, a.out.MessageID}, {aInbox, ar, bReq, b.out.MessageID}} {
		page, err := item.client.Wait(item.instance, item.request.Request.Context, msg.ViewBody, "", "", 0)
		if err != nil || page.Outcome != msg.OutcomePage || len(page.Messages) != 1 {
			t.Fatal("native delivery", page, err)
		}
		note := page.Messages[0]
		if note.Header.MessageID != item.id || note.Header.FromContext != item.request.ReturnContext || note.Body != item.request.Request.Body {
			t.Fatal("native body does not match receipt", note, item.id)
		}
	}
	if !overlapped {
		t.Error("second warm native SendChat could only publish after releasing the first")
	}

	if !defaultPolicy {
		return
	}
	policy := f.run.f.a.rights
	revision, _, err := policy.policy.DefaultsSnapshot(f.run.ctx)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := policy.policy.SetDefault(revision, rights.DefaultAgentMessages, false, f.run.f.panel, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied || edit.Current == nil || edit.Current.On {
		t.Fatal("revoke default", edit, err)
	}
	if got := policy.decide(f.run.ctx, f.self, ActionApplicationMessage, "app:other-agent").Outcome; got != rwire.DecisionOutcomeNotGranted {
		t.Fatal("unexpected grant after default revocation", got)
	}
	refused := request("other-agent", "target-b", aRoute, "revoked")
	out, err := first.SendChat(ai, "source-a", refused)
	if err != nil || out.Outcome != msg.OutcomeSent || !out.AwaitingPermission || out.MessageID == "" {
		t.Fatal("revoked default did not fall back to the permission hold", out, err)
	}
	page, err := bInbox.Wait(br, "target-b", msg.ViewBody, "", "", 0)
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal("read after default revocation", page, err)
	}
	for _, note := range page.Messages {
		if note.Header.MessageID == out.MessageID || note.Body == refused.Request.Body {
			t.Fatal("permission-held note appeared in the inbox", note.Header.MessageID)
		}
	}
}
