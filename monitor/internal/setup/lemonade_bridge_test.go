package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// withLemonadeJournal points the provider bridge at journal for one test.
func withLemonadeJournal(t *testing.T, journal Journal) {
	t.Helper()
	saved := lemonadeJournal
	lemonadeJournal = func() (Journal, error) { return journal, nil }
	t.Cleanup(func() { lemonadeJournal = saved })
}

// lemonadeInfoFor is a setup-info reply version 2 whose selection binds a
// at its install folder, with a's pin and pairing as the provider's entry.
func lemonadeInfoFor(t *testing.T, a Artifact, root string, selected bool) lemonadeInfo {
	t.Helper()
	destination, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	info := lemonadeInfo{Version: 2, ProviderRoot: root, ProviderID: lemonadeProviderID, SelectionPresent: selected,
		DownloadDir: filepath.Join(root, "engine-downloads"),
		KnownPackages: []lemonadeKnownPackage{{Version: a.Version, ArchiveSHA256: a.SHA256, ProgramSHA256: a.SHA256,
			BackendArchiveSHA256: a.TestedBackendSHA256, ModelRevision: a.TestedModelRevision}}}
	if selected {
		info.ActivePackage = &lemonadeActivePackage{Version: a.Version, Program: filepath.Join(destination, "lemonade-embeddable-2026.39.1-windows-x64", "lemond.exe")}
	}
	return info
}

