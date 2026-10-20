package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// AddedComponents is the one list the Setup page's rows and its total read:
// a package installed at its pin is absent, another installed version is an
// update of the whole package, and AddedSizes is its sum (the R17 page's
// details counted installed packages and disagreed with its footer).
func TestAddedComponentsAreOneListForRowsAndTotal(t *testing.T) {
	c := withTheNoisePin(catalogForTarget("windows", "amd64"))
	plan := mustPlan(t, c, TheNoise)
	pin := *plan.Components[0].Artifact
	if added, err := c.AddedComponents(plan, map[string]Artifact{"thenoise-engine": pin}); err != nil || len(added) != 0 {
		t.Fatalf("an installed pin is listed: %+v %v", added, err)
	}
	older := pin
	older.Version = "0.0.1"
	added, err := c.AddedComponents(plan, map[string]Artifact{"thenoise-engine": older})
	if err != nil || len(added) != 1 || !added[0].Update || added[0].ComponentID != "thenoise-engine" || added[0].DownloadBytes != plan.DownloadBytes {
		t.Fatalf("an older version is not one update of the whole package: %+v %v", added, err)
	}
	download, peak, err := c.AddedSizes(plan, map[string]Artifact{"thenoise-engine": older})
	if err != nil || download != added[0].DownloadBytes || peak != added[0].DiskBytes {
		t.Fatalf("AddedSizes %d/%d is not the list's sum %+v: %v", download, peak, added, err)
	}
}

// Install keeps a package installed at the same pin: saving the next plan
// carries its Installed effect, so its package is not fetched again.
func TestSavePlanKeepsAPackageInstalledAtItsPin(t *testing.T) {
	c := withTheNoisePin(catalogForTarget("windows", "amd64"))
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	ctx := context.Background()
	state, err := j.SavePlan(ctx, mustPlan(t, c, TheNoise))
	if err != nil {
		t.Fatal(err)
	}
	state.Effects["thenoise-engine"] = Effect{Phase: Installed, Revision: 1}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(j.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The next Install saves the same package under a plan with the same
	// choice and keeps the effect.
	state, err = j.SavePlan(ctx, mustPlan(t, c, TheNoise))
	if err != nil {
		t.Fatal(err)
	}
	if effect := state.Effects["thenoise-engine"]; effect.Phase != Installed {
		t.Fatalf("an installed package at its pin was reset to %q", effect.Phase)
	}
}

// Available offers a catalogue row only when every package is pinned in
// this release: Lemonade, pinned for Windows x64, is offered there and
// nowhere its package is pending.
func TestAvailableNeedsEveryPackagePinned(t *testing.T) {
	if withoutLemonadePin(catalogForTarget("windows", "amd64")).Available(Lemonade) {
		t.Fatal("Lemonade is offered while its package is pending")
	}
	c := catalogForTarget("windows", "amd64")
	if !c.Available(Lemonade) {
		t.Fatal("Lemonade, pinned for Windows x64, is not offered")
	}
	for _, target := range [][2]string{{"windows", "amd64"}, {"windows", "arm64"}, {"darwin", "arm64"}, {"linux", "amd64"}} {
		if catalogForTarget(target[0], target[1]).Available(TheNoise) {
			t.Fatalf("TheNoise is offered on %s/%s though the release ships no TheNoise package", target[0], target[1])
		}
	}
	if !withTheNoisePin(c).Available(TheNoise) {
		t.Fatal("TheNoise, pinned for Windows x64, is not offered")
	}
	if c.Available("unknown") {
		t.Fatal("an unknown row is offered")
	}
}

// InstalledSize reads a portable package's size from its receipt; a native
// application's receipt omits its backend and reports no size.
func TestInstalledSizeReadsTheReceipt(t *testing.T) {
	c := withTheNoisePin(catalogForTarget("windows", "amd64"))
	// A portable package under its own short identity: TheNoise's folder
	// needs a shorter path than a test's temporary folder.
	pin := *mustPlan(t, c, TheNoise).Components[0].Artifact
	pin.InstallIdentity = "fixture-app"
	standInHome(t)
	UseRuntimeState(t.TempDir())
	t.Cleanup(UseUserState)
	folder, err := installPath(pin)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := InstalledSize(pin); err == nil {
		t.Fatal("a missing receipt reported a size")
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(archiveReceipt{Format: 2, ArtifactSHA256: pin.SHA256, Version: pin.Version, InstallIdentity: pin.InstallIdentity, Files: []archiveFileRecord{{Path: "a", Size: 1000}, {Path: "b", Size: 234}}})
	if err := os.WriteFile(filepath.Join(folder, archiveMarker), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	got, bytes, err := InstalledSize(pin)
	if err != nil || bytes != 1234 || got != folder {
		t.Fatalf("InstalledSize = %q %d %v", got, bytes, err)
	}
	native := pin
	native.NativeResources = &nativeArchiveResources{}
	if _, _, err := InstalledSize(native); err == nil {
		t.Fatal("a native application reported its front files as its size")
	}
}
