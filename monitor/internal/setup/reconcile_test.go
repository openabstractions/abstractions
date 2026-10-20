package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/client"
)

type cancellingTransfer struct {
	*fakeTransfer
	cancels int
}

func (c *cancellingTransfer) Cancel(context.Context, WorkIdentity) error {
	c.cancels++
	return nil
}

// nextRelease returns a catalog that pins version for componentID and
// carries the given earlier pins.
func nextRelease(base Catalog, componentID, version string, earlier ...Artifact) Catalog {
	release := base
	release.Components = append([]Component(nil), base.Components...)
	for i := range release.Components {
		if release.Components[i].ID == componentID {
			pin := *release.Components[i].Artifact
			pin.Version, pin.SHA256 = version, strings.Repeat(version[:1], 64)
			release.Components[i].Artifact = &pin
		}
	}
	release.PreviousInstallPins = nil
	for _, pin := range earlier {
		release.PreviousInstallPins = append(release.PreviousInstallPins, KnownInstallPin{ComponentID: componentID, Artifact: pin})
	}
	return release
}

func TestReconcilePriorCancelsAnOpenDownloadAndUnblocksTheNewPlan(t *testing.T) {
	ctx := context.Background()
	oldCatalog := fixtureCatalog()
	oldPlan := mustPlan(t, oldCatalog, Lemonade)
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &oldCatalog}
	state, err := journal.SavePlan(ctx, oldPlan)
	if err != nil {
		t.Fatal(err)
	}
	work := &WorkIdentity{Key: "old-request", HistoryEpoch: "old-epoch",
		Binding:           client.JobsBinding{Endpoint: "old-accept", LogicalOwner: "owner"},
		OperationsBinding: client.JobsBinding{Endpoint: "old-operations", LogicalOwner: "owner"}}
	if _, err := journal.Update(ctx, oldPlan.ID, "lemonade-engine", state.Effects["lemonade-engine"], Effect{Phase: Requested, Work: work}); err != nil {
		t.Fatal(err)
	}
	oldPin := *oldPlan.Components[0].Artifact
	newCatalog := nextRelease(oldCatalog, "lemonade-engine", "4.0.0", oldPin)
	journal.catalogOverride = &newCatalog
	newPlan := mustPlan(t, newCatalog, Lemonade)
	if _, err := journal.SavePlan(ctx, newPlan); err == nil {
		t.Fatal("an open download of the earlier plan was dropped")
	}

	failing := &cancellingTransfer{fakeTransfer: &fakeTransfer{state: "running"}}
	if _, err := NewRunner(journal, &observeFails{failing}).ReconcilePrior(ctx, "lemonade-engine"); err == nil {
		t.Fatal("an unobserved download was settled")
	}
	if effect, _ := journal.Load(ctx); effect.Effects["lemonade-engine"].Phase != Requested {
		t.Fatal("a failed observation changed the earlier plan")
	}
	transfer := &cancellingTransfer{fakeTransfer: &fakeTransfer{state: "running"}}
	status, err := NewRunner(journal, transfer).ReconcilePrior(ctx, "lemonade-engine")
	if err != nil || transfer.cancels != 1 || status.Components[0].Effect.Phase != Failed || status.CurrentCatalog {
		t.Fatalf("open download was not cancelled and settled: %+v, %v, cancels=%d", status, err, transfer.cancels)
	}
	if transfer.submit != 0 || transfer.copy != 0 {
		t.Fatal("reconciliation resumed the earlier download")
	}
	if _, err := journal.SavePlan(ctx, newPlan); err != nil {
		t.Fatalf("the reconciled earlier plan still blocks the current one: %v", err)
	}
	if _, err := NewRunner(journal, transfer).ReconcilePrior(ctx, "lemonade-engine"); err == nil {
		t.Fatal("the current release's plan was reconciled as an earlier one")
	}
}

type observeFails struct{ *cancellingTransfer }

func (o *observeFails) Observe(context.Context, WorkIdentity) (TransferObservation, error) {
	return TransferObservation{}, errors.New("job service unreachable")
}

