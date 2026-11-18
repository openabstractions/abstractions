//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestMessagesNativeChild(t *testing.T) {
	endpoint := os.Getenv("OA_MESSAGES1608_CHILD")
	if endpoint == "" {
		return
	}
	c, err := listen.Dial(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = listen.WriteFrameTo(c, []byte("{}"), 4096); err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
}

func messages1608Call(t *testing.T) (*listen.FramedCall, func()) {
	return messages1608CallImage(t, "")
}

func messages1608CallImage(t *testing.T, selected string) (*listen.FramedCall, func()) {
	t.Helper()
	id, err := applicationToken()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := mustProviderEndpoint(t, "oa-test-messages1608-"+id[:16])
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	// The retained call is reused by bounded native/race controls after setup.
	// Its fixture deadline must cover that work; cleanup still joins by EOF.
	ctx, cancel := runtimeTestContext(t, t.Context())
	t.Cleanup(cancel)
	accepted := make(chan *listen.FramedCall, 1)
	failed := make(chan error, 1)
	go func() {
		c, e := l.Accept()
		if e != nil {
			failed <- e
			return
		}
		k, e := listen.ReceiveFramed(ctx, c, listen.Program, 4096)
		if e != nil {
			c.Close()
			failed <- e
			return
		}
		accepted <- k
	}()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if selected != "" {
		exe = selected
	}
	cmd := exec.Command(exe, "-test.run=^TestMessagesNativeChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "OA_MESSAGES1608_CHILD="+endpoint)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			in.Close()
			if e := cmd.Wait(); e != nil {
				t.Error(e)
			}
		}
	}
	t.Cleanup(stop)
	select {
	case call := <-accepted:
		t.Cleanup(func() { call.Close() })
		return call, stop
	case err := <-failed:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return nil, nil
}

func messages1608Presence(t *testing.T, run *selfRegisteredRun, call *listen.FramedCall, instance, contextName string) string {
	t.Helper()
	p, err := call.Peer()
	if err != nil {
		t.Fatal(err)
	}
	pin, err := call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	_, s, session, _, err := nativeInboxPeer(pin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Program == "" || p == nil {
		t.Fatal("native caller missing")
	}
	r := run.d.announceInSession(run.ctx, s, session, wire.ApplicationPresence{Application: "native-agent", Instance: instance, LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: contextName}}})
	if r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	return r.Instance
}

func messages1608Fixture(t *testing.T) (*selfRegisteredRun, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return messages1608FixtureImage(t, exe)
}

// messages1608FixtureImage enrolls program, which the child must run from.
func messages1608FixtureImage(t *testing.T, program string) (*selfRegisteredRun, string) {
	t.Helper()
	run := agent1478Fixture(t)
	original := run.d.decide
	run.d.decide = func(ctx context.Context, s rights.Subject, a, r string) (bool, error) {
		if a == ActionApplicationAnnounce {
			return true, nil
		}
		return original(ctx, s, a, r)
	}
	agent1478Register(t, run, "native-agent", identity.NormalizeSubjectProgram(program))
	run.d.agentImage = nil
	pending := agent1478Set(t, run, "native-agent", "", true)
	q := agent1478Question(t, run, "native-agent")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "native-agent", "enrolled")
	t.Cleanup(run.d.closeNativeInboxes)
	return run, pending.Revision
}

func messages1608Row(t *testing.T, d *applicationDirectory) *nativeInbox {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.nativeInboxes == nil {
		return nil
	}
	return d.nativeInboxes.rows["native-agent\x00ctx"]
}

