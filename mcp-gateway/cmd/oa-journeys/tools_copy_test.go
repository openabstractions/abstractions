//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A run against an installed tools folder starts the isolated runtime and its
// Panel from private copies of the folder's top-level programs, so the Panel
// the journeys drive is a different file from the installed (guarded) one and
// sits beside the runtime that admits it. A build folder is used as it is.
func TestRuntimeToolsCopiesAnInstalledFolder(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "installed")
	build := filepath.Join(root, "build")
	for _, dir := range []string{filepath.Join(installed, "engines"), build} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Abstraction Panel.exe", "openabstractions.exe", "modelhostd.exe", filepath.Join("engines", "llama.zip")} {
		if err := os.WriteFile(filepath.Join(installed, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(build, "Abstraction Panel.exe"), []byte("build"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := installedToolFolders
	installedToolFolders = func() []string { return []string{"", installed} }
	t.Cleanup(func() { installedToolFolders = previous })

	if got, err := runtimeTools(build, filepath.Join(root, "unused")); err != nil || got != build {
		t.Fatalf("build folder: %q, %v; want it unchanged", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "unused")); !os.IsNotExist(err) {
		t.Fatalf("a build folder staged a copy: %v", err)
	}

	link := filepath.Join(root, "linked")
	if err := os.MkdirAll(link, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(installed, "Abstraction Panel.exe"), filepath.Join(link, "Abstraction Panel.exe")); err != nil {
		t.Fatal(err)
	}
	if !holdsInstalledPanel(link) {
		t.Fatal("a hard link of the installed Panel reads as another Panel; the guard compares file identity")
	}

	staged := filepath.Join(root, "work", "tools")
	got, err := runtimeTools(installed, staged)
	if err != nil || got != staged {
		t.Fatalf("installed folder: %q, %v; want %q", got, err, staged)
	}
	for _, name := range []string{"Abstraction Panel.exe", "openabstractions.exe", "modelhostd.exe"} {
		data, err := os.ReadFile(filepath.Join(staged, name))
		if err != nil || string(data) != name {
			t.Fatalf("%s copy: %q, %v", name, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(staged, "engines")); !os.IsNotExist(err) {
		t.Fatalf("subfolders stay in -tools: %v", err)
	}
	if holdsInstalledPanel(staged) {
		t.Fatal("the staged Panel is the installed Panel")
	}
}
