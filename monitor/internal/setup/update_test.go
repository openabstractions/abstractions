package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

var errCrash = errors.New("simulated crash")

// crashAt stops the runner at one named step, as a process exit would.
func crashAt(step string) func(string) error {
	return func(reached string) error {
		if reached == step {
			return errCrash
		}
		return nil
	}
}

// packageEntries builds a fixture package of one kind whose provenance
// matches fixturePackagePins.
func packageEntries(componentID, version string) []archiveEntry {
	switch componentID {
	case "comfyui-app":
		provenance, _ := json.Marshal(map[string]string{
			"corresponding_comfyui_source_zip_sha256": strings.Repeat("d", 64),
			"private_python_records_sha256":           strings.Repeat("e", 64),
		})
		return []archiveEntry{
			{name: "OA-PROVENANCE.json", data: string(provenance)},
			{name: comfyPythonRelativePath, data: "python " + version},
			{name: comfyPortableDirectory + "/" + comfyLauncherName, data: "launcher " + version},
			{name: comfyPortableDirectory + "/ComfyUI/main.py", data: "main " + version},
		}
	case fixtureComponent:
		return []archiveEntry{
			{name: fixtureProgram, data: "fixture " + version},
			{name: fixtureSettings, data: `{"version":"` + version + `"}`},
		}
	}
	return []archiveEntry{{name: "lemonade/lemond.exe", data: "lemond " + version}}
}

type updateFixture struct {
	t         *testing.T
	ctx       context.Context
	component string
	journal   Journal
	v1, v2    Artifact
	root1     string
	root2     string
	transfer  *fakeTransfer
}

func choiceFor(componentID string) AppID {
	switch componentID {
	case "comfyui-app":
		return ComfyUI
	case fixtureComponent:
		return fixtureApp
	}
	return Lemonade
}

