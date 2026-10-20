//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// stateAliases names the existing installed state folder by a symbolic link,
// the alias this platform offers.
func stateAliases(t *testing.T, installed string) map[string]string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(installed, link); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"symbolic link": link}
}

// shareAliases is empty: share paths are a Windows spelling.
func shareAliases(t *testing.T, folder string) map[string]string { return nil }

// absentStateAliases builds a state directory that becomes the installed
// runtime's state folder once created: the installed folder's parent is a
// symbolic link into the isolated state's parent.
func absentStateAliases(t *testing.T, installed string) map[string]string {
	t.Helper()
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(installed)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Dir(installed)); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"parent link into the isolated state": filepath.Join(elsewhere, filepath.Base(installed))}
}
