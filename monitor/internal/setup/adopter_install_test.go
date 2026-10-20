package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

// standInAdopter is one stand-in OA build: the app ZIP's bytes and the pin
// row that names it and its source ZIP by digest.
type standInAdopter struct {
	componentID string
	choice      AppID
	pin         adopterAssetPin
	app         []byte
}

// standInAdopterPins builds a ComfyUI app ZIP with its source ZIP in
// t.TempDir(), and the pins document naming them, in the schema
// parseAdopterAssets accepts. The app's provenance names its own source ZIP's
// digest and its RECORD-set pin, as OA's build does.
func standInAdopterPins(t *testing.T, version string) ([]byte, []standInAdopter) {
	t.Helper()
	commit := strings.Repeat("a", 40)
	records := sha256.Sum256([]byte("stand-in private Python RECORD set " + version))
	var adopters []standInAdopter
	var apps []adopterAssetPin
	for _, app := range []struct {
		id        AppID
		component string
		identity  string
		license   string
	}{
		{ComfyUI, "comfyui-app", "comfyui-oa", "GPL-3.0-only"},
	} {
		_, source := archiveFixture(t, archiveEntry{name: string(app.id) + "-source/README.md", data: "corresponding source " + version})
		entries := packageEntries(app.component, version)
		mark, _ := json.Marshal(map[string]string{
			"corresponding_comfyui_source_zip_sha256": source.SHA256,
			"private_python_records_sha256":           hex.EncodeToString(records[:]),
		})
		entries[0].data = string(mark)
		appPath, built := archiveFixture(t, entries...)
		data, err := os.ReadFile(appPath)
		if err != nil {
			t.Fatal(err)
		}
		pin := adopterAssetPin{
			ID: string(app.id), BinaryZIP: string(app.id) + "-oa-" + version + "-windows-x64.zip",
			BinarySHA256: built.SHA256, BinaryBytes: built.Bytes,
			SourceZIP: string(app.id) + "-oa-" + version + "-source.zip", SourceSHA256: source.SHA256, SourceBytes: source.Bytes,
			SourceCommit: commit, SourceURL: "https://github.com/openabstractions/abstractions/commit/" + commit,
			Signer: "unsigned", Version: version, Platform: "windows/amd64", PeakBytes: 4 << 20,
			License: app.license, Publisher: "openabstractions", InstallIdentity: app.identity,
			Action: "archive_per_user", RecordsSHA256: hex.EncodeToString(records[:]),
		}
		apps = append(apps, pin)
		adopters = append(adopters, standInAdopter{componentID: app.component, choice: app.id, pin: pin, app: data})
	}
	encoded, err := json.Marshal(adopterAssetPins{Schema: 1, ReleaseTag: "v0.4.0", Apps: apps})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAdopterAssets(encoded); err != nil {
		t.Fatalf("the stand-in pins document is not in the release schema: %v", err)
	}
	return encoded, adopters
}

// standInCatalog is the windows/amd64 release catalog with the stand-in pins
// projected over whatever the embedded document lists.
func standInCatalog(t *testing.T, pins []byte) Catalog {
	t.Helper()
	c := catalogForTarget("windows", "amd64")
	if err := projectAdopterAssets(&c, "windows", "amd64", pins); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the projected stand-in pins fail catalog validation: %v", err)
	}
	return c
}

// unpinnedAppsCatalog is the windows/amd64 release catalog with the app
// component pending, as before their pins exist, whatever the embedded
// pins document lists. Tests of the pending path use it.
func unpinnedAppsCatalog() Catalog {
	c := catalogForTarget("windows", "amd64")
	unpinned := catalogForTarget("linux", "amd64")
	c.Components = slices.Clone(c.Components)
	for i := range c.Components {
		if c.Components[i].ID == "comfyui-app" {
			c.Components[i] = unpinned.Components[i]
		}
	}
	return c
}

