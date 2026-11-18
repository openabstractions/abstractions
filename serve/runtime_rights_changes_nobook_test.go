package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
)

// Security review 2026-11-22, LOW: without the asks Book the gate admits the
// Panel window alone: no exemption, no request.
func TestChangeGateWithoutBookAdmitsOnlyThePanel(t *testing.T) {
	c, r := changeGateUnit(t, false)
	if c == nil {
		t.Fatal("no gate was composed without the Book")
	}
	r.peerElevated = func(*identity.Peer) bool { return true }
	peer := &identity.Peer{}
	if err := c.Admit(context.Background(), peer, r.self()); !errors.Is(err, rightsservice.ErrOperatorForbidden) {
		t.Fatalf("an elevated caller was admitted without the Book: %v", err)
	}
	rule := rwire.PolicyRule{Subject: rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "app.exe")}, Action: host.ModelLookupAction, Resource: "hf", Permit: true}
	if got := c.Request(context.Background(), peer, r.self(), rwire.PolicyChange{Kind: rwire.ChangeKindSetRule, Rule: &rule}, time.Millisecond).State.Outcome; got != rwire.ChangeRequestOutcomeUnavailable {
		t.Fatalf("a request was held without the Book: %v", got)
	}
	if err := c.authorizeAnswer(context.Background(), peer); err == nil {
		t.Fatal("an answer was admitted without the Book")
	}
	r.operatorDirect = func(*identity.Peer) bool { return true }
	if err := c.Admit(context.Background(), peer, r.self()); err != nil {
		t.Fatalf("the Panel window was refused: %v", err)
	}
}
