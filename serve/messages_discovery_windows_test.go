//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
)

func messagesDiscoveryFixture(t *testing.T) *messagesObserveRun {
	t.Helper()
	f := messagesObserveFixture(t, true)
	if err := f.run.d.refreshMessageParties(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func messagesDiscoveryAgent(t *testing.T, f *messagesObserveRun, name, title string, enrolled bool) {
	t.Helper()
	raw, err := os.ReadFile(f.otherProgram)
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), name+".exe")
	if err := os.WriteFile(image, raw, 0700); err != nil {
		t.Fatal(err)
	}
	if out := f.run.d.register(f.run.ctx, f.run.f.panel, wire.ApplicationDescriptor{Name: name, Title: title, Program: identity.NormalizeSubjectProgram(image)}); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if enrolled {
		agent1478Set(t, f.run, name, "", true)
		q := agent1478Question(t, f.run, name)
		answer(t, f.run.f, q.ID, "keep")
		agent1478Wait(t, f.run, name, "enrolled")
	}
}

func TestMessagesDiscoveryBeforePermissionDisclosesOnlyEligibleNames(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	messagesStateOpen(t, f, f.other, "other-agent", "secret-context")
	messagesDiscoveryAgent(t, f, "third-agent", "Third agent", true)
	messagesDiscoveryAgent(t, f, "unenrolled-agent", "Hidden unenrolled", false)
	messagesDiscoveryAgent(t, f, "denied-agent", "Hidden denied", true)
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:denied-agent", false); err != nil {
		t.Fatal(err)
	}
	if err := f.run.d.refreshMessageParties(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Inboxes) != 0 || len(page.NeedsPermission) != 2 {
		t.Fatal(page, err)
	}
	if page.NeedsPermission[0] != (msg.AgentApplication{Program: "app:other-agent", Title: "Fixture agent"}) || page.NeedsPermission[1] != (msg.AgentApplication{Program: "app:third-agent", Title: "Third agent"}) {
		t.Fatal(page.NeedsPermission)
	}
	raw := string(msg.EncodeInboxPage(&page))
	for _, secret := range []string{"secret-context", "chat_title", "last_seen", "watching", "revision", "Hidden", "app:native-agent"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("names-only discovery leaked %q: %s", secret, raw)
		}
	}
}

func TestMessagesDiscoveryGrantAndRevokeMoveReceiver(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	messagesStateOpen(t, f, f.other, "other-agent", "target")
	for _, permitted := range []bool{false, true, false} {
		var err error
		if permitted {
			err = f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", true)
		} else {
			err = f.reader.host.messageRights.policy.Revoke(f.self, ActionApplicationMessage, "app:other-agent")
		}
		if err != nil {
			t.Fatal(err)
		}
		page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
		if err != nil || page.Outcome != msg.OutcomePage || (len(page.Inboxes) == 1) != permitted || (len(page.NeedsPermission) == 1) == permitted {
			t.Fatal(permitted, page, err)
		}
	}
}

func TestMessagesDiscoveryRequiresCurrentCallerEnrollment(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	current := f.run.d.currentAgentEnrollment(f.run.ctx, "native-agent")
	agent1478Set(t, f.run, "native-agent", current.Enrollment.Revision, false)
	page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomeForbidden || len(page.Inboxes) != 0 || len(page.NeedsPermission) != 0 {
		t.Fatal(page, err)
	}
}

func TestMessagesDiscoveryMissingOtherImageKeepsIndependentInbox(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	messagesStateOpen(t, f, f.other, "other-agent", "independent")
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", true); err != nil {
		t.Fatal(err)
	}
	messagesDiscoveryAgent(t, f, "missing-agent", "Missing agent", true)
	if err := f.run.d.refreshMessageParties(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	before, err := messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || before.Outcome != msg.OutcomePage || len(before.Inboxes) != 1 || len(before.NeedsPermission) != 1 || before.NeedsPermission[0].Program != "app:missing-agent" {
		t.Fatal(before, err)
	}
	observer, outcome := f.reader.messageObserver("")
	if observer == nil {
		t.Fatal(outcome)
	}
	defer observer.pin.Close()
	_, _, beforeDigest, err := observer.inboxDiscoverySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, _, err := f.run.d.agentSnapshot(f.run.ctx, "missing-agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(descriptor.Program); err != nil {
		t.Fatal(err)
	}
	// Keep the real enrollment and registered roster intact. The current
	// executable image alone is unavailable, as after an agent update.
	if target := f.run.d.currentAgentEnrollment(f.run.ctx, "missing-agent"); target.Outcome != applications.OutcomeUnavailable || target.Reason != "native_identity" {
		t.Fatal(target)
	}
	page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Inboxes) != 1 || page.Inboxes[0].Program != "app:other-agent" || page.Inboxes[0].Context != "independent" || len(page.NeedsPermission) != 0 {
		t.Fatal(page, err)
	}
	_, _, afterDigest, err := observer.inboxDiscoverySnapshot()
	if err != nil || afterDigest == beforeDigest {
		t.Fatal("unavailable roster member retained the old snapshot")
	}
	if stale, err := messagesBaseListing(f.reader, msg.InboxQuery{Cursor: beforeDigest + ".0"}); err != nil || stale.Outcome != msg.OutcomeGap || len(stale.Inboxes) != 0 || len(stale.NeedsPermission) != 0 {
		t.Fatal(stale, err)
	}
}

