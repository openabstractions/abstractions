//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	resourcewire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// keepHoldRun is the Connect fixture (a real unsigned applications@3 image
// through the production register path) with the runtime's Keep hold writer
// connected, and the runtime's lease book over the same rights.
func keepHoldRun(t *testing.T) (*connectRun, *resourceservice.Book) {
	t.Helper()
	run := newConnectRun(t)
	r := run.f.a.rights
	if err := r.policy.RegisterAction(resourceservice.ActionHold); err != nil {
		t.Fatal(err)
	}
	run.d.programHoldKeep = r.permitKeptHolds
	run.d.programConfigReport = func(err error) { t.Errorf("Keep report: %v", err) }

	composed := composeResources("", router.New(), nil, func(error) {})
	t.Cleanup(composed.close)
	composed.path = t.TempDir()
	options := leaseBookOptions(composed, router.New(), r, &standInWake{})
	// The test's peers carry no identity: Hold decides for the connector's
	// subject through the runtime's own rights, as holdPolicy does for the
	// subject its peer binds.
	options.Hold = func(ctx context.Context, _ *identity.Peer, action, resource string) error {
		if d := r.decide(ctx, run.subject, action, resource); d.Outcome != rwire.DecisionOutcomePermitted {
			return errors.New(d.Outcome.String())
		}
		return nil
	}
	book, err := resourceservice.OpenBook(options)
	if err != nil {
		t.Fatal(err)
	}
	return run, book
}

// connect is the person's Connect click in the Panel for the image on disk,
// then the connector's own register-self, which the click keeps.
func (run *connectRun) connect(t *testing.T) {
	t.Helper()
	if r := run.expect(run.f.panel, "comfyui", run.program, run.pin(t)); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("ExpectConnect: %+v", r)
	}
	if e := run.registerSelf(t); e.State != "enabled" {
		t.Fatalf("Connect did not keep the integration: %+v", e)
	}
}

func (run *connectRun) holdRule(resource string) rwire.RuleRead {
	r := run.f.a.rights
	return r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: run.program}, resourceservice.ActionHold, resource)
}

func (run *connectRun) requireKeptHolds(t *testing.T, resources ...string) {
	t.Helper()
	for _, resource := range resources {
		read := run.holdRule(resource)
		if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit || read.Record.Why != keptHoldWhy {
			t.Fatalf("no hold on %s for the kept integration: %+v", resource, read)
		}
	}
}

func (run *connectRun) acquireCard(book *resourceservice.Book) resourcewire.AcquireOutcome {
	who := resourceservice.Subject{Account: run.subject.Account, Program: run.subject.Program}
	return book.Acquire(context.Background(), who, &identity.Peer{}, instrument.Card0, 1<<20, 0).Outcome
}

// Owner ruling 2026-11-12, architect R35: the Connect click keeps ComfyUI's
// integration image, the one the Panel installed with its release pin, and
// that Keep writes hold on card:0 and memory for it. Its card lease, refused
// before, is granted after.
func TestKeepingComfyUIIntegrationLetsItHoldTheCard(t *testing.T) {
	run, book := keepHoldRun(t)
	if got := run.acquireCard(book); got != resourcewire.AcquireOutcomeNotPermitted {
		t.Fatalf("an unkept connector's card lease read %s", got)
	}
	run.connect(t)
	run.requireKeptHolds(t, instrument.Card0, instrument.Memory)
	if got := run.acquireCard(book); got != resourcewire.AcquireOutcomeAcquired {
		t.Fatalf("the kept integration's card lease read %s", got)
	}
}

