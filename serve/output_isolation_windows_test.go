package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeSettingsIsolationWorkingStdoutDoesNotResolveAccountLog(t *testing.T) {
	if file := outputForState(os.Stdout, func() (string, error) { t.Fatal("working stdout consulted host log path"); return "", nil }); file != nil {
		file.Close()
		t.Fatal("working stdout replaced")
	}
	closed, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	path := filepath.Join(t.TempDir(), "private", "host.log")
	file := outputForState(closed, func() (string, error) { return path, nil })
	if file == nil {
		t.Fatal("private fallback missing")
	}
	file.Close()
}
