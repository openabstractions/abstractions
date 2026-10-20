package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func digestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// fixupFixture is the release catalogue with OpenCode's fix-up hashing test
// bytes, OpenCode 1.18.34's official program in a temporary local
// application data folder, its adopter package installed with the fixed
// file it carries, and nothing running. repairs counts the repair package
// runs; repair, when set, is what one does.
type fixupFixture struct {
	catalog Catalog
	folder  string
	local   string
	writes  int
	running bool
	repairs int
	repair  func() error
}

var openCodeWatcherFixup = *installerFixup(OpenCodeOfficialComponent)

func newFixupFixture(t *testing.T, file string) *fixupFixture {
	t.Helper()
	local := t.TempDir()
	f := &fixupFixture{catalog: NewCatalog(), folder: filepath.Join(local, "Programs", "@opencode-aidesktop"), local: local}
	for i := range f.catalog.Components {
		if f.catalog.Components[i].ID == OpenCodeOfficialComponent {
			fixup := *f.catalog.Components[i].Fixup
			fixup.Stock, fixup.Fixed = []string{digestOf("stock")}, []string{digestOf("fixed")}
			fixup.ProgramSHA256 = map[string]string{"1.18.34": digestOf("program 1.18.34"), "1.19.5": digestOf("program 1.19.5")}
			f.catalog.Components[i].Fixup = &fixup
		}
	}
	if err := os.MkdirAll(f.folder, 0o755); err != nil {
		t.Fatal(err)
	}
	f.program(t, "program 1.18.34")
	carried := filepath.Join(local, filepath.FromSlash(openCodeWatcherFixup.Package), filepath.FromSlash(openCodeWatcherFixup.PackageFile))
	if err := os.MkdirAll(filepath.Dir(carried), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(carried, []byte("fixed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if file != "" {
		f.write(t, file)
	}
	oldLocal, oldInstalled, oldRunning, oldRepair := fixupLocalAppData, fixupPackageInstalled, fixupRunning, fixupRepairPackage
	t.Cleanup(func() {
		fixupLocalAppData, fixupPackageInstalled, fixupRunning, fixupRepairPackage = oldLocal, oldInstalled, oldRunning, oldRepair
	})
	fixupLocalAppData = func() (string, error) { return local, nil }
	fixupPackageInstalled = func(code string) (bool, error) {
		if code != openCodeWatcherFixup.RepairUpgradeCode {
			t.Fatalf("package %s, want the OpenCode integration's", code)
		}
		return true, nil
	}
	fixupRunning = func(folder string) (bool, error) {
		if folder != f.folder {
			t.Fatalf("running check of %s, want %s", folder, f.folder)
		}
		return f.running, nil
	}
	fixupRepairPackage = func(_ context.Context, code string) error {
		if code != openCodeWatcherFixup.RepairUpgradeCode {
			t.Fatalf("repair of %s, want the OpenCode integration's", code)
		}
		f.repairs++
		if f.repair != nil {
			return f.repair()
		}
		return nil
	}
	return f
}

// program writes OpenCode's official program; a new size or time makes
// the check hash it again.
func (f *fixupFixture) program(t *testing.T, text string) {
	t.Helper()
	name := filepath.Join(f.folder, "OpenCode.exe")
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	f.writes++
	when := time.Date(2026, 11, 10, 0, 0, f.writes, 0, time.UTC)
	if err := os.Chtimes(name, when, when); err != nil {
		t.Fatal(err)
	}
}

func (f *fixupFixture) carried() string {
	return filepath.Join(f.local, filepath.FromSlash(openCodeWatcherFixup.Package), filepath.FromSlash(openCodeWatcherFixup.PackageFile))
}

func (f *fixupFixture) watcher() string {
	return filepath.Join(f.folder, filepath.FromSlash(openCodeWatcherFixup.File))
}

func (f *fixupFixture) write(t *testing.T, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.watcher()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.watcher(), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixupFixture) check(t *testing.T) FixupStatus {
	t.Helper()
	statuses, err := f.catalog.CheckFixups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses %+v, want one", statuses)
	}
	return statuses[0]
}

func TestOpenCodeFixupEntry(t *testing.T) {
	var fixup *Fixup
	for _, component := range NewCatalog().Components {
		if component.ID == OpenCodeOfficialComponent {
			fixup = component.Fixup
		}
	}
	if fixup == nil {
		t.Fatal("OpenCode's component names no fix-up")
	}
	if err := validateFixup(fixup); err != nil {
		t.Fatal(err)
	}
	if fixup.File != "resources/app.asar.unpacked/node_modules/@parcel/watcher-win32-x64/watcher.node" ||
		fixup.Stock[0] != "a8199cf7b6c5102267a5889f80321a4cc92e631d7504c474d32a35b4d440f315" ||
		fixup.Fixed[0] != "0251ee3ba7027af84a0cb8e0c2ffa1c5989eacf21bc091f95a7c5c00cc486e12" ||
		fixup.ProgramSHA256["1.18.34"] != "b82ea814eb265c07c97df0e112f673c8be4718e00c311c154db85e06d3c1f9ac" ||
		fixup.ProgramSHA256["1.19.5"] != "64f43dc4e75cee06c85f03361d12a7791a60873114a2420cbdd94ae7f0c5da3f" ||
		fixup.Package != "Programs/OpenAbstractions/apps/opencode" || fixup.PackageFile != "desktop-fix/watcher.node" {
		t.Fatalf("fix-up %+v", fixup)
	}
	// The fix-up is no part of the catalogue digest a plan binds.
	encoded, err := json.Marshal(NewCatalog())
	if err != nil || strings.Contains(string(encoded), "watcher") || strings.Contains(string(encoded), fixup.Stock[0]) {
		t.Fatalf("the catalogue's JSON carries the fix-up: %v", err)
	}
}

func TestFixupValidateRefuses(t *testing.T) {
	good := openCodeWatcherFixup
	for name, change := range map[string]func(*Fixup){
		"absolute":     func(f *Fixup) { f.File = "/watcher.node" },
		"parent":       func(f *Fixup) { f.File = "../watcher.node" },
		"backslash":    func(f *Fixup) { f.File = `a\watcher.node` },
		"file stream":  func(f *Fixup) { f.File = "watcher.node:other" },
		"device":       func(f *Fixup) { f.File = "NUL.node" },
		"trailing dot": func(f *Fixup) { f.File = "watcher.node." },
		"short digest": func(f *Fixup) { f.Stock = []string{"abc"} },
		"both":         func(f *Fixup) { f.Fixed = f.Stock },
		"no versions":  func(f *Fixup) { f.ProgramVersions = nil },
		"no program":   func(f *Fixup) { f.ProgramSHA256 = map[string]string{"1.18.34": f.ProgramSHA256["1.18.34"]} },
		"package":      func(f *Fixup) { f.Package = "" },
		"package file": func(f *Fixup) { f.PackageFile = "../watcher.node" },
		"upgrade code": func(f *Fixup) { f.RepairUpgradeCode = "8E0B8005-0E00-5720-9556-E5B984F15A96" },
	} {
		fixup := good
		change(&fixup)
		if validateFixup(&fixup) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFixupStockHashNeedsRepair(t *testing.T) {
	f := newFixupFixture(t, "stock")
	status := f.check(t)
	if status.State != FixupNeedsRepair || !status.Repair || status.Running || status.App != OpenCode || status.Component != OpenCodeOfficialComponent {
		t.Fatalf("status %+v", status)
	}
	if status.Sentence != "Needs its OpenAbstractions fix again" || !strings.Contains(status.Detail, "OpenCode's server") {
		t.Fatalf("words %q / %q", status.Sentence, status.Detail)
	}
	if f.repairs != 0 {
		t.Fatal("a check ran the repair")
	}
}

func TestFixupFixedHashIsCurrent(t *testing.T) {
	f := newFixupFixture(t, "fixed")
	status := f.check(t)
	if status.State != FixupCurrent || status.Repair || status.Sentence != "" {
		t.Fatalf("status %+v", status)
	}
}

// A supported OpenCode whose file is missing or unknown says the fix can't
// be checked; an unsupported one names the supported versions. Neither
// offers Repair.
func TestFixupUnknownFileOffersNoRepair(t *testing.T) {
	for name, file := range map[string]string{"other bytes": "someone else's build", "missing": ""} {
		for _, program := range []string{"program 1.19.5", "program 1.20.0"} {
			f := newFixupFixture(t, file)
			f.program(t, program)
			status := f.check(t)
			if status.State != FixupUnknown || status.Repair || strings.Join(status.Versions, " ") != "1.18.34 1.19.5" {
				t.Fatalf("%s, %s: status %+v", name, program, status)
			}
			supported := program == "program 1.19.5"
			sentence, detail := "Not a version this release supports", "This release of OpenAbstractions supports OpenCode 1.18.34 and 1.19.5. Setup leaves this OpenCode as it is."
			if supported {
				sentence, detail = "The OpenAbstractions fix can't be checked", "OpenCode 1.19.5 is a supported version. The file the fix replaces is missing or changed. Reinstall OpenCode with OpenAbstractions to put the fix back."
			}
			if status.Supported != supported || status.Sentence != sentence || status.Detail != detail {
				t.Fatalf("%s, %s: words %v %q / %q", name, program, status.Supported, status.Sentence, status.Detail)
			}
			if _, err := f.catalog.RepairFixup(context.Background(), OpenCodeOfficialComponent); err == nil || f.repairs != 0 {
				t.Fatalf("%s, %s: Repair over an unknown file: %v, %d runs", name, program, err, f.repairs)
			}
		}
	}
}

func TestFixupAppOrPackageAbsentShowsNothing(t *testing.T) {
	f := newFixupFixture(t, "stock")
	fixupPackageInstalled = func(string) (bool, error) { return false, nil }
	if statuses, err := f.catalog.CheckFixups(context.Background()); err != nil || len(statuses) != 0 {
		t.Fatalf("without the adopter package: %+v, %v", statuses, err)
	}
	f = newFixupFixture(t, "stock")
	if err := os.Remove(filepath.Join(f.folder, "OpenCode.exe")); err != nil {
		t.Fatal(err)
	}
	if statuses, err := f.catalog.CheckFixups(context.Background()); err != nil || len(statuses) != 0 {
		t.Fatalf("without OpenCode: %+v, %v", statuses, err)
	}
}

func TestFixupRepairRefusedWhileAppRuns(t *testing.T) {
	f := newFixupFixture(t, "stock")
	f.running = true
	status := f.check(t)
	if !status.Running || status.Close != "Close OpenCode, then press Repair." {
		t.Fatalf("status %+v", status)
	}
	_, err := f.catalog.RepairFixup(context.Background(), OpenCodeOfficialComponent)
	if !errors.Is(err, ErrFixupAppRunning) || !strings.Contains(err.Error(), "Close OpenCode, then press Repair.") {
		t.Fatalf("err %v", err)
	}
	if f.repairs != 0 {
		t.Fatal("Repair ran while OpenCode was open")
	}
}

func TestFixupRepairRunsPackageRepairThenChecks(t *testing.T) {
	f := newFixupFixture(t, "stock")
	f.repair = func() error { f.write(t, "fixed"); return nil }
	status, err := f.catalog.RepairFixup(context.Background(), OpenCodeOfficialComponent)
	if err != nil || status.State != FixupCurrent || f.repairs != 1 {
		t.Fatalf("status %+v, err %v, %d runs", status, err, f.repairs)
	}
	// A second Repair finds the fix in place and runs nothing.
	if status, err := f.catalog.RepairFixup(context.Background(), OpenCodeOfficialComponent); err != nil || status.State != FixupCurrent || f.repairs != 1 {
		t.Fatalf("again: %+v, %v, %d runs", status, err, f.repairs)
	}
}

func TestFixupRepairThatChangesNothingFails(t *testing.T) {
	f := newFixupFixture(t, "stock")
	status, err := f.catalog.RepairFixup(context.Background(), OpenCodeOfficialComponent)
	if err == nil || status.State != FixupNeedsRepair || f.repairs != 1 || !strings.Contains(err.Error(), "still needs") {
		t.Fatalf("status %+v, err %v", status, err)
	}
	f.repair = func() error { return errors.New("Windows Installer did not repair it (code 1603)") }
	if _, err := f.catalog.RepairFixup(context.Background(), OpenCodeOfficialComponent); err == nil || !strings.Contains(err.Error(), "1603") {
		t.Fatalf("err %v", err)
	}
}

func TestFixupRepairUnknownComponent(t *testing.T) {
	f := newFixupFixture(t, "stock")
	if _, err := f.catalog.RepairFixup(context.Background(), "comfyui-app"); err == nil || f.repairs != 0 {
		t.Fatalf("err %v", err)
	}
}

// B1: OpenCode's stock watcher is the same in every release; the official
// program's own bytes decide whether the adopter's installer accepts it.
func TestFixupStockWatcherOfAnUnlistedVersionOffersNoRepair(t *testing.T) {
	f := newFixupFixture(t, "stock")
	f.program(t, "program 1.20.0")
	status := f.check(t)
	if status.State != FixupUnknown || status.Repair || status.Sentence != "Not a version this release supports" {
		t.Fatalf("status %+v", status)
	}
	if _, err := f.catalog.RepairFixup(context.Background(), OpenCodeOfficialComponent); err == nil || f.repairs != 0 {
		t.Fatalf("Repair of 1.20.0: %v, %d runs", err, f.repairs)
	}
	f.program(t, "program 1.19.5")
	if status := f.check(t); status.State != FixupNeedsRepair || !status.Repair {
		t.Fatalf("1.19.5: %+v", status)
	}
}

// B2: a package that carries no fixed file, or another file, offers nothing.
func TestFixupPackageWithoutTheFixShowsNothing(t *testing.T) {
	f := newFixupFixture(t, "stock")
	if err := os.Remove(f.carried()); err != nil {
		t.Fatal(err)
	}
	if statuses, err := f.catalog.CheckFixups(context.Background()); err != nil || len(statuses) != 0 {
		t.Fatalf("without the package's fixed file: %+v, %v", statuses, err)
	}
	if _, err := f.catalog.RepairFixup(context.Background(), OpenCodeOfficialComponent); err == nil || f.repairs != 0 {
		t.Fatalf("Repair without the package's fix: %v, %d runs", err, f.repairs)
	}
	if err := os.WriteFile(f.carried(), []byte("another build"), 0o644); err != nil {
		t.Fatal(err)
	}
	if statuses, err := f.catalog.CheckFixups(context.Background()); err != nil || len(statuses) != 0 {
		t.Fatalf("with another file: %+v, %v", statuses, err)
	}
}