func TestMessagesNativeInboxRequestRetirementSiblingAndProcessExit(t *testing.T) {
	run, _ := messages1608Fixture(t)
	call, exit := messages1608Call(t)
	instance := messages1608Presence(t, run, call, "", "ctx")
	first := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready")
	if first.outcome != "opened" || first.revision == "" {
		t.Fatal(first)
	}
	if again := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready"); again.revision != first.revision {
		t.Fatal("replay changed revision", again)
	}
	if !run.d.checkNativeInboxOwner(run.ctx, call, instance, "ctx") {
		t.Fatal("owner refused")
	}
	consumed := 0
	callbackValid := false
	consumeError := errors.New("receiving store refused")
	owned, err := run.d.withNativeInboxOwner(run.ctx, call, instance, "ctx", func(row *nativeInbox) error {
		consumed++
		callbackValid = row.instance == instance && row.application == "native-agent" && row.context == "ctx" && row.pin != nil
		return consumeError
	})
	if owned || !errors.Is(err, consumeError) || consumed != 1 || !callbackValid {
		t.Fatal("receiving error or actual inbox lost", owned, err, consumed, callbackValid)
	}
	other, _ := messages1608Call(t)
	if run.d.checkNativeInboxOwner(run.ctx, other, instance, "ctx") {
		t.Fatal("sibling native process borrowed handle")
	}
	owned, err = run.d.withNativeInboxOwner(run.ctx, other, instance, "ctx", func(*nativeInbox) error {
		consumed++
		return nil
	})
	if owned || err != nil || consumed != 1 {
		t.Fatal("sibling reached receiving effect", owned, err, consumed)
	}
	if got := run.d.openNativeInbox(run.ctx, other, instance, "ctx", "forged"); got.outcome != "held" {
		t.Fatal("sibling changed same instance", got)
	}
	sibling := messages1608Presence(t, run, other, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, other, sibling, "ctx", "other"); got.outcome != "held" {
		t.Fatal("exclusive context", got)
	}
	row := messages1608Row(t, run.d)
	call.Close() // actual request/connection retirement, owning process stays live
	if live, err := row.pin.Alive(); !live || err != nil {
		t.Fatal("request retired independent pin", live, err)
	}
	if messages1608Row(t, run.d) != row {
		t.Fatal("request retired inbox")
	}
	exit()
	until := time.Now().Add(3 * time.Second)
	for messages1608Row(t, run.d) != nil && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	if messages1608Row(t, run.d) != nil {
		t.Fatal("process exit retained inbox")
	}
	if _, err := row.pin.Peer(); err == nil {
		t.Fatal("expired inbox pin not closed")
	}
}

func TestMessagesNativeInboxEnrollmentAndPresenceLifecycle(t *testing.T) {
	for _, variant := range []string{"revoked", "withdrawn", "expired", "context-removed", "shutdown"} {
		t.Run(variant, func(t *testing.T) {
			run, revision := messages1608Fixture(t)
			call, _ := messages1608Call(t)
			instance := messages1608Presence(t, run, call, "", "ctx")
			if got := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready"); got.outcome != "opened" {
				t.Fatal(got)
			}
			row := messages1608Row(t, run.d)
			switch variant {
			case "revoked":
				agent1478Set(t, run, "native-agent", revision, false)
			case "withdrawn":
				if got := run.d.withdrawInSession(run.ctx, row.subject, row.session, "native-agent", instance); got.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal(got)
				}
			case "expired":
				run.d.mu.Lock()
				l := run.d.instances[instance]
				l.deadline = time.Now().Add(-time.Second)
				run.d.instances[instance] = l
				run.d.expireLocked()
				run.d.mu.Unlock()
			case "context-removed":
				messages1608Presence(t, run, call, instance, "different")
			case "shutdown":
				run.d.closeNativeInboxes()
			}
			if run.d.checkNativeInboxOwner(run.ctx, call, instance, "ctx") {
				t.Fatal("ended ownership admitted", variant)
			}
			until := time.Now().Add(3 * time.Second)
			for messages1608Row(t, run.d) != nil && time.Now().Before(until) {
				time.Sleep(10 * time.Millisecond)
			}
			if messages1608Row(t, run.d) != nil {
				t.Fatal("ended ownership retained", variant)
			}
			if _, err := row.pin.Peer(); err == nil {
				t.Fatal("ended pin leaked")
			}
		})
	}
}

