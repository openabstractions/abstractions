//go:build windows

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
)

// The agent-messages default (rights RIGHTS-R22, owner 2026-11-09): off, an
// agent's request asks the person; on, every pair of kept agents is permitted
// through request_access and prepare with no question; a per-pair Don't allow
// still refuses and a per-pair Allow survives the switch going off; a program
// outside the kept-agent class and a non-agent receiver still ask; no agent is
// an operator that could turn the switch.
func TestAgentMessagesDefaultCoversEveryKeptAgentPair(t *testing.T) {
	f, client, _ := messagePermissionFixture(t)
	ctx, cancel := context.WithTimeout(f.run.ctx, 90*time.Second)
	defer cancel()
	a := f.run.f.a
	const second = "second-agent"
	secondImage := enrollExtraMessageAgent(t, f, second)
	secondSubject := rwire.Subject{Account: f.self.Account, Program: identity.NormalizeSubjectProgram(secondImage)}
	agents := []struct {
		name    string
		subject rwire.Subject
	}{{"native-agent", f.self}, {"other-agent", f.otherSubject}, {second, secondSubject}}
	decide := func(s rwire.Subject, receiver string) rwire.DecisionOutcome {
		return a.rights.decide(ctx, s, ActionApplicationMessage, "app:"+receiver).Outcome
	}
	setSwitch := func(on bool) {
		t.Helper()
		revision, _, err := a.rights.policy.DefaultsSnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		edit, err := a.rights.policy.SetDefault(revision, rights.DefaultAgentMessages, on, f.run.f.panel, func() error { return nil })
		if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied || edit.Current == nil || edit.Current.On != on {
			t.Fatal(edit, err)
		}
	}
	_, listed, err := a.rights.policy.DefaultsSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range listed {
		if d.Name == rights.DefaultAgentMessages && (d.On || d.Action != ActionApplicationMessage || !d.EveryResource) {
			t.Fatal("fresh agent-messages default", d)
		}
	}
	// Off: an agent's request asks the person.
	if got := decide(f.self, "other-agent"); got != rwire.DecisionOutcomeNotGranted {
		t.Fatal("switch off decided", got)
	}
	ai, _ := messagesTypedChatAt(t, f, f.reader, "native-agent", "agents-default-chat", "", "source")
	_, _ = messagesStateOpen(t, f, f.other, "other-agent", "target")
	prepare := func() msg.PrepareResult {
		t.Helper()
		out, err := f.reader.Prepare(ai, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "target", Body: "question"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := prepare(); out.Outcome != msg.OutcomeForbidden || out.Reason != "access:other-agent" {
		t.Fatal("prepare with the switch off", out)
	}
	// Per-pair rules the person set one by one.
	if err := a.rights.policy.Set(f.self, ActionApplicationMessage, "app:"+second, false); err != nil {
		t.Fatal(err)
	}
	if err := a.rights.policy.Set(f.otherSubject, ActionApplicationMessage, "app:"+second, true); err != nil {
		t.Fatal(err)
	}
	setSwitch(true)
	for _, sender := range agents {
		for _, receiver := range agents {
			want := rwire.DecisionOutcomePermitted
			if sender.name == receiver.name {
				continue
			}
			if sender.name == "native-agent" && receiver.name == second {
				want = rwire.DecisionOutcomeDenied
			}
			if got := decide(sender.subject, receiver.name); got != want {
				t.Errorf("%s -> %s = %s, want %s", sender.name, receiver.name, got, want)
			}
		}
	}
	access, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "agents-default-on"))
	if err != nil || access.Outcome != askswire.PermissionOutcomePermitted {
		t.Fatal("request_access with the switch on", access, err)
	}
	if pending, err := a.book.PendingTypedPermissions(); err != nil || len(pending) != 0 {
		t.Fatal("the switch on still asked", pending, err)
	}
	if out := prepare(); out.Outcome != msg.OutcomePage || out.SendRequest == nil {
		t.Fatal("prepare with the switch on", out)
	}
	// Outside the class: an ordinary program and a non-agent receiver.
	ordinary := rwire.Subject{Account: f.self.Account, Program: filepath.Join(t.TempDir(), "editor.exe")}
	if got := decide(ordinary, "other-agent"); got != rwire.DecisionOutcomeNotGranted {
		t.Fatal("a program outside the kept agents was covered", got)
	}
	if got := decide(f.otherSubject, "editor"); got != rwire.DecisionOutcomeNotGranted {
		t.Fatal("a non-agent receiver was covered", got)
	}
	// No agent can turn the switch: none is an operator program.
	for _, agent := range []*messagesReceiver{f.other} {
		peer, err := agent.call.Peer()
		if err != nil {
			t.Fatal(err)
		}
		if err := a.rights.authorizeOperator(ctx, peer); !errors.Is(err, rightsservice.ErrOperatorForbidden) {
			t.Fatal("an agent was admitted as a rights operator", err)
		}
	}
	if a.rights.isOperator(secondSubject.Program) {
		t.Fatal("a kept agent is an operator program")
	}
	// Off again: the person is asked; the individual Allow stays.
	setSwitch(false)
	if got := decide(f.self, "other-agent"); got != rwire.DecisionOutcomeNotGranted {
		t.Fatal("switch off still permitted", got)
	}
	if got := decide(f.otherSubject, second); got != rwire.DecisionOutcomePermitted {
		t.Fatal("turning the switch off removed a per-pair Allow", got)
	}
	again, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "agents-default-off"))
	if err != nil || again.Outcome != askswire.PermissionOutcomePending || again.Answer == nil {
		t.Fatal("request_access with the switch off", again, err)
	}
}