// newUpdateFixture installs version 1.0.0 of a package under one release and
// approves a plan of a second release that pins 2.0.0, built from v2.
func newUpdateFixture(t *testing.T, componentID string, v2 []archiveEntry) *updateFixture {
	t.Helper()
	ctx := context.Background()
	standInHome(t)
	catalog := fixtureCatalog()
	index := -1
	for i := range catalog.Components {
		if catalog.Components[i].ID == componentID {
			index = i
		}
	}
	zip1, built1 := archiveFixture(t, packageEntries(componentID, "1.0.0")...)
	first := *catalog.Components[index].Artifact
	first.Version, first.SHA256, first.Bytes, first.PeakBytes = "1.0.0", built1.SHA256, built1.Bytes, built1.PeakBytes
	catalog.Components[index].Artifact = &first
	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json"), catalogOverride: &catalog}
	plan := mustPlan(t, catalog, choiceFor(componentID))
	state, err := journal.SavePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	root1, err := installPath(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(ctx, first, zip1, root1); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Update(ctx, plan.ID, componentID, state.Effects[componentID], Effect{Phase: Installed}); err != nil {
		t.Fatal(err)
	}

	zip2, built2 := archiveFixture(t, v2...)
	second := first
	second.Version, second.SHA256, second.Bytes, second.PeakBytes = "2.0.0", built2.SHA256, built2.Bytes, built2.PeakBytes
	release := catalog
	release.Components = append([]Component(nil), catalog.Components...)
	release.Components[index].Artifact = &second
	release.PreviousInstallPins = []KnownInstallPin{{ComponentID: componentID, Artifact: first}}
	journal.catalogOverride = &release
	state, err = journal.SavePlan(ctx, mustPlan(t, release, choiceFor(componentID)))
	if err != nil {
		t.Fatal(err)
	}
	if effect := state.Effects[componentID]; effect.Phase != Pending || effect.Retained == nil || *effect.Retained != first {
		t.Fatalf("approving the new release did not retain the installed version: %+v", effect)
	}
	root2, err := installPath(second)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(zip2)
	if err != nil {
		t.Fatal(err)
	}
	return &updateFixture{t: t, ctx: ctx, component: componentID, journal: journal, v1: first, v2: second,
		root1: root1, root2: root2, transfer: &fakeTransfer{state: "complete", bytes: data}}
}

// runner is a fresh process: it reads only the journal and the disk.
func (f *updateFixture) runner() Runner {
	r := NewRunner(f.journal, f.transfer)
	r.RetireGuard = func(context.Context, string, string) error { return nil }
	return r
}

func (f *updateFixture) effect() Effect {
	f.t.Helper()
	state, err := f.journal.Load(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return state.Effects[f.component]
}

func (f *updateFixture) advance(r Runner, want Phase) Effect {
	f.t.Helper()
	if _, err := r.Advance(f.ctx, f.component); err != nil {
		f.t.Fatalf("advance to %s: %v", want, err)
	}
	effect := f.effect()
	if effect.Phase != want {
		f.t.Fatalf("phase %s, want %s: %+v", effect.Phase, want, effect)
	}
	return effect
}

func (f *updateFixture) installed(a Artifact, root string) bool {
	f.t.Helper()
	ok, err := ArchiveInstalled(a, root)
	if err != nil {
		f.t.Fatal(err)
	}
	return ok
}

func (f *updateFixture) active() Artifact {
	f.t.Helper()
	state, err := f.journal.Load(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	a, err := f.journal.knownInstalledArtifact(state, f.component)
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

func TestUpdateStagesBesideTheInstalledVersionAndActivatesAfterItsHealthCheck(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	if filepath.Dir(f.root1) != filepath.Dir(f.root2) || f.root1 == f.root2 {
		t.Fatalf("new version is not a sibling of the installed one: %s, %s", f.root1, f.root2)
	}
	r := f.runner()
	f.advance(r, Requested)
	f.advance(r, Verified)
	f.advance(r, Staged)
	if !f.installed(f.v1, f.root1) || !f.installed(f.v2, f.root2) || f.active() != f.v1 {
		t.Fatal("staging changed the installed version or made the new one active")
	}
	f.advance(r, Checked)
	if f.active() != f.v1 {
		t.Fatal("a checked but not activated version became active")
	}
	effect := f.advance(r, Installed)
	if effect.Retained != nil || len(effect.Retired) != 1 || effect.Retired[0] != f.v1 || f.active() != f.v2 {
		t.Fatalf("activation did not switch versions and retire the earlier one: %+v", effect)
	}
	if !f.installed(f.v1, f.root1) {
		t.Fatal("the earlier version was removed before cleanup")
	}
	effect = f.advance(r, Installed)
	if len(effect.Retired) != 0 || !f.installed(f.v2, f.root2) {
		t.Fatalf("cleanup did not finish: %+v", effect)
	}
	if _, err := os.Lstat(f.root1); !os.IsNotExist(err) {
		t.Fatalf("earlier version still on disk: %v", err)
	}
	if f.transfer.reserve != 1 || f.transfer.copy != 1 {
		t.Fatalf("update transferred more than once: %+v", f.transfer)
	}
}

func TestUpdateRecoversAfterACrashAtEachStep(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	f.advance(f.runner(), Requested)
	f.advance(f.runner(), Verified)

	// Crash after the new tree was published, before the journal said so.
	crashing := f.runner()
	crashing.crashAfter = func(step string) error {
		if step == "install" {
			return errCrash
		}
		return nil
	}
	if _, err := crashing.Advance(f.ctx, f.component); !errors.Is(err, errCrash) {
		t.Fatalf("install crash not simulated: %v", err)
	}
	if f.effect().Phase != Installing || !f.installed(f.v2, f.root2) {
		t.Fatalf("crash point not reached: %+v", f.effect())
	}
	f.advance(f.runner(), Staged)

	// Crash after the health check passed, before Checked was recorded.
	crashing.crashAfter = func(step string) error {
		if step == "health" {
			return errCrash
		}
		return nil
	}
	if _, err := crashing.Advance(f.ctx, f.component); !errors.Is(err, errCrash) {
		t.Fatalf("health crash not simulated: %v", err)
	}
	if f.effect().Phase != Staged || f.active() != f.v1 {
		t.Fatalf("an unrecorded health check changed the update: %+v", f.effect())
	}
	f.advance(f.runner(), Checked)

	// Crash after Checked was recorded: the next start activates.
	effect := f.advance(f.runner(), Installed)
	if len(effect.Retired) != 1 || !f.installed(f.v1, f.root1) {
		t.Fatalf("activation lost the earlier version: %+v", effect)
	}

	// Crash during cleanup: the tree was moved aside and partly removed.
	aside := filepath.Join(filepath.Dir(f.root1), "."+filepath.Base(f.root1)+".removing")
	if err := os.Rename(f.root1, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(aside, filepath.FromSlash(fixtureProgram))); err != nil {
		t.Fatal(err)
	}
	effect = f.advance(f.runner(), Installed)
	if len(effect.Retired) != 0 {
		t.Fatalf("interrupted cleanup did not resume: %+v", effect)
	}
	for _, path := range []string{f.root1, aside} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup left %s: %v", path, err)
		}
	}
	if !f.installed(f.v2, f.root2) || f.active() != f.v2 {
		t.Fatal("recovery disturbed the activated version")
	}
}

func TestUnhealthyUpdateIsRemovedAndTheEarlierVersionStaysActive(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, []archiveEntry{{name: fixtureSettings, data: `{"version":"2.0.0"}`}})
	r := f.runner()
	f.advance(r, Requested)
	f.advance(r, Verified)
	f.advance(r, Staged)
	if _, err := r.Advance(f.ctx, f.component); err == nil || !strings.Contains(err.Error(), "lacks its program") {
		t.Fatalf("a package without its program passed its health check: %v", err)
	}
	effect := f.effect()
	if effect.Phase != Failed || effect.Retained == nil || *effect.Retained != f.v1 || len(effect.Retired) != 0 {
		t.Fatalf("failed update did not keep the earlier version: %+v", effect)
	}
	if _, err := os.Lstat(f.root2); !os.IsNotExist(err) {
		t.Fatalf("unhealthy version remains: %v", err)
	}
	if !f.installed(f.v1, f.root1) || f.active() != f.v1 {
		t.Fatal("earlier version is no longer the active one")
	}
	status, err := r.Retry(f.ctx, f.component)
	if err != nil || status.Components[0].Effect.Phase != Verified {
		t.Fatalf("explicit retry of the verified download refused: %+v, %v", status, err)
	}
}

func TestComfyUpdateRefusesUpstreamUpdatersAndPairsSourceAndRecords(t *testing.T) {
	for name, mutate := range map[string]func([]archiveEntry) []archiveEntry{
		"update folder": func(entries []archiveEntry) []archiveEntry {
			return append(entries, archiveEntry{name: comfyPortableDirectory + "/update/update_comfyui.bat", data: "git pull"})
		},
		"manager node": func(entries []archiveEntry) []archiveEntry {
			return append(entries, archiveEntry{name: comfyPortableDirectory + "/ComfyUI/custom_nodes/ComfyUI-Manager/__init__.py", data: "x"})
		},
		"other source": func(entries []archiveEntry) []archiveEntry {
			entries[0].data = `{"corresponding_comfyui_source_zip_sha256":"` + strings.Repeat("0", 64) +
				`","private_python_records_sha256":"` + strings.Repeat("e", 64) + `"}`
			return entries
		},
		"other records": func(entries []archiveEntry) []archiveEntry {
			entries[0].data = `{"corresponding_comfyui_source_zip_sha256":"` + strings.Repeat("d", 64) +
				`","private_python_records_sha256":"` + strings.Repeat("0", 64) + `"}`
			return entries
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpdateFixture(t, "comfyui-app", mutate(packageEntries("comfyui-app", "2.0.0")))
			r := NewRunner(f.journal, f.transfer) // no retire guard: a never-active version needs none
			f.advance(r, Requested)
			f.advance(r, Verified)
			f.advance(r, Staged)
			if _, err := r.Advance(f.ctx, f.component); err == nil || !strings.Contains(err.Error(), "failed its health check") {
				t.Fatalf("unhealthy ComfyUI package was not refused: %v", err)
			}
			if effect := f.effect(); effect.Phase != Failed || len(effect.Retired) != 0 {
				t.Fatalf("unhealthy ComfyUI version not removed: %+v", effect)
			}
			paths, err := OpenInstalledComfy(f.ctx, f.journal)
			if err != nil || !strings.HasPrefix(paths.CallerProgram(), f.root1) {
				t.Fatalf("earlier ComfyUI is not the one that launches: %+v, %v", paths, err)
			}
		})
	}
}