// standInHome points Setup's per-user folders at a temporary folder, so no
// test installs on this account, and returns the data folder Setup now
// resolves (CurrentHome's Data). Each platform reads its own variable:
// LOCALAPPDATA on Windows, XDG_CACHE_HOME on Linux and HOME on macOS, where
// the folder is $HOME/Library/Caches. Every test that installs into the
// person's folders calls this; setting only one platform's variable wrote
// into the account's real cache elsewhere.
func standInHome(t *testing.T) string {
	t.Helper()
	person := t.TempDir()
	t.Setenv("HOME", person)
	t.Setenv("APPDATA", filepath.Join(person, "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(person, "LocalAppData"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(person, "LocalAppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(person, "Roaming"))
	home, err := CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	if !identity.Inside(home.Data, person) || !identity.Inside(home.Config, person) {
		t.Fatalf("Setup's per-user folders %+v stay outside the stand-in %s", home, person)
	}
	if err := os.MkdirAll(home.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	return home.Data
}

// installThroughRunner runs Setup's real transfer staging, archive install,
// package health check and activation for one component, with the public
// download replaced by the stand-in bytes.
func installThroughRunner(t *testing.T, ctx context.Context, journal Journal, componentID string, data []byte, phases ...Phase) *fakeTransfer {
	t.Helper()
	transfer := &fakeTransfer{state: "complete", bytes: data}
	r := NewRunner(journal, transfer)
	r.RetireGuard = func(context.Context, string, string) error { return nil }
	for _, want := range phases {
		if _, err := r.Advance(ctx, componentID); err != nil {
			t.Fatalf("%s to %s: %v", componentID, want, err)
		}
		state, err := journal.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := state.Effects[componentID]; got.Phase != want {
			t.Fatalf("%s reached %s, want %s: %+v", componentID, got.Phase, want, got)
		}
	}
	return transfer
}

var firstInstallPhases = []Phase{Requested, Verified, Staged, Checked, Installed}

// The chooser's catalog offers the app once pins exist: a pins document with
// a ComfyUI row clears the app component's Pending reason, and the goal that
// names it plans with no blocker.
func TestStandInPinsOfferTheAppWithNoPendingBlocker(t *testing.T) {
	pins, adopters := standInAdopterPins(t, "v0.4.0")
	c := standInCatalog(t, pins)
	for _, adopter := range adopters {
		for _, component := range c.Components {
			if component.ID == adopter.componentID && (component.Pending != "" || component.Artifact == nil ||
				component.Artifact.SHA256 != adopter.pin.BinarySHA256 || component.Artifact.SourceArchiveSHA256 != adopter.pin.SourceSHA256) {
				t.Fatalf("%s is not offered from its pin: %+v", adopter.componentID, component)
			}
		}
	}
	for _, selected := range [][]AppID{{ComfyUI}} {
		plan, err := c.Plan(selected)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Preparable || len(plan.Blockers) != 0 {
			t.Fatalf("goals %v are not offered: blockers %v", selected, plan.Blockers)
		}
		for _, component := range plan.Components {
			if !component.Ready || component.Pending != "" {
				t.Fatalf("goals %v carry an unpinned component: %+v", selected, component)
			}
		}
	}
	titles := map[AppID]string{}
	for _, choice := range c.Choices {
		titles[choice.ID] = choice.Title
	}
	if titles[ComfyUI] != "Create pictures" || titles[OpenCode] != "Use an assistant" {
		t.Fatalf("the goals are not the chooser's: %v", titles)
	}
}

// Stand-in packages install end to end through Setup's archive and runner
// path into a temporary per-user folder. The installed folder carries the OA
// mark as the other-copies readers read it, so CopyFinder leaves OA's build
// out; a later install recognizes the marked build, and a new pin replaces
// it in place.
func TestStandInPackagesInstallWithTheOAMarkAndAreReplacedInPlace(t *testing.T) {
	ctx := context.Background()
	local := standInHome(t)
	pins, adopters := standInAdopterPins(t, "v0.4.0")
	c := standInCatalog(t, pins)
	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json"), catalogOverride: &c}
	if _, err := NewRunner(journal, nil).Start(ctx, []AppID{ComfyUI}); err != nil {
		t.Fatal(err)
	}
	roots := map[string]string{}
	for _, adopter := range adopters {
		installThroughRunner(t, ctx, journal, adopter.componentID, adopter.app, firstInstallPhases...)
		root, err := installPath(*adopter.pin.artifact("v0.4.0"))
		if err != nil {
			t.Fatal(err)
		}
		if !identity.Inside(root, local) {
			t.Fatalf("%s installed outside the temporary per-user folder: %s", adopter.componentID, root)
		}
		if ok, err := ArchiveInstalled(*adopter.pin.artifact("v0.4.0"), root); err != nil || !ok {
			t.Fatalf("%s receipt: %v %v", adopter.componentID, ok, err)
		}
		roots[adopter.componentID] = root
	}
	comfyRoot := roots["comfyui-app"]
	if !oaMarkedComfy(filepath.Join(comfyRoot, comfyPortableDirectory)) || !oaMarkedComfy(comfyRoot) {
		t.Fatal("the installed ComfyUI does not carry the OA mark the other-copies reader reads")
	}

	// The finder looks where OA's builds now are: a portable candidate under
	// the profile, and uninstall entries naming each install folder.
	finder := CopyFinder{UserProfile: comfyRoot, Uninstall: func() ([]UninstallEntry, error) {
		return []UninstallEntry{
			{DisplayName: "ComfyUI", InstallLocation: comfyRoot},
		}, nil
	}}
	if found, err := finder.Find(); err != nil || len(found) != 0 {
		t.Fatalf("OA's installed builds reported as other copies: %+v %v", found, err)
	}

	// A later Setup with a fresh journal recognizes the marked build at its
	// pinned identity and downloads nothing.
	again := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json"), catalogOverride: &c}
	if _, err := NewRunner(again, nil).Start(ctx, []AppID{ComfyUI}); err != nil {
		t.Fatal(err)
	}
	for _, adopter := range adopters {
		transfer := installThroughRunner(t, ctx, again, adopter.componentID, adopter.app, Staged, Checked, Installed)
		if transfer.reserve != 0 || transfer.copy != 0 {
			t.Fatalf("reinstalling the marked %s downloaded it again: %+v", adopter.componentID, transfer)
		}
	}

	// A release with new pins replaces OA's marked build in place: the new
	// version is installed beside it and marked, and the earlier one removed.
	nextPins, nextAdopters := standInAdopterPins(t, "v0.4.1")
	next := standInCatalog(t, nextPins)
	for _, adopter := range adopters {
		next.PreviousInstallPins = append(next.PreviousInstallPins, KnownInstallPin{ComponentID: adopter.componentID, Artifact: *adopter.pin.artifact("v0.4.0")})
	}
	journal.catalogOverride = &next
	state, err := journal.SavePlan(ctx, mustPlanOf(t, next, ComfyUI))
	if err != nil {
		t.Fatal(err)
	}
	for _, adopter := range nextAdopters {
		if effect := state.Effects[adopter.componentID]; effect.Retained == nil || effect.Retained.Version != "v0.4.0" {
			t.Fatalf("the new pin does not replace OA's installed %s: %+v", adopter.componentID, effect)
		}
		installThroughRunner(t, ctx, journal, adopter.componentID, adopter.app, Requested, Verified, Staged, Checked, Installed, Installed)
		newRoot, err := installPath(*adopter.pin.artifact("v0.4.0"))
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(newRoot) != filepath.Dir(roots[adopter.componentID]) {
			t.Fatalf("%s moved to another install identity: %s", adopter.componentID, newRoot)
		}
		if _, err := os.Lstat(roots[adopter.componentID]); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("OA's earlier %s build is still on disk: %v", adopter.componentID, err)
		}
		roots[adopter.componentID] = newRoot
	}
	if !oaMarkedComfy(roots["comfyui-app"]) {
		t.Fatal("the replacing builds do not carry the OA mark")
	}

	// The finder looked in the right place: without Setup's receipt the
	// same ComfyUI folder is another copy.
	finder.UserProfile = roots["comfyui-app"]
	finder.Uninstall = func() ([]UninstallEntry, error) {
		return []UninstallEntry{
			{DisplayName: "ComfyUI", InstallLocation: roots["comfyui-app"]},
		}, nil
	}
	if found, err := finder.Find(); err != nil || len(found) != 0 {
		t.Fatalf("the replacing builds reported as other copies: %+v %v", found, err)
	}
	if err := os.Remove(filepath.Join(roots["comfyui-app"], archiveMarker)); err != nil {
		t.Fatal(err)
	}
	found, _ := finder.Find()
	if len(found) != 2 || found[0].App != ComfyUI || found[1].App != ComfyUI ||
		found[0].Location != filepath.Join(roots["comfyui-app"], comfyPortableDirectory) {
		t.Fatalf("an unreceipted ComfyUI folder was not reported: %+v", found)
	}
}

func mustPlanOf(t *testing.T, c Catalog, selected ...AppID) Plan {
	t.Helper()
	plan, err := c.Plan(selected)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// treeDigest names every file and folder under root with its content digest,
// so a test proves a folder is neither modified nor removed.
func treeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() {
			tree[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		tree[rel] = hex.EncodeToString(sum[:]) + " " + info.Mode().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// An unmarked ComfyUI portable is reported with its location; the default
// plan is refused; the person's choice to install beside it lets OA's build
// install; and nothing in the other copy changes. OpenCode's own installs,
// in a folder and behind an uninstall entry, are none of Setup's concern and
// are not reported.
func TestOtherCopiesAreReportedRefusedAndLeftUnchangedByABesideInstall(t *testing.T) {
	ctx := context.Background()
	local := standInHome(t)
	finder, root := fixtureFinder(t, nil)
	finder.LocalAppData = local
	portable := filepath.Join(finder.UserProfile, "Downloads", comfyPortableDirectory)
	portableComfy(t, portable)
	writeFixture(t, filepath.Join(portable, "ComfyUI", "models", "checkpoints", "person.safetensors"))
	writeFixture(t, filepath.Join(portable, "ComfyUI", "user", "default", "workflows", "mine.json"))
	writeFixture(t, filepath.Join(portable, "update", "update_comfyui.bat"))
	openCodeBin := filepath.Join(finder.UserProfile, ".opencode", "bin")
	writeFixture(t, filepath.Join(openCodeBin, "opencode.exe"))
	registered := filepath.Join(root, "Program Files", "OpenCode Desktop")
	writeFixture(t, filepath.Join(registered, "OpenCode.exe"))
	finder.Uninstall = func() ([]UninstallEntry, error) {
		return []UninstallEntry{{DisplayName: "OpenCode Desktop 1.0", InstallLocation: `"` + registered + `"`}}, nil
	}
	before := map[string]map[string]string{}
	for _, dir := range []string{portable, openCodeBin, registered} {
		before[dir] = treeDigest(t, dir)
	}

	found, err := finder.Find()
	if err != nil {
		t.Fatal(err)
	}
	want := []OtherCopy{
		{App: ComfyUI, Title: "ComfyUI", Location: portable, Source: "folder"},
	}
	if !slices.Equal(found, want) {
		t.Fatalf("found %+v\nwant %+v", found, want)
	}
	for _, app := range []AppID{ComfyUI} {
		var other *OtherCopyError
		err := CheckBeside([]AppID{app}, nil, found)
		if !errors.Is(err, ErrOtherCopy) || !errors.As(err, &other) || other.Copy.App != app || !strings.Contains(err.Error(), other.Copy.Location) {
			t.Fatalf("the default %s plan was not refused with the copy's location: %v", app, err)
		}
	}
	if err := CheckBeside([]AppID{ComfyUI}, []AppID{ComfyUI}, found); err != nil {
		t.Fatalf("the person's explicit choice was refused: %v", err)
	}

	// The explicit choice installs OA's builds in their own folders.
	pins, adopters := standInAdopterPins(t, "v0.4.0")
	c := standInCatalog(t, pins)
	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json"), catalogOverride: &c}
	if _, err := NewRunner(journal, nil).Start(ctx, []AppID{ComfyUI}); err != nil {
		t.Fatal(err)
	}
	for _, adopter := range adopters {
		installThroughRunner(t, ctx, journal, adopter.componentID, adopter.app, firstInstallPhases...)
		installed, err := installPath(*adopter.pin.artifact("v0.4.0"))
		if err != nil {
			t.Fatal(err)
		}
		for dir := range before {
			if identity.Inside(installed, dir) || identity.Inside(dir, installed) {
				t.Fatalf("OA's %s shares a folder with another copy: %s, %s", adopter.componentID, installed, dir)
			}
		}
	}
	for dir, digest := range before {
		if after := treeDigest(t, dir); !mapsEqual(after, digest) {
			t.Fatalf("another copy changed during OA's install: %s\nbefore %v\nafter  %v", dir, digest, after)
		}
	}
	if again, err := finder.Find(); err != nil || !slices.Equal(again, want) {
		t.Fatalf("after OA's install the report changed: %+v %v", again, err)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}