// A third-party app the person keeps through the Keep question gets no hold,
// though its manifest declares a model need and uses the ComfyUI name. The
// image was never installed by a Connect click.
func TestKeepingAThirdPartyAppWithAModelNeedWritesNoHold(t *testing.T) {
	run, book := keepHoldRun(t)
	manifest := registration.EncodeManifest(&registration.Manifest{
		Version: "applications@3", Name: "comfyui", Title: "ComfyUI",
		Host:   registration.HostRecipe{ReadinessDeadlineMs: 1000},
		Offers: appmodel.CommandDeclaration{Version: "appmodel@1"},
		Needs: registration.Needs{Models: []registration.ModelNeed{{
			Modality: "image", RequiredFeatures: []string{}, Placement: registration.PlacementAny,
		}}},
	})
	if err := os.WriteFile(run.program, manifestPE(manifest, false), 0o600); err != nil {
		t.Fatal(err)
	}
	run.register(t)
	q := selfRegisteredQuestion(t, run.f, run.d, "comfyui")
	answer(t, run.f, q.ID, "keep")
	waitDirectory(t, run.d, func() bool { e, _, _ := run.entry(); return e.State == "enabled" })
	waitUnfollowed(t, run.k, q.ID)
	for _, resource := range []string{instrument.Card0, instrument.Memory} {
		if read := run.holdRule(resource); read.Outcome != rwire.RuleReadOutcomeUnknown {
			t.Fatalf("Keep wrote hold on %s for a third-party app: %+v", resource, read)
		}
	}
	if got := run.acquireCard(book); got != resourcewire.AcquireOutcomeNotPermitted {
		t.Fatalf("the third-party app's card lease read %s", got)
	}
}

// The person's existing deny on the card stays; the Keep still writes memory.
func TestKeepingTheIntegrationLeavesAnExistingCardDeny(t *testing.T) {
	run, book := keepHoldRun(t)
	r := run.f.a.rights
	if err := r.policy.Set(rwire.Subject{Account: r.owner, Program: run.program}, resourceservice.ActionHold, instrument.Card0, false); err != nil {
		t.Fatal(err)
	}
	run.connect(t)
	if read := run.holdRule(instrument.Card0); read.Outcome != rwire.RuleReadOutcomeFound || read.Record.Rule.Permit {
		t.Fatalf("the Keep replaced the person's deny on the card: %+v", read)
	}
	run.requireKeptHolds(t, instrument.Memory)
	if got := run.acquireCard(book); got != resourcewire.AcquireOutcomeNotPermitted {
		t.Fatalf("a denied integration's card lease read %s", got)
	}
}

// An integration kept before these rules existed gets them at the next start
// (writeKeptHolds).
func TestStartWritesHoldsForAKeptIntegration(t *testing.T) {
	run, _ := keepHoldRun(t)
	write := run.d.programHoldKeep
	run.d.programHoldKeep = nil
	run.connect(t)
	if read := run.holdRule(instrument.Card0); read.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatalf("a hold before the sweep: %+v", read)
	}
	run.d.programHoldKeep = write
	run.d.writeKeptHolds(run.ctx)
	run.requireKeptHolds(t, instrument.Card0, instrument.Memory)
}

// keepByQuestion is the person's Keep through the Keep question, waiting
// until the runtime has applied it.
func (run *connectRun) keepByQuestion(t *testing.T) applicationV3Registration {
	t.Helper()
	q := selfRegisteredQuestion(t, run.f, run.d, "comfyui")
	answer(t, run.f, q.ID, "keep")
	waitDirectory(t, run.d, func() bool { e, _, _ := run.entry(); return e.State == "enabled" })
	waitUnfollowed(t, run.k, q.ID)
	e, _, _ := run.entry()
	return e
}

// installerRepin is Setup's step: it names OA's release pin for the
// connector through ExpectConnect, then re-pins the kept entry, by its
// revision, to the image at the connector's path.
func (run *connectRun) installerRepin(t *testing.T, pin, revision string) wire.ApplicationOutcome {
	t.Helper()
	if r := run.expect(run.f.panel, "comfyui", run.program, pin); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("ExpectConnect: %+v", r)
	}
	return run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, run.program, revision).Outcome
}

func (run *connectRun) requireNoHolds(t *testing.T) {
	t.Helper()
	for _, resource := range []string{instrument.Card0, instrument.Memory} {
		if read := run.holdRule(resource); read.Outcome != rwire.RuleReadOutcomeUnknown {
			t.Fatalf("hold on %s for an image that is no kept integration: %+v", resource, read)
		}
	}
}