// The senders are OA's agent connector helpers (oa-<party>-integration.exe),
// never a script host the defaults exclude.
func TestAgentConnectorHelpersAreNotScriptHosts(t *testing.T) {
	for _, party := range []string{"claude", "codex", "opencode"} {
		program := filepath.Join(t.TempDir(), "tools", "oa-"+party+"-integration.exe")
		if defaultExcluded(rwire.Subject{Account: "person", Program: program}) {
			t.Errorf("%s is excluded from the account defaults", program)
		}
	}
}

// agentMessagesOn turns the agent-messages switch on as the Panel would.
func agentMessagesOn(t *testing.T, f *messagesObserveRun) {
	t.Helper()
	a := f.run.f.a
	revision, _, err := a.rights.policy.DefaultsSnapshot(f.run.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if edit, err := a.rights.policy.SetDefault(revision, rights.DefaultAgentMessages, true, f.run.f.panel, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
}

// Review B1, 2026-11-09: a message accepted under the switch is delivered
// after a registration write republished the roles without messaging facts;
// the receiver's progress pass decides it again from the last prepared facts.
func TestAgentMessagesSwitchMessageSurvivesRolesRepublish(t *testing.T) {
	f, _, _ := messagePermissionFixture(t)
	agentMessagesOn(t, f)
	ai, _ := messagesTypedChatAt(t, f, f.reader, "native-agent", "switch-chat", "", "source")
	other, _ := messagesStateOpen(t, f, f.other, "other-agent", "target")
	prepared, err := f.reader.Prepare(ai, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "target", Body: "switch body"})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
		t.Fatal("prepare", prepared, err)
	}
	if out, err := f.reader.Send(ai, "source", *prepared.SendRequest); err != nil || out.Outcome != msg.OutcomeSent || out.AwaitingPermission {
		t.Fatal("send", out, err)
	}
	if roles := f.run.d.roles.Load(); roles != nil {
		cleared := *roles
		cleared.messaging = nil
		f.run.d.roles.Store(&cleared)
	}
	if got := f.run.f.a.rights.decide(f.run.ctx, f.self, ActionApplicationMessage, "app:other-agent").Outcome; got != rwire.DecisionOutcomePermitted {
		t.Fatal("cleared roles ended the switch's permit", got)
	}
	received, err := f.other.Wait(other, "target", msg.ViewBody, "", "", 0)
	if err != nil || len(received.Messages) != 1 {
		t.Fatalf("accepted message not delivered after roles republish: %+v %v", received, err)
	}
}

// Review B1: with the switch on, Prepare and Send ask nothing while
// registration writers keep clearing the messaging facts.
func TestAgentMessagesSwitchAsksNothingWhileRolesAreRepublished(t *testing.T) {
	f, _, _ := messagePermissionFixture(t)
	agentMessagesOn(t, f)
	ai, _ := messagesTypedChatAt(t, f, f.reader, "native-agent", "republish-chat", "", "source")
	_, _ = messagesStateOpen(t, f, f.other, "other-agent", "target")
	stop := clearMessagingFacts(f)
	var prepared msg.PrepareResult
	var err error
	for i := 0; i < 20; i++ {
		prepared, err = f.reader.Prepare(ai, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "target", Body: "republished body"})
		if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
			stop()
			t.Fatal("prepare while roles are republished", i, prepared, err)
		}
	}
	sent, err := f.reader.Send(ai, "source", *prepared.SendRequest)
	stop()
	if err != nil || sent.Outcome != msg.OutcomeSent || sent.AwaitingPermission {
		t.Fatal("send while roles are republished", sent, err)
	}
	if pending, err := f.run.f.a.book.PendingTypedPermissions(); err != nil || len(pending) != 0 {
		t.Fatal("the switch on still asked", pending, err)
	}
	if pending := f.run.f.a.book.List(true); len(pending) != 0 {
		t.Fatal("a first-use question was asked", pending)
	}
}
