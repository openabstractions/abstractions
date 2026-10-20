package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// R20 render check, 2026-11-10: an uninstall refused at its registration
// step left Removing set with every file in place, and no step ran it on.
// SettleRemoval keeps the intact installation; a package whose files changed
// stays removing for an explicit remove or reinstall.
func TestSettleRemovalKeepsAnIntactInstallation(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	source, a, _, plan := nativeArchiveFixture(t)
	c := fixtureCatalog()
	for i := range c.Components {
		if c.Components[i].ID == fixtureComponent {
			c.Components[i].Artifact = &a
		}
	}
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	ctx := context.Background()
	state, err := j.SavePlan(ctx, mustPlan(t, j.Catalog(), fixtureApp))
	if err != nil {
		t.Fatal(err)
	}
	state.Effects[fixtureComponent] = Effect{Phase: Installed, Revision: 1}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(j.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(ctx, a, source, root); err != nil {
		t.Fatal(err)
	}
	old := nativeArchiveInstalledCommand
	t.Cleanup(func() { nativeArchiveInstalledCommand = old })
	nativeArchiveInstalledCommand = func(_ context.Context, _ sevenZipPin, original string) ([]byte, error) {
		plan.InstalledRoot = original
		return json.Marshal(plan)
	}
	r := NewRunner(j, &fakeTransfer{})
	refused := func(context.Context, Artifact, string) error {
		return errors.New("application ownership observation refused")
	}
	if err := r.UninstallNative(ctx, fixtureComponent, refused); err == nil {
		t.Fatal("the refused registration step finished the uninstall")
	}
	if current, _ := j.Load(ctx); !current.Effects[fixtureComponent].Removing {
		t.Fatal("the refused uninstall left no removal to settle")
	}

	if _, err := r.SettleRemoval(ctx, fixtureComponent); err != nil {
		t.Fatal(err)
	}
	current, err := j.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e := current.Effects[fixtureComponent]; e.Removing || e.Phase != Installed {
		t.Fatalf("settled effect %+v", e)
	}
	if _, err := os.Stat(filepath.Join(root, a.ApplicationProgram)); err != nil {
		t.Fatal("settling deleted the installation")
	}
	again, _ := j.Load(ctx)
	if _, err := r.SettleRemoval(ctx, fixtureComponent); err != nil {
		t.Fatal(err)
	}
	if after, _ := j.Load(ctx); after.Effects[fixtureComponent].Revision != again.Effects[fixtureComponent].Revision {
		t.Fatal("settling a settled component changed it")
	}

	// The owner's record, R20b: uncertain and removing. Intact files settle
	// it installed.
	setEffect := func(e Effect) {
		t.Helper()
		state, err := j.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		state.Effects[fixtureComponent] = e
		raw, _ := json.Marshal(state)
		if err := os.WriteFile(j.Path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	setEffect(Effect{Phase: Uncertain, Removing: true, Revision: 8, Reason: "component is being removed or was removed", ResourceIndex: 1})
	if _, err := r.SettleRemoval(ctx, fixtureComponent); err != nil {
		t.Fatal(err)
	}
	if e := mustEffect(t, j); e.Removing || e.Phase != Installed {
		t.Fatalf("uncertain removal with intact files settled to %+v", e)
	}

	// A removal whose files changed stays removing.
	if err := r.UninstallNative(ctx, fixtureComponent, refused); err == nil {
		t.Fatal("the refused registration step finished the uninstall")
	}
	if err := os.Remove(filepath.Join(root, a.ApplicationProgram)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SettleRemoval(ctx, fixtureComponent); err == nil {
		t.Fatal("a removal whose files changed was settled as installed")
	}
	if current, _ := j.Load(ctx); !current.Effects[fixtureComponent].Removing {
		t.Fatal("the refused settle cleared the removal")
	}
	// An uncertain one with changed files stays uncertain for its retry.
	setEffect(Effect{Phase: Uncertain, Removing: true, Revision: 9})
	if _, err := r.SettleRemoval(ctx, fixtureComponent); err != nil {
		t.Fatal(err)
	}
	if e := mustEffect(t, j); e.Removing || e.Phase != Uncertain {
		t.Fatalf("uncertain removal with changed files settled to %+v", e)
	}
}

// Owner, 2026-11-10 (R20c): a removal moved ComfyUI aside and was deleting
// its files when the page lost its request. Settling it reports the removal
// under way, and the removal continues from the moved folder, reporting its
// files as it goes.
func TestSettleRemovalContinuesARemovalUnderWay(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	source, a, _, plan := nativeArchiveFixture(t)
	c := fixtureCatalog()
	for i := range c.Components {
		if c.Components[i].ID == fixtureComponent {
			c.Components[i].Artifact = &a
		}
	}
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	ctx := context.Background()
	state, err := j.SavePlan(ctx, mustPlan(t, j.Catalog(), fixtureApp))
	if err != nil {
		t.Fatal(err)
	}
	state.Effects[fixtureComponent] = Effect{Phase: Installed, Revision: 1}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(j.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(ctx, a, source, root); err != nil {
		t.Fatal(err)
	}
	old := nativeArchiveInstalledCommand
	t.Cleanup(func() { nativeArchiveInstalledCommand = old })
	nativeArchiveInstalledCommand = func(_ context.Context, _ sevenZipPin, original string) ([]byte, error) {
		plan.InstalledRoot = original
		return json.Marshal(plan)
	}
	r := NewRunner(j, &fakeTransfer{})
	// The removal is recorded and the folder moved aside, as the owner's was.
	if err := r.UninstallNative(ctx, fixtureComponent, func(context.Context, Artifact, string) error { return errors.New("request lost") }); err == nil {
		t.Fatal("the lost step finished")
	}
	setEffect := func(e Effect) {
		t.Helper()
		state, err := j.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		state.Effects[fixtureComponent] = e
		raw, _ := json.Marshal(state)
		if err := os.WriteFile(j.Path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	removing := mustEffect(t, j)
	// An installed removal whose folder is gone and not moved aside also
	// continues; the removal finishes it.
	away := filepath.Join(t.TempDir(), "away")
	if err := os.Rename(root, away); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SettleRemoval(ctx, fixtureComponent); !errors.Is(err, ErrRemovalUnderway) {
		t.Fatalf("an installed removal with its folder gone settled as %v", err)
	}
	aside := filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+".removing")
	if err := os.Rename(away, aside); err != nil {
		t.Fatal(err)
	}
	// An uncertain removal with the folder moved aside settles for its own
	// retry.
	setEffect(Effect{Phase: Uncertain, Removing: true, Revision: 20})
	if _, err := r.SettleRemoval(ctx, fixtureComponent); err != nil {
		t.Fatalf("an uncertain removal under way did not settle: %v", err)
	}
	if e := mustEffect(t, j); e.Removing || e.Phase != Uncertain {
		t.Fatalf("uncertain removal under way settled to %+v", e)
	}
	setEffect(removing)
	if _, err := r.SettleRemoval(ctx, fixtureComponent); !errors.Is(err, ErrRemovalUnderway) {
		t.Fatalf("a removal under way settled as %v", err)
	}
	reports := 0
	progress := WithProgress(ctx, func(done, total int) {
		if total > 0 {
			reports++
		}
	})
	if err := r.UninstallNative(progress, fixtureComponent, func(context.Context, Artifact, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if e := mustEffect(t, j); e.Removing || e.Phase != Removed {
		t.Fatalf("the continued removal ended as %+v", e)
	}
	if reports == 0 {
		t.Fatal("the removal reported no progress")
	}
}

func mustEffect(t *testing.T, j Journal) Effect {
	t.Helper()
	state, err := j.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state.Effects[fixtureComponent]
}