// Architect R35: the pin compared is OA's release pin. A Connect click whose
// pin is not the image the connector registers keeps nothing and records
// nothing; the person's later Keep by the question gets no hold.
func TestKeepHoldNeedsTheReleasePin(t *testing.T) {
	run, book := keepHoldRun(t)
	other := filepath.Join(t.TempDir(), "other.exe")
	writeUnsignedImage(t, other, "ComfyUI", true)
	release, _ := imagePin(t, other)
	if r := run.expect(run.f.panel, "comfyui", run.program, release); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("ExpectConnect: %+v", r)
	}
	if e := run.registerSelf(t); e.State != "pending" {
		t.Fatalf("a connector that is not the release pin was kept: %+v", e)
	}
	run.keepByQuestion(t)
	run.requireNoHolds(t)
	if got := run.acquireCard(book); got != resourcewire.AcquireOutcomeNotPermitted {
		t.Fatalf("the card lease read %s", got)
	}
}

// Architect R35: an installer re-pin of an integration the person kept by
// the Keep question to OA's new release pin writes the record with source
// installer_update, and the hold follows.
func TestInstallerRepinRecordsTheUpdateAndTheHoldFollows(t *testing.T) {
	run, book := keepHoldRun(t)
	run.register(t)
	kept := run.keepByQuestion(t)
	run.requireNoHolds(t)
	writeUnsignedImage(t, run.program, "ComfyUI", true)
	if got := run.installerRepin(t, run.pin(t), kept.Revision); got != wire.ApplicationOutcomeApplied {
		t.Fatalf("the installer's re-pin: %s", got)
	}
	if record := connectKeptRecord(t, run)["comfyui"]; record.Decision != installerUpdateSource || record.SHA256 != run.pin(t) {
		t.Fatalf("installer record %+v", record)
	}
	run.requireKeptHolds(t, instrument.Card0, instrument.Memory)
	if got := run.acquireCard(book); got != resourcewire.AcquireOutcomeAcquired {
		t.Fatalf("the updated integration's card lease read %s", got)
	}
}

// Architect R35 migration: the owner's state before R35, ComfyUI kept
// through the Keep question with its connector at OA's pin and no record.
// The R35 step re-pins it to the same image under the pin: it gets its
// record and the hold, with no click.
func TestInstallerMigrationGivesAKeptIntegrationItsHold(t *testing.T) {
	run, book := keepHoldRun(t)
	run.register(t)
	kept := run.keepByQuestion(t)
	run.requireNoHolds(t)
	if got := run.installerRepin(t, run.pin(t), kept.Revision); got != wire.ApplicationOutcomeApplied {
		t.Fatalf("the migration re-pin: %s", got)
	}
	// The fixture clock is fixed and the manifest and image are unchanged.
	// The exact expectation still needs to commit a durable receipt.
	if !run.d.lock(run.ctx) {
		t.Fatal(run.ctx.Err())
	}
	entry := run.d.v3.entries["comfyui"]
	run.d.unlock()
	if entry.Revision == kept.Revision || entry.KeepIdentity == "" || entry.KeepReceipt.ID == "" {
		t.Fatalf("migration did not publish a Keep receipt: %+v", entry)
	}
	if record := connectKeptRecord(t, run)["comfyui"]; record.Decision != installerUpdateSource {
		t.Fatalf("migration record %+v", record)
	}
	if got := run.installerRepin(t, run.pin(t), kept.Revision); got != wire.ApplicationOutcomeConflict {
		t.Fatalf("the superseded migration revision returned %s", got)
	}
	if got := run.installerRepin(t, run.pin(t), entry.Revision); got != wire.ApplicationOutcomeApplied {
		t.Fatalf("the current identical revision returned %s", got)
	}
	if record := connectKeptRecord(t, run)["comfyui"]; record.Receipt != entry.KeepReceipt.ID {
		t.Fatalf("identical re-pin replaced the migration receipt: %+v", record)
	}

	run.requireKeptHolds(t, instrument.Card0, instrument.Memory)
	if got := run.acquireCard(book); got != resourcewire.AcquireOutcomeAcquired {
		t.Fatalf("the migrated integration's card lease read %s", got)
	}
}

