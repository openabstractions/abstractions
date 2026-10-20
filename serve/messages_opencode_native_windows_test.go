//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
)

// Actual disposable native peers use the same registration/SetAgentEnrollment/
// person Keep owners as Connect. Stock metadata supplies no helper authority.
// No installed OpenCode binary, provider, UI or vendor session is launched.
func TestOpenCodeNativeMessagesUsesKeptHelperWithoutStockEnrollment(t *testing.T) {
	f := messagesObserveFixtureNamed(t, "oa-opencode-integration", "oa-opencode-integration.exe")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	stock := filepath.Join(t.TempDir(), "OpenCode.exe")
	if err := os.WriteFile(stock, raw, 0700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, f.run, "opencode", identity.NormalizeSubjectProgram(stock))
	stockCall, _ := messages1608CallImage(t, stock)
	stockReader := &messagesReceiver{host: f.reader.host, call: stockCall, ctx: f.run.ctx}
	pin, err := stockCall.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: "opencode", LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "stock-context"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	if opened, err := stockReader.InboxOpen(presence.Instance, "stock-context", ""); err != nil || opened.Outcome != msg.OutcomeForbidden {
		t.Fatal("stock metadata became Messages authority", opened, err)
	}
	if out, err := stockReader.Prepare(presence.Instance, "stock-context", msg.PrepareRequest{Receiver: "app:native-agent", Body: "ping"}); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal(out, err)
	}
	enrolled := f.run.d.currentAgentEnrollmentImage(f.run.ctx, "opencode", nil)
	if enrolled.Outcome != applications.OutcomePage || enrolled.Enrollment == nil || enrolled.Enrollment.State != applications.AgentEnrollmentStateNone {
		t.Fatal("stock was enrolled", enrolled)
	}
	firstCall, stopFirst := messages1608CallImage(t, f.otherProgram)
	firstReader := &messagesReceiver{host: f.other.host, call: firstCall, ctx: f.run.ctx}
	first, contextA := messagesTypedChatAt(t, f, firstReader, "oa-opencode-integration", "ses_A opaque host id", "", "source-a")
	helper := messagesStateClient(firstReader)
	secondCall, _ := messages1608CallImage(t, f.otherProgram)
	secondReader := &messagesReceiver{host: f.other.host, call: secondCall, ctx: f.run.ctx}
	second, contextB := messagesTypedChatAt(t, f, secondReader, "oa-opencode-integration", "ses_B opaque host id", "", "source-b")
	otherHelper := messagesStateClient(secondReader)
	if first == second || contextA == contextB {
		t.Fatal("two native processes share an occurrence")
	}
	senderInstance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	prepared, err := helper.Prepare(first, "source-a", msg.PrepareRequest{Receiver: "app:native-agent", Body: "ping"})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
		t.Fatal(prepared, err)
	}
	sent, err := helper.SendChat(first, "source-a", msg.ChatSendRequest{Request: *prepared.SendRequest, ReturnContext: contextA})
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	// A different native connection cannot select the first one's context.
	if page, err := sender.Wait(senderInstance, "source", msg.ViewBody, "", "", 0); err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	if reply, err := sender.Reply(senderInstance, "source", sent.MessageID, "pong for A"); err != nil || reply.Outcome != msg.OutcomeSent {
		t.Fatal(reply, err)
	}
	if page, err := helper.Wait(first, contextA, msg.ViewBody, "", "", 0); err != nil || len(page.Messages) != 1 || page.Messages[0].Body != "pong for A" {
		t.Fatal(page, err)
	}
	if page, err := otherHelper.Wait(second, contextA, msg.ViewBody, "", "", 0); err != nil || page.Outcome != msg.OutcomeForbidden {
		t.Fatal("connection crossed owner", page, err)
	}
	if page, err := otherHelper.Wait(second, contextB, msg.ViewBody, "", "", 0); err != nil || page.Outcome != msg.OutcomePage || len(page.Messages) != 0 {
		t.Fatal(page, err)
	}
	stopFirst()
	queued, err := sender.Send(senderInstance, "source", msg.SendRequest{To: "app:oa-opencode-integration", Context: contextA, Kind: msg.KindNote, Body: "queued while A closed", IdempotencyKey: "closed-a", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()})
	if err != nil || queued.Outcome != msg.OutcomeSent {
		t.Fatal(queued, err)
	}
	respawnCall, _ := messages1608CallImage(t, f.otherProgram)
	respawn := &messagesReceiver{host: f.other.host, call: respawnCall, ctx: f.run.ctx}
	newInstance, newContext := messagesTypedChatAt(t, f, respawn, "oa-opencode-integration", "ses_A opaque host id", "", "respawn-source-a")
	if newContext != contextA {
		t.Fatal("respawn changed canonical chat", newContext, contextA)
	}
	page, err := messagesStateClient(respawn).Wait(newInstance, newContext, msg.ViewBody, "", "", 0)
	found := false
	for _, message := range page.Messages {
		if message.Header.MessageID == queued.MessageID && message.Body == "queued while A closed" {
			found = true
		}
	}
	if err != nil || page.Outcome != msg.OutcomePage || !found {
		t.Fatal("respawn lost queued message", page, err)
	}
}
