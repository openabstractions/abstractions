//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesBoundLiveWakeIgnoresColdStartSettings(t *testing.T) {
	f := messagesObserveFixture(t)
	_, chat := messagesTypedChat(t, f, f.reader, "native-agent", "live-chat", "Live chat")
	messagesStateOpen(t, f, f.other, "other-agent", "sender")
	options := runtimeFlags{stateDir: t.TempDir()}
	path := filepath.Join(t.TempDir(), "config.json")
	serveRuntimeNASConfig(t, &options, path)
	if err := os.WriteFile(path, []byte(`{"messages_start_questions":false,"messages_start_notes":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	h := f.reader.host
	configureRuntimeMessageStartSettings(h, options)
	settings, err := h.readMessageStartSettings(f.run.ctx, "native-agent")
	if err != nil || settings.Questions || !settings.Notes {
		t.Fatal(settings, err)
	}
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "sender"}
	request := msg.SendRequest{To: "app:native-agent", Context: chat, Kind: msg.KindNote, Body: "note waits", IdempotencyKey: "live-note", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	if sent := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, ""); sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	note := f.reader.headersWait(chat, "", 0)
	if note.Outcome != msg.OutcomePage || len(note.Headers) != 1 || note.Headers[0].Kind != msg.KindNote || note.Wake == "" || *note.PendingQuestions != 0 {
		t.Fatal("an unacknowledged note did not wake the live chat", note)
	}
	request.Kind, request.Body, request.IdempotencyKey = msg.KindQuestion, "question wakes", "live-question"
	request.AnswerByUnixMs = f.now.Add(time.Minute).UnixMilli()
	sent := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	question := f.reader.headersWait(chat, note.Revision, 0)
	if question.Outcome != msg.OutcomePage || question.Wake == "" || question.Wake < note.Wake {
		t.Fatal("disabled cold questions suppressed a live wake", question)
	}
	found := false
	for _, header := range question.Headers {
		if header.MessageID == sent.MessageID {
			found = header.TaskState != nil && *header.TaskState == msg.TaskStateSubmitted
		}
	}
	if !found {
		t.Fatal("wake did not belong to the submitted question", question)
	}
}

// An open idle chat obeys the dormant start's hop limit: a note or question
// whose chain already holds messageWakeHopLimit woken turns stays unread
// and projects no wake, so no Claude, Codex or OpenCode adapter reserves a
// pointer for it. One hop less still wakes.
func TestMessagesLiveWakeStopsAtHopLimit(t *testing.T) {
	f := messagesObserveFixture(t)
	_, chat := messagesTypedChat(t, f, f.reader, "native-agent", "hop-chat", "Hop chat")
	messagesStateOpen(t, f, f.other, "other-agent", "sender")
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "sender"}
	limit := []string{"q-person", "q-second"}[:messageWakeHopLimit]
	revision := ""
	for i, kind := range []msg.Kind{msg.KindNote, msg.KindQuestion} {
		request := msg.SendRequest{To: "app:native-agent", Context: chat, Kind: kind, Body: "loop", IdempotencyKey: fmt.Sprintf("at-limit-%d", i), ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
		if kind == msg.KindQuestion {
			request.AnswerByUnixMs = f.now.Add(time.Minute).UnixMilli()
		}
		if sent := f.store.accept(withMessageOrigin(f.run.ctx, limit), actor, f.selfProgram, request, false, ""); sent.Outcome != msg.OutcomeSent {
			t.Fatal(sent)
		}
		page := f.reader.headersWait(chat, revision, 0)
		if page.Outcome != msg.OutcomePage || page.Wake != "" || *page.Unread != int64(i+1) {
			t.Fatal("a message at the hop limit woke the idle chat", kind, page)
		}
		revision = page.Revision
	}
	request := msg.SendRequest{To: "app:native-agent", Context: chat, Kind: msg.KindNote, Body: "one hop", IdempotencyKey: "below-limit", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	if sent := f.store.accept(withMessageOrigin(f.run.ctx, limit[:messageWakeHopLimit-1]), actor, f.selfProgram, request, false, ""); sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	if page := f.reader.headersWait(chat, revision, 0); page.Outcome != msg.OutcomePage || page.Wake == "" {
		t.Fatal("a message below the hop limit did not wake the idle chat", page)
	}
}

func messagesTypedChat(t *testing.T, f *messagesObserveRun, receiver *messagesReceiver, application, chat, title string) (string, string) {
	return messagesTypedChatAt(t, f, receiver, application, chat, title, fmt.Sprintf("source-%d", time.Now().UnixNano()))
}

func messagesTypedChatAt(t *testing.T, f *messagesObserveRun, receiver *messagesReceiver, application, chat, title, source string) (string, string) {
	t.Helper()
	pin, err := receiver.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	peer, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = peer
	presence := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: application, LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: source}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	if out, err := receiver.InboxOpenChat(presence.Instance, source, "", msg.ChatBinding{}); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	allocated, err := receiver.InboxOpenChat(presence.Instance, source, "", msg.ChatBinding{ChatID: chat, ChatTitle: title, SourceInstance: presence.Instance})
	if err != nil || allocated.Outcome != msg.OutcomePage || allocated.Context == "" {
		t.Fatal(allocated, err)
	}
	canonical := allocated.Context
	declared := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: application, Instance: presence.Instance, LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: source}, {Name: canonical}}})
	if declared.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(declared)
	}
	if out, err := receiver.InboxOpenChat(presence.Instance, source, "", msg.ChatBinding{ChatID: chat, ChatTitle: title, SourceInstance: presence.Instance}); err != nil || out.Outcome != msg.OutcomeOpened || out.Context != canonical {
		t.Fatal(out, err)
	}
	return presence.Instance, canonical
}

func TestMessagesTypedChatTakeoverEndsPriorOwnerAndPreservesNewOwner(t *testing.T) {
	f := messagesObserveFixture(t)
	ai, chat := messagesTypedChatAt(t, f, f.reader, "native-agent", "same-chat", "Review\u202e\n plan", "source-a")
	listed, err := messagesBaseListing(f.other, msg.InboxQuery{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range listed.Inboxes {
		if row.Context == chat {
			count++
			if row.ChatTitle != "Review plan" {
				t.Fatal(row)
			}
		}
		if strings.HasPrefix(row.Context, "source-") {
			t.Fatal("unbound source advertised", row)
		}
	}
	if count != 1 {
		t.Fatal(listed)
	}
	callB, _ := messages1608Call(t)
	receiverB := &messagesReceiver{host: f.reader.host, call: callB, ctx: f.run.ctx}
	bi, again := messagesTypedChatAt(t, f, receiverB, "native-agent", "same-chat", "Second owner", "source-b")
	if again != chat || bi == ai {
		t.Fatal("chat route changed or instance reused", ai, bi, chat, again)
	}
	if ended, err := f.reader.InboxOpenChat(ai, "source-a", "", msg.ChatBinding{ChatID: "same-chat", SourceInstance: ai}); err != nil || ended.Outcome != msg.OutcomeEnded {
		t.Fatal("retired owner renewed", ended, err)
	}
	if body, err := f.reader.Wait(ai, chat, msg.ViewBody, "", "", 0); err != nil || body.Outcome != msg.OutcomeForbidden {
		t.Fatal("retired owner retained body access", body, err)
	}
	pin, err := f.reader.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	_ = f.run.d.withdrawInSession(f.run.ctx, subject, session, "native-agent", ai)
	if body, err := receiverB.Wait(bi, chat, msg.ViewBody, "", "", 0); err != nil || body.Outcome != msg.OutcomePage {
		t.Fatal("old cleanup retired newer owner", body, err)
	}
	if out, err := receiverB.InboxOpenChat(bi, "source-b", "", msg.ChatBinding{ChatID: "different-chat", SourceInstance: bi}); err != nil || out.Outcome == msg.OutcomeOpened || out.Outcome == msg.OutcomePage {
		t.Fatal("changed host chat retargeted native source", out, err)
	}
	listed, err = messagesBaseListing(f.other, msg.InboxQuery{})
	if err != nil {
		t.Fatal(err)
	}
	count = 0
	for _, row := range listed.Inboxes {
		if row.Context == chat {
			count++
			if row.ChatTitle != "Second owner" {
				t.Fatal(row)
			}
		}
	}
	if count != 1 {
		t.Fatal("takeover left duplicate inboxes", listed)
	}
}

func TestMessagesWakeAckNewestRevealsOlderWithoutNewIdentity(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	request := msg.SendRequest{To: "app:native-agent", Context: "target", Kind: msg.KindQuestion, Subject: "question", Body: "first", IdempotencyKey: "first", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
	one := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
	if one.Outcome != msg.OutcomeSent {
		t.Fatal(one)
	}
	prior := f.reader.headersWait("target", "", 0)
	if prior.Wake == "" {
		t.Fatal("eligible record has no wake", prior)
	}
	request.Body, request.IdempotencyKey = "second", "second"
	two := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
	if two.Outcome != msg.OutcomeSent {
		t.Fatal(two)
	}
	latest := f.reader.headersWait("target", prior.Revision, 0)
	if latest.Wake <= prior.Wake {
		t.Fatal("same-ms new item did not increase wake", prior, latest)
	}
	r, key, err := f.store.readByID(f.run.ctx, two.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	// The cursor owner ACKs exactly its previously returned selection. A
	// permission-filtered page can contain the newest record alone.
	permitted := map[string]string{key: r.Token}
	body := f.store.bodyPage(f.run.ctx, f.self.Account, f.selfProgram, "target", "test-owner", "", permitted)
	if len(body.Messages) != 1 || body.Messages[0].Header.MessageID != two.MessageID {
		t.Fatal(body)
	}
	_ = f.store.bodyPage(f.run.ctx, f.self.Account, f.selfProgram, "target", "test-owner", body.Cursor, permitted)
	older := f.reader.headersWait("target", latest.Revision, 0)
	if older.Wake != prior.Wake || older.Wake >= latest.Wake {
		t.Fatal("ACK of newest minted a fresh wake for old work", prior, latest, older)
	}
}

func TestMessagesChatAllocationWithoutDeclaredReceiverPreservesPriorCustody(t *testing.T) {
	f := messagesObserveFixture(t)
	ai, canonical := messagesTypedChatAt(t, f, f.reader, "native-agent", "same-chat", "Current", "source-a")
	callB, _ := messages1608Call(t)
	b := &messagesReceiver{host: f.reader.host, call: callB, ctx: f.run.ctx}
	bi, _ := messagesStateOpen(t, f, b, "native-agent", "source-b")
	if out, err := b.InboxOpenChat(bi, "source-b", "", msg.ChatBinding{}); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	allocated, err := b.InboxOpenChat(bi, "source-b", "", msg.ChatBinding{ChatID: "same-chat", SourceInstance: bi})
	if err != nil || allocated.Outcome != msg.OutcomePage || allocated.Context != canonical {
		t.Fatal(allocated, err)
	}
	if out, err := b.Wait(bi, canonical, msg.ViewBody, "", "", 0); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("undeclared allocation acquired body", out, err)
	}
	if out, err := f.reader.Wait(ai, canonical, msg.ViewBody, "", "", 0); err != nil || out.Outcome != msg.OutcomePage {
		t.Fatal("allocation retired prior owner", out, err)
	}
	if out, err := b.InboxOpenChat(bi, "opaque-label", "", msg.ChatBinding{ChatID: "same-chat", SourceInstance: bi}); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("label substituted for exact source", out, err)
	}
	listed, err := messagesBaseListing(f.other, msg.InboxQuery{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range listed.Inboxes {
		if row.Context == canonical {
			count++
			if row.ChatTitle != "Current" {
				t.Fatal("allocation replaced display owner", row)
			}
		}
		if row.Context == "source-b" {
			t.Fatal("unbound source advertised", row)
		}
	}
	if count != 1 {
		t.Fatal(listed)
	}
}

func TestMessagesLegacyRouteRemainsUnadoptedUnderExactSourceCustody(t *testing.T) {
	f := messagesObserveFixture(t)
	instance, legacy := messagesStateOpen(t, f, f.reader, "native-agent", "legacy-route")
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	accepted := f.store.accept(f.run.ctx, actor, f.selfProgram, msg.SendRequest{To: "app:native-agent", Context: "legacy-route", Kind: msg.KindNote, Body: "retained legacy body", IdempotencyKey: "legacy", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}, false, "")
	if accepted.Outcome != msg.OutcomeSent {
		t.Fatal(accepted)
	}
	if out, err := f.reader.InboxOpenChat(instance, "legacy-route", "", msg.ChatBinding{ChatID: "new-host", SourceInstance: instance}); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("opaque legacy label adopted without typed source proof", out, err)
	}
	if out, err := f.reader.InboxOpenChat(instance, "legacy-route", "", msg.ChatBinding{}); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	allocated, err := f.reader.InboxOpenChat(instance, "legacy-route", "", msg.ChatBinding{ChatID: "new-host", SourceInstance: instance})
	if err != nil || allocated.Outcome != msg.OutcomePage || allocated.Context == "legacy-route" || allocated.Context == "" {
		t.Fatal("new allocation reassigned legacy address", allocated, err)
	}
	page, err := legacy.Wait(instance, "legacy-route", msg.ViewBody, "", "", 0)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Body != "retained legacy body" || page.Messages[0].Header.MessageID != accepted.MessageID {
		t.Fatal("legacy exact-source custody lost accepted content", page, err)
	}
	record, _, err := f.store.readByID(f.run.ctx, accepted.MessageID)
	if err != nil || record == nil {
		t.Fatal(record, err)
	}
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil || request.Context != "legacy-route" {
		t.Fatal("accepted route rewritten", request, err)
	}
}

func TestMessagesChatTakeoverPreservesOtherChatOnSharedNativeInstance(t *testing.T) {
	f := messagesObserveFixture(t)
	pin, err := f.reader.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: "native-agent", LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "source-a"}, {Name: "source-b"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	contexts := map[string]string{}
	for _, chat := range []string{"a", "b"} {
		source := "source-" + chat
		if out, err := f.reader.InboxOpenChat(presence.Instance, source, "", msg.ChatBinding{}); err != nil || out.Outcome != msg.OutcomeOpened {
			t.Fatal(out, err)
		}
		out, err := f.reader.InboxOpenChat(presence.Instance, source, "", msg.ChatBinding{ChatID: chat, SourceInstance: presence.Instance})
		if err != nil || out.Outcome != msg.OutcomePage {
			t.Fatal(out, err)
		}
		contexts[chat] = out.Context
	}
	declared := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: "native-agent", Instance: presence.Instance, LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "source-a"}, {Name: "source-b"}, {Name: contexts["a"]}, {Name: contexts["b"]}}})
	if declared.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(declared)
	}
	for _, chat := range []string{"a", "b"} {
		if out, err := f.reader.InboxOpenChat(presence.Instance, "source-"+chat, "", msg.ChatBinding{ChatID: chat, SourceInstance: presence.Instance}); err != nil || out.Outcome != msg.OutcomeOpened {
			t.Fatal(out, err)
		}
	}
	oi, other := messagesStateOpen(t, f, f.other, "other-agent", "other-source")
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "other-source"}
	request := msg.SendRequest{To: "app:native-agent", Context: contexts["b"], Kind: msg.KindQuestion, Body: "question for B", IdempotencyKey: "b-first", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
	first := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
	if first.Outcome != msg.OutcomeSent {
		t.Fatal(first)
	}
	priorWake := f.reader.headersWait(contexts["b"], "", 0)
	call, _ := messages1608Call(t)
	newA := &messagesReceiver{host: f.reader.host, call: call, ctx: f.run.ctx}
	newInstance, newContext := messagesTypedChatAt(t, f, newA, "native-agent", "a", "new A", "new-source-a")
	if newContext != contexts["a"] {
		t.Fatal("A route changed", newContext, contexts)
	}
	if out, err := f.reader.InboxOpenChat(presence.Instance, "source-a", "", msg.ChatBinding{ChatID: "a", SourceInstance: presence.Instance}); err != nil || out.Outcome != msg.OutcomeEnded {
		t.Fatal("old A reclaimed route", out, err)
	}
	readmitted := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: "native-agent", LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "source-a"}}})
	if readmitted.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(readmitted)
	}
	if out, err := f.reader.InboxOpenChat(readmitted.Instance, "source-a", "", msg.ChatBinding{}); err != nil || out.Outcome != msg.OutcomeEnded {
		t.Fatal("fresh presence erased old A source-lifetime fence", out, err)
	}
	if out, err := f.reader.InboxOpenChat(presence.Instance, "source-b", "", msg.ChatBinding{ChatID: "b", SourceInstance: presence.Instance}); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal("A takeover retired unrelated B", out, err)
	}
	body, err := f.reader.Wait(presence.Instance, contexts["b"], msg.ViewBody, "", "", 0)
	if err != nil || len(body.Messages) != 1 || body.Messages[0].Header.MessageID != first.MessageID {
		t.Fatal("B lost body", body, err)
	}
	if out, err := f.reader.Reply(presence.Instance, contexts["b"], first.MessageID, "B answer"); err != nil || out.Outcome != msg.OutcomeSent {
		t.Fatal("B lost reply", out, err)
	}
	if page, err := other.Wait(oi, "other-source", msg.ViewBody, "", "", 0); err != nil || !chatPageHasReply(page, "B answer") {
		t.Fatal(page, err)
	}
	request.IdempotencyKey, request.Body = "b-next", "new B work"
	if out := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, ""); out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	nextWake := f.reader.headersWait(contexts["b"], priorWake.Revision, 0)
	if priorWake.Wake == "" || nextWake.Wake <= priorWake.Wake {
		t.Fatal("B lost ordered wake", priorWake, nextWake)
	}
	if out := f.run.d.withdrawInSession(f.run.ctx, subject, session, "native-agent", presence.Instance); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if page, err := f.reader.Wait(presence.Instance, contexts["b"], msg.ViewBody, "", "", 0); err != nil || page.Outcome != msg.OutcomeForbidden {
		t.Fatal("whole-instance withdrawal retained B", page, err)
	}
	if page, err := newA.Wait(newInstance, contexts["a"], msg.ViewBody, "", "", 0); err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal("old shared-instance cleanup retired new A", page, err)
	}
}

func TestMessagesOpaqueClaudeChatsKeepReadReplyWakeAcrossColdConnection(t *testing.T) {
	f := messagesObserveFixture(t)
	ai, aContext := messagesTypedChatAt(t, f, f.reader, "native-agent", "local_a9343ad9", "Claude A", "claude-source-a")
	callB, _ := messages1608Call(t)
	b := &messagesReceiver{host: f.reader.host, call: callB, ctx: f.run.ctx}
	bi, bContext := messagesTypedChatAt(t, f, b, "native-agent", "local_b9343ad9", "Claude B", "claude-source-b")
	if aContext == bContext || ai == bi {
		t.Fatal("independent chats shared ownership", ai, bi, aContext, bContext)
	}
	oi, other := messagesStateOpen(t, f, f.other, "other-agent", "claude-question-source")
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "claude-question-source"}
	question := func(contextName, key string) msg.SendResult {
		request := msg.SendRequest{To: "app:native-agent", Context: contextName, Kind: msg.KindQuestion, Body: key, IdempotencyKey: key, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
		out := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
		if out.Outcome != msg.OutcomeSent {
			t.Fatal(out)
		}
		return out
	}
	qa, qb := question(aContext, "Claude A question"), question(bContext, "Claude B question")
	wa, wb := f.reader.headersWait(aContext, "", 0), b.headersWait(bContext, "", 0)
	if wa.Wake == "" || wb.Wake == "" || wa.Wake == wb.Wake {
		t.Fatal("chat wake identities collapsed", wa, wb)
	}
	first, err := f.reader.Wait(ai, aContext, msg.ViewBody, "", "", 0)
	if err != nil || len(first.Messages) != 1 || first.Messages[0].Header.MessageID != qa.MessageID {
		t.Fatal(first, err)
	}
	callC, _ := messages1608Call(t)
	cold := &messagesReceiver{host: f.reader.host, call: callC, ctx: f.run.ctx}
	ci, coldContext := messagesTypedChatAt(t, f, cold, "native-agent", "local_a9343ad9", "Claude A reopened", "claude-source-cold")
	if coldContext != aContext || ci == ai {
		t.Fatal("cold connection changed chat address or reused owner", ci, ai, coldContext, aContext)
	}
	if old, err := f.reader.Wait(ai, aContext, msg.ViewBody, "", "", 0); err != nil || old.Outcome != msg.OutcomeForbidden {
		t.Fatal("stale connection retained body", old, err)
	}
	if foreign, err := cold.Wait(ci, bContext, msg.ViewBody, "", "", 0); err != nil || foreign.Outcome != msg.OutcomeForbidden {
		t.Fatal("cold connection read another chat", foreign, err)
	}
	if foreign, err := cold.Reply(ci, aContext, qb.MessageID, "foreign"); err != nil || foreign.Outcome != msg.OutcomeForbidden {
		t.Fatal("cold connection replied for another chat", foreign, err)
	}
	againB := b.headersWait(bContext, wb.Revision, 0)
	if againB.Wake != wb.Wake {
		t.Fatal("A reconnect changed B wake", wb, againB)
	}
	readA, err := cold.Wait(ci, aContext, msg.ViewBody, "", "", 0)
	if err != nil || len(readA.Messages) != 1 || readA.Messages[0].Header.MessageID != qa.MessageID {
		t.Fatal("cold connection lost A work", readA, err)
	}
	if out, err := cold.Reply(ci, aContext, qa.MessageID, "Claude A answer"); err != nil || out.Outcome != msg.OutcomeSent {
		t.Fatal(out, err)
	}
	readB, err := b.Wait(bi, bContext, msg.ViewBody, "", "", 0)
	if err != nil || len(readB.Messages) != 1 || readB.Messages[0].Header.MessageID != qb.MessageID {
		t.Fatal("B lost independent work", readB, err)
	}
	if out, err := b.Reply(bi, bContext, qb.MessageID, "Claude B answer"); err != nil || out.Outcome != msg.OutcomeSent {
		t.Fatal(out, err)
	}
	answers, err := other.Wait(oi, actor.Context, msg.ViewBody, "", "", 0)
	if err != nil || !chatPageHasReply(answers, "Claude A answer") || !chatPageHasReply(answers, "Claude B answer") {
		t.Fatal(answers, err)
	}
}

func TestMessagesPersonInboxInventoryRequiresOperatorAndBindsCursor(t *testing.T) {
	f := messagesObserveFixture(t)
	if out, err := f.reader.InboxesForPerson(""); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("ordinary agent listed person metadata", out, err)
	}
	_, chat := messagesTypedChat(t, f, f.other, "other-agent", "one", "Named chat")
	messagesPersonPermitOperator(t, f)
	page, err := f.reader.InboxesForPerson("")
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Inboxes) != 1 || page.Inboxes[0].Context != chat || page.Inboxes[0].ChatTitle != "Named chat" {
		t.Fatal(page, err)
	}
	for n := 0; n < 64; n++ {
		messagesTypedChat(t, f, f.other, "other-agent", fmt.Sprintf("more-%d", n), "Another chat")
	}
	first, err := f.reader.InboxesForPerson("")
	if err != nil || len(first.Inboxes) != 64 || first.Cursor == "" {
		t.Fatal("native inventory did not page", first, err)
	}
	messagesTypedChat(t, f, f.other, "other-agent", "one", "Renamed on takeover")
	if stale, err := f.reader.InboxesForPerson(first.Cursor); err != nil || stale.Outcome != msg.OutcomeGap || len(stale.Inboxes) != 0 {
		t.Fatal("changed snapshot cursor disclosed metadata", stale, err)
	}
	if foreign, err := f.other.InboxesForPerson("foreign.snapshot.0"); err != nil || foreign.Outcome != msg.OutcomeForbidden {
		t.Fatal(foreign, err)
	}
	if stale, err := f.reader.InboxesForPerson("foreign.snapshot.0"); err != nil || stale.Outcome != msg.OutcomeGap {
		t.Fatal(stale, err)
	}
	calls := 0
	f.run.d.operator = func(string) bool { calls++; return calls == 1 }
	if revoked, err := f.reader.InboxesForPerson(""); err != nil || revoked.Outcome != msg.OutcomeForbidden || len(revoked.Inboxes) != 0 || calls < 2 {
		t.Fatal("operator was not rechecked before disclosure", revoked, err, calls)
	}
	f.run.d.operator = func(string) bool { return false }
	if revoked, err := f.reader.InboxesForPerson(""); err != nil || revoked.Outcome != msg.OutcomeForbidden || len(revoked.Inboxes) != 0 {
		t.Fatal("revoked operator disclosed metadata", revoked, err)
	}
}

// Architect ruling B: waiting notes wake an idle chat once, the woken turn
// reads them all, and acknowledged notes stop waking it.
func TestMessagesNotesWakeOnceUntilAcknowledged(t *testing.T) {
	f := messagesObserveFixture(t)
	instance, chat := messagesTypedChat(t, f, f.reader, "native-agent", "note-chat", "Note chat")
	messagesStateOpen(t, f, f.other, "other-agent", "sender")
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "sender"}
	for i, body := range []string{"first note", "second note"} {
		request := msg.SendRequest{To: "app:native-agent", Context: chat, Kind: msg.KindNote, Body: body, IdempotencyKey: fmt.Sprintf("note-%d", i), ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
		if sent := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, ""); sent.Outcome != msg.OutcomeSent {
			t.Fatal(sent)
		}
	}
	woken := f.reader.headersWait(chat, "", 0)
	if woken.Outcome != msg.OutcomePage || woken.Wake == "" || len(woken.Headers) != 2 {
		t.Fatal("waiting notes did not wake the chat", woken)
	}
	if again := f.reader.headersWait(chat, "", 0); again.Wake != woken.Wake {
		t.Fatal("two waiting notes produced a second wake", woken.Wake, again.Wake)
	}
	page, err := f.reader.Wait(instance, chat, msg.ViewBody, "", "", 0)
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Messages) != 2 {
		t.Fatal("the woken turn did not read every note", page, err)
	}
	if _, err := f.reader.Wait(instance, chat, msg.ViewBody, page.Cursor, "", 0); err != nil {
		t.Fatal(err)
	}
	if quiet := f.reader.headersWait(chat, "", 0); quiet.Outcome != msg.OutcomePage || quiet.Wake != "" {
		t.Fatal("acknowledged notes still wake the chat", quiet)
	}
}