func TestMessagesDiscoverySnapshotTracksRosterAndRightsWithoutRows(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	observer, outcome := f.reader.messageObserver("")
	if observer == nil {
		t.Fatal(outcome)
	}
	defer observer.pin.Close()
	_, _, first, err := observer.inboxDiscoverySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
		t.Fatal(err)
	}
	rows, needs, denied, err := observer.inboxDiscoverySnapshot()
	if err != nil || denied == first || len(rows) != 0 || len(needs) != 0 {
		t.Fatal(rows, needs, first, denied, err)
	}
	if page := messageInboxesPage(rows, denied, first+".0", needs...); page.Outcome != msg.OutcomeGap || len(page.NeedsPermission) != 0 {
		t.Fatal(page)
	}
	messagesDiscoveryAgent(t, f, "added-agent", "Added", true)
	if err := f.run.d.refreshMessageParties(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	rows, needs, added, err := observer.inboxDiscoverySnapshot()
	if err != nil || added == denied || len(needs) != 1 {
		t.Fatal(rows, needs, added, err)
	}
	if page := messageInboxesPage(rows, added, denied+".0", needs...); page.Outcome != msg.OutcomeGap {
		t.Fatal(page)
	}
	// A permitted agent with no conversation is absent from both lists.
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:added-agent", true); err != nil {
		t.Fatal(err)
	}
	page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage || len(page.NeedsPermission) != 0 || len(page.Inboxes) != 0 {
		t.Fatal(page, err)
	}
}

func TestMessagesDiscoveryRightsOutageReturnsNoPartialPage(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.run.f.panel.Program), "decisions.json"), []byte("broken policy"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomeUnavailable || len(page.Inboxes) != 0 || len(page.NeedsPermission) != 0 {
		t.Fatal(page, err)
	}
}

func TestMessagesDiscoveryDoesNotAuthorizePrepare(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || len(page.NeedsPermission) != 1 {
		t.Fatal(page, err)
	}
	prepared, err := sender.Prepare(instance, "source", msg.PrepareRequest{Receiver: page.NeedsPermission[0].Program, Body: "hello"})
	if err != nil || prepared.Outcome != msg.OutcomeForbidden || prepared.Reason != "access:other-agent" || prepared.SendRequest != nil || len(prepared.Choices) != 0 {
		t.Fatal(prepared, err)
	}
}

func TestMessagesDiscoveryEmptyListsPreservePrepareOutcomes(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	messagesDiscoveryAgent(t, f, "denied-agent", "Denied agent", true)
	for target, permit := range map[string]bool{"app:other-agent": true, "app:denied-agent": false} {
		if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, target, permit); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.run.d.refreshMessageParties(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	if chats, err := f.store.retainedChats(f.run.ctx, f.self.Account, f.otherProgram); err != nil || len(chats) != 0 {
		t.Fatal("fixture already has a retained receiver conversation", chats, err)
	}
	if _, err := f.run.d.prepareMessageStart(f.run.ctx, "other-agent"); err == nil {
		t.Fatal("fixture unexpectedly has an approved receiver start profile")
	}
	if sessions := f.run.d.messageAttachSessionsFor(f.run.ctx, "other-agent"); len(sessions) != 0 {
		t.Fatal("fixture unexpectedly has receiver attach sessions", sessions)
	}
	// The caller needs an owned source for Prepare. An empty typed binding
	// opens that source without adding a receiving conversation to discovery.
	instance := messages1608Presence(t, f.run, f.reader.call, "", "source")
	if out, err := f.reader.InboxOpenChat(instance, "source", "", msg.ChatBinding{}); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Inboxes) != 0 || len(page.NeedsPermission) != 0 {
		t.Fatal(page, err)
	}
	for _, tc := range []struct {
		receiver string
		outcome  msg.Outcome
		reason   string
	}{
		{"app:other-agent", msg.OutcomeUnavailable, messageNoReachableChat},
		{"app:denied-agent", msg.OutcomeForbidden, "access:denied-agent"},
	} {
		prepared, err := f.reader.Prepare(instance, "source", msg.PrepareRequest{Receiver: tc.receiver, Body: "the person's message"})
		if err != nil || prepared.Outcome != tc.outcome || prepared.Reason != tc.reason || prepared.SendRequest != nil || len(prepared.Choices) != 0 {
			t.Fatal(tc.receiver, prepared, err)
		}
	}
	for _, name := range []string{"keys", "ids"} {
		entries, err := os.ReadDir(filepath.Join(f.store.dir, name))
		if err != nil || len(entries) != 0 {
			t.Fatal("Prepare accepted a message", name, entries, err)
		}
	}
}