func TestLemonadeSetupInfoBindsTheInstalledVersion(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("reviewed provider package is Windows amd64 only")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	var artifact Artifact
	for _, component := range NewCatalog().Components {
		if component.ID == "lemonade-engine" {
			artifact = *component.Artifact
		}
	}
	root := filepath.Join(cache, "OpenAbstractions", "providers", "lemonade")
	parse := func(info lemonadeInfo, active *Artifact) error {
		raw, _ := json.Marshal(info)
		_, err := parseLemonadeInfo(raw, active)
		return err
	}
	info := lemonadeInfoFor(t, artifact, root, true)
	if err := parse(info, &artifact); err != nil {
		t.Fatal(err)
	}
	if err := parse(lemonadeInfoFor(t, artifact, root, false), nil); err != nil {
		t.Fatalf("a provider with no selection before Setup installed Lemonade: %v", err)
	}
	if err := parse(info, nil); err == nil {
		t.Fatal("a provider selection was accepted while Setup has no installed version")
	}
	changed := info
	changed.ActivePackage = &lemonadeActivePackage{Version: artifact.Version, Program: filepath.Join(cache, "another-app", "lemond.exe")}
	if err := parse(changed, &artifact); err == nil {
		t.Fatal("provider reported a package outside the installed version's folder")
	}
	changed = info
	changed.ProviderRoot = filepath.Join(filepath.Dir(cache), "another-user")
	if err := parse(changed, &artifact); err == nil {
		t.Fatal("provider reported private state outside this user's cache")
	}
	changed = info
	changed.KnownPackages = []lemonadeKnownPackage{info.KnownPackages[0]}
	changed.KnownPackages[0].ModelRevision = "1111111111111111111111111111111111111111"
	if err := parse(changed, &artifact); err == nil {
		t.Fatal("a provider entry with another pairing was accepted")
	}
	changed.KnownPackages[0] = info.KnownPackages[0]
	changed.KnownPackages[0].ArchiveSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := parse(changed, &artifact); err == nil {
		t.Fatal("a provider entry for another archive was accepted")
	}
	changed = info
	changed.Version = 1
	if err := parse(changed, &artifact); err == nil {
		t.Fatal("a reply version 1 was accepted")
	}
	raw := []byte(`{"version":2,"provider_root":` + string(mustJSON(t, root)) + `,"provider_id":"lemonade-image","selection_present":false,"known_packages":[{"version":"v1","archive_sha256":"a","program_sha256":"b","backend_archive_sha256":"c","model_revision":"d"}],"package_program":"C:\\x"}`)
	if _, err := parseLemonadeInfo(raw, nil); err == nil {
		t.Fatal("a reply carrying package_program was accepted")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// An existing Lemonade setup whose engine files lack the read-only folder ACL
// (inference CONTRACT.md INF-F3) verifies incomplete. Setup then runs the
// engine-files step, which leaves the 16 GB private model as it is, instead
// of refusing or copying the model again. An explicit replacement replaces
// the backends and the model and names the installed version.
func TestExistingLemonadeSetupInstallsOnlyTheEngineFiles(t *testing.T) {
	f := newUpdateFixture(t, "lemonade-engine", packageEntries("lemonade-engine", "2.0.0"))
	withLemonadeJournal(t, f.journal)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(cache, "OpenAbstractions", "providers", "lemonade")
	info := lemonadeInfoFor(t, f.v1, root, true)
	inputs := t.TempDir()
	var paths []string
	for _, name := range []string{"main", "encoder", "vae", "backend.zip"} {
		path := filepath.Join(inputs, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	var configured [][]string
	installed := false
	cli := lemonadeCLI
	t.Cleanup(func() { lemonadeCLI = cli })
	lemonadeCLI = func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "setup-info":
			return json.Marshal(info)
		case "verify-setup":
			return json.Marshal(lemonadeProof{Version: 1, Verified: installed})
		case "configure":
			configured = append(configured, args)
			installed = true
			return nil, nil
		}
		return nil, errors.New("unexpected provider command")
	}
	// The retained version stays the installed one while the update is
	// pending; the real receipt check reads its folder.
	if err := ConfigureLemonade(context.Background(), root, paths[0], paths[1], paths[2], paths[3], nil, false); err != nil {
		t.Fatalf("an existing setup without engine files was refused: %v", err)
	}
	want := []string{"configure", "--activate-reviewed-package", "--engine-files-only", "--vulkan-backend-archive", paths[3]}
	if len(configured) != 1 || !slices.Equal(configured[0], want) {
		t.Fatalf("configure ran as %q, want only the engine-files step %q", configured, want)
	}
	installed, configured = false, nil
	if err := ConfigureLemonade(context.Background(), root, paths[0], paths[1], paths[2], paths[3], nil, true); err != nil {
		t.Fatal(err)
	}
	if len(configured) != 1 || !slices.Contains(configured[0], "--replace-backend") || !slices.Contains(configured[0], "--replace-private-model") ||
		!slices.Contains(configured[0], "--bf16-main") || slices.Contains(configured[0], "--engine-files-only") {
		t.Fatalf("an explicit replacement ran as %q", configured)
	}
	if i := slices.Index(configured[0], "--package"); i < 0 || configured[0][i+1] != f.v1.Version {
		t.Fatalf("configure does not name the installed version: %q", configured[0])
	}
}

func TestLemonadeSetupReplyRejectsUnexpectedFieldsAndTrailingData(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"version":1,"verified":true,"extra":1}`),
		[]byte(`{"version":1,"verified":true}{"version":1,"verified":true}`),
	} {
		var proof lemonadeProof
		if err := decodeProviderReply(raw, &proof); err == nil {
			t.Fatalf("accepted extra provider output: %s", raw)
		}
	}
}

// An offered model is added to the configured provider by its verb, roles in
// a fixed order, and the bridge reads the staged models back from setup-info.
func TestLemonadeModelAddNamesEachDownloadAndReadsTheStagedModels(t *testing.T) {
	f := newUpdateFixture(t, "lemonade-engine", packageEntries("lemonade-engine", "2.0.0"))
	withLemonadeJournal(t, f.journal)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(cache, "OpenAbstractions", "providers", "lemonade")
	info := lemonadeInfoFor(t, f.v1, root, true)
	info.StagedModels = []string{BF16ModelID}
	inputs := t.TempDir()
	files := map[string]string{"vae": filepath.Join(inputs, "vae"), "main": filepath.Join(inputs, "main")}
	for _, path := range files {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var added [][]string
	cli := lemonadeCLI
	t.Cleanup(func() { lemonadeCLI = cli })
	lemonadeCLI = func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "setup-info":
			return json.Marshal(info)
		case "verify-setup":
			return json.Marshal(lemonadeProof{Version: 1, Verified: true})
		case "model":
			added = append(added, args)
			info.StagedModels = append(info.StagedModels, args[3])
			return []byte(`{"version":1,"ok":true,"model":"` + args[3] + `"}`), nil
		}
		return nil, errors.New("unexpected provider command")
	}
	if staged, err := LemonadeStagedModels(context.Background()); err != nil || !slices.Equal(staged, []string{BF16ModelID}) {
		t.Fatalf("staged %v %v", staged, err)
	}
	if err := StageLemonadeModel(context.Background(), root, "user.Z-Image-Turbo", files, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"model", "add", "--id", "user.Z-Image-Turbo", "--file", "main=" + files["main"], "--file", "vae=" + files["vae"]}
	if len(added) != 1 || !slices.Equal(added[0], want) {
		t.Fatalf("model add ran as %q, want %q", added, want)
	}
	for name, bad := range map[string]map[string]string{"relative": {"main": "main"}, "unknown role": {"lora": files["main"]}} {
		if err := StageLemonadeModel(context.Background(), root, "user.Z-Image-Turbo", bad, nil); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := StageLemonadeModel(context.Background(), filepath.Join(root, "other"), "user.Z-Image-Turbo", files, nil); err == nil {
		t.Fatal("another provider root accepted")
	}
	// The provider is the authority: a model this Setup does not know is left
	// out, logged once, and the known models stay usable.
	var logged []string
	savedLog := logUnknownModel
	t.Cleanup(func() { logUnknownModel = savedLog; unknownStagedModels.Delete("user.Unknown") })
	logUnknownModel = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	info.StagedModels = []string{BF16ModelID, "user.Unknown", "user.Z-Image-Turbo"}
	for range 2 {
		staged, err := LemonadeStagedModels(context.Background())
		if err != nil || !slices.Equal(staged, []string{BF16ModelID, "user.Z-Image-Turbo"}) {
			t.Fatalf("staged %v %v", staged, err)
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], `"user.Unknown"`) {
		t.Fatalf("an unknown staged model was logged %q", logged)
	}
}