func TestComfyUpdateKeepsTheEarlierVersionUntilItsRegistrationMoves(t *testing.T) {
	f := newUpdateFixture(t, "comfyui-app", packageEntries("comfyui-app", "2.0.0"))
	r := NewRunner(f.journal, f.transfer)
	f.advance(r, Requested)
	f.advance(r, Verified)
	f.advance(r, Staged)
	paths, err := OpenInstalledComfy(f.ctx, f.journal)
	if err != nil || !strings.HasPrefix(paths.CallerProgram(), f.root1) {
		t.Fatalf("the earlier version must stay the launched one during an update: %+v, %v", paths, err)
	}
	f.advance(r, Checked)
	f.advance(r, Installed)
	paths, err = OpenInstalledComfy(f.ctx, f.journal)
	if err != nil || !strings.HasPrefix(paths.CallerProgram(), f.root2) || paths.records != f.v2.RecordsSHA256 {
		t.Fatalf("activation did not switch the launched version: %+v, %v", paths, err)
	}
	earlier, err := EarlierComfyDescriptors(f.ctx, f.journal)
	if err != nil || len(earlier) != 1 || !ProgramWithin(f.root1, earlier[0].Program) {
		t.Fatalf("the earlier registration is not recognizable for replacement: %+v, %v", earlier, err)
	}
	if _, err := r.Advance(f.ctx, f.component); err == nil || !f.installed(f.v1, f.root1) {
		t.Fatal("earlier ComfyUI was removed without a registration check")
	}
	r.RetireGuard = func(_ context.Context, _ string, root string) error {
		if ProgramWithin(root, earlier[0].Program) {
			return errors.New("the registered comfyui entry still names this version")
		}
		return nil
	}
	if _, err := r.Advance(f.ctx, f.component); err == nil || !f.installed(f.v1, f.root1) {
		t.Fatal("earlier ComfyUI was removed while its registration still named it")
	}
	r.RetireGuard = func(context.Context, string, string) error { return nil }
	if effect := f.advance(r, Installed); len(effect.Retired) != 0 {
		t.Fatalf("earlier ComfyUI not removed after its registration moved: %+v", effect)
	}
}

