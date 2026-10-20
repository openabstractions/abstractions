package main

import (
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Counterexample to autonomous late-UI continuation after a cold body ACK.
// The native takeover probe proves original-ID reply authority survives, while
// this new adapter has not read the acknowledged body on its new connection.
func TestColdReplyAcknowledgedHandoffAcceptanceCounterexample(t *testing.T) {
	cold, _ := reply1884Fixture()
	page, err := cold.MessagesWait(t.Context(), gateway.MessagesWaitInput{WaitMs: 0})
	if err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	id := page.Messages[0].Header.MessageID
	late, native := reply1884Fixture()
	native.page = msg.Page{Outcome: msg.OutcomePage, Messages: []msg.Message{}, Headers: []msg.Header{}}
	if page, err := late.MessagesWait(t.Context(), gateway.MessagesWaitInput{WaitMs: 0}); err != nil || len(page.Messages) != 0 {
		t.Fatal(page, err)
	}
	out, err := late.MessagesReply(t.Context(), gateway.MessagesReplyInput{MessageID: id, Body: "same-session answer"})
	if err != nil || out.Outcome != msg.OutcomeSent {
		t.Fatalf("late UI cannot continue original pending question: %+v %v", out, err)
	}
}
