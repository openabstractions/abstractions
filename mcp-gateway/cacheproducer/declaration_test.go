package cacheproducer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func declaredCarrier(t *testing.T, path, self string, raw []byte) {
	t.Helper()
	_, stored, err := setupIdentityClosed(t.Context(), self, false)
	if err != nil {
		t.Fatal(err)
	}
	body := PluginJSON(map[string]any{"Self": stored, "Entry": map[string]any{"type": "stdio", "command": self, "args": []string{}, "env": map[string]string{}}, CompanionDeclarationField: json.RawMessage(raw)})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCompanionExplicitNoneAndMissingDeclaration(t *testing.T) {
	o, self, _ := cachedClaudePlugin(t)
	config := t.TempDir()
	o.State = filepath.Join(config, "claude-connections")
	o.Scope = "local"
	if err := privatedir.Make(o.State); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	image, _, err := setupIdentityClosed(ctx, self, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCompanionDeclarationInConfig(ctx, config, self, image.Key()); err == nil {
		t.Fatal("absence became none")
	}
	carrier := filepath.Join(o.State, "claude-setup-declared.json")
	raw, err := DesiredCompanion(ctx, CompanionSelection{Host: "claude", Requirement: "none", State: o.State, CLI: o.CLI, Project: o.Project, Scope: o.Scope, Name: "fixture"}, carrier, self, image.Key())
	if err != nil {
		t.Fatal(err)
	}
	declaredCarrier(t, carrier, self, raw)
	aggregate, err := ReadCompanionDeclarationInConfig(ctx, config, self, image.Key())
	if err != nil {
		t.Fatal(err)
	}
	reading, err := observeCompanionDeclaration(ctx, aggregate, func(context.Context, string, string, []string) ([]byte, error) {
		t.Fatal("none invoked vendor")
		return nil, nil
	}, nil)
	if err != nil || reading.State != CopyCurrent {
		t.Fatal(reading, err)
	}
	legacy := filepath.Join(o.State, "claude-setup-legacy.json")
	_, stored, err := setupIdentityClosed(ctx, self, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, PluginJSON(map[string]string{"Self": stored}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCompanionDeclarationInConfig(ctx, config, self, image.Key()); err == nil {
		t.Fatal("legacy missing membership ignored")
	}
}

func TestCompanionRecordedClaudeVersionSurvivesNewTemplateAndRejectsDrift(t *testing.T) {
	for _, edge := range []string{"old-version", "source-drift", "cache-drift", "vendor-version", "extra-member"} {
		t.Run(edge, func(t *testing.T) {
			config, o, self, vendor := installedCopyFixture(t, true)
			image, _, err := setupIdentityClosed(t.Context(), self, false)
			if err != nil {
				t.Fatal(err)
			}
			current := claudePluginModule
			claudePluginModule = append(append([]byte(nil), current...), []byte("\n// predecessor\n")...)
			carrier := filepath.Join(o.State, "claude-setup-declared.json")
			raw, err := DesiredCompanion(t.Context(), CompanionSelection{Host: "claude", Requirement: "required", State: o.State, CLI: o.CLI, Project: o.Project, Scope: o.Scope, Name: "fixture"}, carrier, self, image.Key())
			claudePluginModule = current
			if err != nil {
				t.Fatal(err)
			}
			declaredCarrier(t, carrier, self, raw)
			aggregate, err := ReadCompanionDeclarationInConfig(t.Context(), config, self, image.Key())
			if err != nil {
				t.Fatal(err)
			}
			goal, err := SuccessorCompanionDeclaration(t.Context(), aggregate, self, "future-staged-key")
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(goal, aggregate) {
				t.Fatal("future goal did not capture new version and key")
			}
			if _, err := ObserveCompanionDeclaration(t.Context(), goal); err == nil {
				t.Fatal("prospective goal became ready before publication")
			}
			d, _ := parseCompanion(aggregate)
			member := d.Members[0]
			switch edge {
			case "source-drift":
				err = os.WriteFile(filepath.Join(member.Root, "plugin", "hooks", "register.js"), []byte("person edit"), 0600)
			case "cache-drift":
				err = os.WriteFile(filepath.Join(vendor.folder(), "hooks", "register.js"), []byte("person edit"), 0600)
			case "vendor-version":
				vendor.v.version = "oa-cache-" + string(bytes.Repeat([]byte("a"), 64))
			case "extra-member":
				other := member
				other.Name = "other"
				other.Binding = PluginHash([]byte(other.Owner + "\x00" + other.State + "\x00" + other.Project + "\x00" + other.Scope + "\x00" + other.Name))
				other.Receipt = filepath.Join(o.State, "claude-setup-other.json")
				extra, _ := canonicalCompanion(companionDeclaration{Version: 1, Program: self, FileKey: image.Key(), Members: []companionMember{other}})
				declaredCarrier(t, other.Receipt, self, extra)
			}
			if err != nil {
				t.Fatal(err)
			}
			reading, err := observeCompanionDeclaration(t.Context(), aggregate, vendor.run, nil)
			if edge == "old-version" {
				if err != nil || reading.State != CopyCurrent {
					t.Fatal(reading, err)
				}
			} else if err == nil || reading.State == CopyCurrent {
				t.Fatal("drift became ready", reading, err)
			}
		})
	}
}

func TestCompanionCodexReceiptBindsHelperAndActualCacheDigests(t *testing.T) {
	config, home := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	state := filepath.Join(config, "codex-connections")
	if err := privatedir.Make(state); err != nil {
		t.Fatal(err)
	}
	self, cli := filepath.Join(t.TempDir(), "helper.exe"), filepath.Join(t.TempDir(), "cli.exe")
	for _, p := range []string{self, cli} {
		if err := os.WriteFile(p, []byte("inert identity fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	image, _, err := setupIdentityClosed(t.Context(), self, false)
	if err != nil {
		t.Fatal(err)
	}
	carrier := filepath.Join(state, "codex-setup-declared.json")
	raw, err := DesiredCompanion(t.Context(), CompanionSelection{Host: "codex", Requirement: "required", State: state, CLI: cli, Name: "OpenAbstractions"}, carrier, self, image.Key())
	if err != nil {
		t.Fatal(err)
	}
	declaredCarrier(t, carrier, self, raw)
	if reading, err := observeCompanionDeclaration(t.Context(), raw, nil, nil); err == nil || reading.State == CopyCurrent {
		t.Fatal("missing required receipt became ready")
	}
	vendor := &copyHookVendor{home: home}
	status := ""
	if err := ApplyCodexStatus(t.Context(), CodexOptions{Action: "connect", State: state, CLI: cli, Name: "OpenAbstractions", Program: self, FileKey: image.Key()}, home, vendor.run, &status); err != nil {
		t.Fatal(err)
	}
	reading, err := observeCompanionDeclaration(t.Context(), raw, nil, vendor.run)
	if err != nil || reading.State != CopyCurrent {
		t.Fatal(reading, err)
	}
	path := filepath.Join(home, "plugins", "cache", vendor.name, codexHookPlugin, codexHookVersion, "hooks", "hooks.json")
	if err := os.WriteFile(path, []byte("changed cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if reading, err := observeCompanionDeclaration(t.Context(), raw, nil, vendor.run); err == nil || reading.State == CopyCurrent {
		t.Fatal("changed cached bytes became ready")
	}
}
