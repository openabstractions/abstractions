package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComfyPathsUseStableUserDataAndPictures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "install")
	python := filepath.Join(root, filepath.FromSlash(comfyPythonRelativePath))
	pictures := filepath.Join(t.TempDir(), "Redirected Pictures")
	localData := filepath.Join(t.TempDir(), "Local")
	paths, err := comfyPathsAt(root, python, localData, pictures)
	if err != nil {
		t.Fatal(err)
	}
	if paths.CallerProgram() != python || paths.UserDirectory() != filepath.Join(localData, "OpenAbstractions", "ComfyUI", "user") ||
		paths.InputDirectory() != filepath.Join(localData, "OpenAbstractions", "ComfyUI", "input") ||
		paths.TempDirectory() != filepath.Join(localData, "OpenAbstractions", "ComfyUI", "temp") ||
		paths.OutputDirectory() != filepath.Join(pictures, "OpenAbstractions", "ComfyUI") ||
		paths.recipe != filepath.Join(paths.UserDirectory(), "openabstractions", "image-recipe.json") {
		t.Fatalf("unexpected Comfy path layout: %+v", paths)
	}
	if err := paths.PrepareDataDirectories(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{paths.UserDirectory(), paths.InputDirectory(), paths.TempDirectory(), paths.OutputDirectory(), filepath.Dir(paths.recipe)} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("data directory %q was not prepared: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "user")); !os.IsNotExist(err) {
		t.Fatalf("mutable data was created inside versioned app tree: %v", err)
	}
}

