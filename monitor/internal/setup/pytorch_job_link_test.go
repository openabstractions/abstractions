package setup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-provider-kit/go/modelplace"
)

// The owner's R18 Setup failed the compact Klein's model step with
// "folder is not a model folder of this account: ...added_tokens.json: held
// file has a name outside OA's own folders": the job service had linked its
// verified result into the stage (JOB-A17), and Setup ran models install
// with an empty --job-store, so protecting the set folder refused the job
// store's name for the same file. Setup passes no --job-store; the provider's
// own roots (modelplace.OwnedRoots) admit the job store, and the folder is
// protected with the staged file linked from there.
func TestPyTorchModelsInstallAdmitsTheJobStoresLink(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"LOCALAPPDATA", "XDG_DATA_HOME", "HOME"} {
		t.Setenv(name, base)
	}
	jobStore := modelplace.DefaultJobStore(os.Getenv)
	if jobStore == "" || !filepath.IsAbs(jobStore) {
		t.Fatalf("no job store for this platform: %q", jobStore)
	}
	models := filepath.Join(base, "OA", "m", "pt", "flux2-klein-4b")
	args := pyTorchModelsInstallArgs("flux2-klein-4b", models, filepath.Join(base, "OA", "p", "pt"))
	flag := func(name string) string {
		i := slices.Index(args, name)
		if i < 0 || i+1 >= len(args) {
			t.Fatalf("models install has no %s: %q", name, args)
		}
		return args[i+1]
	}
	if slices.Contains(args, "--job-store") {
		t.Fatalf("Setup names the job store itself: %q", args)
	}
	root := flag("--models-root")

	result := filepath.Join(jobStore, "results", "1794220690054-ed8aa54c0f9685d2e385")
	staged := filepath.Join(models, "tokenizer", "added_tokens.json")
	for _, dir := range []string{filepath.Dir(result), filepath.Dir(staged)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(result, []byte(`{"<think>": 151667}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(result, staged); err != nil {
		t.Skipf("this volume makes no hard links: %v", err)
	}
	files := []modelplace.File{{Name: "tokenizer/added_tokens.json"}}
	found := map[string]string{files[0].Name: staged}

	// The old arguments: no link root beside the models root.
	err := modelplace.AdoptSetFolders(root, nil, files, found, nil)
	if !errors.Is(err, privatedir.ErrNotModelFolder) || !strings.Contains(err.Error(), "added_tokens.json") {
		t.Fatalf("without the job store the linked stage was not refused: %v", err)
	}
	adopted := 0
	if err := modelplace.AdoptSetFolders(root, modelplace.OwnedRoots(os.Getenv), files, found, func(string) { adopted++ }); err != nil {
		t.Fatalf("the job store's link refused the stage: %v", err)
	}
	if adopted != 1 {
		t.Fatalf("adopted %d set folders, want 1", adopted)
	}
	if err := privatedir.CheckModelFolder(models); err != nil {
		t.Fatalf("the set folder is not a model folder: %v", err)
	}
}

// A failed provider verb names its own engine.
func TestProviderFailureNamesItsEngine(t *testing.T) {
	err := providerFailure("PyTorch", "pytorchd models install: refused", errors.New("exit status 1"))
	if got := err.Error(); got != "installed PyTorch setup failed: pytorchd models install: refused" {
		t.Fatalf("PyTorch failure reads %q", got)
	}
	if got := providerFailure("Lemonade", "held", errors.New("exit status 3")).Error(); got != "installed Lemonade setup failed: held" {
		t.Fatalf("Lemonade failure reads %q", got)
	}
	if got := (&providerExit{Engine: "Lemonade", Code: 2, Reason: "refused"}).Error(); got != "installed Lemonade setup failed: refused" {
		t.Fatalf("an unnamed exit reads %q", got)
	}
}
