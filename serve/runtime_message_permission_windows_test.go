//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func messagePermissionFixture(t *testing.T) (*messagesObserveRun, *askclient.PermissionClient, *askclient.PermissionOperator) {
	t.Helper()
	return messagePermissionFixtureFor(t, messagesObserveFixture(t, true), "permission")
}

func messagePermissionFixtureFor(t *testing.T, f *messagesObserveRun, pipe string) (*messagesObserveRun, *askclient.PermissionClient, *askclient.PermissionOperator) {
	t.Helper()
	a := f.run.f.a
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		t.Fatal(err)
	}
	a.rights.policy.Definitions = catalog
	endpoint := testPipe("message-access", pipe)
	h, err := asksservice.Listen(endpoint, a.book)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePermission(a.preparePermission); err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePermissionObservation(a.observePermission); err != nil {
		t.Fatal(err)
	}
	if err = h.EnableOperator(func(context.Context, *identity.Peer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(context.Background()) }()
	t.Cleanup(func() {
		h.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("permission host did not drain")
		}
	})
	return f, askclient.NewPermission(endpoint), askclient.NewPermissionOperator(endpoint)
}

func messagePermissionIntent(name, key string) askswire.PermissionIntent {
	return askswire.PermissionIntent{RequestKey: key, Selection: &rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: grants.ApplicationMessageAction, ResourceProfile: grants.AgentMessageResourceProfile}, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindOne, Target: applications.EncodeNamedApplicationTarget(&applications.NamedApplicationTarget{Name: name})}}}
}

// enrollExtraMessageAgent registers and keeps one more agent image, as the
// person would through Setup, and refreshes the messaging roster.
func enrollExtraMessageAgent(t *testing.T, f *messagesObserveRun, name string) string {
	t.Helper()
	image := filepath.Join(t.TempDir(), name+"-message-agent.exe")
	raw, err := os.ReadFile(f.otherProgram)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, raw, 0700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, f.run, name, identity.NormalizeSubjectProgram(image))
	agent1478Set(t, f.run, name, "", true)
	enrollment := agent1478Question(t, f.run, name)
	answer(t, f.run.f, enrollment.ID, "keep")
	agent1478Wait(t, f.run, name, "enrolled")
	if err := f.run.d.refreshMessageParties(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	return image
}

// The one pending typed question and the receivers it names.
func pendingMessageQuestion(t *testing.T, f *messagesObserveRun) (string, []string) {
	t.Helper()
	pending, err := f.run.f.a.book.PendingTypedPermissions()
	if err != nil || len(pending) != 1 {
		t.Fatal("expected one pending question", pending, err)
	}
	names, ok := grants.AgentMessageReceivers(pending[0].TypedPermission.Effect)
	if !ok {
		t.Fatal("pending question is not a message permission", pending[0])
	}
	return pending[0].ID, names
}

