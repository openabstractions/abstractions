package setup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureFinder names only fixture folders and a fake uninstall registry.
func fixtureFinder(t *testing.T, entries []UninstallEntry) (CopyFinder, string) {
	t.Helper()
	root := t.TempDir()
	return CopyFinder{
		UserProfile:  filepath.Join(root, "home"),
		LocalAppData: filepath.Join(root, "local"),
		AppData:      filepath.Join(root, "roaming"),
		ProgramFiles: []string{filepath.Join(root, "Program Files")},
		SystemDrive:  filepath.Join(root, "drive"),
		Uninstall:    func() ([]UninstallEntry, error) { return entries, nil },
	}, root
}

func portableComfy(t *testing.T, dir string) {
	writeFixture(t, filepath.Join(dir, "ComfyUI", "main.py"))
	writeFixture(t, filepath.Join(dir, "python_embeded", "python.exe"))
}

// Unmarked copies of ComfyUI are found in their usual folders and in the
// uninstall registry, each with its location. OpenCode's own installs, which
// Setup no longer installs beside, are not reported.
func TestFindReportsUnmarkedCopiesWithTheirLocations(t *testing.T) {
	finder, root := fixtureFinder(t, nil)
	desktop := filepath.Join(root, "local", "Programs", "@comfyorgcomfyui-electron")
	finder.Uninstall = func() ([]UninstallEntry, error) {
		return []UninstallEntry{
			{DisplayName: "ComfyUI 0.4.80", InstallLocation: `"` + desktop + `"`},
			{DisplayName: "OpenCode Desktop", DisplayIcon: filepath.Join(root, "Program Files", "OpenCode Desktop", "OpenCode.exe") + ",0"},
			{DisplayName: "OpenAbstractions", InstallLocation: filepath.Join(root, "Program Files", "OpenAbstractions")},
			{DisplayName: "Notepad++", InstallLocation: filepath.Join(root, "Program Files", "Notepad++")},
		}, nil
	}
	portable := filepath.Join(finder.UserProfile, "Desktop", "ComfyUI_windows_portable")
	portableComfy(t, portable)
	writeFixture(t, filepath.Join(desktop, "ComfyUI.exe"))
	writeFixture(t, filepath.Join(finder.UserProfile, ".opencode", "bin", "opencode.exe"))
	writeFixture(t, filepath.Join(finder.AppData, "npm", "node_modules", "opencode-ai", "package.json"))
	found, err := finder.Find()
	if err != nil {
		t.Fatal(err)
	}
	want := []OtherCopy{
		{App: ComfyUI, Title: "ComfyUI", Location: portable, Source: "folder"},
		{App: ComfyUI, Title: "ComfyUI Desktop", Location: desktop, Source: "folder"},
	}
	if !slices.Equal(found, want) {
		t.Fatalf("found %+v\nwant %+v", found, want)
	}
	var other *OtherCopyError
	if err := CheckBeside([]AppID{ComfyUI, Lemonade}, nil, found); !errors.As(err, &other) || other.Copy.Location != portable || !errors.Is(err, ErrOtherCopy) {
		t.Fatalf("an unmarked ComfyUI did not keep OA's build out of the plan: %v", err)
	}
	if err := CheckBeside([]AppID{ComfyUI}, []AppID{ComfyUI}, found); err != nil {
		t.Fatalf("the explicit choice was refused: %v", err)
	}
	if err := CheckBeside([]AppID{Lemonade}, nil, found); err != nil {
		t.Fatalf("a goal with no other copy was refused: %v", err)
	}
}

// OA's own marked builds are Setup's to replace and never count as another
// copy, in a folder or behind an uninstall entry.
func TestFindLeavesOutOAMarkedBuilds(t *testing.T) {
	finder, _ := fixtureFinder(t, nil)
	comfyRoot := filepath.Join(finder.UserProfile, "Desktop")
	portableComfy(t, filepath.Join(comfyRoot, "ComfyUI_windows_portable"))
	writeFixture(t, filepath.Join(comfyRoot, "OA-PROVENANCE.json"))
	writeFixture(t, filepath.Join(comfyRoot, archiveMarker))
	found, err := finder.Find()
	if err != nil || len(found) != 0 {
		t.Fatalf("OA-marked builds reported as other copies: %+v %v", found, err)
	}
	// A provenance file without Setup's receipt is no OA mark.
	if err := os.Remove(filepath.Join(comfyRoot, archiveMarker)); err != nil {
		t.Fatal(err)
	}
	if found, _ := finder.Find(); len(found) != 1 || found[0].App != ComfyUI {
		t.Fatalf("an unreceipted provenance file hid a copy: %+v", found)
	}
}

// With nothing installed, Setup finds nothing and refuses nothing.
func TestFindOnAnEmptyMachineFindsNothing(t *testing.T) {
	finder, _ := fixtureFinder(t, nil)
	found, err := finder.Find()
	if err != nil || len(found) != 0 {
		t.Fatalf("empty fixture machine: %+v %v", found, err)
	}
	if err := CheckBeside([]AppID{ComfyUI, Lemonade}, nil, found); err != nil {
		t.Fatal(err)
	}
	finder.Uninstall = func() ([]UninstallEntry, error) { return nil, errors.New("registry unavailable") }
	if found, err := finder.Find(); err == nil || len(found) != 0 {
		t.Fatalf("an unreadable registry was not reported: %+v %v", found, err)
	}
}