func TestMessagesNativeInboxContextRemovalCannotReviveOwnership(t *testing.T) {
	run, _ := messages1608Fixture(t)
	call, _ := messages1608Call(t)
	instance := messages1608Presence(t, run, call, "", "ctx")
	first := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "first")
	row := messages1608Row(t, run.d)
	if row == nil {
		t.Fatal(first)
	}
	// Stop the cleanup sweep without closing the book. Only each actual
	// Announce edit can now release the old row; no owner-check masks the bug.
	run.d.nativeInboxes.cancel()
	<-run.d.nativeInboxes.done
	messages1608Presence(t, run, call, instance, "different")
	if _, err := row.pin.Peer(); err == nil {
		t.Fatal("context removal did not synchronously close pin")
	}
	messages1608Presence(t, run, call, instance, "ctx")
	if run.d.checkNativeInboxOwner(run.ctx, call, instance, "ctx") {
		t.Fatal("context re-add revived old owner")
	}
	second := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "second")
	if second.outcome != "opened" || second.revision == first.revision || messages1608Row(t, run.d) == row {
		t.Fatal("re-add lacked fresh acquisition", first, second)
	}
}

// A helper that missed a renewal announces a new instance and opens its own
// context again. The same process takes its context back at once; before
// R20c's fix the older instance's row answered held until its lease lapsed,
// and every tool in that helper answered "reconnecting" meanwhile.
func TestMessagesNativeInboxSameProcessReadmitsOwnContext(t *testing.T) {
	run, _ := messages1608Fixture(t)
	call, _ := messages1608Call(t)
	first := messages1608Presence(t, run, call, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, first, "ctx", "first"); got.outcome != "opened" {
		t.Fatal(got)
	}
	second := messages1608Presence(t, run, call, "", "ctx")
	if second == first {
		t.Fatal("a fresh announce reused the instance")
	}
	if got := run.d.openNativeInbox(run.ctx, call, second, "ctx", "second"); got.outcome != "opened" {
		t.Fatal("same process re-admission held by its own older instance", got)
	}
	if !run.d.checkNativeInboxOwner(run.ctx, call, second, "ctx") || run.d.checkNativeInboxOwner(run.ctx, call, first, "ctx") {
		t.Fatal("ownership did not move to the new instance")
	}
	other, _ := messages1608Call(t)
	sibling := messages1608Presence(t, run, other, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, other, sibling, "ctx", "other"); got.outcome != "held" {
		t.Fatal("another process took the context", got)
	}
}

// A reused PID belongs to another process: the older row's start time
// differs, so the context stays held.
func TestMessagesNativeInboxReusedPIDStaysHeld(t *testing.T) {
	run, _ := messages1608Fixture(t)
	call, _ := messages1608Call(t)
	first := messages1608Presence(t, run, call, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, first, "ctx", "first"); got.outcome != "opened" {
		t.Fatal(got)
	}
	run.d.mu.Lock()
	row := run.d.nativeInboxes.rows["native-agent\x00ctx"]
	if row == nil {
		run.d.mu.Unlock()
		t.Fatal("no row")
	}
	row.process.StartTime = row.process.StartTime.Add(-time.Hour)
	run.d.mu.Unlock()
	second := messages1608Presence(t, run, call, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, second, "ctx", "second"); got.outcome != "held" {
		t.Fatal("a reused PID took the context", got)
	}
}

