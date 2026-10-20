package main

import (
	"context"
	"path/filepath"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-identity/hostfixture"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// ASK-R7, TD83: a question the gateway subject's refusal raised, with the
// stock OpenCode binary enrolled as the answerer, reads forbidden. Stock
// OpenCode is a Bun standalone executable that runs any script under
// BUN_BE_BUN and keeps Bun's version resource; it runs foreign code, so it
// never answers for other applications, whatever its enrollment. A plain
// enrolled program answers the same question.
func TestGatewayRequesterEnrolledOpenCodeAnswererReadsForbidden(t *testing.T) {
	f := newAnswererFixture(t)
	dir := filepath.Dir(f.cli.Program)
	opencode := rwire.Subject{Account: f.answerer.Account, Program: hostfixture.Bun(t, dir, "OpenCode.exe", true)}
	bunResource := asks.Publisher{Subject: "Anomaly Innovations, Inc", Trusted: true, OriginalName: "bun.exe", Description: "Bun"}
	gateway := rwire.Subject{Account: f.answerer.Account, Program: filepath.Join(dir, "openabstractions-mcp-gateway.exe")}
	// The enrollment refusal reads the same rule: the Panel cannot enroll it.
	if edit := f.enroll(t, opencode); edit.Outcome != rwire.PolicyEditOutcomeInvalid {
		t.Fatalf("enrolling the stock OpenCode binary: %v", edit.Outcome)
	}
	// A rule written before this check, or around it, still answers nothing.
	f.a.rights.policy.MetaHolder = nil
	f.enroll(t, opencode)

	q := f.ask(t, gateway, inference.ActionComplete, "host:lemonade")
	stock := answerer{subject: opencode, publisher: bunResource}
	if f.answerable(stock, q, "allow") {
		t.Error("the enrolled stock OpenCode binary is offered the gateway's question")
	}
	if outcome, _ := f.answer(stock, q.ID, "allow"); outcome != "forbidden" {
		t.Fatalf("the enrolled stock OpenCode binary answered the gateway's question: %s", outcome)
	}
	if got := f.a.askForAnswering(context.Background(), stock).Outcome; got != rwire.DecisionOutcomeForbidden {
		t.Errorf("the stock OpenCode binary's request to answer reads %s", got)
	}
	plain := newAnswererFixture(t)
	plain.enroll(t, plain.answerer)
	if !plain.answerable(plain.me(), plain.ask(t, gateway, inference.ActionComplete, "host:lemonade"), "allow") {
		t.Fatal("a plain enrolled answerer is refused the same question")
	}
	// The same binary is no shared interpreter: the account-wide defaults
	// still reach it (RIGHTS-R22).
	if asks.ScriptHostSubject(opencode, bunResource) {
		t.Error("the stock OpenCode binary reads as a shared interpreter and loses its defaults")
	}
}
