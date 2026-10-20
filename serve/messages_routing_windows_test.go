//go:build windows

package main

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Architect ruling 2026-11-12 (messages CONTRACT, "Program-only routing and
// chat disclosure"), through the runtime Messages service: a sender in its own
// context and Claude-integration chats owned by another native process.

const routingClaude = "oa-claude-integration"

type routingRun struct {
	t      *testing.T
	f      *messagesObserveRun
	si     string
	sender *msg.MessagesClient
}

func newRoutingRun(t *testing.T) *routingRun {
	f := messagesObserveFixtureNamed(t, routingClaude, routingClaude+".exe")
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	return &routingRun{t: t, f: f, si: si, sender: sender}
}

// open starts one live Claude chat and returns its instance and context.
func (r *routingRun) open(chat, title string) (string, string) {
	r.t.Helper()
	return messagesTypedChat(r.t, r.f, r.f.other, routingClaude, chat, title)
}

// closed leaves one Claude chat retained with no live inbox.
func (r *routingRun) closed(chat, title string) string {
	r.t.Helper()
	instance, context := r.open(chat, title)
	// The retained-chat listing reads the registered parties, as in
	// TestMessagesRetainedChatDiscoverySurvivesHelperExitAndKeepsMessagePermit.
	r.f.run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	if err := r.f.run.d.attachApplicationV3(r.f.run.ctx, registration1459Decoder); err != nil {
		r.t.Fatal(err)
	}
	if err := r.f.run.d.refreshMessageParties(r.f.run.ctx); err != nil {
		r.t.Fatal(err)
	}
	pin, err := r.f.other.call.RetainProcess()
	if err != nil {
		r.t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		r.t.Fatal(err)
	}
	r.f.run.d.withdrawInSession(r.f.run.ctx, subject, session, routingClaude, instance)
	return context
}

func (r *routingRun) prepare(context string) msg.PrepareResult {
	r.t.Helper()
	out, err := r.sender.Prepare(r.si, "source", msg.PrepareRequest{Receiver: "app:" + routingClaude, Context: context, Body: "Please review the plan."})
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *routingRun) send(request msg.SendRequest) msg.SendResult {
	r.t.Helper()
	out, err := r.sender.Send(r.si, "source", request)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// messagesBaseListing is the authority-filtered listing before the disclosure
// projection of the public InboxesList: every reachable chat with its row.
// Tests of reach, discovery and permission read it; disclosure is tested
// through InboxesList below.
func messagesBaseListing(r *messagesReceiver, query msg.InboxQuery) (msg.InboxPage, error) {
	if query.PeerKey != "" || query.Receiver != "" || query.PairingEpoch != "" {
		return r.InboxesList(query)
	}
	return r.inboxesList(query.Cursor), nil
}

func routingContexts(rows []msg.Inbox) map[string]bool {
	contexts := map[string]bool{}
	for _, row := range rows {
		contexts[row.Context] = true
	}
	return contexts
}

func TestMessagesProgramOnlySendNoLiveChatAnswersNoReachableChat(t *testing.T) {
	r := newRoutingRun(t)
	closedOne := r.closed("closed-one", "Old taxes")
	closedTwo := r.closed("closed-two", "Old holiday")
	// The closed chats stay listed and reachable by their context.
	whole, _ := r.f.reader.inboxesListWhole(false)
	if listed := routingContexts(whole.Inboxes); !listed[closedOne] || !listed[closedTwo] {
		t.Fatal("closed chats are not listed", whole)
	}
	out := r.prepare("")
	if out.Outcome != msg.OutcomeUnavailable || out.Reason != messageNoReachableChat || out.SendRequest != nil || len(out.Choices) != 0 || out.ChatTitle != "" {
		t.Fatal("program-only send without a live chat", out)
	}
	// An explicit context still reaches a closed chat (rule 5).
	if explicit := r.prepare(closedOne); explicit.Outcome != msg.OutcomePage || explicit.SendRequest == nil || explicit.SendRequest.Context != closedOne || explicit.ChatTitle != "Old taxes" {
		t.Fatal("explicit closed chat lost", explicit)
	}
}

func TestMessagesProgramOnlySendDeliversToTheOneLiveChat(t *testing.T) {
	r := newRoutingRun(t)
	r.closed("closed-one", "Old taxes")
	r.closed("closed-two", "Old holiday")
	_, live := r.open("live-one", "Pictures")
	prepared := r.prepare("")
	if prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil || prepared.SendRequest.Context != live || prepared.ChatTitle != "Pictures" {
		t.Fatal("one live chat among closed ones was not selected", prepared)
	}
	// A second chat opens between prepare and send: the prepared chat stays.
	_, second := r.open("live-two", "Reports")
	sent := r.send(*prepared.SendRequest)
	if sent.Outcome != msg.OutcomeSent || sent.Context != live || sent.ChatTitle != "Pictures" || sent.MessageID == "" {
		t.Fatal("send did not reach and name the prepared chat", sent)
	}
	// The same idempotency key answers the original result.
	retry := r.send(*prepared.SendRequest)
	if retry.Outcome != msg.OutcomeSent || retry.MessageID != sent.MessageID || retry.Context != live || retry.ChatTitle != "Pictures" {
		t.Fatal("retry re-resolved", sent, retry)
	}
	if second == live {
		t.Fatal("two chats share one context")
	}
}

func TestMessagesProgramOnlySendUsesInboxOpenTime(t *testing.T) {
	r := newRoutingRun(t)
	clock := r.f.run.d.now
	var offset atomic.Int64
	r.f.run.d.now = func() time.Time { return clock().Add(time.Duration(offset.Load())) }
	firstInstance, first := messagesTypedChatAt(t, r.f, r.f.other, routingClaude, "live-one", "Pictures", "first-source")
	offset.Add(int64(time.Second))
	_, second := r.open("live-two", "Reports")
	offset.Add(int64(time.Second))
	r.closed("newest-but-closed", "Old taxes")
	if renewed, err := r.f.other.InboxOpenChat(firstInstance, "first-source", "", msg.ChatBinding{ChatID: "live-one", ChatTitle: "Pictures", SourceInstance: firstInstance}); err != nil || renewed.Outcome != msg.OutcomeOpened {
		t.Fatal(renewed, err)
	}
	out := r.prepare("")
	if out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != second || out.ChatTitle != "Reports" {
		t.Fatal("newest live inbox lost to renewal or closed chat", out)
	}
	if chosen := r.prepare(first); chosen.Outcome != msg.OutcomePage || chosen.SendRequest == nil || chosen.SendRequest.Context != first {
		t.Fatal("exact context did not override fallback", chosen)
	}
}

func TestMessagesProgramOnlySendBreaksOpenTimeTiesByContext(t *testing.T) {
	r := newRoutingRun(t)
	opened := r.f.run.d.now()
	r.f.run.d.now = func() time.Time { return opened }
	_, a := r.open("tie-a", "A")
	_, b := r.open("tie-b", "B")
	out := r.prepare("")
	if out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != min(a, b) {
		t.Fatal("tie resolution", out)
	}
}

func TestMessagesAmbiguousChoicesAreBounded(t *testing.T) {
	rows := make([]msg.Inbox, 12)
	for i := range rows {
		rows[i] = msg.Inbox{Context: "c" + string(rune('a'+i)), LastSeenUnixMs: int64(i)}
	}
	choices := messageAmbiguousChoices(rows)
	if len(choices) != 8 || choices[0].LastSeenUnixMs != 11 || choices[7].LastSeenUnixMs != 4 {
		t.Fatal("choices are not the 8 most recent first", choices)
	}
}

func TestMessagesInboxesListCountsChatsWithoutThreads(t *testing.T) {
	r := newRoutingRun(t)
	_, pictures := r.open("live-one", "Pictures")
	r.open("live-two", "Reports")
	r.closed("closed-one", "Old taxes")
	page, err := r.f.reader.InboxesList(msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal(page, err)
	}
	for _, row := range page.Inboxes {
		if row.Program == "app:"+routingClaude {
			t.Fatal("a chat without a thread is listed", row)
		}
	}
	if len(page.OtherChats) != 1 || page.OtherChats[0].Program != "app:"+routingClaude || page.OtherChats[0].Chats != 3 {
		t.Fatal("other chats are not counted per program", page.OtherChats)
	}
	raw := msg.EncodeInboxPage(&page)
	for _, title := range []string{"Pictures", "Reports", "Old taxes"} {
		if bytes.Contains(raw, []byte(title)) {
			t.Fatal("listing discloses a foreign title", title, string(raw))
		}
	}
	// A message makes a thread: that chat is listed with its title.
	prepared := r.prepare(pictures)
	if prepared.SendRequest == nil {
		t.Fatal(prepared)
	}
	if sent := r.send(*prepared.SendRequest); sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	page, err = r.f.reader.InboxesList(msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal(page, err)
	}
	threads := 0
	for _, row := range page.Inboxes {
		if row.Program == "app:"+routingClaude {
			threads++
			if row.Context != pictures || row.ChatTitle != "Pictures" {
				t.Fatal("thread row", row)
			}
		}
	}
	if threads != 1 || len(page.OtherChats) != 1 || page.OtherChats[0].Chats != 2 || bytes.Contains(msg.EncodeInboxPage(&page), []byte("Reports")) {
		t.Fatal("thread disclosure", page)
	}
}

func TestMessagesReceiptTitleUsesAuthorizedSiblingsWithoutDiscovery(t *testing.T) {
	r := newRoutingRun(t)
	r.closed("retained-fixture", "Other")
	_, target := r.open("live-duplicate-one", "Pictures")
	r.open("live-duplicate-two", "Pictures")
	prepared := r.prepare(target)
	if prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil || prepared.ChatTitle == "Pictures" || prepared.ChatTitle == "" {
		t.Fatalf("expected a disambiguated title: %+v", prepared)
	}
	var warm atomic.Int32
	r.f.run.d.messageAttachWarm = func(context.Context, []string) bool { warm.Add(1); return true }
	result := r.send(*prepared.SendRequest)
	if result.Outcome != msg.OutcomeSent || result.Context != target || result.ChatTitle != prepared.ChatTitle || warm.Load() != 0 {
		t.Fatalf("receipt changed accepted destination or warmed discovery: %+v, warm=%d", result, warm.Load())
	}
	if err := r.f.reader.host.messageRights.policy.Set(r.f.self, ActionApplicationMessage, "app:"+routingClaude, false); err != nil {
		t.Fatal(err)
	}
	replayed := r.send(*prepared.SendRequest)
	if replayed.Outcome != msg.OutcomeSent || replayed.MessageID != result.MessageID || replayed.Context != target || replayed.ChatTitle != "" || warm.Load() != 0 {
		t.Fatalf("receipt replay disclosed a revoked title or changed acceptance: %+v, warm=%d", replayed, warm.Load())
	}
}
