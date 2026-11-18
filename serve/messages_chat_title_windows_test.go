//go:build windows

package main

import (
	"strings"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesPrepareChatTitlesRemainUntrustedAndAmbiguous(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	routes := map[string]string{}
	open := func(chat, title string) {
		t.Helper()
		_, route := messagesTypedChat(t, f, f.other, "other-agent", chat, title)
		routes[chat] = route
	}
	open("one", "Review\u202e\n plan")
	open("unnamed", "")
	longTitle := strings.Repeat("😀", 200)
	open("long-title", longTitle)
	prepare := func(title, context string) msg.PrepareResult {
		t.Helper()
		out, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: routes[context], ContextTitle: title, Body: "question"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := prepare("Review plan", ""); out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != routes["one"] {
		t.Fatal(out)
	}
	if out := prepare(longTitle, ""); out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != routes["long-title"] {
		t.Fatal("maximum scalar/byte title did not select", out)
	}
	if out := prepare("Review\u202e plan", ""); out.Outcome != msg.OutcomeInvalid {
		t.Fatal("unsafe title accepted", out)
	}
	if out := prepare("Unnamed chat", ""); out.Outcome != msg.OutcomeUnavailable {
		t.Fatal("display fallback became a routing claim", out)
	}
	open("two", "Review plan")
	if out := prepare("Review plan", ""); out.Outcome != msg.OutcomeConflict || out.SendRequest != nil {
		t.Fatal("duplicate title selected a chat", out)
	}
	if out := prepare("Review plan", "two"); out.Outcome != msg.OutcomePage || out.SendRequest.Context != routes["two"] {
		t.Fatal("explicit duplicate choice failed", out)
	}
	if out := prepare("Review plan", "unnamed"); out.Outcome != msg.OutcomeUnavailable {
		t.Fatal("context/title mismatch accepted", out)
	}
}

func TestMessagesPrepareClaudeFolderLabelsInActualChoices(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	for _, claim := range []struct{ id, title string }{{"opaque-claude-one", "Pictures"}, {"opaque-claude-two", "Reports"}} {
		messagesTypedChat(t, f, f.other, "other-agent", claim.id, claim.title)
	}
	prepare := func(title string) msg.PrepareResult {
		out, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", ContextTitle: title, Body: "ping"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := prepare("Pictures")
	if out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.ChatTitle != "Pictures" {
		t.Fatal(out)
	}
	messagesTypedChat(t, f, f.other, "other-agent", "opaque-claude-three", "Pictures")
	if duplicate := prepare("Pictures"); duplicate.Outcome != msg.OutcomeConflict || len(duplicate.Choices) != 2 || duplicate.SendRequest != nil {
		t.Fatal(duplicate)
	}
}

// A Watch on the earlier chat leaves the newest opened chat first among the
// choices (ruling P3).
func TestMessagesPrepareWatchingDoesNotChangeOpenTimeOrder(t *testing.T) {
	const application = "oa-claude-integration"
	const title = "AI abstractions"
	f := messagesObserveFixtureNamed(t, application, application+".exe")
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	dormantInstance, dormant := messagesTypedChat(t, f, f.other, application, "dormant-chat", title)
	firstInstance, first := messagesTypedChat(t, f, f.other, application, "first-chat", title)
	request := msg.PrepareRequest{Receiver: "app:" + application, Body: "question"}
	prepare := func(context, requestedTitle string) msg.PrepareResult {
		t.Helper()
		in := request
		in.Context, in.ContextTitle = context, requestedTitle
		out, err := sender.Prepare(si, "source", in)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	watch := func(instance, context string) {
		t.Helper()
		actor, _, _, ok := f.other.owner(instance, context)
		if !ok {
			t.Fatal("watch owner missing", context)
		}
		stop, err := f.other.watchMessages(actor)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(stop)
	}
	if out := prepare("", ""); out.Outcome != msg.OutcomeConflict || out.SendRequest != nil || len(out.Choices) != 2 || out.Choices[0].Context != first {
		t.Fatal("newest inbox is not the first choice", out)
	}
	watch(dormantInstance, dormant)
	if out := prepare("", ""); out.Outcome != msg.OutcomeConflict || out.SendRequest != nil || len(out.Choices) != 2 || out.Choices[0].Context != first {
		t.Fatal("watch on earlier chat changed the choice order", out)
	}
	watch(firstInstance, first)
	out := prepare("", title)
	if out.Outcome != msg.OutcomeConflict || len(out.Choices) != 2 {
		t.Fatal("requested title should retain duplicate-title choices", out)
	}
	for _, choice := range out.Choices {
		if (choice.Context != first && choice.Context != dormant) || !strings.HasPrefix(choice.ChatTitle, title+" · ") ||
			!choice.Watching || choice.LastSeenUnixMs <= 0 || choice.Reach == nil || *choice.Reach != msg.InboxReachOpen {
			t.Fatal("choice lacks the authorized connection observation", choice)
		}
	}
	if out := prepare(dormant, title); out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != dormant {
		t.Fatal("explicit dormant choice should still prepare", out)
	}
	for _, receiver := range []*messagesReceiver{{remote: &messageRemoteCall{}}, {selectedPeer: "paired-peer"}} {
		out, err := receiver.Prepare(si, "source", request)
		if err != nil || out.Outcome != msg.OutcomeForbidden || out.SendRequest != nil || len(out.Choices) != 0 {
			t.Fatal("remote preparation must remain forbidden", out, err)
		}
	}
}

func TestMessagesConfirmationTitleFollowsExactContextAcrossRename(t *testing.T) {
	f := messagesObserveFixture(t)
	const sourceApp, targetApp = "native-agent", "other-agent"
	senderInstance, senderContext := messagesTypedChat(t, f, f.reader, sourceApp, "sender-chat", "Asker")
	targetInstance, targetContext := messagesTypedChat(t, f, f.other, targetApp, "target-chat", "Greeting")
	messagesTypedChat(t, f, f.other, targetApp, "another-chat", "Greeting")

	prepare := func(body string) msg.PrepareResult {
		t.Helper()
		out, err := f.reader.Prepare(senderInstance, senderContext, msg.PrepareRequest{
			Receiver: "app:" + targetApp, Context: targetContext, Body: body,
		})
		if err != nil || out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != targetContext {
			t.Fatal(out, err)
		}
		return out
	}
	first := prepare("first question")
	sent, err := f.reader.Send(senderInstance, senderContext, *first.SendRequest)
	if err != nil || sent.Outcome != msg.OutcomeSent || sent.Context != targetContext ||
		first.ChatTitle == "Greeting" || sent.ChatTitle != first.ChatTitle {
		t.Fatal(first, sent, err)
	}

	second := prepare("second question")
	targetInstance, renamedContext := messagesTypedChat(t, f, f.other, targetApp, "target-chat", "Renamed")
	if renamedContext != targetContext {
		t.Fatal("rename changed the receiving context", renamedContext, targetContext)
	}
	sent, err = f.reader.Send(senderInstance, senderContext, *second.SendRequest)
	if err != nil || sent.Outcome != msg.OutcomeSent || sent.Context != targetContext || sent.ChatTitle != "Renamed" {
		t.Fatal(second, sent, err)
	}
	_, renamedSender := messagesTypedChat(t, f, f.reader, sourceApp, "sender-chat", "Renamed asker")
	replied, err := f.other.Reply(targetInstance, targetContext, sent.MessageID, "answer")
	if err != nil || replied.Outcome != msg.OutcomeSent || replied.Context != senderContext ||
		renamedSender != senderContext || replied.ChatTitle != "Renamed asker" {
		t.Fatal(replied, err)
	}
}
