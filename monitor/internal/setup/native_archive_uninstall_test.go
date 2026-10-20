package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeUninstallRetainsIntentAndCompletedState(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(root, "personal.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "user-data")
	if err := os.WriteFile(outside, []byte("workflow"), 0600); err != nil {
		t.Fatal(err)
	}
	old := nativeArchiveInstalledCommand
	t.Cleanup(func() { nativeArchiveInstalledCommand = old })
	nativeArchiveInstalledCommand = func(_ context.Context, _ sevenZipPin, original string) ([]byte, error) {
		plan.InstalledRoot = original
		return json.Marshal(plan)
	}
	r := NewRunner(j, &fakeTransfer{})
	refuse := true
	calls := 0
	join := func(context.Context, Artifact, string) error {
		calls++
		if refuse {
			return errors.New("lost registration reply")
		}
		return nil
	}
	if err := r.UninstallNative(ctx, fixtureComponent, join); err == nil {
		t.Fatal("lost registration reply accepted")
	}
	current, err := j.Load(ctx)
	if err != nil || !current.Effects[fixtureComponent].Removing {
		t.Fatalf("intent lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, a.ApplicationProgram)); err != nil {
		t.Fatal("refusal deleted image")
	}
	if _, err := r.Advance(ctx, fixtureComponent); err == nil {
		t.Fatal("background advancement accepted removal intent")
	}
	refuse = false
	if err := r.UninstallNative(ctx, fixtureComponent, join); err != nil {
		t.Fatal(err)
	}
	current, err = j.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Effects[fixtureComponent].Phase != Removed || current.Effects[fixtureComponent].Removing || current.Ready() {
		t.Fatal("completed uninstall state is not durable")
	}
	if calls != 2 {
		t.Fatal("explicit retry did not reconcile registration")
	}
	if _, err := os.Stat(filepath.Join(root, a.ApplicationProgram)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source-owned image remains")
	}
	for _, p := range []string{filepath.Join(root, "personal.txt"), outside} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal("personal data lost")
		}
	}
	if _, err := r.Advance(ctx, fixtureComponent); err == nil {
		t.Fatal("removed component automatically advanced")
	}
	saved, err := j.SavePlan(ctx, current.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Effects[fixtureComponent].Phase != Removed {
		t.Fatal("same cached plan restarted removed component")
	}
	if _, err := j.ReinstallNative(ctx, saved, fixtureComponent, false); err == nil {
		t.Fatal("reinstall without consent")
	}
	if _, err := j.ReinstallNative(ctx, saved, "foreign", true); err == nil {
		t.Fatal("foreign component reset")
	}
	stale := saved
	stale.Effects = map[string]Effect{fixtureComponent: {Phase: Removed, Revision: 999}}
	if _, err := j.ReinstallNative(ctx, stale, fixtureComponent, true); err == nil {
		t.Fatal("stale CAS reset")
	}
	reinstalled, err := j.ReinstallNative(ctx, saved, fixtureComponent, true)
	if err != nil {
		t.Fatal(err)
	}
	if reinstalled.Effects[fixtureComponent].Phase != Pending {
		t.Fatal("explicit reviewed reinstall did not reset selected effect")
	}
	if _, err := j.ReinstallNative(ctx, reinstalled, fixtureComponent, true); err == nil {
		t.Fatal("wrong phase reset")
	}
}