func TestMessagesNativeInboxExclusiveAcrossApplicationNames(t *testing.T) {
	run, revision := messages1608Fixture(t)
	call, _ := messages1608Call(t)
	instance := messages1608Presence(t, run, call, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "first"); got.outcome != "opened" {
		t.Fatal(got)
	}
	row := messages1608Row(t, run.d)
	// Retain the revoked row until acquisition, making the transition independent
	// of sweep timing. The enrollment owner permits only one name at a time.
	run.d.nativeInboxes.cancel()
	<-run.d.nativeInboxes.done
	agent1478Set(t, run, "native-agent", revision, false)
	agent1478Register(t, run, "new-agent-name", row.subject.Program)
	agent1478Set(t, run, "new-agent-name", "", true)
	q := agent1478Question(t, run, "new-agent-name")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "new-agent-name", "enrolled")
	presence := run.d.announceInSession(run.ctx, row.subject, row.session, wire.ApplicationPresence{Application: "new-agent-name", LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: "ctx"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	if got := run.d.openNativeInbox(run.ctx, call, presence.Instance, "ctx", "second"); got.outcome != "held" {
		t.Fatal("native program/context duplicated through new name", got)
	}
	run.d.mu.Lock()
	count := len(run.d.nativeInboxes.rows)
	run.d.mu.Unlock()
	if count != 1 {
		t.Fatal("duplicate native program/context rows", count)
	}
}

func TestMessagesNativeInboxLeasePinsOriginalEnrollmentAcrossRenewal(t *testing.T) {
	run, revision := messages1608Fixture(t)
	call, _ := messages1608Call(t)
	instance := messages1608Presence(t, run, call, "", "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready"); got.outcome != "opened" {
		t.Fatal(got)
	}
	run.d.mu.Lock()
	original := run.d.instances[instance].agentAdmission
	run.d.mu.Unlock()
	if original == nil || original.Enrollment.Revision != revision {
		t.Fatal("initial admission not pinned", original)
	}
	// A different party's new Keep must preserve the original native binding.
	agent1478Register(t, run, "unrelated-agent", run.f.answerer.Program)
	agent1478Set(t, run, "unrelated-agent", "", true)
	q := agent1478Question(t, run, "unrelated-agent")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "unrelated-agent", "enrolled")
	messages1608Presence(t, run, call, instance, "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready"); got.outcome != "opened" {
		t.Fatal("unrelated Keep disrupted inbox", got)
	}
	// Own revocation and fresh Keep cannot replace an old transport's capture.
	revoked := agent1478Set(t, run, "native-agent", revision, false)
	agent1478Set(t, run, "native-agent", revoked.Revision, true)
	q = agent1478Question(t, run, "native-agent")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "native-agent", "enrolled")
	// Remove/re-add the context as well, so a missing inbox row cannot erase the
	// presence lease's original enrollment receipt.
	messages1608Presence(t, run, call, instance, "removed")
	messages1608Presence(t, run, call, instance, "ctx")
	if got := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready"); got.outcome != "forbidden" {
		t.Fatal("own re-Keep replaced original admission", got)
	}
	if run.d.checkNativeInboxOwner(run.ctx, call, instance, "ctx") {
		t.Fatal("changed enrollment consumed old native inbox")
	}
	run.d.mu.Lock()
	retained := run.d.instances[instance].agentAdmission
	run.d.mu.Unlock()
	if retained != original {
		t.Fatal("same-instance renewal overwrote original admission")
	}
}

func TestMessagesNativeInboxRenewalReplacesOccurrencePin(t *testing.T) {
	run, _ := messages1608Fixture(t)
	call, _ := messages1608Call(t)
	instance := messages1608Presence(t, run, call, "", "ctx")
	first := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready")
	old := messages1608Row(t, run.d)
	if old == nil {
		t.Fatal(first)
	}
	// Keep the old watch snapshot while a normal authenticated renewal wins.
	again := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready")
	renewed := messages1608Row(t, run.d)
	if again.outcome != "opened" || again.revision != first.revision || !again.opened.Equal(first.opened) || renewed == old || renewed.pin == old.pin {
		t.Fatal("renewal did not replace retained occurrence", first, again)
	}
	if _, err := old.pin.Peer(); err == nil {
		t.Fatal("superseded pin retained")
	}
	if !run.d.checkNativeInboxOwner(run.ctx, call, instance, "ctx") {
		t.Fatal("fresh pin lost owner")
	}
	// A late failure from the prior snapshot cannot remove its replacement.
	run.d.mu.Lock()
	run.d.nativeInboxes.retireLocked("native-agent\x00ctx", old)
	run.d.mu.Unlock()
	if messages1608Row(t, run.d) != renewed {
		t.Fatal("stale watch removed renewal")
	}
	changed := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "busy")
	if changed.outcome != "opened" || changed.revision == first.revision || !changed.opened.Equal(first.opened) {
		t.Fatal(changed)
	}
}

