package main

import (
	"context"
	"slices"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// selfRegisteredFixture is a directory decided by the answerer fixture's
// rights, with the runtime's defaults, the Panel as operator holding
// application.manage, and the self-registration question connected. state is
// the directory's state folder, which a restart opens again.
type selfRegisteredRun struct {
	f      *answererFixture
	d      *applicationDirectory
	k      *selfRegisteredQuestions
	ctx    context.Context
	cancel context.CancelFunc
	state  string
}

func selfRegisteredFixture(t *testing.T) (*answererFixture, *applicationDirectory, context.Context) {
	run := newSelfRegisteredRun(t)
	return run.f, run.d, run.ctx
}

func newSelfRegisteredRun(t *testing.T) *selfRegisteredRun {
	t.Helper()
	f := newAnswererFixture(t)
	policy := f.a.rights.policy
	for _, action := range []string{ActionApplicationRegisterSelf, ActionApplicationManage} {
		if err := policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	if err := policy.ConfigureDefaults(f.answerer.Account, grants.RuntimeAccountDefaults(), defaultExcluded); err != nil {
		t.Fatal(err)
	}
	if err := policy.Set(f.panel, ActionApplicationManage, "account", true); err != nil {
		t.Fatal(err)
	}
	run := &selfRegisteredRun{f: f, state: t.TempDir()}
	run.start(t)
	return run
}

// start opens the directory from its state folder and connects the questions,
// as a runtime start does; an earlier start's followers stop first.
func (run *selfRegisteredRun) start(t *testing.T) {
	t.Helper()
	if run.cancel != nil {
		run.cancel()
	}
	f := run.f
	d, err := openApplications(run.state, f.answerer.Account, func(ctx context.Context, s rwire.Subject, action, resource string) (bool, error) {
		return f.a.rights.decide(ctx, s, action, resource).Outcome == rwire.DecisionOutcomePermitted, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	run.ctx, run.cancel = context.WithCancel(context.Background())
	t.Cleanup(run.cancel)
	run.d = d
	run.k = f.a.keepSelfRegistered(run.ctx, d)
}

// selfRegisteredQuestion is the pending question recorded for name's current
// registration.
func selfRegisteredQuestion(t *testing.T, f *answererFixture, d *applicationDirectory, name string) asks.Record {
	t.Helper()
	_, mark, ok := d.selfEntry(name)
	if !ok || mark.Question == "" {
		t.Fatalf("no question recorded for %s", name)
	}
	record, found, err := f.a.book.ApplicationRecord(mark.Question)
	if err != nil || !found || !record.Pending() {
		t.Fatalf("question about %s: found=%t %v %+v", name, found, err, record)
	}
	return record
}

func unconfirmedNow(d *applicationDirectory, name string) (registered, unconfirmed bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, registered = d.descriptors[name]
	_, unconfirmed = d.unconfirmed[name]
	return registered, unconfirmed
}

// waitDirectory waits for want to hold, woken by each directory change.
func waitDirectory(t *testing.T, d *applicationDirectory, want func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !want() {
		d.mu.Lock()
		changed := d.changed
		d.mu.Unlock()
		if want() {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("the directory did not reach the expected state")
		}
	}
}

// waitUnfollowed waits until no goroutine follows the question id.
func waitUnfollowed(t *testing.T, k *selfRegisteredQuestions, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for k.followed(id) {
		if time.Now().After(deadline) {
			t.Fatal("the question is still followed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func answer(t *testing.T, f *answererFixture, id, option string) {
	t.Helper()
	if _, outcome, err := f.a.book.AnswerApplication(id, option); err != nil || outcome != "answered" {
		t.Fatal(outcome, err)
	}
}

// rights RIGHTS-R22, asks ASK-R10: a program that registered itself is asked
// about once, as "<program> added itself to OpenAbstractions as <title>. Keep
// it?" with Keep and Remove only, carrying the publisher bound from the
// registering connection. Keep confirms the entry; Remove removes it and
// blocks the program from registering itself again; an assistant cannot
// answer.
func TestSelfRegistrationAsksThePersonToKeepIt(t *testing.T) {
	f, d, ctx := selfRegisteredFixture(t)
	editor := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor", StartGuidance: "Open"}
	signed := asks.Publisher{Subject: "Example Software", Trusted: true, Description: "Example Editor", OriginalName: "editor.exe"}
	if r := d.registerFrom(ctx, f.comfy, signed, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	if _, unconfirmed := unconfirmedNow(d, "editor"); !unconfirmed {
		t.Fatal("a self-registered entry reads confirmed")
	}
	q := selfRegisteredQuestion(t, f, d, "editor")
	if q.Text != f.comfy.Program+" added itself to OpenAbstractions as Editor. Keep it?" || q.About != f.comfy.Program ||
		!slices.Equal(q.Options, []string{"keep", "remove"}) || q.Key != asks.SelfRegisteredKey || q.AskerProgram != "" {
		t.Fatalf("question %+v", q)
	}
	if q.Publisher != signed.Subject || !q.PublisherTrusted || q.ProgramDescription != signed.Description || q.ProgramOriginalName != signed.OriginalName {
		t.Fatalf("the question does not carry the registration's publisher: %+v", q)
	}
	f.enroll(t, f.answerer)
	if _, err := f.a.authorizeAnswerer(ctx, answerer{subject: f.answerer}, q, f.a.scope, "keep"); err == nil {
		t.Fatal("an assistant may answer the self-registration question")
	}
	answer(t, f, q.ID, "keep")
	waitDirectory(t, d, func() bool { _, unconfirmed := unconfirmedNow(d, "editor"); return !unconfirmed })

	tool := wire.ApplicationDescriptor{Name: "tool", Program: f.lemonade.Program, Title: "Tool", StartGuidance: "Open"}
	if r := d.register(ctx, f.lemonade, tool); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	q = selfRegisteredQuestion(t, f, d, "tool")
	answer(t, f, q.ID, "remove")
	waitDirectory(t, d, func() bool { registered, _ := unconfirmedNow(d, "tool"); return !registered })
	if got := f.a.rights.decide(ctx, f.lemonade, ActionApplicationRegisterSelf, "account").Outcome; got != rwire.DecisionOutcomeDenied {
		t.Fatal("Remove wrote no Block", got)
	}
	if r := d.register(ctx, f.lemonade, tool); r.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal("a removed program registered itself straight back", r)
	}
	read := f.a.rights.policy.ReadRule(f.lemonade, ActionApplicationRegisterSelf, "account")
	if read.Record == nil || read.Record.Rule.Permit || read.Record.Why != selfRegisteredWhy {
		t.Fatalf("the Block %+v", read)
	}
}

// Closing the prompt or its expiry answers nothing: the entry stays
// unconfirmed, and AskConfirmation from Apps asks again with a new question.
func TestSelfRegistrationQuestionExpiryLeavesTheEntryUnconfirmed(t *testing.T) {
	f, d, ctx := selfRegisteredFixture(t)
	editor := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor", StartGuidance: "Open"}
	if r := d.register(ctx, f.comfy, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	q := selfRegisteredQuestion(t, f, d, "editor")
	if err := f.a.book.SetPendingLifetime(time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := f.a.book.ApplicationRecord(q.ID); found {
		t.Fatal("the expired question is still retained")
	}
	if registered, unconfirmed := unconfirmedNow(d, "editor"); !registered || !unconfirmed {
		t.Fatal("expiry changed the entry", registered, unconfirmed)
	}
	if err := f.a.book.SetPendingLifetime(asks.DefaultPendingLifetime); err != nil {
		t.Fatal(err)
	}
	if r := d.askConfirmation(ctx, f.cli, "editor"); r.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal("a program without application.manage asked again", r)
	}
	if r := d.askConfirmation(ctx, f.panel, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	selfRegisteredQuestion(t, f, d, "editor")
}

// Review round 2, A: the question is tied to its registration. A Keep given
// to an earlier registration's question never confirms a later registration
// of the same name, after a restart included.
func TestAnEarlierKeepDoesNotConfirmALaterRegistration(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f := run.f
	editor := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor", StartGuidance: "Open"}
	if r := run.d.register(run.ctx, f.comfy, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	first := selfRegisteredQuestion(t, f, run.d, "editor")
	answer(t, f, first.ID, "keep")
	waitDirectory(t, run.d, func() bool { _, unconfirmed := unconfirmedNow(run.d, "editor"); return !unconfirmed })
	if r := run.d.remove(run.ctx, f.panel, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("the Panel's Remove", r)
	}
	if r := run.d.register(run.ctx, f.comfy, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	second := selfRegisteredQuestion(t, f, run.d, "editor")
	if second.ID == first.ID {
		t.Fatal("the new registration reuses the earlier question")
	}
	run.start(t)
	if registered, unconfirmed := unconfirmedNow(run.d, "editor"); !registered || !unconfirmed {
		t.Fatal("the earlier Keep confirmed the new registration after a restart", registered, unconfirmed)
	}
	if !run.k.followed(second.ID) || run.k.followed(first.ID) {
		t.Fatal("the restart follows another question than the registration's")
	}
}

// Review round 2, A: lifting a Block that a Remove wrote and restarting does
// not apply the old Remove to the program's new registration.
func TestALiftedBlockDoesNotReapplyAnOldRemove(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f := run.f
	tool := wire.ApplicationDescriptor{Name: "tool", Program: f.lemonade.Program, Title: "Tool", StartGuidance: "Open"}
	if r := run.d.register(run.ctx, f.lemonade, tool); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	old := selfRegisteredQuestion(t, f, run.d, "tool")
	answer(t, f, old.ID, "remove")
	waitDirectory(t, run.d, func() bool { registered, _ := unconfirmedNow(run.d, "tool"); return !registered })
	waitUnfollowed(t, run.k, old.ID)

	policy := f.a.rights.policy
	read := policy.ReadRule(f.lemonade, ActionApplicationRegisterSelf, "account")
	if edit, err := policy.ChangeRule(read.Revision, f.lemonade, ActionApplicationRegisterSelf, "account", rights.RuleEdit{By: f.panel}, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal("lifting the Block", edit, err)
	}
	if r := run.d.register(run.ctx, f.lemonade, tool); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("registering again after the Block was lifted", r)
	}
	run.start(t)
	if registered, unconfirmed := unconfirmedNow(run.d, "tool"); !registered || !unconfirmed {
		t.Fatal("the old Remove acted on the new registration", registered, unconfirmed)
	}
	if got := f.a.rights.decide(run.ctx, f.lemonade, ActionApplicationRegisterSelf, "account").Outcome; got != rwire.DecisionOutcomePermitted {
		t.Fatal("the old Remove wrote its Block again", got)
	}
	// An answer given to the old question again changes nothing either.
	run.k.apply(asks.Record{ID: old.ID, Key: asks.SelfRegisteredKey, Option: "remove", Answered: time.Now()}, "tool", f.lemonade.Program)
	if registered, _ := unconfirmedNow(run.d, "tool"); !registered {
		t.Fatal("an old Remove removed the new registration")
	}
}

// Review round 2, C: a program has one unconfirmed self-registration at a
// time, and Remove takes away every unconfirmed entry of that program.
func TestOneUnconfirmedSelfRegistrationPerProgram(t *testing.T) {
	f, d, ctx := selfRegisteredFixture(t)
	editor := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor", StartGuidance: "Open"}
	second := wire.ApplicationDescriptor{Name: "editor-two", Program: f.comfy.Program, Title: "Editor Two", StartGuidance: "Open"}
	if r := d.register(ctx, f.comfy, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	if r := d.register(ctx, f.comfy, second); r.Outcome != wire.ApplicationOutcomeConflict || r.Reason != "unconfirmed" {
		t.Fatal("a second self-registration while one is unconfirmed", r)
	}
	// An entry stored before the rule: two unconfirmed entries of one program.
	if r := d.register(ctx, f.panel, second); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	d.mu.Lock()
	err := d.setMarkLocked(ctx, "editor-two", selfMark{})
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	q := selfRegisteredQuestion(t, f, d, "editor")
	answer(t, f, q.ID, "remove")
	waitDirectory(t, d, func() bool {
		one, _ := unconfirmedNow(d, "editor")
		two, _ := unconfirmedNow(d, "editor-two")
		return !one && !two
	})
}

// Review round 2, F: asking again about a pending question follows it once.
func TestAPendingQuestionIsFollowedOnce(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f := run.f
	editor := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor", StartGuidance: "Open"}
	if r := run.d.register(run.ctx, f.comfy, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	q := selfRegisteredQuestion(t, f, run.d, "editor")
	for i := 0; i < 3; i++ {
		if r := run.d.askConfirmation(run.ctx, f.panel, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(r)
		}
	}
	run.k.resume()
	run.k.mu.Lock()
	following := len(run.k.following)
	run.k.mu.Unlock()
	if following != 1 || !run.k.followed(q.ID) {
		t.Fatalf("followers %d for one pending question", following)
	}
	if again := selfRegisteredQuestion(t, f, run.d, "editor"); again.ID != q.ID {
		t.Fatal("asking again about a pending question admitted another")
	}
}

// A title that reads as OpenAbstractions is refused whether the app registers
// itself or an operator registers it (client.ReservedApplicationTitle).
func TestReservedTitlesAreRefusedOnBothRegistrationPaths(t *testing.T) {
	f, d, ctx := selfRegisteredFixture(t)
	helper := wire.ApplicationDescriptor{Name: "helper", Program: f.comfy.Program, Title: "OpenAbstractions Helper", StartGuidance: "Open"}
	if r := d.register(ctx, f.comfy, helper); r.Outcome != wire.ApplicationOutcomeInvalid || r.Reason != "title" {
		t.Fatal("register_self took a reserved title", r)
	}
	if r := d.register(ctx, f.panel, helper); r.Outcome != wire.ApplicationOutcomeInvalid || r.Reason != "title" {
		t.Fatal("Register took a reserved title", r)
	}
}
