package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The presenter's resource script names the process "OpenAbstractions" in
// Task Manager, keeps the Panel's icon and native manifest, carries no
// application manifest, and the committed script is this generator's output
// for the Panel's selected version.
func TestPresenterResourceScriptNamesOpenAbstractions(t *testing.T) {
	panel, err := os.ReadFile(filepath.Join("..", "..", "panel.rc"))
	if err != nil {
		t.Fatal(err)
	}
	version := regexp.MustCompile(`VALUE "FileVersion",\s*"([0-9]+\.[0-9]+\.[0-9]+)\.0"`).FindSubmatch(panel)
	if version == nil {
		t.Fatal("panel.rc names no version")
	}
	want, err := presenterResourceScript(string(version[1]))
	if err != nil {
		t.Fatal(err)
	}
	script := string(want)
	for _, line := range []string{`VALUE "FileDescription",  "OpenAbstractions"`, `VALUE "OriginalFilename", "OpenAbstractions Presenter.exe"`, `1 ICON "icon/panel.ico"`, `1 24 "panel.manifest"`} {
		if !strings.Contains(script, line) {
			t.Errorf("presenter.rc lacks %s", line)
		}
	}
	if strings.Contains(script, "OA_APPLICATION_MANIFEST") {
		t.Error("the presenter carries the Panel's application manifest")
	}
	committed, err := os.ReadFile(filepath.Join("..", "..", "presenter.rc"))
	if err != nil || !bytes.Equal(committed, want) {
		t.Fatal("monitor/presenter.rc differs from the generator; run -resources -syso", err)
	}
}