// One sender with two enrolled receivers gets one question naming both; one
// Allow writes both per-pair rules; a later agent asks once for itself; each
// pair is removed on its own and asked about again alone.
func TestMessagePermissionOneQuestionForEveryReceiver(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx, cancel := context.WithTimeout(f.run.ctx, 60*time.Second)
	defer cancel()
	const second, third = "second-agent", "third-agent"
	enrollExtraMessageAgent(t, f, second)
	a := f.run.f.a
	first, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "first-access"))
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	id, names := pendingMessageQuestion(t, f)
	if id != first.Answer.ID || strings.Join(names, ",") != "other-agent,"+second && strings.Join(names, ",") != second+",other-agent" {
		t.Fatal("one question does not name both receivers", id, names)
	}
	// Asking about the other receiver joins the same question.
	joined, err := client.RequestContext(ctx, messagePermissionIntent(second, "second-access"))
	if err != nil || joined.Outcome != askswire.PermissionOutcomePending || joined.Answer == nil || joined.Answer.ID != id {
		t.Fatal("a second question was asked", joined, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, id, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, id, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied || len(edit.Current) != 2 {
		t.Fatal("one Allow did not write both pair rules", edit, err)
	}
	subject := edit.Current[0].Rule.Subject
	for _, name := range []string{"other-agent", second} {
		if got := a.rights.decide(ctx, subject, grants.ApplicationMessageAction, "app:"+name); got.Outcome != rwire.DecisionOutcomePermitted {
			t.Fatal(name, got)
		}
	}
	if out, err := client.RequestContext(ctx, messagePermissionIntent(second, "second-access")); err != nil || out.Outcome != askswire.PermissionOutcomeAnswered && out.Outcome != askswire.PermissionOutcomePermitted {
		t.Fatal("same-key retry lost the allowed question", out, err)
	}
	// A newly enrolled agent is asked about once, alone.
	enrollExtraMessageAgent(t, f, third)
	later, err := client.RequestContext(ctx, messagePermissionIntent(third, "third-access"))
	if err != nil || later.Outcome != askswire.PermissionOutcomePending || later.Answer == nil {
		t.Fatal(later, err)
	}
	if laterID, names := pendingMessageQuestion(t, f); laterID != later.Answer.ID || len(names) != 1 || names[0] != third {
		t.Fatal("already allowed receivers were asked again", names)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, later.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	revision, _, err = a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, later.Answer.ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied || len(edit.Current) != 1 {
		t.Fatal(edit, err)
	}
	// Removing one pair leaves the others.
	revision, _, err = a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := a.rights.policy.ChangeRule(revision, subject, grants.ApplicationMessageAction, "app:"+second, rights.RuleEdit{By: f.run.f.panel}, func() error { return nil }); err != nil || out.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(out, err)
	}
	if got := a.rights.decide(ctx, subject, grants.ApplicationMessageAction, "app:"+second); got.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatal("removed pair still allowed", got)
	}
	if got := a.rights.decide(ctx, subject, grants.ApplicationMessageAction, "app:other-agent"); got.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatal("removing one pair removed the other", got)
	}
	again, err := client.RequestContext(ctx, messagePermissionIntent(second, "second-again"))
	if err != nil || again.Outcome != askswire.PermissionOutcomePending || again.Answer == nil {
		t.Fatal(again, err)
	}
	if _, names := pendingMessageQuestion(t, f); len(names) != 1 || names[0] != second {
		t.Fatal("the removed pair is not asked about alone", names)
	}
	// An already allowed receiver asks nothing.
	if out, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "other-again")); err != nil || out.Outcome != askswire.PermissionOutcomePermitted {
		t.Fatal(out, err)
	}
}

func TestMessagePermissionFreshNativeBootstrapAndUndo(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	self := messages1608Presence(t, f.run, f.reader.call, "", "source")
	pin, err := f.other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, otherSubject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := f.run.d.announceInSession(ctx, otherSubject, session, fwire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Contexts: []fwire.ApplicationContext{{Name: "opaque-native-receiver"}}})
	if presence.Outcome != fwire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	other := presence.Instance
	for _, row := range []struct {
		r                 *messagesReceiver
		instance, context string
	}{{f.reader, self, "source"}, {f.other, other, "opaque-native-receiver"}} {
		if out, err := row.r.InboxOpen(row.instance, row.context, ""); err != nil || out.Outcome != msg.OutcomeOpened {
			t.Fatal(out, err)
		}
	}
	assertOwnOnly := func(page msg.InboxPage, program, contextName string) bool {
		t.Helper()
		return page.Outcome == msg.OutcomePage && len(page.Inboxes) == 1 && page.Inboxes[0].Program == program && page.Inboxes[0].Context == contextName
	}
	if err := f.run.d.refreshMessageParties(ctx); err != nil {
		t.Fatal(err)
	}
	if page := f.reader.inboxesList(""); !assertOwnOnly(page, "app:native-agent", "source") || len(page.NeedsPermission) != 1 || page.NeedsPermission[0].Program != "app:other-agent" {
		t.Fatal("fresh metadata visible", page)
	}
	observed, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "bootstrap-access"))
	if err != nil || observed.Outcome != askswire.PermissionOutcomePending || observed.Answer == nil {
		t.Fatal(observed, err)
	}
	if page := f.reader.inboxesList(""); !assertOwnOnly(page, "app:native-agent", "source") || len(page.NeedsPermission) != 1 || page.NeedsPermission[0].Program != "app:other-agent" {
		t.Fatal("pending disclosed inbox", page)
	}
	if _, err = operator.AnswerPermissionIfPendingContext(ctx, observed.Answer.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	a := f.run.f.a
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, observed.Answer.ID, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	page := f.reader.inboxesList("")
	var destination *msg.Inbox
	for i := range page.Inboxes {
		if page.Inboxes[i].Program == "app:other-agent" && page.Inboxes[i].Context == "opaque-native-receiver" {
			destination = &page.Inboxes[i]
		}
	}
	if page.Outcome != msg.OutcomePage || len(page.Inboxes) != 2 || destination == nil || len(page.NeedsPermission) != 0 {
		t.Fatal("authorized discovery failed", page)
	}
	if reverse := f.other.inboxesList(""); !assertOwnOnly(reverse, "app:other-agent", "opaque-native-receiver") {
		t.Fatal("direction widened", reverse)
	}
	prepared, err := f.reader.Prepare(self, "source", msg.PrepareRequest{Receiver: destination.Program, Context: destination.Context, Kind: new(msg.KindNote), Subject: "bootstrap", Body: "actual discovered destination"})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
		t.Fatal("durable Allow did not authorize Prepare", prepared, err)
	}
	request := *prepared.SendRequest
	if out, err := f.reader.Send(self, "source", request); err != nil || out.Outcome != msg.OutcomeSent || out.AwaitingPermission {
		t.Fatal(out, err)
	}
	if received, err := f.other.Wait(other, "opaque-native-receiver", msg.ViewBody, "", "", 0); err != nil || len(received.Messages) != 1 || received.Messages[0].Body != request.Body {
		t.Fatal(received, err)
	}
	undo, err := a.undoPermissionEffect(ctx, f.run.f.panel, edit.Revision, observed.Answer.ID, func() error { return nil })
	if err != nil || undo.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(undo, err)
	}
	if page := f.reader.inboxesList(""); !assertOwnOnly(page, "app:native-agent", "source") || len(page.NeedsPermission) != 1 || page.NeedsPermission[0].Program != "app:other-agent" {
		t.Fatal("undo retained discovery", page)
	}
}

