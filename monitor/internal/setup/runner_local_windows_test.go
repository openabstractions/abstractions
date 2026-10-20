package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

// An install folder on a network drive is refused by installPath, through
// identity.LocalPath, in both the Verified step that would install and the
// Installing step that would recover: the runner ends on the refusal without
// probing the folder, and identity opens nothing on that drive. The drive is
// stood in by identity.TreatDriveAsNetwork, so no drive is mapped.
func TestRunnerRefusesAnInstallFolderOnANetworkDrive(t *testing.T) {
	ctx := context.Background()
	local := filepath.Join(t.TempDir(), "LocalAppData")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", local)
	const network = `Q:\Users\someone\AppData\Local`
	zipPath, built := archiveFixture(t, packageEntries(fixtureComponent, "1.0.0")...)
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	c := fixtureCatalog()
	index := len(c.Components) - 1 // fixtureComponent
	pin := *c.Components[index].Artifact
	pin.Version, pin.SHA256, pin.Bytes, pin.PeakBytes = "1.0.0", built.SHA256, built.Bytes, built.PeakBytes
	c.Components[index].Artifact = &pin
	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json"), catalogOverride: &c}

	probes := 0
	saved := archiveInstalled
	t.Cleanup(func() { archiveInstalled = saved })
	archiveInstalled = func(a Artifact, destination string) (bool, error) {
		probes++
		return saved(a, destination)
	}
	var mu sync.Mutex
	var opened []string
	stop, ok := identity.ObserveOpens(func(path string) {
		if strings.HasPrefix(strings.ToUpper(path), `Q:`) {
			mu.Lock()
			opened = append(opened, path)
			mu.Unlock()
		}
	})
	if !ok {
		t.Fatal("another test holds identity's open observer")
	}
	t.Cleanup(stop)
	restore := identity.TreatDriveAsNetwork('Q')
	t.Cleanup(restore)
	phase := func() Phase {
		state, err := journal.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return state.Effects[fixtureComponent].Phase
	}
	refused := func(step string, err error) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if !errors.Is(err, identity.ErrNotLocalPath) || probes != 0 || len(opened) != 0 {
			t.Fatalf("%s step on a network drive: %v; %d probes, opened %q", step, err, probes, opened)
		}
	}

	runner := NewRunner(journal, &fakeTransfer{state: "complete", bytes: data})
	if _, err := runner.Start(ctx, []AppID{fixtureApp}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := runner.Advance(ctx, fixtureComponent); err != nil {
			t.Fatal(err)
		}
	}
	if phase() != Verified {
		t.Fatalf("phase %v before the install step", phase())
	}
	t.Setenv("LOCALAPPDATA", network)
	probes = 0
	_, err = runner.Advance(ctx, fixtureComponent)
	refused("Verified", err)
	if phase() != Verified {
		t.Fatalf("a refused install step recorded %v", phase())
	}

	// The Installing step: a crash right after the claim leaves Installing
	// on record; recovery on the network drive is refused unprobed.
	t.Setenv("LOCALAPPDATA", local)
	runner.crashAfter = crashAt("installing")
	if _, err := runner.Advance(ctx, fixtureComponent); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	if phase() != Installing {
		t.Fatalf("phase %v after the crash", phase())
	}
	runner.crashAfter = nil
	t.Setenv("LOCALAPPDATA", network)
	probes = 0
	_, err = runner.Advance(ctx, fixtureComponent)
	refused("Installing", err)
}
