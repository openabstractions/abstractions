//go:build windows

package main

import (
	"strings"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestNamedMessagesRetainsExactPeerGeneration(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	instance, source := f.sender(t, 0)
	_, generation, _, err := messagePeerSnapshot(t.Context(), f.states[0], f.keys[1])
	if err != nil {
		t.Fatal(err)
	}
	receiver := *f.nodes[0].reader
	receiver.selectedPeer, receiver.selectedGeneration = f.keys[1], generation
	client := msg.NewMessagesClient(&msg.MessagesDispatcher{Handler: &receiver})
	request := f.request(0, "named-machine")
	request.PeerKey = ""
	out, err := client.Send(instance, source, request)
	if err != nil || out.Outcome != msg.OutcomeSent {
		t.Fatalf("named send: %+v %v", out, err)
	}
	request.PeerKey = strings.Repeat("f", 64)
	if out, err := client.Send(instance, source, request); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatalf("foreign selector: %+v %v", out, err)
	}
	request.PeerKey = ""
	receiver.selectedGeneration = strings.Repeat("e", 64)
	if out, err := client.Send(instance, source, request); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatalf("old generation: %+v %v", out, err)
	}
	page, err := client.InboxesList(msg.InboxQuery{Receiver: "app:other-agent"})
	if err != nil || page.Outcome == msg.OutcomePage || len(page.Inboxes) != 0 {
		t.Fatalf("old generation list: %+v %v", page, err)
	}
}