func TestRemovingAnEarlierVersionKeepsFilesOutsideItsReceipt(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged, Checked, Installed} {
		f.advance(r, phase)
	}
	notes := filepath.Join(f.root1, "fixture", "notes.txt")
	edited := filepath.Join(f.root1, filepath.FromSlash(fixtureSettings))
	for _, path := range []string{notes, edited} {
		if err := os.WriteFile(path, []byte("the person's own words"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	effect := f.advance(r, Installed)
	if len(effect.Retired) != 0 || !strings.Contains(effect.Reason, "kept 2 files") {
		t.Fatalf("cleanup did not report the kept files: %+v", effect)
	}
	for _, path := range []string{notes, edited} {
		if data, err := os.ReadFile(path); err != nil || string(data) != "the person's own words" {
			t.Fatalf("a file outside the receipt was touched: %s, %q, %v", path, data, err)
		}
	}
	for _, gone := range []string{filepath.Join(f.root1, filepath.FromSlash(fixtureProgram)), filepath.Join(f.root1, archiveMarker)} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Fatalf("package file %s was not removed: %v", gone, err)
		}
	}
}

func TestRemovingAnEarlierVersionWaitsWhileItIsInUse(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows refuses to move a folder with an open file")
	}
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	r := f.runner()
	for _, phase := range []Phase{Requested, Verified, Staged, Checked, Installed} {
		f.advance(r, phase)
	}
	open, err := os.Open(filepath.Join(f.root1, filepath.FromSlash(fixtureProgram)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(f.ctx, f.component); err == nil {
		open.Close()
		t.Fatal("an earlier version with an open file was removed")
	}
	if !f.installed(f.v1, f.root1) || len(f.effect().Retired) != 1 {
		open.Close()
		t.Fatal("a refused removal changed the earlier version")
	}
	open.Close()
	if effect := f.advance(r, Installed); len(effect.Retired) != 0 {
		t.Fatalf("removal did not finish after the file closed: %+v", effect)
	}
}

func TestInstallingIsRecordedBeforeTheArchiveIsExtracted(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	r := f.runner()
	f.advance(r, Requested)
	f.advance(r, Verified)
	crashing := f.runner()
	crashing.crashAfter = func(step string) error {
		if step == "installing" {
			return errCrash
		}
		return nil
	}
	if _, err := crashing.Advance(f.ctx, f.component); !errors.Is(err, errCrash) {
		t.Fatalf("crash before extraction not simulated: %v", err)
	}
	if effect := f.effect(); effect.Phase != Installing {
		t.Fatalf("extraction may start before Installing is recorded: %+v", effect)
	}
	if _, err := os.Lstat(f.root2); !os.IsNotExist(err) {
		t.Fatalf("the crash point is after extraction: %v", err)
	}
	f.advance(f.runner(), Uncertain)
	if _, err := r.Retry(f.ctx, f.component); err != nil {
		t.Fatal(err)
	}
	f.advance(r, Staged)
}

func TestVersionStringsNameOneSafeFolder(t *testing.T) {
	for _, version := range []string{"1.0.", "CON", "con.1", "1.0 "} {
		a := *fixtureCatalog().Components[1].Artifact
		a.Version = version
		if err := a.validate(); err == nil {
			t.Fatalf("version %q is not a safe folder name but was accepted", version)
		}
	}
	reused := strings.Replace(string(installPinsJSON), `"version": "v2026.39.1",
        "source_commit"`, `"version": "V2026.39.1",
        "source_commit"`, 1)
	reused = strings.Replace(reused, `"sha256": "235c`, `"sha256": "335c`, 1)
	if reused == string(installPinsJSON) {
		t.Fatal("fixture edit did not apply")
	}
	// Two versions name one folder where the file system compares names
	// ignoring case (identity.SamePathName: Windows, where these pins
	// install); on a case-sensitive host they name two folders.
	if identity.SamePathName("v2026.39.1", "V2026.39.1") {
		if _, err := parseInstallPins([]byte(reused)); err == nil {
			t.Fatal("a recorded version differing only in case was accepted for other bytes")
		}
	}
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	upper := f.v2
	upper.Version = "1.0.0"
	if err := f.runner().checkChange(f.component, upper, f.v1); err == nil {
		t.Fatal("an update reusing the installed folder was accepted")
	}
	upper.Version = "1.0.0"
	retained := f.v1
	retained.Version = "1.0.0"
	if _, err := changedPackageEffect(f.component, PlannedComponent{Component: Component{Artifact: &retained}},
		Effect{Phase: Installed}, &Artifact{InstallIdentity: retained.InstallIdentity, Version: "1.0.0"}); err == nil {
		t.Fatal("a plan reusing the installed folder was accepted")
	}
	if !identity.SamePathName("v1", "V1") {
		return
	}
	retained.Version = "v1"
	if _, err := changedPackageEffect(f.component, PlannedComponent{Component: Component{Artifact: &retained}},
		Effect{Phase: Installed}, &Artifact{InstallIdentity: retained.InstallIdentity, Version: "V1"}); err == nil {
		t.Fatal("a plan reusing the installed folder in another case was accepted")
	}
	upper.Version = strings.ToUpper("v1")
	if err := f.runner().checkChange(f.component, upper, retained); err == nil {
		t.Fatal("an update reusing the installed folder in another case was accepted")
	}
}

func TestRetirePassesOverAGuardedVersionToRemoveAFailedCopy(t *testing.T) {
	f := newUpdateFixture(t, "comfyui-app", packageEntries("comfyui-app", "2.0.0"))
	r := NewRunner(f.journal, f.transfer)
	r.RetireGuard = func(_ context.Context, _ string, root string) error {
		if root == f.root1 {
			return errors.New("the registered comfyui entry still starts the earlier version")
		}
		return nil
	}
	for _, phase := range []Phase{Requested, Verified, Staged, Checked, Installed} {
		f.advance(r, phase)
	}
	broken := packageEntries("comfyui-app", "3.0.0")
	broken = append(broken, archiveEntry{name: comfyPortableDirectory + "/update/update_comfyui.bat", data: "git pull"})
	zip3, built3 := archiveFixture(t, broken...)
	third := f.v2
	third.Version, third.SHA256, third.Bytes, third.PeakBytes = "3.0.0", built3.SHA256, built3.Bytes, built3.PeakBytes
	release := *f.journal.catalogOverride
	release.Components = append([]Component(nil), release.Components...)
	for i := range release.Components {
		if release.Components[i].ID == f.component {
			release.Components[i].Artifact = &third
		}
	}
	release.PreviousInstallPins = []KnownInstallPin{{ComponentID: f.component, Artifact: f.v1}, {ComponentID: f.component, Artifact: f.v2}}
	f.journal.catalogOverride = &release
	if _, err := f.journal.SavePlan(f.ctx, mustPlan(t, release, ComfyUI)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(zip3)
	if err != nil {
		t.Fatal(err)
	}
	f.transfer.bytes = data
	r.Journal = f.journal
	for _, phase := range []Phase{Requested, Verified, Staged} {
		f.advance(r, phase)
	}
	root3, err := installPath(third)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(f.ctx, f.component); err == nil {
		t.Fatal("an unhealthy third version passed")
	}
	effect := f.effect()
	if effect.Phase != Failed || len(effect.Retired) != 1 || effect.Retired[0] != f.v1 {
		t.Fatalf("the failed copy is not removed while the guarded version waits: %+v", effect)
	}
	if _, err := os.Lstat(root3); !os.IsNotExist(err) {
		t.Fatalf("the failed copy remains: %v", err)
	}
	if !f.installed(f.v1, f.root1) || !f.installed(f.v2, f.root2) {
		t.Fatal("the guarded or the active version was touched")
	}
}

func TestFirstInstallRunsThePackageHealthCheck(t *testing.T) {
	for _, componentID := range []string{"comfyui-app", fixtureComponent} {
		t.Run(componentID, func(t *testing.T) {
			ctx := context.Background()
			standInHome(t)
			install := func(entries []archiveEntry, version string) (Runner, *fakeTransfer, Journal, string) {
				catalog := fixtureCatalog()
				zipPath, built := archiveFixture(t, entries...)
				for i := range catalog.Components {
					if catalog.Components[i].ID == componentID {
						pin := *catalog.Components[i].Artifact
						pin.Version, pin.SHA256, pin.Bytes, pin.PeakBytes = version, built.SHA256, built.Bytes, built.PeakBytes
						catalog.Components[i].Artifact = &pin
					}
				}
				data, err := os.ReadFile(zipPath)
				if err != nil {
					t.Fatal(err)
				}
				journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json"), catalogOverride: &catalog}
				transfer := &fakeTransfer{state: "complete", bytes: data}
				r := NewRunner(journal, transfer)
				if _, err := r.Start(ctx, []AppID{choiceFor(componentID)}); err != nil {
					t.Fatal(err)
				}
				pin, _ := planComponent(mustPlan(t, catalog, choiceFor(componentID)), componentID)
				root, err := installPath(*pin.Artifact)
				if err != nil {
					t.Fatal(err)
				}
				return r, transfer, journal, root
			}
			phase := func(journal Journal) Phase {
				state, err := journal.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return state.Effects[componentID].Phase
			}
			r, _, journal, root := install(packageEntries(componentID, "1.2.3"), "1.2.3")
			for _, want := range []Phase{Requested, Verified, Staged, Checked, Installed} {
				if _, err := r.Advance(ctx, componentID); err != nil || phase(journal) != want {
					t.Fatalf("first install step to %s: %s, %v", want, phase(journal), err)
				}
			}
			if ok, err := ArchiveInstalled(*mustArtifact(t, journal, componentID), root); err != nil || !ok {
				t.Fatalf("healthy first install not in place: %v, %v", ok, err)
			}
			entries := packageEntries(componentID, "1.2.3")
			if componentID == fixtureComponent {
				entries = entries[1:]
			} else {
				entries[0].data = `{}`
			}
			r, _, journal, root = install(entries, "1.2.4")
			for range 3 {
				if _, err := r.Advance(ctx, componentID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Advance(ctx, componentID); err == nil || phase(journal) != Failed {
				t.Fatalf("an unhealthy first install was accepted: %s, %v", phase(journal), err)
			}
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Fatalf("the unhealthy first install remains: %v", err)
			}
		})
	}
}

func mustArtifact(t *testing.T, journal Journal, componentID string) *Artifact {
	t.Helper()
	state, err := journal.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	component, _ := planComponent(state.Plan, componentID)
	return component.Artifact
}

func TestAnUnfinishedRemovalNamesTheFolderToInspect(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	aside := filepath.Join(filepath.Dir(f.root1), "."+filepath.Base(f.root1)+".removing")
	if err := os.MkdirAll(aside, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := removePackageTree(f.v1, f.root1)
	if err == nil || !strings.Contains(err.Error(), aside) || !strings.Contains(err.Error(), "move") {
		t.Fatalf("the refusal does not name the folder and what to do: %v", err)
	}
	if !f.installed(f.v1, f.root1) {
		t.Fatal("a refused removal touched the version")
	}
}

func TestRetryOfAnUncertainInstallWithNoTreeReinstallsTheVerifiedDownload(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	r := f.runner()
	f.advance(r, Requested)
	f.advance(r, Verified)
	crashing := f.runner()
	crashing.crashAfter = crashAt("install")
	if _, err := crashing.Advance(f.ctx, f.component); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.root2); err != nil {
		t.Fatal(err)
	}
	f.advance(r, Uncertain)
	status, err := r.Retry(f.ctx, f.component)
	if err != nil || status.Components[0].Effect.Phase != Verified {
		t.Fatalf("explicit retry after an absent install identity refused: %+v, %v", status, err)
	}
	f.advance(r, Staged)
}

func TestJournalOwnsTheUpdateFields(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	state, err := f.journal.Load(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	pending := state.Effects[f.component]
	if _, err := f.journal.Update(f.ctx, state.Plan.ID, f.component, pending, Effect{Phase: Installed}); err == nil {
		t.Fatal("an update became installed without staging and a health check")
	}
	if _, err := f.journal.Update(f.ctx, state.Plan.ID, f.component, pending, Effect{Phase: Failed, Retained: nil}); err != nil {
		t.Fatal(err)
	}
	if effect := f.effect(); effect.Retained == nil || *effect.Retained != f.v1 {
		t.Fatalf("a caller cleared the retained version: %+v", effect)
	}

	fresh := fixtureCatalog()
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &fresh}
	plan := mustPlan(t, fresh, Lemonade)
	saved, err := journal.SavePlan(f.ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	// A first Lemonade installation passes the staged steps, since its
	// provider's package check proves any root.
	if _, err := journal.Update(f.ctx, plan.ID, "lemonade-engine", saved.Effects["lemonade-engine"], Effect{Phase: Staged}); err != nil {
		t.Fatalf("a first Lemonade installation did not enter the staged steps: %v", err)
	}
}

func TestStagedUpdateAndReusedVersionBlockPlanReplacement(t *testing.T) {
	f := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	r := f.runner()
	f.advance(r, Requested)
	f.advance(r, Verified)
	f.advance(r, Staged)
	third := *f.journal.catalogOverride
	third.Components = append([]Component(nil), third.Components...)
	for i := range third.Components {
		if third.Components[i].ID == f.component {
			pin := f.v2
			pin.Version, pin.SHA256 = "3.0.0", strings.Repeat("3", 64)
			third.Components[i].Artifact = &pin
		}
	}
	third.PreviousInstallPins = append(third.PreviousInstallPins, KnownInstallPin{ComponentID: f.component, Artifact: f.v2})
	f.journal.catalogOverride = &third
	if _, err := f.journal.SavePlan(f.ctx, mustPlan(t, third, fixtureApp)); err == nil {
		t.Fatal("a staged update was dropped by a plan replacement")
	}

	g := newUpdateFixture(t, fixtureComponent, packageEntries(fixtureComponent, "2.0.0"))
	reused := *g.journal.catalogOverride
	reused.Components = append([]Component(nil), reused.Components...)
	for i := range reused.Components {
		if reused.Components[i].ID == g.component {
			pin := g.v2
			pin.Version, pin.SHA256 = g.v1.Version, strings.Repeat("4", 64)
			reused.Components[i].Artifact = &pin
		}
	}
	// Replace the approved 2.0.0 plan with the pre-update state first.
	if err := os.Remove(g.journal.Path); err != nil {
		t.Fatal(err)
	}
	original := fixtureCatalog()
	for i := range original.Components {
		if original.Components[i].ID == g.component {
			pin := g.v1
			original.Components[i].Artifact = &pin
		}
	}
	g.journal.catalogOverride = &original
	plan := mustPlan(t, original, fixtureApp)
	state, err := g.journal.SavePlan(g.ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.journal.Update(g.ctx, plan.ID, g.component, state.Effects[g.component], Effect{Phase: Installed}); err != nil {
		t.Fatal(err)
	}
	reused.PreviousInstallPins = []KnownInstallPin{{ComponentID: g.component, Artifact: g.v1}}
	g.journal.catalogOverride = &reused
	if _, err := g.journal.SavePlan(g.ctx, mustPlan(t, reused, fixtureApp)); err == nil || !strings.Contains(err.Error(), "needs a new version") {
		t.Fatalf("a changed package reusing the installed version's folder was accepted: %v", err)
	}
}
