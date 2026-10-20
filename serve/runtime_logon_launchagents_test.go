package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
)

type macLogonFixture struct {
	ops                      macLogonOps
	home, bin, share, agents string
	selected                 bootstrap.Selection
	edits                    []string
}

func newMacLogonFixture(t *testing.T, panel bool) *macLogonFixture {
	t.Helper()
	f := &macLogonFixture{home: t.TempDir()}
	f.bin = filepath.Join(f.home, ".local", "bin")
	f.share = filepath.Join(f.home, ".local", "share", "abstraction")
	f.agents = filepath.Join(f.home, "Library", "LaunchAgents")
	for _, dir := range []string{f.bin, f.share, f.agents} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	put := func(path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0700); err != nil {
			t.Fatal(err)
		}
	}
	program := filepath.Join(f.bin, "openabstractions")
	put(program, []byte("installed runtime"))
	put(filepath.Join(f.share, "com.openabstractions.runtime-login.plist"), []byte(runtimeLoginTemplate))
	put(filepath.Join(f.share, "com.openabstractions.panel.plist"), []byte(panelLoginTemplate))
	var ledger string
	for _, name := range []string{"com.openabstractions.runtime-login.plist", "com.openabstractions.panel.plist"} {
		path := filepath.Join(f.agents, name)
		ledger += path + "\n" + path + ".lock\n"
	}
	put(filepath.Join(f.share, "MANIFEST"), []byte(ledger))
	if panel {
		path := filepath.Join(f.bin, "Abstraction Panel.app", "Contents", "MacOS", "Abstraction Panel")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		put(path, []byte("installed Panel"))
	}
	f.selected = bootstrap.Selection{Server: listen.ServerExpectation{Principal: identity.User{Kind: "posix", UID: 1000, GID: -1}, Program: program}}
	f.ops = macLogonOps{
		selection: func(context.Context) (bootstrap.Selection, error) { return f.selected, nil }, uid: func() int { return 1000 }, euid: func() int { return 1000 }, executable: func() (string, error) { return program, nil },
		open: func(ctx context.Context, path string) (fileid.Handle, error) {
			return fileid.Open(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks, Read: true})
		},
		owned: func(os.FileInfo, int) bool { return true },
		write: func(ctx context.Context, path string, before, after []byte, validate func() error) error {
			f.edits = append(f.edits, "write "+filepath.Base(path))
			return cas.ChangeLimitContext(ctx, path, 16384, func(current []byte) ([]byte, error) {
				if err := validate(); err != nil {
					return nil, err
				}
				if !bytes.Equal(current, before) || (current == nil) != (before == nil) {
					return nil, cas.ErrMoved
				}
				return after, nil
			})
		},
		remove: func(ctx context.Context, path string, expected []byte, validate func() error) error {
			if err := validate(); err != nil {
				return err
			}
			f.edits = append(f.edits, "remove "+filepath.Base(path))
			return os.Remove(path)
		},
	}
	return f
}

