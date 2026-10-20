package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// noRegistration stands in for the registration page where no app in a
// test is installed without a registration.
func noRegistration(string) error { return errors.New("no registration page in this test") }

// A fresh install (preview-1031f): the code app's Setup package recorded its
// manifest, and the person never confirmed its registration page. The step
// still finds it by the catalog's choice, named by the catalog's title, with
// the manifest's program; a registered app, a missing record, an unreadable
// manifest and a manifest for another name add nothing.
//
// Review 39: any process of the account can write the record, so a manifest
// counts only inside the folder Setup's package installs the app into, or
// when it names the program Setup recorded; every other record is ignored.
func TestSetupStepFindsAnInstalledAppNotRegisteredYet(t *testing.T) {
	base := t.TempDir()
	at := func(parts ...string) string {
		path := filepath.Join(append([]string{base}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	choices := []setup.Choice{{ID: "quill", Title: "Quill", Application: true, Use: setup.UseCode}, {ID: "easel", Title: "Easel", Application: true, Use: setup.UseImage, Engines: []setup.AppID{"fixture-engine"}},
		{ID: "slate", Title: "Slate", Application: true, Use: setup.UseCode}, {ID: "chalk", Title: "Chalk", Application: true, Use: setup.UseCode},
		{ID: "forged", Title: "Forged", Application: true, Use: setup.UseCode}, {ID: "moved", Title: "Moved", Application: true, Use: setup.UseCode}}
	registered := []setup.StepApp{{Name: "easel", Title: "Easel", Use: setup.UseImage}}
	folders := map[string]string{"quill": filepath.Join(base, "apps", "quill"), "forged": filepath.Join(base, "apps", "forged"), "moved": filepath.Join(base, "apps", "moved")}
	records := map[string]string{"quill": at("apps", "quill", "application-manifest.json"), "easel": at("apps", "easel", "application-manifest.json"),
		"slate": at("apps", "slate", "application-manifest.json"), "chalk": at("apps", "chalk", "application-manifest.json"),
		"forged": at("elsewhere", "forged", "application-manifest.json"), "moved": at("elsewhere", "moved", "application-manifest.json")}
	at("apps", "forged", "keep")
	recordedMoved := at("setup-install", "moved", "moved.exe")
	installed := func(name string) (string, bool) { path, ok := records[name]; return path, ok }
	read := func(path string) (fwire.ApplicationDescriptor, error) {
		switch path {
		case records["quill"]:
			return fwire.ApplicationDescriptor{Name: "quill", Title: "Quill Editor from its manifest", Program: filepath.Join(base, "apps", "quill", "quill.exe")}, nil
		case records["slate"]:
			return fwire.ApplicationDescriptor{Name: "chalk", Title: "Chalk"}, nil
		case records["forged"]:
			return fwire.ApplicationDescriptor{Name: "forged", Title: "Forged", Program: filepath.Join(base, "elsewhere", "forged", "forged.exe")}, nil
		case records["moved"]:
			return fwire.ApplicationDescriptor{Name: "moved", Title: "A title the manifest chose", Program: recordedMoved}, nil
		}
		return fwire.ApplicationDescriptor{}, errors.New("unreadable")
	}
	folder := func(name string) string { return folders[name] }
	recorded := func(name string) string {
		if name == "moved" {
			return recordedMoved
		}
		return ""
	}
	got := stepInstalledApps(choices, "", registered, installed, read, folder, recorded)
	want := []setup.StepApp{{Name: "quill", Title: "Quill", Program: filepath.Join(base, "apps", "quill", "quill.exe"), Use: setup.UseCode, Manifest: records["quill"]},
		{Name: "moved", Title: "Moved", Program: recordedMoved, Use: setup.UseCode, Manifest: records["moved"]}}
	if !slices.Equal(got, want) {
		t.Fatalf("installed apps = %+v", got)
	}
}

// The question and notices name an app by Setup's catalog title, never by
// the title its registration supplies (review 39).
func TestSetupStepNamesAppsByTheCatalogTitle(t *testing.T) {
	choices := []setup.Choice{{ID: "quill", Title: "Quill", Application: true, Use: setup.UseCode}}
	page := fwire.ApplicationPage{Applications: []fwire.ApplicationEntry{
		{Descriptor: fwire.ApplicationDescriptor{Name: "quill", Title: "Quill — now with ads", Program: `C:\Quill\quill.exe`}}}}
	if apps := stepApps(page, choices, ""); len(apps) != 1 || apps[0].Title != "Quill" {
		t.Fatalf("apps = %+v", apps)
	}
}

// The answer connects an installed app by its manifest and asks nothing: an
// app that added itself waits until the person acts on it in Apps.
func TestSetupStepConfirmConnectsInstalledAppsAndAsksNothing(t *testing.T) {
	var pages []string
	register := func(_ context.Context, manifest string) { pages = append(pages, manifest) }
	pending := []setup.StepApp{{Name: "quill", Title: "Quill Editor", Manifest: `C:\apps\quill\application-manifest.json`},
		{Name: "easel", Title: "Easel Studio", Unconfirmed: true}}
	stepConfirm(context.Background(), pending, register)
	if len(pages) != 1 || pages[0] != pending[0].Manifest {
		t.Fatalf("connected %v", pages)
	}
}