func TestMessagePermissionRejectsUntrustedTargetsAndSharedCaller(t *testing.T) {
	f, client, _ := messagePermissionFixture(t)
	ctx := f.run.ctx
	// With same-app messaging off, the sender's own app is no receiver
	// (TestMessagePermissionSameAppGrantPath covers it on).
	sameApp := messageSameAppReceivers
	messageSameAppReceivers = false
	t.Cleanup(func() { messageSameAppReceivers = sameApp })
	for _, mode := range []string{"all", "captured", "self", "wording"} {
		intent := messagePermissionIntent("other-agent", "invalid-"+mode)
		switch mode {
		case "all":
			intent.Selection.Selector.Kind = rwire.ResourceSelectorKindAll
			intent.Selection.Selector.Target = nil
		case "captured":
			// Raw untrusted capture input must reach native refusal, not a
			// generated encoder that correctly rejects incomplete records.
			intent.Selection.Selector.Target = []byte(`{"sender":{},"receiver":{}}`)
		case "self":
			intent = messagePermissionIntent("native-agent", "invalid-self")
		case "wording":
			intent.Purpose = "Trust me"
		}
		out, err := client.RequestContext(ctx, intent)
		if err != nil || out.Outcome == askswire.PermissionOutcomePending || out.Outcome == askswire.PermissionOutcomePermitted {
			t.Fatal(mode, out, err)
		}
	}
	excluded := f.run.d.agentExcluded
	f.run.d.agentExcluded = func(context.Context, string, string) (bool, error) { return true, nil }
	out, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "shared-excluded"))
	f.run.d.agentExcluded = excluded
	if err != nil || out.Outcome == askswire.PermissionOutcomePending || out.Outcome == askswire.PermissionOutcomePermitted {
		t.Fatal(out, err)
	}
}