func TestMessagesNativeInboxRenewalPreservesActiveBodyWait(t *testing.T) {
	f := messagesObserveFixture(t)
	instance := messages1608Presence(t, f.run, f.reader.call, "", "ctx")
	if opened, err := f.reader.InboxOpen(instance, "ctx", "ready"); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	done := make(chan msg.Page, 1)
	go func() { page, _ := f.reader.Wait(instance, "ctx", msg.ViewBody, "", "", 3000); done <- page }()
	until := time.Now().Add(2 * time.Second)
	for !f.reader.host.messageIsWatching(f.run.ctx, f.self.Account, f.selfProgram, "ctx") && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if !f.reader.host.messageIsWatching(f.run.ctx, f.self.Account, f.selfProgram, "ctx") {
		t.Fatal("body wait did not acquire its own request pin")
	}
	old := messages1608Row(t, f.run.d)
	if opened, err := f.reader.InboxOpen(instance, "ctx", "ready"); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	if _, err := old.pin.Peer(); err == nil {
		t.Fatal("old inbox pin survived renewal")
	}
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	request := msg.SendRequest{To: "app:native-agent", Context: "ctx", Kind: msg.KindNote, Subject: "after renewal", Body: "delivered", IdempotencyKey: "renewal-active-wait", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	if sent := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, ""); sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	select {
	case page := <-done:
		if page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Messages[0].Body != "delivered" {
			t.Fatal("renewal retired an active request", page)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("body wait failed to complete")
	}
}

func TestMessagesNativeInboxSnapshotsRetainPinsAcrossRenewal(t *testing.T) {
	for _, application := range []string{"", "native-agent"} {
		t.Run("filter="+application, func(t *testing.T) {
			run, _ := messages1608Fixture(t)
			call, _ := messages1608Call(t)
			instance := messages1608Presence(t, run, call, "", "ctx")
			opened := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready")
			old := messages1608Row(t, run.d)
			run.d.mu.Lock()
			// Local listing uses the unfiltered snapshot; remote listing uses
			// the registered application filter. Both retain before unlocking.
			rows, err := run.d.nativeInboxes.snapshotLocked(application)
			run.d.mu.Unlock()
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			defer closeNativeInboxSnapshots(rows)
			deadline := rows[0].pin.EvidenceValidUntil()
			// Force the exact copy/use interleaving from the review finding.
			renewed := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready")
			if _, err := old.pin.Peer(); err == nil {
				t.Fatal("old owner pin remained open")
			}
			if _, _, _, _, err := nativeInboxPeer(rows[0].pin, run.d.now()); err != nil {
				t.Fatal("renewal retired the listing snapshot", err)
			}
			if renewed.outcome != "opened" || rows[0].revision != opened.revision || renewed.revision != opened.revision || !rows[0].opened.Equal(opened.opened) || !rows[0].pin.EvidenceValidUntil().Equal(deadline) {
				t.Fatal("same-status renewal changed stable listing or its evidence", opened, renewed, rows)
			}
			closeNativeInboxSnapshots(rows)
			if _, err := rows[0].pin.Peer(); err == nil {
				t.Fatal("snapshot cleanup leaked its pin")
			}
			if !run.d.checkNativeInboxOwner(run.ctx, call, instance, "ctx") {
				t.Fatal("snapshot cleanup closed the renewed owner")
			}
		})
	}
}
