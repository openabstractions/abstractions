//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestActualLifecycleStopsEveryOwnedJSONRow(t *testing.T) {
	script, err := filepath.Abs("Lifecycle.test.ps1")
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-Run", "-Dir", t.TempDir()).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "PASS: actual lifecycle stopped both") {
		t.Fatalf("actual process ownership regression: %v\n%s", err, output)
	}
}
