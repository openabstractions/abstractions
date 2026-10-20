package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// setupKeepFixture is a stored directory where OpenCode, which Setup's
// question names, and an editor it does not name both registered themselves
// and wait unconfirmed, with the runtime's Setup question owner.
func setupKeepFixture(t *testing.T) (*setupRequests, *applicationDirectory, *asks.Book) {
	t.Helper()
	dir := t.TempDir()
	d, err := openApplications(dir, setupTestAccount, func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	d.operator = func(program string) bool { return program == setupTestPanel }
	for _, entry := range []wire.ApplicationDescriptor{{Name: "opencode", Program: setupTestApp, Title: "OpenCode"}, {Name: "editor", Program: setupTestOther, Title: "Editor"}} {
		mark := newSelfMark(entry.Program, asks.Publisher{})
		if r := d.addDescriptor(context.Background(), entry, &mark); r.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(entry.Name, r)
		}
	}
	book, err := asks.LoadApplicationBook(filepath.Join(dir, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	d.agentQuestions = &agentEnrollmentQuestions{a: &runtimeAsks{book: book, scope: "setup-native", display: "runtime", rights: &runtimeRights{owner: setupTestAccount}}}
	q, _, _ := setupBookFixture(t)
	q.receiptPath = filepath.Join(dir, "receipt.json")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	q.keepContext = ctx
	return q, d, book
}

func unconfirmedEntry(d *applicationDirectory, name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	mark, ok := d.unconfirmed[name]
	return ok && mark.genericPending()
}

// The person's Set up answer keeps the apps the question named, with
// provenance setup; an app it did not name still waits for the person's
// click in Apps. Undo (AskConfirmation from the Activity line) returns the
// kept app to unconfirmed and asks the person.
func TestTheSetUpAnswerKeepsTheNamedAppsAndUndoRevertsThem(t *testing.T) {
	q, d, book := setupKeepFixture(t)
	var asked []string
	d.askKeep = func(name string) error { asked = append(asked, name); return nil }
	started := startSetupQuestion(t, q, d, "")
	if !unconfirmedEntry(d, "opencode") {
		t.Fatal("the question kept an app before its answer")
	}
	answerSetupQuestion(t, book, started.Question, "setup")
	deadline := time.Now().Add(10 * time.Second)
	for unconfirmedEntry(d, "opencode") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if unconfirmedEntry(d, "opencode") {
		t.Fatal("the Set up answer did not keep the app it named")
	}
	if !unconfirmedEntry(d, "editor") {
		t.Fatal("an app the question did not name was kept")
	}
	d.mu.Lock()
	kept, err := d.readSetupKeptLocked(context.Background())
	d.mu.Unlock()
	if err != nil || len(kept) != 1 || kept["opencode"].Provenance != "setup" || kept["opencode"].Occurrence != started.Question || kept["opencode"].Mark == nil {
		t.Fatalf("the Keep's provenance: %+v %v", kept, err)
	}
	// Applying the same answer again keeps nothing again.
	if names, err := d.setupKeepAnswer(context.Background(), book, started.Question); err != nil || len(names) != 0 {
		t.Fatal(names, err)
	}

	panel := rights.Subject{Account: setupTestAccount, Program: setupTestPanel}
	if r := d.askConfirmation(context.Background(), panel, "opencode"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("Undo:", r)
	}
	if !unconfirmedEntry(d, "opencode") || len(asked) != 1 || asked[0] != "opencode" {
		t.Fatalf("Undo did not return the app to unconfirmed and ask: %v", asked)
	}
	reopened, err := openApplications(filepath.Dir(d.dir), setupTestAccount, d.decide)
	if err != nil {
		t.Fatal(err)
	}
	if mark, ok := reopened.unconfirmed["opencode"]; !ok || !mark.genericPending() || mark.Question != "" {
		t.Fatalf("the restored mark did not persist: %+v", mark)
	}
}

// A Not now answer, and a question answered by anyone but the person through
// the operator, keeps nothing.
func TestOnlyThePersonsSetUpAnswerKeeps(t *testing.T) {
	q, d, book := setupKeepFixture(t)
	started := startSetupQuestion(t, q, d, "")
	answerSetupQuestion(t, book, started.Question, "not_now")
	if names, err := d.setupKeepAnswer(context.Background(), book, started.Question); err != nil || len(names) != 0 || !unconfirmedEntry(d, "opencode") {
		t.Fatal("Not now kept", names, err)
	}
}

// Undo of a Setup Keep returns the entry to unconfirmed through
// AskConfirmation, which asks the person whether to keep it. Not now, the
// prompt closed with no answer, leaves the entry unconfirmed: it waits for
// the person's click in Apps, and a later Keep answer still confirms it.
func TestNotNowAfterUndoLeavesTheEntryUnconfirmed(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f, d, ctx := run.f, run.d, run.ctx
	editor := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor"}
	if r := d.register(ctx, f.comfy, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	// The Keep Setup's answer makes (setupKeepAnswer), recorded with what
	// Undo restores.
	d.mu.Lock()
	mark := d.unconfirmed["editor"]
	err := d.writeSetupKeptLocked(ctx, map[string]setupKeep{"editor": {Provenance: setupKeepProvenance, Occurrence: "setup-occurrence", Program: editor.Program, Mark: &mark}})
	if err == nil {
		err = d.clearMarkLocked(ctx, "editor")
	}
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, unconfirmed := unconfirmedNow(d, "editor"); unconfirmed {
		t.Fatal("Setup's Keep did not confirm the entry")
	}
	if r := d.askConfirmation(ctx, f.panel, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("Undo:", r)
	}
	owner := &selfRegisteredQuestions{d: d}
	_, restored, pending := owner.selfEntry("editor")
	if !pending || restored.Question == "" {
		t.Fatalf("Undo did not ask the person: %+v %t", restored, pending)
	}
	if _, outcome, err := f.a.book.RetireApplicationAuthorized(restored.Question, nil); err != nil || outcome != "retired" {
		t.Fatal("Not now:", outcome, err)
	}
	if registered, unconfirmed := unconfirmedNow(d, "editor"); !registered || !unconfirmed {
		t.Fatal("Not now changed the entry", registered, unconfirmed)
	}
	if _, ok := owner.selfEntries()["editor"]; !ok {
		t.Fatal("the entry no longer waits for the person in Apps")
	}
	// The person's click in Apps asks again; Keep confirms it.
	if r := d.askConfirmation(ctx, f.panel, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("the click in Apps:", r)
	}
	_, again, _ := owner.selfEntry("editor")
	if again.Question == "" || again.Question == restored.Question {
		t.Fatalf("the click in Apps did not ask afresh: %+v", again)
	}
	answer(t, f, again.Question, "keep")
	waitDirectory(t, d, func() bool { _, unconfirmed := unconfirmedNow(d, "editor"); return !unconfirmed })
}
