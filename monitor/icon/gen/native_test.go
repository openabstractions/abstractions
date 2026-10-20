package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeResourcesMatchCanonicalSources(t *testing.T) {
	panelDir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	_, monitorDir, err := layout(panelDir)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(monitorDir, "panel-application.json"))
	if err != nil || !bytes.Equal(manifest, nativeManifest()) {
		t.Fatalf("native manifest freshness: %v", err)
	}
	want := map[string]bool{}
	for _, name := range nativeSDKFiles {
		want[filepath.FromSlash(name)] = true
		source, err := os.ReadFile(filepath.Join(filepath.Dir(monitorDir), "openabstractions-flat", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := os.ReadFile(filepath.Join(monitorDir, "static/native-sdk", filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(source, copied) {
			t.Fatalf("canonical SDK bytes differ: %s: %v", name, err)
		}
	}
	root := filepath.Join(monitorDir, "static/native-sdk")
	err = filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || !want[rel] {
			t.Fatalf("unselected native SDK resource %s", rel)
		}
		delete(want, rel)
		return nil
	})
	if err != nil || len(want) != 0 {
		t.Fatalf("native SDK resource closure: %v, missing %v", err, want)
	}
	rc, err := resourceScript("0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(monitorDir, "panel.rc"))
	if err != nil || !bytes.Equal(current, rc) {
		t.Fatalf("native resource script freshness: %v", err)
	}
	for _, label := range []string{`"FileDescription",  "OpenAbstractions Panel"`, `"ProductName",      "OpenAbstractions Panel"`, `"OriginalFilename", "Abstraction Panel.exe"`} {
		if !strings.Contains(string(rc), label) {
			t.Fatalf("native display name or executable identity missing: %s", label)
		}
	}
}

func TestResourceLayout(t *testing.T) {
	panel := filepath.Join(t.TempDir(), "monitor")
	if err := os.Mkdir(panel, 0755); err != nil {
		t.Fatal(err)
	}
	trimmed := filepath.Join("github.com", "openabstractions", "abstractions", "monitor", "icon", "gen", "main.go")
	icon, selected, err := resourceLayout(panel, trimmed)
	if err != nil || selected != panel || icon != filepath.Join(panel, "icon") {
		t.Fatalf("explicit source with trimmed caller: %s %s %v", icon, selected, err)
	}
	_, fallback, err := resourceLayout("", filepath.Join(panel, "icon", "gen", "main.go"))
	if err != nil || fallback != panel {
		t.Fatalf("untrimmed source compatibility: %s %v", fallback, err)
	}
	for _, source := range []string{"", "relative-panel", filepath.Join(panel, "missing")} {
		_, _, err := resourceLayout(source, trimmed)
		if err == nil || !strings.Contains(err.Error(), "-panel-dir") {
			t.Fatalf("invalid source must refuse with explicit-source guidance: %q: %v", source, err)
		}
	}
	entries, err := os.ReadDir(panel)
	if err != nil || len(entries) != 0 {
		t.Fatalf("layout admission wrote resources: %v %v", entries, err)
	}
}
