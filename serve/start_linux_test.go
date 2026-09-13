package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLinuxActivationRequiresInstalledUserUnit(t *testing.T) {
	home := t.TempDir()
	calls := 0
	run := func(_ context.Context, path string, args ...string) error {
		calls++
		if path != "/usr/bin/systemctl" || !reflect.DeepEqual(args, []string{"--user", "start", "abstraction-runtime.service"}) {
			t.Fatal(path, args)
		}
		return nil
	}
	if activateLinux(context.Background(), home, os.Stat, run) == nil || calls != 0 {
		t.Fatal("absent unit activated")
	}
	unit := filepath.Join(home, ".config/systemd/user/abstraction-runtime.service")
	if err := os.MkdirAll(filepath.Dir(unit), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := activateLinux(context.Background(), home, os.Stat, run); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