func TestComfyPathsRequireReceiptBoundEmbeddedPython(t *testing.T) {
	journal, destination, python := installComfyFixture(t)
	paths, err := OpenInstalledComfy(context.Background(), journal)
	if err != nil {
		t.Fatal(err)
	}
	if paths.CallerProgram() != python || !strings.HasPrefix(paths.CallerProgram(), destination) {
		t.Fatalf("caller path did not come from the installed archive: %+v", paths)
	}
	if err := os.WriteFile(python, []byte("changed executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenInstalledComfy(context.Background(), journal); err == nil {
		t.Fatal("modified archive executable retained trusted caller identity")
	}
}

func TestComfyPathsCarryTheArtifactsRecordsPin(t *testing.T) {
	journal, _, _ := installComfyFixture(t)
	paths, err := OpenInstalledComfy(context.Background(), journal)
	if err != nil {
		t.Fatal(err)
	}
	state, err := journal.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	comfy, ok := planComponent(state.Plan, "comfyui-app")
	if !ok || comfy.Artifact == nil {
		t.Fatal("fixture has no installed ComfyUI pin")
	}
	pin := comfy.Artifact.RecordsSHA256
	if pin == "" || paths.records != pin {
		t.Fatalf("paths did not take the RECORD-set pin from the artifact: %q, want %q", paths.records, pin)
	}
	descriptor, err := ComfyApplicationDescriptor(paths)
	if err != nil {
		t.Fatal(err)
	}
	if args := descriptor.Activation.Arguments; len(args) < 5 || args[3] != "--records" || args[4] != pin {
		t.Fatalf("activation does not pass the artifact's RECORD-set pin: %#v", args)
	}
}

func TestPriorCatalogRecognizesOnlyReceiptBoundInstalledComfy(t *testing.T) {
	ctx := context.Background()
	journal, destination, python := installComfyFixture(t)
	old, err := journal.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldComfy, ok := planComponent(old.Plan, "comfyui-app")
	if !ok || oldComfy.Artifact == nil {
		t.Fatal("fixture has no installed ComfyUI pin")
	}
	newCatalog := *journal.catalogOverride
	newCatalog.Components = append([]Component(nil), newCatalog.Components...)
	newCatalog.PreviousInstallPins = []KnownInstallPin{{ComponentID: "comfyui-app", Artifact: *oldComfy.Artifact}}
	for i := range newCatalog.Components {
		if newCatalog.Components[i].ID == "comfyui-app" {
			newPin := *newCatalog.Components[i].Artifact
			newPin.Version = "1.2.4"
			newPin.SHA256 = strings.Repeat("c", 64)
			newCatalog.Components[i].Artifact = &newPin
		}
	}
	journal.catalogOverride = &newCatalog
	status, err := NewRunner(journal, nil).Status(ctx)
	if err != nil || status.CurrentCatalog || status.Ready || status.Plan.Preparable {
		t.Fatalf("old installed plan must be review-only: %+v, %v", status, err)
	}
	if len(status.Components) != 2 || status.Components[0].Effect.Phase != Installed ||
		status.Components[1].Effect.Phase != Pending {
		t.Fatalf("old installed identity or pending component lost: %+v", status.Components)
	}
	paths, err := OpenInstalledComfy(ctx, journal)
	if err != nil || paths.CallerProgram() != python || !strings.HasPrefix(paths.CallerProgram(), destination) {
		t.Fatalf("old receipt-bound executable was not preserved: %+v, %v", paths, err)
	}
	if _, err := NewRunner(journal, nil).Advance(ctx, "lemonade-engine"); err == nil {
		t.Fatal("old pending work advanced under the new catalog")
	}
	if _, err := NewRunner(journal, nil).Retry(ctx, "lemonade-engine"); err == nil {
		t.Fatal("old pending work retried under the new catalog")
	}
	if _, err := journal.Update(ctx, old.Plan.ID, "lemonade-engine", old.Effects["lemonade-engine"], Effect{Phase: Installed}); err == nil {
		t.Fatal("old journal acquired a new effect under the new catalog")
	}

	marker := filepath.Join(destination, archiveMarker)
	if err := os.WriteFile(marker, []byte("tampered receipt"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err = NewRunner(journal, nil).Status(ctx)
	if err != nil || status.Components[0].Effect.Phase != Uncertain {
		t.Fatalf("tampered old receipt retained installed status: %+v, %v", status, err)
	}
	if _, err := OpenInstalledComfy(ctx, journal); err == nil {
		t.Fatal("tampered old receipt produced a trusted executable")
	}
}

func TestPriorCatalogRejectsJournalSuppliedUnknownAppPin(t *testing.T) {
	ctx := context.Background()
	journal, _, _ := installComfyFixture(t)
	old, err := journal.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newCatalog := *journal.catalogOverride
	newCatalog.Components = append([]Component(nil), newCatalog.Components...)
	for i := range newCatalog.Components {
		if newCatalog.Components[i].ID == "comfyui-app" {
			pin := *newCatalog.Components[i].Artifact
			pin.Version = "1.2.4"
			newCatalog.Components[i].Artifact = &pin
		}
	}
	journal.catalogOverride = &newCatalog
	if _, err := NewRunner(journal, nil).Status(ctx); err == nil {
		t.Fatal("old app pin without a release-owned historical entry was recognized")
	}
	if _, err := OpenInstalledComfy(ctx, journal); err == nil {
		t.Fatal("old app pin without a release-owned historical entry was launched")
	}
	if old.Effects["comfyui-app"].Phase != Installed {
		t.Fatal("fixture did not exercise an installed app")
	}
	oldPin, ok := planComponent(old.Plan, "comfyui-app")
	if !ok || oldPin.Artifact == nil {
		t.Fatal("fixture did not retain its app pin")
	}
	newCatalog.PreviousInstallPins = []KnownInstallPin{{ComponentID: "comfyui-app", Artifact: *oldPin.Artifact}}
	journal.catalogOverride = &newCatalog
	for i := range old.Plan.Components {
		if old.Plan.Components[i].ID == "comfyui-app" {
			forged := *old.Plan.Components[i].Artifact
			forged.URL = "https://github.com/attacker/x/releases/download/v1/payload.zip"
			old.Plan.Components[i].Artifact = &forged
		}
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(journal, nil).Status(ctx); err == nil {
		t.Fatal("journal-supplied URL gained historical status authority")
	}
	if _, err := OpenInstalledComfy(ctx, journal); err == nil {
		t.Fatal("journal-supplied URL gained launch authority")
	}
}

func TestOpenInstalledComfyRequiresApprovedInstalledPlan(t *testing.T) {
	ctx := context.Background()
	catalog := fixtureCatalog()
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &catalog}
	if _, err := OpenInstalledComfy(ctx, journal); err == nil {
		t.Fatal("missing plan produced a trusted caller")
	}
	plan := mustPlan(t, catalog, ComfyUI)
	state, err := journal.SavePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenInstalledComfy(ctx, journal); err == nil {
		t.Fatal("approved plan without installed app produced a trusted caller")
	}
	state.Effects["comfyui-app"] = Effect{Phase: Installed}
	if err := os.MkdirAll(filepath.Dir(journal.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenInstalledComfy(ctx, journal); err == nil {
		t.Fatal("installed effect without valid archive receipt produced trusted caller")
	}
}

func TestComfyImagePolicyWriteAndReadAreFixedAndStrict(t *testing.T) {
	root := t.TempDir()
	paths, err := comfyPathsAt(filepath.Join(root, "install"), filepath.Join(root, "install", "python.exe"),
		filepath.Join(root, "Local"), filepath.Join(root, "Pictures"))
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.WriteImagePolicy(); err != nil {
		t.Fatal(err)
	}
	if err := paths.WriteImagePolicy(); err != nil {
		t.Fatalf("writing the exact existing policy should be idempotent: %v", err)
	}
	data, err := os.ReadFile(paths.recipe)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"version\":1,\"selection_policy\":\"oa-image-default\"}\n" {
		t.Fatalf("unexpected recipe contents: %q", data)
	}
	if err := paths.VerifyImagePolicy(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"version":1,"selection_policy":"other"}`,
		`{"version":1,"selection_policy":"oa-image-default","model":"untrusted"}`,
		`{"version":1,"selection_policy":"oa-image-default"} {}`,
	} {
		if err := os.WriteFile(paths.recipe, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := paths.VerifyImagePolicy(); err == nil {
			t.Fatalf("accepted invalid image recipe %q", invalid)
		}
	}
}

func TestComfyImagePolicyRefusesToReplaceExistingRecipe(t *testing.T) {
	root := t.TempDir()
	paths, err := comfyPathsAt(filepath.Join(root, "install"), filepath.Join(root, "install", "python.exe"),
		filepath.Join(root, "Local"), filepath.Join(root, "Pictures"))
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.PrepareDataDirectories(); err != nil {
		t.Fatal(err)
	}
	existing := []byte("{\"version\":1,\"selection_policy\":\"my-existing-policy\"}\n")
	if err := os.WriteFile(paths.recipe, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := paths.WriteImagePolicy(); err == nil {
		t.Fatal("existing alternate image recipe was replaced")
	}
	after, err := os.ReadFile(paths.recipe)
	if err != nil || string(after) != string(existing) {
		t.Fatalf("existing image recipe changed after refusal: %q, %v", after, err)
	}
}

func installComfyFixture(t *testing.T) (Journal, string, string) {
	t.Helper()
	standInHome(t)
	catalog := fixtureCatalog()
	index := -1
	for i := range catalog.Components {
		if catalog.Components[i].ID == "comfyui-app" {
			index = i
			catalog.Components[i].Artifact.InstallIdentity = "test-comfyui"
		}
	}
	if index < 0 {
		t.Fatal("fixture catalog lacks ComfyUI package")
	}
	pythonBytes := []byte("fixture embedded Python executable")
	archive, artifact := archiveFixture(t, archiveEntry{
		name: comfyPythonRelativePath,
		data: string(pythonBytes),
	})
	pin := *catalog.Components[index].Artifact
	pin.SHA256 = artifact.SHA256
	pin.Bytes = artifact.Bytes
	pin.PeakBytes = artifact.PeakBytes
	catalog.Components[index].Artifact = &pin
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &catalog}
	plan := mustPlan(t, catalog, ComfyUI)
	state, err := journal.SavePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := installPath(pin)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(context.Background(), pin, archive, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Update(context.Background(), plan.ID, "comfyui-app", state.Effects["comfyui-app"], Effect{Phase: Installed}); err != nil {
		t.Fatal(err)
	}
	return journal, destination, filepath.Join(destination, filepath.FromSlash(comfyPythonRelativePath))
}
