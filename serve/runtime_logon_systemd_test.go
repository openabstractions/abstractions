package main

// The production systemd body is received with owned files and a mock manager;
// the platform adapter supplies Linux identity and the real command separately.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
)

type linuxLogonFixture struct {
	ops       linuxLogonOps
	selection bootstrap.Selection
	program   string
	commands  [][]string
}

func newLinuxLogonFixture(t *testing.T) *linuxLogonFixture {
	t.Helper()
	f := &linuxLogonFixture{program: filepath.Join(t.TempDir(), "openabstractions")}
	if err := os.WriteFile(f.program, []byte("owned installed executable"), 0700); err != nil {
		t.Fatal(err)
	}
	f.selection = bootstrap.Selection{Server: listen.ServerExpectation{Principal: identity.User{Kind: "posix", UID: 1000, GID: -1}, Program: f.program}}
	f.ops = linuxLogonOps{
		selection: func(context.Context) (bootstrap.Selection, error) { return f.selection, nil },
		uid:       func() int { return 1000 }, euid: func() int { return 1000 },
		executable: func() (string, error) { return f.program, nil },
		open: func(ctx context.Context, path string) (fileid.Handle, error) {
			return fileid.Open(ctx, fileid.Self, path, fileid.Options{})
		},
		run: func(ctx context.Context, path string, args ...string) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("manager has no bound")
			}
			f.commands = append(f.commands, append([]string{path}, args...))
			return nil
		},
	}
	return f
}

func TestLinuxLogonChangesOnlyLoginTriggerAndKeepsInstallation(t *testing.T) {
	f := newLinuxLogonFixture(t)
	for _, enabled := range []bool{false, true, false} {
		if err := reconcileLinuxLogon(context.Background(), enabled, f.ops); err != nil {
			t.Fatal(err)
		}
	}
	want := [][]string{{"/usr/bin/systemctl", "--user", "disable", "abstraction-runtime.service"}, {"/usr/bin/systemctl", "--user", "enable", "abstraction-runtime.service"}, {"/usr/bin/systemctl", "--user", "disable", "abstraction-runtime.service"}}
	if !reflect.DeepEqual(f.commands, want) {
		t.Fatal(f.commands)
	}
	if raw, err := os.ReadFile(f.program); err != nil || string(raw) != "owned installed executable" {
		t.Fatal("installation changed", err)
	}
}

func TestLinuxLogonRefusesUnselectedOrForeignInstallationBeforeManager(t *testing.T) {
	for _, mode := range []string{"root", "changed-uid", "override", "account", "process", "selection", "foreign", "directory", "missing"} {
		t.Run(mode, func(t *testing.T) {
			f := newLinuxLogonFixture(t)
			switch mode {
			case "root":
				f.ops.euid = func() int { return 0 }
			case "changed-uid":
				f.ops.uid = func() int { return 1001 }
			case "override":
				f.selection.EndpointFromEnvironment = true
			case "account":
				f.selection.Server.Principal.UID = 1001
			case "process":
				f.selection.Server.Process = &identity.Process{}
			case "selection":
				f.ops.selection = func(context.Context) (bootstrap.Selection, error) {
					return bootstrap.Selection{}, bootstrap.ErrNoTrustedInstallation
				}
			case "foreign":
				f.selection.Server.Program = filepath.Join(t.TempDir(), "other")
				if err := os.WriteFile(f.selection.Server.Program, nil, 0700); err != nil {
					t.Fatal(err)
				}
			case "directory":
				f.ops.executable = func() (string, error) { return filepath.Dir(f.program), nil }
			case "missing":
				f.selection.Server.Program += ".absent"
			}
			if err := reconcileLinuxLogon(context.Background(), true, f.ops); err == nil || len(f.commands) != 0 {
				t.Fatal("unsafe manager call", err, f.commands)
			}
		})
	}
}

func TestLinuxLogonUsesHeldFileIdentityForSelectedAlias(t *testing.T) {
	f := newLinuxLogonFixture(t)
	alias := filepath.Join(t.TempDir(), "selected-runtime")
	if err := os.Link(f.program, alias); err != nil {
		t.Fatal(err)
	}
	f.selection.Server.Program = alias
	if err := reconcileLinuxLogon(context.Background(), true, f.ops); err != nil || len(f.commands) != 1 {
		t.Fatal(err, f.commands)
	}
}

func TestLinuxLogonRechecksSelectionAndAccountAfterManager(t *testing.T) {
	for _, mode := range []string{"replacement", "account"} {
		t.Run(mode, func(t *testing.T) {
			f := newLinuxLogonFixture(t)
			run := f.ops.run
			f.ops.run = func(ctx context.Context, path string, args ...string) error {
				if err := run(ctx, path, args...); err != nil {
					return err
				}
				if mode == "replacement" {
					if err := os.Rename(f.program, f.program+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(f.program, []byte("replacement"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			if mode == "account" {
				changed := false
				f.ops.euid = func() int {
					if changed {
						return 1001
					}
					return 1000
				}
				f.ops.run = func(ctx context.Context, path string, args ...string) error {
					err := run(ctx, path, args...)
					changed = true
					return err
				}
			}
			if err := reconcileLinuxLogon(context.Background(), false, f.ops); err == nil || len(f.commands) != 1 {
				t.Fatal("changed installation reported success", err, f.commands)
			}
		})
	}
}

func TestLinuxLogonManagerFailureAndOriginalDeadlineRemainVisible(t *testing.T) {
	f := newLinuxLogonFixture(t)
	refused := errors.New("manager refused")
	f.ops.run = func(context.Context, string, ...string) error { return refused }
	if err := reconcileLinuxLogon(context.Background(), false, f.ops); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	want, _ := ctx.Deadline()
	f.ops.run = func(ctx context.Context, _ string, _ ...string) error {
		if got, _ := ctx.Deadline(); !got.Equal(want) {
			t.Error("manager escaped caller deadline")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	if err := reconcileLinuxLogon(ctx, true, f.ops); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
