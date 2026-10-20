package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
	"os"
	"path/filepath"
	"testing"
)

type codexVendorFixture struct {
	entry  *codexEntry
	writes int
	lost   bool
	hooks  *codexHookVendorFixture
}

func (f *codexVendorFixture) run(_ context.Context, _ string, _ string, args []string) (codexCLIResult, error) {
	if args[0] == "plugin" {
		if f.hooks == nil {
			return codexCLIResult{}, errors.New("plugin fixture not configured")
		}
		return f.hooks.run(args)
	}
	if len(args) == 1 && args[0] == "--version" {
		return codexCLIResult{Output: []byte("codex-cli 0.164.0-alpha.11.1\n")}, nil
	}
	switch args[1] {
	case "get":
		if f.entry == nil {
			return codexCLIResult{Code: 1, Diagnostic: []byte("Error: No MCP server named '" + args[2] + "' found.\n")}, nil
		}
		raw, _ := json.Marshal(f.entry)
		return codexCLIResult{Output: raw}, nil
	case "add":
		f.writes++
		f.entry = &codexEntry{Name: args[2], Enabled: true, Transport: codexTransport{Type: "stdio", Command: args[4], Args: args[5:], EnvVars: []string{}}}
	case "remove":
		f.writes++
		f.entry = nil
	default:
		return codexCLIResult{}, errors.New("unexpected command")
	}
	if f.lost {
		return codexCLIResult{}, errors.New("lost reply")
	}
	return codexCLIResult{}, nil
}
func codexFixture(t *testing.T) (codexSetupOptions, string, string, *codexVendorFixture) {
	t.Helper()
	old, self, _ := claudeSetupFixture(t)
	home := t.TempDir()
	return codexSetupOptions{action: "connect", state: old.state, cli: old.cli, name: "openabstractions"}, self, home, &codexVendorFixture{hooks: &codexHookVendorFixture{home: home, server: "openabstractions"}}
}
func TestCodexSetupOwnsOnlyExactVendorUserEntry(t *testing.T) {
	options, self, home, vendor := codexFixture(t)
	ctx := context.Background()
	page, err := applyCodexSetup(ctx, options, self, home, vendor.run)
	if err != nil || page.Registration != "saved" || page.Scope != "user" || vendor.writes != 1 {
		t.Fatal(page, err, vendor.writes)
	}
	if _, err := applyCodexSetup(ctx, options, self, home, vendor.run); err != nil || vendor.writes != 1 {
		t.Fatal("idempotence", err, vendor.writes)
	}
	vendor.entry.Transport.Env = map[string]string{"foreign": "value"}
	options.action = "disconnect"
	if _, err := applyCodexSetup(ctx, options, self, home, vendor.run); err == nil || vendor.writes != 1 {
		t.Fatal("changed entry removed", err, vendor.writes)
	}
	vendor.entry.Transport.Env = nil
	if _, err := applyCodexSetup(ctx, options, self, home, vendor.run); err != nil || vendor.writes != 2 || vendor.entry != nil {
		t.Fatal("remove", err, vendor.writes)
	}
}

// Factory reset --all moves OA's private setup state while deliberately leaving
// the vendor's external registration untouched. Missing ownership cannot be
// reconstructed from equal configuration, even if a helper is recreated.
func TestCodexSetupAllResetLeavesExternalEntryUnowned(t *testing.T) {
	options, self, home, vendor := codexFixture(t)
	if _, err := applyCodexSetup(context.Background(), options, self, home, vendor.run); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(options.state, options.state+".reset-backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(options.state, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := applyCodexSetup(context.Background(), options, self, home, vendor.run); err == nil || vendor.writes != 1 || vendor.entry == nil {
		t.Fatal("reset lost-marker entry adopted or changed", err, vendor.writes)
	}
}
func TestCodexSetupDoesNotAdoptEqualUnownedOrLostReply(t *testing.T) {
	options, self, home, vendor := codexFixture(t)
	vendor.lost = true
	if _, err := applyCodexSetup(context.Background(), options, self, home, vendor.run); err == nil || vendor.entry == nil {
		t.Fatal("lost reply became success")
	}
	vendor.lost = false
	if _, err := applyCodexSetup(context.Background(), options, self, home, vendor.run); err == nil || vendor.writes != 1 {
		t.Fatal("equal unowned entry adopted")
	}
	options.action = "disconnect"
	if _, err := applyCodexSetup(context.Background(), options, self, home, vendor.run); err == nil || vendor.writes != 1 {
		t.Fatal("unowned entry removed")
	}
}
func TestCodexSetupRemovalNeverOpensDeletedTarget(t *testing.T) {
	options, self, home, vendor := codexFixture(t)
	if _, err := applyCodexSetup(context.Background(), options, self, home, vendor.run); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(self); err != nil {
		t.Fatal(err)
	}
	manager := filepath.Join(t.TempDir(), "manager.exe")
	if err := os.WriteFile(manager, []byte("manager"), 0600); err != nil {
		t.Fatal(err)
	}
	options.action = "disconnect"
	options.registeredProgram = self
	if _, err := applyCodexSetup(context.Background(), options, manager, home, vendor.run); err != nil || vendor.entry != nil {
		t.Fatal(err)
	}
}
func TestCodexSetupScopeAndUnknownGrammarRefuse(t *testing.T) {
	options, self, home, vendor := codexFixture(t)
	if _, err := applyCodexSetup(context.Background(), options, self, home, vendor.run); err != nil {
		t.Fatal(err)
	}
	options.action = "disconnect"
	if _, err := applyCodexSetup(context.Background(), options, self, t.TempDir(), vendor.run); err == nil || vendor.writes != 1 {
		t.Fatal("other scope removed entry")
	}
	if _, err := parseCodexSetup([]string{"connect", "codex", "--project", home}, os.Stderr); err == nil {
		t.Fatal("project isolation invented")
	}
}

