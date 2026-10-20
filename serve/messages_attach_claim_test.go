package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// VISION 2026-10-31, gate (a): the runtime authenticates the sending program
// through native enrollment and a claimed program name never admits. A
// question whose sender names the enrolled "sender" but runs another image,
// or names the receiver's own application, starts nothing and asks the person
// nothing, even while the real sender holds the start permit.
func TestMessageStartRefusesClaimedProgramName(t *testing.T) {
	h, key, r, calls, _ := chatStartFixture(t)
	allowMessageStartFixture(t, h, key, r)
	target, err := h.directory.prepareMessageStart(context.Background(), "receiver")
	if err != nil {
		t.Fatal(err)
	}
	if _, admitted := h.messageStartConsent(context.Background(), r, target); !admitted {
		t.Fatal("enrolled sender not admitted")
	}
	impostor := filepath.Join(t.TempDir(), "sender.exe")
	for name, actor := range map[string]messageActor{
		"enrolled name, other image": {Account: r.Actor.Account, Program: impostor, Application: "sender", Context: "source"},
		"receiver's own name":        {Account: r.Actor.Account, Program: r.Actor.Program, Application: "receiver", Context: "source"},
	} {
		request, err := decodeRetainedMessageRequest(r.Spec)
		if err != nil {
			t.Fatal(err)
		}
		request.IdempotencyKey = "claimed-" + strings.ReplaceAll(name, " ", "-")
		if got := h.messages.accept(context.Background(), actor, r.Receiver, *request, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal(name, got)
		}
		claimedKey := h.messages.key(actor, request.IdempotencyKey)
		claimed, err := h.messages.readRecord(context.Background(), claimedKey)
		if err != nil || claimed == nil {
			t.Fatal(name, err)
		}
		if _, admitted := h.messageStartConsent(context.Background(), claimed, target); admitted {
			t.Fatal("claimed program name admitted", name)
		}
		asked := false
		h.messageRights.firstUse = func(rights.Subject, func() asks.Publisher, string, string) string {
			asked = true
			return "pending"
		}
		if err := h.progressMessageStartSettled(context.Background(), claimedKey, claimed); err != nil {
			t.Fatal(name, err)
		}
		if calls.Load() != 0 || asked {
			t.Fatal("claimed program name started a receiver or asked the person", name, calls.Load(), asked)
		}
	}
	// The enrolled sender itself still starts under its permit.
	if err := h.progressMessageStartSettled(context.Background(), key, r); err != nil || calls.Load() != 1 {
		t.Fatal("enrolled sender", err, calls.Load())
	}
}
