//go:build windows

package main

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Architect ruling P3, 2026-11-22 (decisions/RULING-2026-11-22-P3-choose-chat.md,
// owner decision 7), through the runtime Messages service: a send that names
// only the program reaches its one live chat; with several, OA sends nothing
// and the sending agent chooses among the live chats, shown with hints.

// chooseChatRun opens two live Claude chats, Pictures first and Reports one
// second later, and returns their contexts. Reports' title is longer than 60
// characters.
func chooseChatRun(t *testing.T) (r *routingRun, picturesInstance, pictures, reportsInstance, reports string) {
	t.Helper()
	r = newRoutingRun(t)
	clock := r.f.run.d.now
	var offset atomic.Int64
	r.f.run.d.now = func() time.Time { return clock().Add(time.Duration(offset.Load())) }
	picturesInstance, pictures = messagesTypedChatAt(t, r.f, r.f.other, routingClaude, "chat-pictures", "Pictures", "pictures-source")
	offset.Add(int64(time.Second))
	reportsInstance, reports = messagesTypedChatAt(t, r.f, r.f.other, routingClaude, "chat-reports", chooseChatLongTitle, "reports-source")
	return
}

const chooseChatLongTitle = "Quarterly report for the northern region, second draft with notes"

// chooseChatMessages reads one Claude chat's inbox.
func chooseChatMessages(t *testing.T, r *routingRun, instance, context string) []msg.Message {
	t.Helper()
	page, err := r.f.other.Wait(instance, context, msg.ViewBody, "", "", 0)
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal("chat inbox", page, err)
	}
	return page.Messages
}

func chooseChatHinted(choices []msg.Inbox, hint string) []string {
	var contexts []string
	for _, choice := range choices {
		if slices.Contains(choice.Hints, hint) {
			contexts = append(contexts, choice.Context)
		}
	}
	return contexts
}

// Ruling test 1: two live Claude chats, send by program name.
func TestMessagesChooseChatSeveralLiveChatsSendNothingAndHintLastExchanged(t *testing.T) {
	r, picturesInstance, pictures, reportsInstance, reports := chooseChatRun(t)
	// The sender's earlier note to Pictures makes it the chat this sender last
	// exchanged a message with.
	earlier := r.prepare(pictures)
	if earlier.SendRequest == nil {
		t.Fatal(earlier)
	}
	earlier.SendRequest.Kind, earlier.SendRequest.AnswerByUnixMs = msg.KindNote, 0
	if sent := r.send(*earlier.SendRequest); sent.Outcome != msg.OutcomeSent || sent.Context != pictures {
		t.Fatal(sent)
	}
	out := r.prepare("")
	if out.Outcome != msg.OutcomeConflict || out.Reason != "ambiguous_receiver" || out.SendRequest != nil || out.ChatTitle != "" || len(out.Choices) != 2 {
		t.Fatal("several live chats did not return the choices", out)
	}
	// Most recently opened first; each row names program, context and title.
	reportsRow, picturesRow := out.Choices[0], out.Choices[1]
	if reportsRow.Context != reports || picturesRow.Context != pictures || reportsRow.Program != "app:"+routingClaude || picturesRow.Program != "app:"+routingClaude {
		t.Fatal("choice order or rows", out.Choices)
	}
	if picturesRow.ChatTitle != "Pictures" || reportsRow.ChatTitle != string([]rune(chooseChatLongTitle)[:60]) {
		t.Fatal("choice titles", out.Choices)
	}
	if hinted := chooseChatHinted(out.Choices, messageHintLastExchanged); !slices.Equal(hinted, []string{pictures}) || len(reportsRow.Hints) != 0 {
		t.Fatal("last_exchanged is not on the chat this sender last messaged", out.Choices)
	}
	// Nothing was sent: Reports holds nothing and Pictures only the earlier note.
	if got := chooseChatMessages(t, r, reportsInstance, reports); len(got) != 0 {
		t.Fatal("an ambiguous send delivered", got)
	}
	if got := chooseChatMessages(t, r, picturesInstance, pictures); len(got) != 1 {
		t.Fatal("an ambiguous send delivered", got)
	}
}

// Ruling test 2: the resend with one row's context is delivered there, and
// the result names it.
func TestMessagesChooseChatResendWithChosenContextDelivers(t *testing.T) {
	r, picturesInstance, pictures, reportsInstance, reports := chooseChatRun(t)
	out := r.prepare("")
	if out.Outcome != msg.OutcomeConflict || len(out.Choices) != 2 || out.Choices[0].Context != reports {
		t.Fatal(out)
	}
	chosen := r.prepare(out.Choices[0].Context)
	if chosen.Outcome != msg.OutcomePage || chosen.SendRequest == nil || chosen.SendRequest.Context != reports || chosen.ChatTitle != chooseChatLongTitle || len(chosen.Choices) != 0 {
		t.Fatal("the chosen context did not prepare its chat", chosen)
	}
	sent := r.send(*chosen.SendRequest)
	if sent.Outcome != msg.OutcomeSent || sent.Context != reports || sent.ChatTitle != chooseChatLongTitle || sent.MessageID == "" {
		t.Fatal("the resend was not delivered to and named by the chosen chat", sent)
	}
	if got := chooseChatMessages(t, r, reportsInstance, reports); len(got) != 1 || got[0].Header.MessageID != sent.MessageID {
		t.Fatal("chosen chat inbox", got)
	}
	if got := chooseChatMessages(t, r, picturesInstance, pictures); len(got) != 0 {
		t.Fatal("the other chat received the message", got)
	}
}