func TestMessagePermissionChangedPartiesCannotApplyAnsweredCapture(t *testing.T) {
	for _, party := range []string{"native-agent", "other-agent", "receiver-image"} {
		t.Run(party, func(t *testing.T) {
			f, client, operator := messagePermissionFixture(t)
			ctx := f.run.ctx
			out, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "changed-party"))
			if err != nil || out.Outcome != askswire.PermissionOutcomePending || out.Answer == nil {
				t.Fatal(out, err)
			}
			record, _, err := f.run.f.a.book.ApplicationRecord(out.Answer.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.run.f.a.authorizeAnswerer(ctx, f.run.f.me(), record, f.run.f.a.scope, "allow"); err == nil {
				t.Fatal("agent could answer message consent")
			}
			if _, err = operator.AnswerPermissionIfPendingContext(ctx, out.Answer.ID, "allow"); err != nil {
				t.Fatal(err)
			}
			if party == "receiver-image" {
				read := f.run.d.agentImage
				if read == nil {
					read = readApplicationAgentImage
				}
				f.run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
					image, err := read(ctx, program, binding)
					if program == f.otherProgram {
						image.Key = "changed-image-key"
					}
					return image, err
				}
			} else {
				current := f.run.d.currentAgentEnrollment(ctx, party)
				if current.Enrollment == nil {
					t.Fatal(current)
				}
				agent1478Set(t, f.run, party, current.Enrollment.Revision, false)
			}
			revision, _, err := f.run.f.a.rights.policy.SelectorSnapshotContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			edit, err := f.run.f.a.applyPermissionAllow(ctx, f.run.f.panel, revision, out.Answer.ID, func() error { return nil })
			if edit.Outcome == rwire.PolicyEditOutcomeApplied {
				t.Fatal("changed party allowed", edit, err)
			}
			if got := f.run.f.a.rights.policy.Decide(f.self, ActionApplicationMessage, "app:other-agent"); got.Outcome == rwire.DecisionOutcomePermitted {
				t.Fatal(got)
			}
			fresh, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "after-party-change"))
			if err != nil || fresh.Outcome == askswire.PermissionOutcomePending || fresh.Outcome == askswire.PermissionOutcomePermitted {
				t.Fatal("changed party requested fresh access", fresh, err)
			}
		})
	}
}

func TestMessagePermissionRegisteredPartyRemovalRetiresCapturedPrompt(t *testing.T) {
	for _, party := range []string{"native-agent", "other-agent"} {
		t.Run(party, func(t *testing.T) {
			f, client, _ := messagePermissionFixture(t)
			out, err := client.RequestContext(f.run.ctx, messagePermissionIntent("other-agent", "remove-party"))
			if err != nil || out.Outcome != askswire.PermissionOutcomePending || out.Answer == nil {
				t.Fatal(out, err)
			}
			removed := f.run.d.remove(f.run.ctx, f.run.f.panel, party)
			if removed.Outcome != fwire.ApplicationOutcomeApplied {
				t.Fatal(removed)
			}
			if _, found, err := f.run.f.a.book.ApplicationRecord(out.Answer.ID); err != nil || found {
				t.Fatal("removed registered party retained captured consent", party, found, err)
			}
		})
	}
}

func TestMessagePermissionExistingLegacyAndTypedBlocksRefuseAdmission(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(fmt.Sprint(typed), func(t *testing.T) {
			f, client, _ := messagePermissionFixture(t)
			a := f.run.f.a
			ctx := f.run.ctx
			if typed {
				revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
				if err != nil {
					t.Fatal(err)
				}
				selection := *messagePermissionIntent("other-agent", "block").Selection
				edit, err := a.rights.policy.SetSelectorRuleContext(ctx, revision, rwire.SelectorRule{Subject: f.self, Selection: selection, Permit: false}, rights.RuleEdit{By: f.run.f.panel, Exact: true}, func() error { return nil })
				if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Fatal(edit, err)
				}
			} else if err := a.rights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
				t.Fatal(err)
			}
			out, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "blocked"))
			if err != nil || out.Outcome != askswire.PermissionOutcomeDenied || out.Answer != nil {
				t.Fatal(out, err)
			}
		})
	}
}

