package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWindowsActivationUsesOnlyOwnedInstalledPaths(t *testing.T) {
	absent := exitForStartTest(t, 1060)
	running := exitForStartTest(t, 1056)
	denied := exitForStartTest(t, 5)
	upgrading := exitForStartTest(t, 3)
	root := t.TempDir()
	sibling := filepath.Join(root, "jobdw.exe")
	if err := os.WriteFile(sibling, nil, 0600); err != nil {
		t.Fatal(err)
	}
	sc := `C:\Windows\System32\sc.exe`
	check := []string{sibling, "service", "upgrade-check"}
	for _, mode := range []string{"service", "running", "sibling", "denied", "missing", "upgrading"} {
		t.Run(mode, func(t *testing.T) {
			var commands [][]string
			run := func(ctx context.Context, path string, args ...string) error {
				commands = append(commands, append([]string{path}, args...))
				switch len(commands) {
				case 1:
					if mode == "upgrading" {
						return upgrading
					}
				case 2:
					if mode == "sibling" {
						return absent
					}
					if mode == "denied" {
						return denied
					}
				case 3:
					if mode == "running" {
						return running
					}
				}
				return nil
			}
			stat := os.Stat
			if mode == "missing" {
				stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			}
			err := activateWindows(context.Background(), "OpenAbstractionsSupervisor_123", sc, run, func() (string, error) { return filepath.Join(root, "openabstractions.exe"), nil }, stat)
			switch mode {
			case "missing":
				if err == nil || len(commands) != 0 {
					t.Fatal(err, commands)
				}
				return
			case "upgrading":
				// The exclusion refuses before the service manager or sibling is asked.
				if err == nil || !strings.Contains(err.Error(), "upgrade of this installation is in progress") || !reflect.DeepEqual(commands, [][]string{check}) {
					t.Fatal(err, commands)
				}
				return
			case "denied":
				if err == nil || len(commands) != 2 {
					t.Fatal(err, commands)
				}
				return
			}
			if err != nil || len(commands) != 3 || !reflect.DeepEqual(commands[0], check) || !reflect.DeepEqual(commands[1], []string{sc, "query", "OpenAbstractionsSupervisor_123"}) {
				t.Fatal(err, commands)
			}
			want := []string{sc, "start", "OpenAbstractionsSupervisor_123"}
			if mode == "sibling" {
				want = []string{sibling, "start", "--runtime", "--require-unelevated"}
			}
			if !reflect.DeepEqual(commands[2], want) {
				t.Fatal(commands)
			}
		})
	}
}
