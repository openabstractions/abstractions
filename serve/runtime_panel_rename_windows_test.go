//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Final security check 2026-11-22, F1: the runtime holds the installed Panel
// open with delete sharing, and a per-user install folder is the person's to
// write. Moving the installed Panel away and putting a copy at its path must
// not leave the moved original admitted as the Panel: the Panel is the file
// held open and currently at the installed path.
func TestMovedPanelIsNotThePanel(t *testing.T) {
	tools := t.TempDir()
	runtimeExe := filepath.Join(tools, "openabstractions.exe")
	panel := filepath.Join(tools, "Abstraction Panel.exe")
	for _, file := range []string{runtimeExe, panel} {
		if err := os.WriteFile(file, []byte(filepath.Base(file)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, panels := operatorSiblings(runtimeExe, "")
	r := &runtimeRights{panels: panels}
	t.Cleanup(r.close)
	if !r.isPanel(panel) {
		t.Fatal("the installed Panel was not admitted")
	}
	moved := filepath.Join(t.TempDir(), "Abstraction Panel.exe")
	if err := os.Rename(panel, moved); err != nil {
		t.Fatal("the held Panel could not be moved:", err)
	}
	if err := os.WriteFile(panel, []byte("a copy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if r.isPanel(moved) {
		t.Fatal("the moved original was admitted as the Panel")
	}
	if r.isPanel(panel) {
		t.Fatal("the copy at the installed path was admitted as the Panel")
	}
}
