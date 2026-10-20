package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// A graphics-card holder reads by the registered descriptor's title when the
// filesystem says the two programs are one file: another case spelling and a
// path through a junction match; a twin name NTFS keeps as a separate file
// (U+212A KELVIN SIGN beside k, which a JavaScript toLowerCase merges) does
// not.
func TestCardHolderTitleIsAskedOfTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	registered := filepath.Join(app, "link.exe")
	twin := filepath.Join(app, "lin"+string(rune(0x212A))+".exe")
	for _, file := range []string{registered, twin} {
		if err := os.WriteFile(file, []byte("program"), 0o755); err != nil {
			t.Fatalf("create %q: %v", file, err)
		}
	}
	if same, err := os.Stat(registered); err != nil {
		t.Fatal(err)
	} else if other, err := os.Stat(twin); err != nil || os.SameFile(same, other) {
		t.Fatalf("the volume does not keep the twin apart: %v", err)
	}
	link := filepath.Join(dir, "through")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, app).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	resources := []cardResourceState{{Resource: "card:0", Holders: []cardHolder{
		{Program: filepath.Join(app, "LINK.EXE")},
		{Program: filepath.Join(link, "link.exe")},
		{Program: twin},
	}}}
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{
		{Descriptor: fwire.ApplicationDescriptor{Name: "link", Program: registered, Title: "Link App"}, Scope: fwire.ScopeLocal},
	}}}
	(&servicePanel{comfyApps: apps}).titleCardHolders(context.Background(), resources)
	got := resources[0].Holders
	if got[0].Title != "Link App" {
		t.Errorf("another case spelling of the registered program is untitled: %+v", got[0])
	}
	if got[1].Title != "Link App" {
		t.Errorf("the registered program through a junction is untitled: %+v", got[1])
	}
	if got[2].Title != "" {
		t.Errorf("a twin name the volume keeps apart took the registered title: %+v", got[2])
	}
}
