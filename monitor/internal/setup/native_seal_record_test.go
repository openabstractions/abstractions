package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// A partial install the host sealed before a later step failed leaves its
// seal record. Try again clears the partial tree, and the reinstall seals the
// fresh tree again instead of trusting that record.
func TestRetryOfASealedPartialInstallSealsTheFreshTree(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	source, a, backend, plan := nativeArchiveFixture(t)
	record, err := nativeSealRecord(a)
	if err != nil {
		t.Fatal(err)
	}
	home, err := CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home.Data, "OpenAbstractions", "ComfyUI", "native", a.NativeResources.Backend.Provenance.Manifest.SHA256, "backend.record"); record != want {
		t.Fatalf("seal record %q, want the host's %q", record, want)
	}
	c := fixtureCatalog()
	for i := range c.Components {
		if c.Components[i].ID == fixtureComponent {
			c.Components[i].Artifact = &a
		}
	}
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	ctx := context.Background()
	if _, err := j.SavePlan(ctx, mustPlan(t, j.Catalog(), fixtureApp)); err != nil {
		t.Fatal(err)
	}
	hostBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	transfer := &fakeTransfer{state: "complete", bytes: hostBytes}
	runner := NewRunner(j, transfer)
	runner.nativeArchiveFreeBytes = func(string) (uint64, error) { return 1 << 62, nil }
	oldCommand, oldExtract := nativeArchiveCommand, nativeArchiveExtract
	t.Cleanup(func() { nativeArchiveCommand, nativeArchiveExtract = oldCommand, oldExtract })
	user := t.TempDir()
	if err := privatedir.Tighten(user); err != nil {
		t.Fatal(err)
	}
	// The inert host seals as the real one does: only while the record is
	// absent. Each extraction is a fresh, unsealed tree; the first one fails
	// its check after the seal, as a damaged partial install does.
	sealed, seals, trees := false, 0, 0
	nativeArchiveCommand = func(_ context.Context, _ sevenZipPin, op nativeArchiveOperation) ([]byte, error) {
		switch op {
		case nativeArchivePlan:
			return json.Marshal(plan)
		case nativeArchiveSeal:
			if _, err := os.Lstat(record); err == nil {
				return nil, nil
			}
			if err := os.MkdirAll(filepath.Dir(record), 0700); err != nil {
				return nil, err
			}
			seals++
			sealed = true
			return nil, os.WriteFile(record, []byte("sealed tree"), 0600)
		case nativeArchiveCheck:
			if trees == 1 {
				return nil, errors.New("inert check refusal after the seal")
			}
			if !sealed {
				return nil, errors.New("backend not sealed")
			}
			return json.Marshal(map[string]string{"user_directory": user})
		}
		return nil, nil
	}
	nativeArchiveExtract = func(_ context.Context, in sevenZipArchive) error {
		sealed = false
		trees++
		if err := os.MkdirAll(filepath.Join(in.Destination, "helper"), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(in.Destination, "manifest.json"), []byte("{\"fixture\":true}"), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(in.Destination, "helper/python.exe"), []byte("python"), 0600)
	}
	for i := 0; i < 3; i++ {
		if i == 2 {
			transfer.bytes = backend
		}
		if _, err := runner.Advance(ctx, fixtureComponent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runner.Advance(ctx, fixtureComponent); err == nil {
		t.Fatal("refused check accepted")
	}
	if state, err := j.Load(ctx); err != nil || state.Effects[fixtureComponent].Phase != Uncertain || seals != 1 {
		t.Fatalf("partial install: %+v seals %d %v", state.Effects, seals, err)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("the partial install's seal record: %v", err)
	}
	if _, err := runner.Retry(ctx, fixtureComponent); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Advance(ctx, fixtureComponent); err != nil {
		t.Fatal(err)
	}
	if state, err := j.Load(ctx); err != nil || state.Effects[fixtureComponent].Phase != Staged && state.Effects[fixtureComponent].Phase != Checked {
		t.Fatalf("redone install: %+v %v", state.Effects, err)
	}
	if trees != 2 || seals != 2 || !sealed {
		t.Fatalf("%d trees sealed %d times in all, the last sealed %v", trees, seals, sealed)
	}
}