func TestMessagePermissionLateTypedBlockPreventsProjectedPermit(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	// This case owns policy ordering; other native integration cases retain
	// the actual signature reader. Use the existing immediate FileID seam.
	agent1478Attach(t, f.run)
	ctx := f.run.ctx
	out, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "late-block"))
	if err != nil || out.Answer == nil {
		t.Fatal(out, err)
	}
	if _, err = operator.AnswerPermissionIfPendingContext(ctx, out.Answer.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	a := f.run.f.a
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := a.rights.policy.SetSelectorRuleContext(ctx, revision, rwire.SelectorRule{Subject: f.self, Selection: *messagePermissionIntent("other-agent", "block").Selection, Permit: false}, rights.RuleEdit{By: f.run.f.panel, Exact: true}, func() error { return nil })
	if err != nil || blocked.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(blocked, err)
	}
	edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, blocked.Revision, out.Answer.ID, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeConflict {
		t.Fatal(edit, err)
	}
	rules, err := a.rights.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules.Rules {
		if rule.Subject == f.self && rule.Action == ActionApplicationMessage && rule.Resource == "app:other-agent" && rule.Permit {
			t.Fatal("hidden permit written behind typed block", rule)
		}
	}
}

func TestMessagePermissionUnchangedPartyDeadlineRefusesWithoutProjection(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	agent1478Attach(t, f.run)
	ctx := f.run.ctx
	self := messages1608Presence(t, f.run, f.reader.call, "", "source")
	if opened, err := f.reader.InboxOpen(self, "source", ""); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	intent := messagePermissionIntent("other-agent", "delayed-proof")
	out, err := client.RequestContext(ctx, intent)
	if err != nil || out.Outcome != askswire.PermissionOutcomePending || out.Answer == nil {
		t.Fatal(out, err)
	}
	if answered, err := operator.AnswerPermissionIfPendingContext(ctx, out.Answer.ID, "allow"); err != nil ||
		answered.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(answered, err)
	}
	a := f.run.f.a
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	read := f.run.d.agentImage
	// Preserve the native meta-holder refresh. Delay only the subsequent
	// captured-party preflight whose closed diagnostic this case exercises.
	refresh := a.rights.policy.RefreshMetaHolder
	var refreshed atomic.Bool
	a.rights.policy.RefreshMetaHolder = func(call context.Context) error {
		if refresh != nil {
			if err := refresh(call); err != nil {
				return err
			}
		}
		refreshed.Store(true)
		return nil
	}
	f.run.d.agentImage = func(call context.Context, program, binding string) (applicationAgentImage, error) {
		if refreshed.Load() && program == f.otherProgram {
			<-call.Done()
			return applicationAgentImage{}, call.Err()
		}
		return read(call, program, binding)
	}
	call, cancel := context.WithTimeout(ctx, decisionBudget)
	defer cancel()
	edit, err := a.applyPermissionAllow(call, f.run.f.panel, revision, out.Answer.ID, func() error { return nil })
	f.run.d.agentImage = read
	a.rights.policy.RefreshMetaHolder = refresh
	if edit.Outcome != rwire.PolicyEditOutcomeUnavailable || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(edit, err)
	}
	text := err.Error()
	for _, label := range []string{"party unavailable", "role=receiver", "outcome=unavailable", "field=native_identity", "context=deadline", "mismatch=none"} {
		if !strings.Contains(text, label) {
			t.Fatal("missing closed diagnostic", label, text)
		}
	}
	for _, value := range []string{f.otherProgram, f.selfProgram, "other-agent", f.self.Account} {
		if strings.Contains(text, value) {
			t.Fatal("native value disclosed")
		}
	}
	rules, err := a.rights.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules.Rules {
		if rule.Subject == f.self && rule.Action == ActionApplicationMessage && rule.Resource == "app:other-agent" && rule.Permit {
			t.Fatal("unavailable party projected permit")
		}
	}
	if pending, err := a.book.PendingTypedPermissions(); err != nil || len(pending) != 0 {
		t.Fatal("recorded Allow still pending", pending, err)
	}
	// The unsaved Allow was released (owner, 2026-11-09): the same key asks
	// afresh, and that Allow saves once the party proof completes.
	replay, err := client.RequestContext(ctx, intent)
	if err != nil || replay.Outcome != askswire.PermissionOutcomePending || replay.Answer == nil || replay.Answer.ID == out.Answer.ID {
		t.Fatal("same key did not ask afresh", replay, err)
	}
	if answered, err := operator.AnswerPermissionIfPendingContext(ctx, replay.Answer.ID, "allow"); err != nil ||
		answered.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(answered, err)
	}
	if _, err := client.RequestContext(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if observed, err := client.ObserveQuestionContext(ctx, replay.Answer.ID, 0); err != nil || observed.Observation.Outcome != askswire.PermissionOutcomePermitted {
		t.Fatal("recheck did not save the fresh Allow", observed, err)
	}
	prepared, err := f.reader.Prepare(self, "source", msg.PrepareRequest{
		Receiver: "app:other-agent", Kind: new(msg.KindNote), Body: "requires saved permission"})
	if err != nil || prepared.Outcome == msg.OutcomeForbidden {
		t.Fatal("saved Allow still forbidden", prepared, err)
	}
}

