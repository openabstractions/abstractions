package cacheproducer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

type companionBackupFixture struct {
	prior, current, goal     []byte
	old, new                 companionDeclaration
	vendor                   *cachedClaudePluginFixture
	self, oldImage, newImage string
}

func backupClaudeFixture(t *testing.T) companionBackupFixture {
	t.Helper()
	config, o, self, vendor := installedCopyFixture(t, true)
	ctx := t.Context()
	image, _, err := setupIdentityClosed(ctx, self, false)
	if err != nil {
		t.Fatal(err)
	}
	template := claudePluginModule
	claudePluginModule = append(append([]byte(nil), template...), []byte("\n// predecessor\n")...)
	carrier := filepath.Join(o.State, "claude-setup-declared.json")
	decl, err := DesiredCompanion(ctx, CompanionSelection{Host: "claude", Requirement: "required", State: o.State, CLI: o.CLI, Project: o.Project, Scope: o.Scope, Name: "fixture"}, carrier, self, image.Key())
	claudePluginModule = template
	if err != nil {
		t.Fatal(err)
	}
	declaredCarrier(t, carrier, self, decl)
	aggregate, err := ReadCompanionDeclarationInConfig(ctx, config, self, image.Key())
	if err != nil {
		t.Fatal(err)
	}
	prior, err := captureCompanionBackup(ctx, aggregate, strings.Repeat("a", 48), func() error { return nil }, vendor.run, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldImage, newImage := self+".old", self+".new"
	if err := os.Rename(self, oldImage); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self, []byte("new helper generation; never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	next, _, err := setupIdentityClosed(ctx, self, false)
	if err != nil {
		t.Fatal(err)
	}
	o.Action = "update"
	if err := ApplyClaude(ctx, o, self, vendor.run); err != nil {
		t.Fatal(err)
	}
	newDecl, err := DesiredCompanion(ctx, CompanionSelection{Host: "claude", Requirement: "required", State: o.State, CLI: o.CLI, Project: o.Project, Scope: o.Scope, Name: "fixture"}, carrier, self, next.Key())
	if err != nil {
		t.Fatal(err)
	}
	declaredCarrier(t, carrier, self, newDecl)
	newAggregate, err := ReadCompanionDeclarationInConfig(ctx, config, self, next.Key())
	if err != nil {
		t.Fatal(err)
	}
	current, err := captureCompanionBackup(ctx, newAggregate, strings.Repeat("b", 48), func() error { return nil }, vendor.run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(self, newImage); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldImage, self); err != nil {
		t.Fatal(err)
	}
	goal, err := RebindCompanionDeclaration(ctx, aggregate, self, image.Key())
	if err != nil {
		t.Fatal(err)
	}
	old, _ := parseCompanion(aggregate)
	newer, _ := parseCompanion(newAggregate)
	return companionBackupFixture{prior: prior, current: current, goal: goal, old: old, new: newer, vendor: vendor, self: self, oldImage: oldImage, newImage: newImage}
}
func mutationCount(v *cachedClaudePluginFixture) int {
	n := 0
	for _, args := range v.v.calls {
		if len(args) > 1 && args[0] == "plugin" && (args[1] == "update" || args[1] == "install" || args[1] == "uninstall") {
			n++
		}
	}
	return n
}

func TestCompanionBackupRetainsPriorBytesAndRestoresWithoutOldHelperExecution(t *testing.T) {
	f := backupClaudeFixture(t)
	prior, _, err := parseCompanionBackup(f.prior)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := parseCompanionBackup(f.current)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(prior.Members[0].Files["plugin/hooks/register.js"], current.Members[0].Files["plugin/hooks/register.js"]) {
		t.Fatal("fixture did not overwrite predecessor source")
	}
	if err := restoreCompanionBackup(t.Context(), f.prior, f.current, f.goal, func() error { return nil }, f.vendor.run, nil); err != nil {
		t.Fatal(err)
	}
	for name, want := range prior.Members[0].Files {
		body, err := os.ReadFile(filepath.Join(f.old.Members[0].Root, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(body, want) {
			t.Fatal("prior exact bytes not restored", name, err)
		}
	}
	before := mutationCount(f.vendor)
	if err := restoreCompanionBackup(t.Context(), f.prior, f.current, f.goal, func() error { return nil }, f.vendor.run, nil); err != nil || mutationCount(f.vendor) != before {
		t.Fatal("completed restore repeated vendor mutation", err)
	}
	if reading, err := observeCompanionDeclaration(t.Context(), f.goal, f.vendor.run, nil); err != nil || reading.State != CopyCurrent {
		t.Fatal(reading, err)
	}
}

func TestCompanionBackupRestoreResumesPartialSourceAndLostVendorReply(t *testing.T) {
	for _, edge := range []string{"partial", "lost-reply"} {
		t.Run(edge, func(t *testing.T) {
			f := backupClaudeFixture(t)
			prior, _, _ := parseCompanionBackup(f.prior)
			current, _, _ := parseCompanionBackup(f.current)
			stopped := false
			authorize := func() error {
				if edge == "partial" && !stopped {
					old, new := false, false
					for name, body := range prior.Members[0].Files {
						if bytes.Equal(body, current.Members[0].Files[name]) {
							continue
						}
						actual, _ := os.ReadFile(filepath.Join(f.old.Members[0].Root, filepath.FromSlash(name)))
						old = old || bytes.Equal(actual, body)
						new = new || bytes.Equal(actual, current.Members[0].Files[name])
					}
					if old && new {
						stopped = true
						return errors.New("inert interruption")
					}
				}
				return nil
			}
			if edge == "lost-reply" {
				f.vendor.lost = true
			}
			if err := restoreCompanionBackup(t.Context(), f.prior, f.current, f.goal, authorize, f.vendor.run, nil); err == nil {
				t.Fatal("incomplete restore claimed success")
			}
			f.vendor.lost = false
			before := mutationCount(f.vendor)
			if err := restoreCompanionBackup(t.Context(), f.prior, f.current, f.goal, func() error { return nil }, f.vendor.run, nil); err != nil {
				t.Fatal(err)
			}
			if edge == "lost-reply" && mutationCount(f.vendor) != before {
				t.Fatal("uncertain vendor call repeated after positive old-version readback")
			}
		})
	}
}

func TestCompanionBackupRestorePreservesForeignEditsAndRevocation(t *testing.T) {
	for _, edge := range []string{"person-source", "person-receipt", "person-native", "person-old-cache", "revoked", "wrong-helper"} {
		t.Run(edge, func(t *testing.T) {
			f := backupClaudeFixture(t)
			root := f.old.Members[0].Root
			target := filepath.Join(root, "plugin", "README.md")
			if edge == "person-source" {
				if err := os.WriteFile(target, []byte("person edit"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if edge == "person-receipt" {
				if err := os.WriteFile(root+".json", []byte(`{"person":"changed"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if edge == "wrong-helper" {
				if err := os.WriteFile(f.self, []byte("changed helper bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if edge == "person-native" {
				f.vendor.v.scope = "user"
			}
			if edge == "person-old-cache" {
				prior, _, _ := parseCompanionBackup(f.prior)
				cache, err := fileid.ParseStoredFileID(prior.Members[0].CacheID)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cache.Path(), "README.md"), []byte("person edit in inactive old cache"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(target)
			mutations := mutationCount(f.vendor)
			err := restoreCompanionBackup(t.Context(), f.prior, f.current, f.goal, func() error {
				if edge == "revoked" {
					return errors.New("revoked")
				}
				return nil
			}, f.vendor.run, nil)
			after, _ := os.ReadFile(target)
			if err == nil || !bytes.Equal(before, after) || mutationCount(f.vendor) != mutations {
				t.Fatal("refused restoration changed data", err)
			}
		})
	}
}

func TestCompanionBackupRestoresPriorRecipeWhenVendorCacheWasRemoved(t *testing.T) {
	f := backupClaudeFixture(t)
	prior, _, _ := parseCompanionBackup(f.prior)
	stored, err := fileid.ParseStoredFileID(prior.Members[0].CacheID)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(f.vendor.home, stored.Path())
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
		t.Fatal("fixture cache outside owned temp root")
	}
	if err := os.RemoveAll(stored.Path()); err != nil {
		t.Fatal(err)
	}
	if err := restoreCompanionBackup(t.Context(), f.prior, f.current, f.goal, func() error { return nil }, f.vendor.run, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCompanionBackupRefusesUnknownNativeCredentialMetadata(t *testing.T) {
	config, o, self, vendor := installedCopyFixture(t, false)
	image, _, err := setupIdentityClosed(t.Context(), self, false)
	if err != nil {
		t.Fatal(err)
	}
	carrier := filepath.Join(o.State, "claude-setup-declared.json")
	decl, err := DesiredCompanion(t.Context(), CompanionSelection{Host: "claude", Requirement: "required", State: o.State, CLI: o.CLI, Project: o.Project, Scope: o.Scope, Name: "fixture"}, carrier, self, image.Key())
	if err != nil {
		t.Fatal(err)
	}
	declaredCarrier(t, carrier, self, decl)
	aggregate, err := ReadCompanionDeclarationInConfig(t.Context(), config, self, image.Key())
	if err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, p, dir string, args []string) ([]byte, error) {
		raw, err := vendor.run(ctx, p, dir, args)
		if reflect.DeepEqual(args, []string{"plugin", "list", "--json"}) {
			rows, e := PluginList(raw)
			if e != nil {
				t.Fatal(e)
			}
			rows[0]["credential"] = json.RawMessage(`"never-retain-this"`)
			raw = PluginJSON(rows)
		}
		return raw, err
	}
	raw, err := captureCompanionBackup(t.Context(), aggregate, strings.Repeat("a", 48), func() error { return nil }, run, nil)
	if err == nil || raw != nil {
		t.Fatal("unknown vendor metadata captured", err)
	}
}

func TestCompanionBackupCodexRebindsActualRestoredHelper(t *testing.T) {
	config, home := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	state := filepath.Join(config, "codex-connections")
	if err := privatedir.Make(state); err != nil {
		t.Fatal(err)
	}
	self, cli := filepath.Join(t.TempDir(), "helper.exe"), filepath.Join(t.TempDir(), "cli.exe")
	for _, path := range []string{self, cli} {
		if err := os.WriteFile(path, []byte("inert original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	image, _, err := setupIdentityClosed(t.Context(), self, false)
	if err != nil {
		t.Fatal(err)
	}
	carrier := filepath.Join(state, "codex-setup-declared.json")
	selection := CompanionSelection{Host: "codex", Requirement: "required", State: state, CLI: cli, Name: "OpenAbstractions"}
	decl, err := DesiredCompanion(t.Context(), selection, carrier, self, image.Key())
	if err != nil {
		t.Fatal(err)
	}
	declaredCarrier(t, carrier, self, decl)
	vendor := &copyHookVendor{home: home}
	status := ""
	options := CodexOptions{Action: "connect", State: state, CLI: cli, Name: "OpenAbstractions", Program: self, FileKey: image.Key()}
	if err := ApplyCodexStatus(t.Context(), options, home, vendor.run, &status); err != nil {
		t.Fatal(err)
	}
	prior, err := captureCompanionBackup(t.Context(), decl, strings.Repeat("a", 48), func() error { return nil }, nil, vendor.run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(self, self+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self, []byte("inert new"), 0600); err != nil {
		t.Fatal(err)
	}
	newImage, _, err := setupIdentityClosed(t.Context(), self, false)
	if err != nil {
		t.Fatal(err)
	}
	options.FileKey = newImage.Key()
	if err := ApplyCodexStatus(t.Context(), options, home, vendor.run, &status); err != nil {
		t.Fatal(err)
	}
	next, err := DesiredCompanion(t.Context(), selection, carrier, self, newImage.Key())
	if err != nil {
		t.Fatal(err)
	}
	declaredCarrier(t, carrier, self, next)
	current, err := captureCompanionBackup(t.Context(), next, strings.Repeat("b", 48), func() error { return nil }, nil, vendor.run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(self, self+".new"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(self+".old", self); err != nil {
		t.Fatal(err)
	}
	goal, err := RebindCompanionDeclaration(t.Context(), decl, self, image.Key())
	if err != nil {
		t.Fatal(err)
	}
	before := vendor.writes
	if err := restoreCompanionBackup(t.Context(), prior, current, goal, func() error { return nil }, nil, vendor.run); err != nil {
		t.Fatal(err)
	}
	if vendor.writes != before {
		t.Fatal("unchanged native recipe reinstalled")
	}
	if err := restoreCompanionBackup(t.Context(), prior, current, goal, func() error { return nil }, nil, vendor.run); err != nil {
		t.Fatal(err)
	}
}