// Ruling test 3: one live chat receives directly, with no choices, while a
// closed chat of the same program stays out of the count.
func TestMessagesChooseChatOneLiveChatReceivesDirectly(t *testing.T) {
	r := newRoutingRun(t)
	r.closed("closed-one", "Old taxes")
	liveInstance, live := r.open("live-one", "Pictures")
	prepared := r.prepare("")
	if prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil || prepared.SendRequest.Context != live || prepared.ChatTitle != "Pictures" || len(prepared.Choices) != 0 {
		t.Fatal("the one live chat did not receive directly", prepared)
	}
	sent := r.send(*prepared.SendRequest)
	if sent.Outcome != msg.OutcomeSent || sent.Context != live || sent.ChatTitle != "Pictures" {
		t.Fatal(sent)
	}
	if got := chooseChatMessages(t, r, liveInstance, live); len(got) != 1 {
		t.Fatal(got)
	}
}

// Ruling test 4: a reply by message_id reaches the asking chat while several
// chats are live. A chat that sent this sender a message is the last
// exchanged one too.
func TestMessagesChooseChatReplyByMessageIDReachesAskingChat(t *testing.T) {
	r, picturesInstance, pictures, reportsInstance, reports := chooseChatRun(t)
	note := r.prepare(reports)
	if note.SendRequest == nil {
		t.Fatal(note)
	}
	if sent := r.send(*note.SendRequest); sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	*r.f.now = r.f.now.Add(time.Second)
	asker := messagesStateClient(r.f.other)
	question := msg.SendRequest{To: "app:native-agent", Context: "source", Kind: msg.KindQuestion, Body: "Which branch holds the fix?", IdempotencyKey: "pictures-question", ExpiresUnixMs: r.f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: r.f.now.Add(time.Minute).UnixMilli()}
	asked, err := asker.SendChat(picturesInstance, "pictures-source", msg.ChatSendRequest{Request: question, ReturnContext: pictures})
	if err != nil || asked.Outcome != msg.OutcomeSent {
		t.Fatal("chat question", asked, err)
	}
	if out := r.prepare(""); out.Outcome != msg.OutcomeConflict || !slices.Equal(chooseChatHinted(out.Choices, messageHintLastExchanged), []string{pictures}) {
		t.Fatal("the chat that last messaged this sender lacks last_exchanged", out)
	}
	reply, err := r.sender.Reply(r.si, "source", asked.MessageID, "The fix is on main.")
	if err != nil || reply.Outcome != msg.OutcomeSent {
		t.Fatal("reply", reply, err)
	}
	replied := false
	for _, message := range chooseChatMessages(t, r, picturesInstance, pictures) {
		replied = replied || message.Header.Kind == msg.KindReply && message.Body == "The fix is on main."
	}
	if !replied {
		t.Fatal("the reply did not reach the asking chat")
	}
	for _, message := range chooseChatMessages(t, r, reportsInstance, reports) {
		if message.Header.Kind == msg.KindReply {
			t.Fatal("the reply reached another live chat", message)
		}
	}
}

// Ruling test 5: without the person-typed signal no row carries last_typed,
// and no hint appears on more than one row.
func TestMessagesChooseChatWithoutTypedSignalNoLastTyped(t *testing.T) {
	r, _, pictures, _, reports := chooseChatRun(t)
	for _, context := range []string{pictures, reports} {
		prepared := r.prepare(context)
		if prepared.SendRequest == nil {
			t.Fatal(prepared)
		}
		if sent := r.send(*prepared.SendRequest); sent.Outcome != msg.OutcomeSent {
			t.Fatal(sent)
		}
	}
	out := r.prepare("")
	if out.Outcome != msg.OutcomeConflict || len(out.Choices) != 2 {
		t.Fatal(out)
	}
	if typed := chooseChatHinted(out.Choices, messageHintLastTyped); len(typed) != 0 {
		t.Fatal("last_typed without its signal", out.Choices)
	}
	seen := map[string]bool{}
	for _, choice := range out.Choices {
		for _, hint := range choice.Hints {
			if seen[hint] || hint != messageHintLastExchanged && hint != messageHintLastTyped {
				t.Fatal("a hint repeats or leaves the vocabulary", out.Choices)
			}
			seen[hint] = true
		}
	}
	if !seen[messageHintLastExchanged] {
		t.Fatal("last_exchanged missing", out.Choices)
	}
}