// A never-connected integration gets nothing: an installer re-pin of an
// entry the person never kept is refused and records nothing, and a re-pin
// of another image than the pin records nothing either.
func TestInstallerRepinGivesANeverConnectedIntegrationNothing(t *testing.T) {
	run, _ := keepHoldRun(t)
	waiting := run.register(t)
	if got := run.installerRepin(t, run.pin(t), waiting.Revision); got == wire.ApplicationOutcomeApplied {
		t.Fatal("an installer re-pinned an entry the person never kept")
	}
	if _, err := os.Stat(filepath.Join(run.d.dir, connectKeptFile)); !os.IsNotExist(err) {
		t.Fatalf("a never-connected integration got a record: %v", err)
	}
	run.requireNoHolds(t)

	kept := run.keepByQuestion(t)
	other := filepath.Join(t.TempDir(), "other.exe")
	writeUnsignedImage(t, other, "ComfyUI", true)
	release, _ := imagePin(t, other)
	if got := run.installerRepin(t, release, kept.Revision); got != wire.ApplicationOutcomeApplied {
		t.Fatalf("the re-pin: %s", got)
	}
	if _, err := os.Stat(filepath.Join(run.d.dir, connectKeptFile)); !os.IsNotExist(err) {
		t.Fatalf("a re-pin off the release pin wrote a record: %v", err)
	}
	run.requireNoHolds(t)
}

// R35 review: the hold rules last only as long as the Keep. Once the program
// is no longer a kept integration (here its connect-kept record is gone, as
// after a Remove or a changed image), the next sweep retires the runtime's
// own permits and the card lease is refused again. The person's block on
// the same target stays.
func TestKeptHoldsEndWithTheKeep(t *testing.T) {
	run, book := keepHoldRun(t)
	run.d.programHoldRetire = run.f.a.rights.retireKeptHolds
	run.connect(t)
	run.requireKeptHolds(t, instrument.Card0, instrument.Memory)
	r := run.f.a.rights
	subject := rwire.Subject{Account: r.owner, Program: run.program}
	// The person blocks the memory hold.
	permit := false
	current := r.policy.ReadRule(subject, resourceservice.ActionHold, instrument.Memory)
	if edit, err := r.policy.ChangeRule(current.Revision, subject, resourceservice.ActionHold, instrument.Memory, rights.RuleEdit{Permit: &permit, By: run.f.panel, Why: "person"}, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	if err := os.WriteFile(filepath.Join(run.d.dir, connectKeptFile), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	run.d.writeKeptHolds(run.ctx)
	if read := run.holdRule(instrument.Card0); read.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatalf("the card hold outlived the Keep: %+v", read)
	}
	if read := run.holdRule(instrument.Memory); read.Outcome != rwire.RuleReadOutcomeFound || read.Record.Rule.Permit {
		t.Fatalf("the person's block on memory was retired: %+v", read)
	}
	if got := run.acquireCard(book); got != resourcewire.AcquireOutcomeNotPermitted {
		t.Fatalf("a no longer kept integration's card lease read %s", got)
	}
}

// R35 review: the person's Remove of a Keep hold row lasts. The holds are
// written once per connect-kept record, so the next sweep, as at start,
// leaves the removed rule absent.
func TestRemovedKeepHoldStaysRemoved(t *testing.T) {
	run, _ := keepHoldRun(t)
	run.d.programHoldRetire = run.f.a.rights.retireKeptHolds
	run.connect(t)
	run.requireKeptHolds(t, instrument.Card0, instrument.Memory)
	if !connectKeptRecord(t, run)["comfyui"].HoldsWritten {
		t.Fatal("the record does not say its holds were written")
	}
	r := run.f.a.rights
	subject := rwire.Subject{Account: r.owner, Program: run.program}
	current := r.policy.ReadRule(subject, resourceservice.ActionHold, instrument.Card0)
	if edit, err := r.policy.ChangeRule(current.Revision, subject, resourceservice.ActionHold, instrument.Card0, rights.RuleEdit{By: run.f.panel, Exact: true}, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	run.d.writeKeptHolds(run.ctx)
	if read := run.holdRule(instrument.Card0); read.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatalf("the removed card hold came back: %+v", read)
	}
	run.requireKeptHolds(t, instrument.Memory)
}
