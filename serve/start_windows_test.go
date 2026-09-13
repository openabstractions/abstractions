package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWindowsActivationUsesOnlyOwnedInstalledPaths(t *testing.T) {
	absent := exitForStartTest(t, 1060)
	running := exitForStartTest(t, 1056)
	denied := exitForStartTest(t, 5)
	root := t.TempDir()
	sibling := filepath.Join(root, "jobdw.exe")
	if err := os.WriteFile(sibling, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"service", "running", "sibling", "denied", "missing"} {
		t.Run(mode, func(t *testing.T) {
			var commands [][]string
			run := func(ctx context.Context, path string, args ...string) error {
				commands = append(commands, append([]string{path}, args...))
				if len(commands) == 1 {
					if mode == "sibling" || mode == "missing" {
						return absent
					}
					if mode == "denied" {
						return denied
					}
				}
				if mode == "running" && len(commands) == 2 {
					return running
				}
				return nil
			}
			stat := os.Stat
			if mode == "missing" {
				stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			}
			err := activateWindows(context.Background(), "OpenAbstractionsSupervisor_123", `C:\Windows\System32\sc.exe`, run, func() (string, error) { return filepath.Join(root, "openabstractions.exe"), nil }, stat)
			if mode == "missing" || mode == "denied" {
				if err == nil || len(commands) != 1 {
					t.Fatal(err, commands)
				}
				return
			}
			if err != nil || len(commands) != 2 {
				t.Fatal(err, commands)
			}
			want := []string{`C:\Windows\System32\sc.exe`, "start", "OpenAbstractionsSupervisor_123"}
			if mode == "sibling" {
				want = []string{sibling, "start", "--runtime", "--require-unelevated"}
			}
			if !reflect.DeepEqual(commands[1], want) {
				t.Fatal(commands)
			}
		})
	}
}