func TestCodexSetupSelectedHomeAndBrandPreserveOldAlias(t *testing.T) {
	options, self, home, vendor := codexFixture(t)
	t.Setenv("CODEX_HOME", home)
	selected, err := codexConfigurationHome()
	if err != nil || selected != home {
		t.Fatal(selected, err)
	}
	seen := map[string]bool{}
	run := func(ctx context.Context, exe, directory string, args []string) (codexCLIResult, error) {
		if actual, _ := codexConfigurationHome(); actual != home {
			t.Fatal("scope changed")
		}
		if len(args) > 1 {
			seen[args[1]] = true
		}
		return vendor.run(ctx, exe, directory, args)
	}
	if _, err := applyCodexSetup(context.Background(), options, self, selected, run); err != nil {
		t.Fatal(err)
	}
	options.action = "disconnect"
	if _, err := applyCodexSetup(context.Background(), options, self, selected, run); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"get", "add", "remove"} {
		if !seen[operation] {
			t.Fatal(operation)
		}
	}
	parsed, err := parseCodexSetup([]string{"connect", "codex", "--state-dir", options.state, "--codex", options.cli}, os.Stderr)
	if err != nil || parsed.name != "OpenAbstractions" {
		t.Fatal(parsed, err)
	}
	parsed, err = parseCodexSetup([]string{"disconnect", "codex", "--state-dir", options.state, "--codex", options.cli, "--name", "openabstractions"}, os.Stderr)
	if err != nil || parsed.name != "openabstractions" {
		t.Fatal("old alias lost", parsed, err)
	}
}

func TestCodexSetupEveryInvocationRechecksImageAndMarker(t *testing.T) {
	for _, which := range []string{"version-self", "first-get-cli", "last-get-self", "last-get-marker", "last-get-empty-marker"} {
		t.Run(which, func(t *testing.T) {
			options, self, home, vendor := codexFixture(t)
			gets := 0
			scope, err := fileid.Resolve(context.Background(), fileid.Self, home, fileid.Options{Links: fileid.RefuseLinks})
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256([]byte(scope.Key() + "\x00" + options.name))
			marker := filepath.Join(options.state, "codex-setup-"+hex.EncodeToString(hash[:])+".json")
			run := func(ctx context.Context, program, directory string, args []string) (codexCLIResult, error) {
				result, err := vendor.run(ctx, program, directory, args)
				if len(args) == 1 && args[0] == "--version" && which == "version-self" {
					replaceClaudeFixtureImage(t, self)
				}
				if len(args) > 1 && args[1] == "get" {
					gets++
					if gets == 1 && which == "first-get-cli" {
						replaceClaudeFixtureImage(t, options.cli)
					}
					if gets == 2 {
						switch which {
						case "last-get-self":
							replaceClaudeFixtureImage(t, self)
						case "last-get-marker":
							if err := os.WriteFile(marker, []byte("changed"), 0600); err != nil {
								t.Fatal(err)
							}
						case "last-get-empty-marker":
							if err := os.WriteFile(marker, []byte{}, 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				return result, err
			}
			wantGets := 2
			if which == "version-self" {
				wantGets = 0
			}
			if which == "first-get-cli" {
				wantGets = 1
			}
			if _, err := applyCodexSetup(context.Background(), options, self, home, run); err == nil || vendor.writes != 0 || gets != wantGets {
				t.Fatalf("%s reached another command/mutation: gets=%d want=%d writes=%d err=%v", which, gets, wantGets, vendor.writes, err)
			}
		})
	}
}
