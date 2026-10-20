package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// installedOABuild records an installed OA build of ComfyUI as an earlier
// release did: its pin in the approved plan, its files on disk and a file
// the person added beside them. It returns the release journal over the same
// file, whose catalogue pins no build, and the build's pin and root.
func installedOABuild(t *testing.T) (Journal, Artifact, string) {
	t.Helper()
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	source, a, _, plan := nativeArchiveFixture(t)
	earlier := fixtureCatalog()
	for i := range earlier.Components {
		if earlier.Components[i].ID == retainedArchiveComponent {
			earlier.Components[i].Artifact = &a
		}
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	ctx := context.Background()
	old := Journal{Path: path, catalogOverride: &earlier}
	state, err := old.SavePlan(ctx, mustPlan(t, old.Catalog(), ComfyUI))
	if err != nil {
		t.Fatal(err)
	}
	state.Effects[retainedArchiveComponent] = Effect{Phase: Installed, Revision: 1}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(path, raw, 0600); err != nil {
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
	saved := nativeArchiveInstalledCommand
	t.Cleanup(func() { nativeArchiveInstalledCommand = saved })
	nativeArchiveInstalledCommand = func(_ context.Context, _ sevenZipPin, original string) ([]byte, error) {
		plan.InstalledRoot = original
		return json.Marshal(plan)
	}
	release := withoutSharedTorch(NewCatalog())
	return Journal{Path: path, catalogOverride: &release}, a, root
}

// The OA build an earlier plan installed is removed under this release,
// whose catalogue no longer pins it: a lost registration reply keeps the
// removal under way, the retry finishes it, and the person's file stays.
func TestTheOABuildOfComfyIsRemovedWithoutAPin(t *testing.T) {
	j, a, root := installedOABuild(t)
	ctx := context.Background()
	state, err := j.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if found, _, ok := RetainedArchiveFor(ComfyUI).Installed(state); !ok || found.SHA256 != a.SHA256 {
		t.Fatalf("installed OA build not found: %v", ok)
	}
	if _, err := j.ComponentArtifact(state, retainedArchiveComponent); err == nil {
		t.Fatal("the release catalogue still authorizes the OA build's pin")
	}
	r := NewRunner(j, &fakeTransfer{})
	refuse, calls := true, 0
	unregister := func(context.Context, Artifact, string) error {
		calls++
		if refuse {
			return errors.New("lost registration reply")
		}
		return nil
	}
	if err := RetainedArchiveFor(ComfyUI).Remove(ctx, r.Journal, unregister); err == nil {
		t.Fatal("a lost registration reply was accepted")
	}
	state, _ = j.Load(ctx)
	if _, effect, ok := RetainedArchiveFor(ComfyUI).Installed(state); !ok || !effect.Removing {
		t.Fatal("the removal's intent was not kept")
	}
	refuse = false
	if err := RetainedArchiveFor(ComfyUI).Remove(ctx, r.Journal, unregister); err != nil {
		t.Fatal(err)
	}
	state, _ = j.Load(ctx)
	if _, _, ok := RetainedArchiveFor(ComfyUI).Installed(state); ok || state.Effects[retainedArchiveComponent].Phase != Removed || calls != 2 {
		t.Fatalf("removal not recorded: %+v after %d calls", state.Effects[retainedArchiveComponent], calls)
	}
	if _, err := os.Stat(filepath.Join(root, a.ApplicationProgram)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the build's program remains")
	}
	if _, err := os.Stat(filepath.Join(root, "personal.txt")); err != nil {
		t.Fatal("the person's file was removed")
	}
	if err := RetainedArchiveFor(ComfyUI).Remove(ctx, r.Journal, unregister); err == nil {
		t.Fatal("a removed build was removed again")
	}
}

// A later plan that no longer carries the OA build keeps its record, so
// Setup can still remove it; the finished removal drops the record.
func TestRetainedArchiveRefusesUnselectedApplication(t *testing.T) {
	archive := RetainedArchiveFor(AppID("unrelated-application"))
	if archive.Component() != "" || archive.Registration() != "" {
		t.Fatal("unrelated application inherited the historical archive binding")
	}
	if _, _, ok := archive.Installed(State{}); ok {
		t.Fatal("unrelated application exposed an installed archive")
	}
	if err := archive.Remove(context.Background(), Journal{}, func(context.Context, Artifact, string) error {
		t.Fatal("unrelated application reached unregister")
		return nil
	}); err == nil {
		t.Fatal("unrelated application admitted archive removal")
	}
}

func TestALaterPlanKeepsTheOABuildForItsRemoval(t *testing.T) {
	archive := RetainedArchiveFor(ComfyUI)
	component, registration := archive.Component(), archive.Registration()
	if component != "comfyui-app" || registration != "comfyui" {
		t.Fatalf("historical archive identity changed: %q, %q", component, registration)
	}
	j, a, root := installedOABuild(t)
	ctx := context.Background()
	state, err := j.SavePlan(ctx, mustPlan(t, j.Catalog(), TheNoise))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := planComponent(state.Plan, retainedArchiveComponent); ok {
		t.Fatal("the later plan carries the OA build")
	}
	kept, _, ok := RetainedArchiveFor(ComfyUI).Installed(state)
	if !ok || kept.SHA256 != a.SHA256 || state.RetainedArchive == nil {
		t.Fatal("the later plan dropped the OA build's record")
	}
	// A further plan keeps the kept record.
	if state, err = j.SavePlan(ctx, mustPlan(t, j.Catalog(), Lemonade)); err != nil || state.RetainedArchive == nil {
		t.Fatalf("a further plan dropped the record: %v", err)
	}
	raw, err := os.ReadFile(j.Path)
	if err != nil {
		t.Fatal(err)
	}
	var oldJournal struct {
		Kept *KeptPackage `json:"oa_build_comfy"`
	}
	if err := json.Unmarshal(raw, &oldJournal); err != nil || oldJournal.Kept == nil {
		t.Fatalf("historical kept record is missing: %v", err)
	}
	refused := errors.New("registration removal not confirmed")
	runner := NewRunner(j, &fakeTransfer{})
	checkUnregister := func(artifact Artifact, actualRoot string) {
		t.Helper()
		if !reflect.DeepEqual(artifact, oldJournal.Kept.Artifact) || actualRoot != root {
			t.Fatalf("unregister bound a different archive: %+v in %q", artifact, actualRoot)
		}
	}
	if err := RetainedArchiveFor(ComfyUI).Remove(ctx, runner.Journal, func(_ context.Context, artifact Artifact, actualRoot string) error {
		checkUnregister(artifact, actualRoot)
		return refused
	}); !errors.Is(err, refused) {
		t.Fatalf("unconfirmed unregister: %v", err)
	}
	pending, err := j.Load(ctx)
	if err != nil || pending.RetainedArchive == nil || !pending.RetainedArchive.Effect.Removing {
		t.Fatalf("retained removal intent: %+v, %v", pending.RetainedArchive, err)
	}
	if _, err := matchingReceiptContext(ctx, a, root); err != nil {
		t.Fatalf("unconfirmed unregister disturbed the installed tree or receipt: %v", err)
	}
	stale := oldJournal.Kept.Effect
	if err := historicalArchive.recordRemoval(ctx, j, a, stale, nativeRemovalEffect(stale, true, 0)); err == nil {
		t.Fatal("the retained removal accepted a stale completion")
	}
	if err := RetainedArchiveFor(ComfyUI).Remove(ctx, runner.Journal, func(_ context.Context, artifact Artifact, actualRoot string) error {
		checkUnregister(artifact, actualRoot)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state, _ = j.Load(ctx)
	if state.RetainedArchive != nil {
		t.Fatal("the finished removal kept the record")
	}
	if _, err := os.Stat(filepath.Join(root, a.ApplicationProgram)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the build's program remains")
	}
	if _, err := os.Stat(filepath.Join(root, "personal.txt")); err != nil {
		t.Fatal("the person's file was removed")
	}
}
