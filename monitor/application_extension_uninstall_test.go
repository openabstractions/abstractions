package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// An extension's program lies in its host's folder (ComfyUI's custom_nodes),
// outside any Setup folder. Its installer records its manifest in the
// Applications record (installer/apps/extension.wxs), and its uninstaller
// starts the Panel without waiting, then deletes that record, the manifest
// and the program. The Panel reads the recorded program when the request
// arrives, so the removal still matches it after the files are gone, and the
// Apps entry is gone.
func TestAnExtensionInstallThenUninstallLeavesNoAppsEntry(t *testing.T) {
	host := filepath.Join(t.TempDir(), "ComfyUI", "custom_nodes")
	folder := filepath.Join(host, "comfyui-openabstractions")
	// The helper is named plainly here: an interpreter copy is held to
	// identity's own-copy rule, which this test does not exercise.
	program := filepath.Join(folder, "helper", "comfyui-helper.exe")
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("helper"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"name": "comfyui", "program": "{app}/helper/comfyui-helper.exe", "title": "ComfyUI",
		"start_guidance": "Start ComfyUI yourself. OpenAbstractions does not start it."})
	manifest := filepath.Join(folder, "application-manifest.json")
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	record := map[string]string{"comfyui": manifest}
	saved := installedRecords
	t.Cleanup(func() { installedRecords = saved })
	installedRecords = func() map[string]string { return record }

	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{},
		appRequests: applicationRequestsFor(t, nil), appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
	install, _, err := parseApplicationStart(manifest, "", "overview")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.applyApplicationRequest(context.Background(), install); err != nil {
		t.Fatal(err)
	}
	if apps.entries["comfyui"].Program != program {
		t.Fatalf("the extension is not connected: %+v", apps.entries)
	}

	uninstall, _, err := parseApplicationStart("", "comfyui", "overview")
	if err != nil {
		t.Fatal(err)
	}
	// The uninstaller deletes its record, manifest and program while the
	// Panel reaches the runtime.
	record = map[string]string{}
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}
	late, _, err := parseApplicationStart("", "comfyui", "overview")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.applyApplicationRequest(context.Background(), late); !errors.Is(err, errApplicationNotChanged) || apps.removes != 0 {
		t.Fatalf("a removal without the recorded program removed the entry: %v", err)
	}
	if err := p.applyApplicationRequest(context.Background(), uninstall); err != nil {
		t.Fatal(err)
	}
	if _, ok := apps.entries["comfyui"]; ok || apps.removes != 1 {
		t.Fatalf("the Apps entry outlived its uninstall: %+v", apps.entries)
	}
	if lines := p.appActivity.lines(); len(lines) != 1 || lines[0].Outcome != "Removed" {
		t.Fatalf("Activity: %+v", lines)
	}
}

// The node pack and ComfyUI's native host are one app (AD-02a): both name
// "comfyui", and the native host's applications@3 registration holds it. The
// pack's request is connected through that registration: Apps keeps one
// entry, the native one, and lists no Not connected row offering to replace
// it.
func TestThePacksRequestUnderItsNativeHostsNameIsOneEntry(t *testing.T) {
	native := fwire.ApplicationDescriptor{Name: "comfyui", Program: filepath.Join(t.TempDir(), "oa-backend", "launch-helper.exe"), Title: "ComfyUI — OA build"}
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"comfyui": native}}
	saved := nativeApplicationNamesRead
	t.Cleanup(func() { nativeApplicationNamesRead = saved })
	nativeApplicationNamesRead = func(context.Context) (map[string]bool, error) { return map[string]bool{"comfyui": true}, nil }
	folder := filepath.Join(t.TempDir(), "custom_nodes", "comfyui-openabstractions")
	if err := os.MkdirAll(filepath.Join(folder, "helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"name": "comfyui", "program": "{app}/helper/comfyui-helper.exe", "title": "ComfyUI"})
	manifest := filepath.Join(folder, "application-manifest.json")
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{},
		appRequests: applicationRequestsFor(t, nil), appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
	request, _, err := parseApplicationStart(manifest, "", "overview")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.applyApplicationRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(apps.entries) != 1 || apps.entries["comfyui"] != native || apps.removes != 0 {
		t.Fatalf("Apps: %+v", apps.entries)
	}
	if len(p.appRequests.recorded()) != 0 || len(pendingRows(t, p.handler(applicationTestKey))) != 0 {
		t.Fatal("the pack waits as Not connected beside its own native entry")
	}
	// A request recorded before the native host registered is dropped too.
	if err := p.appRequests.record("comfyui", manifest); err != nil {
		t.Fatal(err)
	}
	if rows := pendingRows(t, p.handler(applicationTestKey)); len(rows) != 0 || len(p.appRequests.recorded()) != 0 {
		t.Fatalf("Not connected rows: %+v", rows)
	}
}