func TestMacLogonConditionalPresenceKeepsDemandAndVisibleWork(t *testing.T) {
	f := newMacLogonFixture(t, true)
	permanent := filepath.Join(f.agents, "com.openabstractions.runtime.plist")
	if err := os.WriteFile(permanent, []byte("already loaded demand registration"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true, true, false, false, true} {
		if err := reconcileMacLogon(context.Background(), enabled, f.ops); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"com.openabstractions.runtime-login.plist", "com.openabstractions.panel.plist"} {
			raw, err := os.ReadFile(filepath.Join(f.agents, name))
			if enabled {
				if err != nil || !bytes.Contains(raw, []byte(f.bin)) {
					t.Fatal(name, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal("trigger retained", name, err)
			}
		}
	}
	if raw, err := os.ReadFile(permanent); err != nil || string(raw) != "already loaded demand registration" {
		t.Fatal("permanent registration changed", err)
	}
	if len(f.edits) != 6 {
		t.Fatal("non-idempotent edit", f.edits)
	}
}

func TestMacLogonOptionalPanelAndPackageTemplateParity(t *testing.T) {
	f := newMacLogonFixture(t, false)
	if err := reconcileMacLogon(context.Background(), true, f.ops); err != nil {
		t.Fatal(err)
	}
	if len(f.edits) != 1 || !strings.Contains(f.edits[0], "runtime-login") {
		t.Fatal(f.edits)
	}
	templates := privateInput(t, "..", "installer", "posix", "macos")
	for i, want := range []string{runtimeLoginTemplate, panelLoginTemplate} {
		name := "com.openabstractions.runtime-login.plist"
		if i == 1 {
			name = "com.openabstractions.panel.plist"
		}
		raw, err := os.ReadFile(filepath.Join(templates, name))
		if err != nil || !bytes.Equal(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte(want)) {
			t.Fatal("package template differs", name, err)
		}
	}
	rendered := renderMacLogin(runtimeLoginTemplate, "/Users/A & <B>/bin")
	if !bytes.Contains(rendered, []byte("A &amp; &lt;B&gt;")) {
		t.Fatal(string(rendered))
	}
}

func TestMacLogonRefusesForeignUnsafeAndUnselectedBeforeMutation(t *testing.T) {
	for _, mode := range []string{"root", "uid", "override", "account", "process", "executable", "template", "foreign-trigger", "foreign-lock", "ledger", "owner", "directory", "link"} {
		t.Run(mode, func(t *testing.T) {
			f := newMacLogonFixture(t, true)
			switch mode {
			case "root":
				f.ops.euid = func() int { return 0 }
			case "uid":
				f.ops.uid = func() int { return 1001 }
			case "override":
				f.selected.EndpointFromEnvironment = true
			case "account":
				f.selected.Server.Principal.UID = 1001
			case "process":
				f.selected.Server.Process = &identity.Process{}
			case "executable":
				f.ops.executable = func() (string, error) { return filepath.Join(f.bin, "absent"), nil }
			case "template":
				if err := os.WriteFile(filepath.Join(f.share, "com.openabstractions.panel.plist"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-trigger":
				if err := os.WriteFile(filepath.Join(f.agents, "com.openabstractions.panel.plist"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-lock":
				if err := os.WriteFile(filepath.Join(f.agents, "com.openabstractions.panel.plist.lock"), []byte("foreign lock"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ledger":
				if err := os.WriteFile(filepath.Join(f.share, "MANIFEST"), []byte("other\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "owner":
				f.ops.owned = func(os.FileInfo, int) bool { return false }
			case "directory":
				if err := os.Mkdir(filepath.Join(f.agents, "com.openabstractions.panel.plist"), 0700); err != nil {
					t.Fatal(err)
				}
			case "link":
				path := filepath.Join(f.share, "com.openabstractions.panel.plist")
				target := filepath.Join(f.share, "other")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip("owned symlink unavailable:", err)
				}
			}
			if err := reconcileMacLogon(context.Background(), true, f.ops); err == nil {
				t.Fatal("accepted", mode)
			}
			if len(f.edits) != 0 {
				t.Fatal("mutated before refusal", f.edits)
			}
		})
	}
}

func TestMacLogonPartialFailureReversalAndCancellation(t *testing.T) {
	f := newMacLogonFixture(t, true)
	write := f.ops.write
	f.ops.write = func(ctx context.Context, path string, before, after []byte, validate func() error) error {
		if strings.HasSuffix(path, "com.openabstractions.panel.plist") {
			return errors.New("owned write failure")
		}
		return write(ctx, path, before, after, validate)
	}
	if err := reconcileMacLogon(context.Background(), true, f.ops); err == nil {
		t.Fatal("hidden partial failure")
	}
	f.ops.write = write
	if err := reconcileMacLogon(context.Background(), false, f.ops); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := len(f.edits)
	if err := reconcileMacLogon(ctx, true, f.ops); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(f.edits) != n {
		t.Fatal("canceled edit")
	}
}

func TestMacLogonRefusesChangedSelectionAndTriggerBeforeEdit(t *testing.T) {
	for _, mode := range []string{"selection", "trigger"} {
		t.Run(mode, func(t *testing.T) {
			f := newMacLogonFixture(t, true)
			calls := 0
			selection := f.ops.selection
			f.ops.selection = func(ctx context.Context) (bootstrap.Selection, error) {
				calls++
				if calls == 2 {
					if mode == "selection" {
						return bootstrap.Selection{}, errors.New("changed installation")
					}
					if err := os.WriteFile(filepath.Join(f.agents, "com.openabstractions.runtime-login.plist"), []byte("foreign arrival"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return selection(ctx)
			}
			if err := reconcileMacLogon(context.Background(), true, f.ops); err == nil {
				t.Fatal("accepted mutation")
			}
			if len(f.edits) != 0 {
				t.Fatal(f.edits)
			}
		})
	}
}
