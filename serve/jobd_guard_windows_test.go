package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstalledJobdGuardAsksTheSiblingJobd(t *testing.T) {
	upgrading := exitForStartTest(t, 3)
	failed := exitForStartTest(t, 1)
	root := t.TempDir()
	sibling := filepath.Join(root, "jobdw.exe")
	if err := os.WriteFile(sibling, nil, 0600); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(root, "folder")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	check := []string{sibling, "service", "upgrade-check"}
	for _, mode := range []string{"clear", "upgrading", "failed", "uninstalled", "directory", "denied"} {
		t.Run(mode, func(t *testing.T) {
			var commands [][]string
			run := func(ctx context.Context, path string, args ...string) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("upgrade check has no deadline")
				}
				commands = append(commands, append([]string{path}, args...))
				switch mode {
				case "upgrading":
					return upgrading
				case "failed":
					return failed
				}
				return nil
			}
			stat := os.Stat
			switch mode {
			case "uninstalled":
				stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			case "directory":
				stat = func(string) (os.FileInfo, error) { return os.Stat(folder) }
			case "denied":
				stat = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
			}
			err := installedJobdGuard(context.Background(), run, func() (string, error) { return filepath.Join(root, "openabstractions.exe"), nil }, stat)
			switch mode {
			case "clear":
				if err != nil || !reflect.DeepEqual(commands, [][]string{check}) {
					t.Fatal(err, commands)
				}
			case "upgrading":
				var exit *exitError
				if !errors.As(err, &exit) || exit.code != 3 || !strings.Contains(err.Error(), "serve jobd refused: an upgrade of this installation is in progress") || !reflect.DeepEqual(commands, [][]string{check}) {
					t.Fatal(err, commands)
				}
			case "failed":
				var exit *exitError
				if err == nil || errors.As(err, &exit) || len(commands) != 1 {
					t.Fatal(err, commands)
				}
			case "uninstalled":
				// No installed jobdw.exe beside this program: no installation to replace.
				if err != nil || len(commands) != 0 {
					t.Fatal(err, commands)
				}
			case "directory", "denied":
				if err == nil || len(commands) != 0 {
					t.Fatal(err, commands)
				}
			}
		})
	}
}
