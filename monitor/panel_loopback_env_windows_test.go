//go:build windows

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"golang.org/x/sys/windows"
)

// guardFoldersChild names the environment variable that makes this test
// binary answer as the forged-environment child of
// TestInstalledPanelIgnoresForgedEnvironment.
const guardFoldersChild = "OA_TEST_GUARD_FOLDERS_CHILD"

type guardFoldersAnswer struct {
	Folders   []string
	Decoy     bool
	Installed map[string]bool
}

// A program that starts the installed Panel with -native=false also chooses
// its environment. The guard finds the install folders through what Windows
// records: the ProgramFiles known folder and the account's profile. This test
// starts the guard in a fresh process whose ProgramFiles, ProgramW6432,
// LOCALAPPDATA, APPDATA, USERPROFILE and HOME all name one forged folder laid
// out as a profile. The forged process must still list the machine and
// account install folders and every folder Windows Installer records, must
// judge a Panel in the forged ProgramFiles folder a stranger, and must guard
// the installed Panel on this machine. The installed Panel's file is only
// read, never started.
func TestInstalledPanelIgnoresForgedEnvironment(t *testing.T) {
	if os.Getenv(guardFoldersChild) != "" {
		t.Skip("the forged-environment child answers in TestGuardFoldersChild")
	}
	machine, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := windows.KnownFolderPath(windows.FOLDERID_Profile, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(machine, "OpenAbstractions", "tools"),
		filepath.Join(profile, "AppData", "Local", "Programs", "OpenAbstractions", "tools"),
	}
	// Windows Installer records where each installation went, which
	// APPLICATIONFOLDER may have put outside both folders.
	registered, err := product.RegisteredToolFolders()
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, registered...)
	forged := t.TempDir()
	for _, sub := range []string{filepath.Join("AppData", "Local"), filepath.Join("AppData", "Roaming")} {
		if err := os.MkdirAll(filepath.Join(forged, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	decoy := filepath.Join(forged, "OpenAbstractions", "tools", "Abstraction Panel.exe")
	if err := os.MkdirAll(filepath.Dir(decoy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decoy, []byte("decoy panel"), 0o700); err != nil {
		t.Fatal(err)
	}
	var installed []string
	for _, folder := range want {
		if file := filepath.Join(folder, "Abstraction Panel.exe"); fileExists(file) {
			installed = append(installed, file)
		}
	}
	request, err := json.Marshal(struct {
		Decoy     string
		Installed []string
	}{decoy, installed})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGuardFoldersChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), guardFoldersChild+"="+string(request),
		"ProgramFiles="+forged, "ProgramW6432="+forged,
		"LOCALAPPDATA="+filepath.Join(forged, "AppData", "Local"), "APPDATA="+filepath.Join(forged, "AppData", "Roaming"),
		"USERPROFILE="+forged, "HOME="+forged)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("forged-environment child: %v\n%s", err, out)
	}
	var answer guardFoldersAnswer
	line := out
	if i := strings.Index(string(out), "{"); i >= 0 {
		line = out[i:]
		if j := strings.Index(string(line), "\n"); j >= 0 {
			line = line[:j]
		}
	}
	if err := json.Unmarshal(line, &answer); err != nil {
		t.Fatalf("forged-environment child answered %q: %v", out, err)
	}
	for _, folder := range want {
		if !slices.ContainsFunc(answer.Folders, func(g string) bool { return strings.EqualFold(filepath.Clean(g), filepath.Clean(folder)) }) {
			t.Errorf("install folders %q under a forged environment miss %q", answer.Folders, folder)
		}
	}
	if answer.Decoy {
		t.Errorf("a Panel in the folder a forged ProgramFiles names was judged the installed Panel")
	}
	for _, file := range installed {
		if !answer.Installed[file] {
			t.Errorf("the installed Panel %s went unguarded under a forged environment", file)
		}
	}
	if len(installed) == 0 {
		t.Log("no installed Panel on this machine; the folder and decoy checks ran")
	}
}

// TestGuardFoldersChild is the forged-environment process of
// TestInstalledPanelIgnoresForgedEnvironment. It runs the production guard
// lookups and prints their answers as one JSON line.
func TestGuardFoldersChild(t *testing.T) {
	raw := os.Getenv(guardFoldersChild)
	if raw == "" {
		t.Skip("runs only as the child of TestInstalledPanelIgnoresForgedEnvironment")
	}
	var request struct {
		Decoy     string
		Installed []string
	}
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	folders, err := panelInstallFolders()
	if err != nil {
		t.Fatal(err)
	}
	answer := guardFoldersAnswer{Folders: folders, Decoy: installedPanel(request.Decoy), Installed: map[string]bool{}}
	for _, file := range request.Installed {
		answer.Installed[file] = installedPanel(file)
	}
	line, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.Write(append(append([]byte{'\n'}, line...), '\n'))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