func TestMessagePermissionFiniteNativeProofCanUseCallerBudget(t *testing.T) {
	t.Run("apply", func(t *testing.T) { finiteMessagePermissionProof(t, false) })
	t.Run("cancel-before-commit", func(t *testing.T) { finiteMessagePermissionProof(t, true) })
}

func finiteMessagePermissionProof(t *testing.T, cancelAfterProof bool) {
	t.Helper()
	f, client, operator := messagePermissionFixture(t)
	agent1478Attach(t, f.run)
	ctx := f.run.ctx
	intent := messagePermissionIntent("other-agent", "finite-proof")
	pending, err := client.RequestContext(ctx, intent)
	if err != nil || pending.Outcome != askswire.PermissionOutcomePending || pending.Answer == nil {
		t.Fatal(pending, err)
	}
	id := pending.Answer.ID
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, id, "allow"); err != nil ||
		out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	a := f.run.f.a
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The existing presenter allows five seconds. Delay only one otherwise
	// valid receiver proof after metadata refresh, outside the policy lock.
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	read, refresh := f.run.d.agentImage, a.rights.policy.RefreshMetaHolder
	var refreshed, started, finished atomic.Bool
	a.rights.policy.RefreshMetaHolder = func(ctx context.Context) error {
		if refresh != nil {
			if err := refresh(ctx); err != nil {
				return err
			}
		}
		refreshed.Store(true)
		return nil
	}
	f.run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		delayed := refreshed.Load() && program == f.otherProgram && started.CompareAndSwap(false, true)
		if delayed {
			timer := time.NewTimer(decisionBudget + 100*time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return applicationAgentImage{}, ctx.Err()
			case <-timer.C:
			}
		}
		image, err := read(ctx, program, binding)
		if delayed && err == nil {
			finished.Store(true)
			if cancelAfterProof {
				cancel()
			}
		}
		return image, err
	}
	edit, err := a.applyPermissionAllow(call, f.run.f.panel, revision, id, func() error { return nil })
	f.run.d.agentImage, a.rights.policy.RefreshMetaHolder = read, refresh
	if !started.Load() || !finished.Load() {
		t.Fatal("finite receiver proof did not finish", edit, err)
	}
	if cancelAfterProof {
		if !errors.Is(call.Err(), context.Canceled) || edit.Outcome == rwire.PolicyEditOutcomeApplied ||
			err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled caller completed permission", edit, err)
		}
		if current, _, err := a.rights.policy.SelectorSnapshotContext(ctx); err != nil || current != revision {
			t.Fatal("cancelled completion changed policy", current, revision, err)
		}
		// A cancelled completion saved nothing: the Allow was released and
		// the same key asks afresh (owner, 2026-11-09).
		replay, replayErr := client.RequestContext(ctx, intent)
		if replayErr != nil || replay.Outcome != askswire.PermissionOutcomePending || replay.Answer == nil || replay.Answer.ID == id {
			t.Fatal("cancelled completion did not ask afresh", replay, replayErr)
		}
		if out, err := operator.AnswerPermissionIfPendingContext(ctx, replay.Answer.ID, "allow"); err != nil ||
			out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
			t.Fatal(out, err)
		}
		if _, err := client.RequestContext(ctx, intent); err != nil {
			t.Fatal(err)
		}
		if out, err := client.ObserveQuestionContext(ctx, replay.Answer.ID, 0); err != nil || out.Observation.Outcome != askswire.PermissionOutcomePermitted {
			t.Fatal("recheck did not save the fresh Allow", out, err)
		}
		return
	}
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal("completed proof did not commit permission", edit, err)
	}
	if out, err := client.ObserveQuestionContext(ctx, id, 0); err != nil || out.Observation.Outcome != askswire.PermissionOutcomePermitted {
		t.Fatal(out, err)
	}
}