func TestReconcilePriorRecordsAFinishedInterruptedInstall(t *testing.T) {
	ctx := context.Background()
	standInHome(t)
	zipPath, built := archiveFixture(t, packageEntries(fixtureComponent, "1.0.0")...)
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	oldCatalog := fixtureCatalog()
	index := len(oldCatalog.Components) - 1 // fixtureComponent
	pin := *oldCatalog.Components[index].Artifact
	pin.Version, pin.SHA256, pin.Bytes, pin.PeakBytes = "1.0.0", built.SHA256, built.Bytes, built.PeakBytes
	oldCatalog.Components[index].Artifact = &pin
	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json"), catalogOverride: &oldCatalog}
	runner := NewRunner(journal, &fakeTransfer{state: "complete", bytes: data})
	if _, err := runner.Start(ctx, []AppID{fixtureApp}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := runner.Advance(ctx, fixtureComponent); err != nil {
			t.Fatal(err)
		}
	}
	runner.crashAfter = crashAt("install")
	if _, err := runner.Advance(ctx, fixtureComponent); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	newCatalog := nextRelease(oldCatalog, fixtureComponent, "2.0.0", pin)
	journal.catalogOverride = &newCatalog
	status, err := NewRunner(journal, nil).ReconcilePrior(ctx, fixtureComponent)
	if err != nil || status.Components[0].Effect.Phase != Installed {
		t.Fatalf("finished install of the earlier plan not recorded: %+v, %v", status, err)
	}
	state, err := journal.SavePlan(ctx, mustPlan(t, newCatalog, fixtureApp))
	if err != nil {
		t.Fatal(err)
	}
	if effect := state.Effects[fixtureComponent]; effect.Retained == nil || *effect.Retained != pin {
		t.Fatalf("the reconciled install is not the update's retained version: %+v", effect)
	}
}

func TestReconcilePriorRemovesAnUnactivatedUpdateAndKeepsTheInstalledVersion(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged} {
		f.advance(r, phase)
	}
	third := nextRelease(*f.journal.catalogOverride, f.component, "3.0.0", f.v1, f.v2)
	f.journal.catalogOverride = &third
	if _, err := f.journal.SavePlan(f.ctx, mustPlan(t, third, fixtureApp)); err == nil {
		t.Fatal("an unactivated update of the earlier plan was dropped")
	}
	status, err := NewRunner(f.journal, nil).ReconcilePrior(f.ctx, f.component)
	if err != nil {
		t.Fatal(err)
	}
	effect := status.Components[0].Effect
	if effect.Phase != Failed || effect.Retained == nil || *effect.Retained != f.v1 || len(effect.Retired) != 0 {
		t.Fatalf("reconciliation did not keep the installed version active: %+v", effect)
	}
	if _, err := os.Lstat(f.root2); !os.IsNotExist(err) {
		t.Fatalf("the unactivated version remains: %v", err)
	}
	if !f.installed(f.v1, f.root1) {
		t.Fatal("reconciliation touched the installed version")
	}
	state, err := f.journal.SavePlan(f.ctx, mustPlan(t, third, fixtureApp))
	if err != nil {
		t.Fatal(err)
	}
	if next := state.Effects[f.component]; next.Phase != Pending || next.Retained == nil || *next.Retained != f.v1 {
		t.Fatalf("the next update does not start from the installed version: %+v", next)
	}
}

func TestReconcilePriorPreservesAForeignFolderAndAnEmptyOneUnblocks(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	r := f.runner()
	f.advance(r, Requested)
	f.advance(r, Verified)
	r.crashAfter = crashAt("install")
	if _, err := r.Advance(f.ctx, f.component); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	mine := filepath.Join(f.root2, "mine.txt")
	if err := os.WriteFile(mine, []byte("not the package"), 0o600); err != nil {
		t.Fatal(err)
	}
	third := nextRelease(*f.journal.catalogOverride, f.component, "3.0.0", f.v1, f.v2)
	f.journal.catalogOverride = &third
	if _, err := NewRunner(f.journal, nil).ReconcilePrior(f.ctx, f.component); err == nil {
		t.Fatal("a folder that differs from the package was settled")
	}
	if data, err := os.ReadFile(mine); err != nil || string(data) != "not the package" || f.effect().Phase != Installing {
		t.Fatalf("a refused reconciliation changed something: %q, %v, %+v", data, err, f.effect())
	}
	if err := os.RemoveAll(f.root2); err != nil {
		t.Fatal(err)
	}
	status, err := NewRunner(f.journal, nil).ReconcilePrior(f.ctx, f.component)
	if err != nil || status.Components[0].Effect.Phase != Failed {
		t.Fatalf("an install that left nothing was not settled: %+v, %v", status, err)
	}
	if _, err := f.journal.SavePlan(f.ctx, mustPlan(t, third, fixtureApp)); err != nil {
		t.Fatalf("the settled earlier plan still blocks: %v", err)
	}
}
