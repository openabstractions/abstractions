package main

import (
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
)

// A silent update keeps the kept state only for the same signer: subject,
// issuer and team. A certificate from another issuer naming the same subject
// is another publisher.
func TestApplicationV3ContinuityComparesTheWholeSigner(t *testing.T) {
	signed := func(subject, issuer, team string) applicationV3ManifestFile {
		return applicationV3ManifestFile{Source: applicationV3Source{Code: identity.Code{Trusted: true, Subject: subject, Issuer: issuer, TeamID: team}, Proof: identity.ProofBound}}
	}
	kept := signed("Example Software", "Example CA", "")
	if !sameApplicationV3Continuity(kept, signed("Example Software", "Example CA", "")) {
		t.Fatal("the same signer lost continuity")
	}
	for _, next := range []applicationV3ManifestFile{signed("Example Software", "Other CA", ""), signed("Example Software", "Example CA", "TEAM2"),
		signed("Other Software", "Example CA", ""), {Image: "aa", ImageSize: 1}} {
		if sameApplicationV3Continuity(kept, next) {
			t.Fatalf("continuity kept for %+v", next.Source.Code)
		}
	}
	unsigned := applicationV3ManifestFile{Image: "aa", ImageSize: 1}
	if !sameApplicationV3Continuity(unsigned, unsigned) || sameApplicationV3Continuity(unsigned, applicationV3ManifestFile{Image: "bb", ImageSize: 1}) {
		t.Fatal("unsigned image pin continuity")
	}
}

// Registration asks nothing (VISION 2026-10-06, amended 2026-11-10): an app
// that registers itself waits unconfirmed with no question, and the Keep
// question is raised only when the person acts on it in Apps
// (AskConfirmation). A restart asks nothing either.
func TestRegistrationAsksNothingUntilThePersonActsInApps(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f, d, ctx := run.f, run.d, run.ctx
	editor := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor", StartGuidance: "Open"}
	if r := d.register(ctx, f.comfy, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	owner := &selfRegisteredQuestions{d: d}
	if _, mark, ok := owner.selfEntry("editor"); !ok || mark.Question != "" {
		t.Fatalf("the registration asked: %+v %t", mark, ok)
	}
	run.start(t)
	d, ctx = run.d, run.ctx
	owner = &selfRegisteredQuestions{d: d}
	if _, mark, ok := owner.selfEntry("editor"); !ok || mark.Question != "" {
		t.Fatalf("the restart asked: %+v %t", mark, ok)
	}
	if r := d.askConfirmation(ctx, f.panel, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("the person's Apps action", r)
	}
	q := selfRegisteredQuestion(t, f, d, "editor")
	answer(t, f, q.ID, "keep")
	waitDirectory(t, d, func() bool { _, unconfirmed := unconfirmedNow(d, "editor"); return !unconfirmed })
}

// Register is create-only. The same program registering its name again
// updates the entry silently and keeps it kept; another program meets
// conflict and the entry stays as it was.
func TestRegistrationSameProgramUpdatesSilentlyAndAnotherConflicts(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f, d, ctx := run.f, run.d, run.ctx
	editor := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor", StartGuidance: "Open"}
	if r := d.register(ctx, f.comfy, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	if r := d.askConfirmation(ctx, f.panel, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	answer(t, f, selfRegisteredQuestion(t, f, d, "editor").ID, "keep")
	waitDirectory(t, d, func() bool { _, unconfirmed := unconfirmedNow(d, "editor"); return !unconfirmed })

	renamed := editor
	renamed.Title, renamed.StartGuidance = "Editor 2", "Open it from the Start menu"
	if r := d.register(ctx, f.comfy, renamed); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("same program", r)
	}
	if registered, unconfirmed := unconfirmedNow(d, "editor"); !registered || unconfirmed {
		t.Fatal("the update lost the kept state", registered, unconfirmed)
	}
	d.mu.Lock()
	title := d.descriptors["editor"].Title
	d.mu.Unlock()
	if title != "Editor 2" {
		t.Fatal("the update was not applied", title)
	}
	owner := &selfRegisteredQuestions{d: d}
	if _, _, pending := owner.selfEntry("editor"); pending {
		t.Fatal("the update asks again")
	}

	other := editor
	other.Program, other.Title = f.lemonade.Program, "Impostor"
	if r := d.register(ctx, f.lemonade, other); r.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal("another program", r)
	}
	d.mu.Lock()
	kept := d.descriptors["editor"]
	d.mu.Unlock()
	if kept.Program != f.comfy.Program || kept.Title != "Editor 2" {
		t.Fatal("the conflict changed the entry", kept)
	}
}

// An applications@3 registration by its own program asks nothing, and the
// same program publishing its name again without a revision updates it in
// place while another program meets conflict.
func TestApplicationV3SameProgramRepublishIsSilent(t *testing.T) {
	d := registration1459Directory(t, t.TempDir())
	asked := 0
	d.askKeep = func(string) error { asked++; return nil }
	program := registration1459Program("native.exe")
	registration1459Publish(t, d, "native", program, "Native", "operator", "")
	first := d.v3.entries["native"]
	registration1459Publish(t, d, "native", program, "Native 2", "operator", "")
	second := d.v3.entries["native"]
	if second.Revision == first.Revision || second.State != first.State || d.descriptors["native"].Title != "Native 2" {
		t.Fatal("same program republish", first, second, d.descriptors["native"])
	}
	v, f := registration1459File(t, "native", registration1459Program("other.exe"), "Other")
	if out := d.publishApplicationV3(t.Context(), v, f, "operator", ""); out.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal("another program", out)
	}
	if d.v3.entries["native"].Revision != second.Revision || d.descriptors["native"].Title != "Native 2" {
		t.Fatal("the conflict changed the entry")
	}
	registration1459Publish(t, d, "selfish", program, "Selfish", "self", "")
	if asked != 0 {
		t.Fatal("a registration asked", asked)
	}
}
