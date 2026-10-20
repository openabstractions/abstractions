package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/openabstractions/abstraction-identity/fileid"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeConnectionsFirstRunMissingStateIsReadOnlyEmptyPage(t *testing.T) {
	for _, relative := range []string{"claude-connections", "setup/claude-connections", "OpenAbstractions/setup/claude-connections"} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, filepath.FromSlash(relative))
			var output, diagnostic bytes.Buffer
			if code := claudeConnectionsCommand([]string{"connections", "claude", "--state-dir", state}, &output, &diagnostic); code != 0 {
				t.Fatal(code, output.String(), diagnostic.String())
			}
			var page claudeConnectionPage
			if err := json.Unmarshal(output.Bytes(), &page); err != nil || page.Outcome != "page" || page.Connections == nil || len(page.Connections) != 0 {
				t.Fatal(page, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatal("inventory changed first-run ancestor", entries, err)
			}
		})
	}
	root := t.TempDir()
	state := filepath.Join(root, "claude-connections")
	var output, diagnostic bytes.Buffer
	if code := claudeConnectionsCommand([]string{"connections", "claude", "--state-dir", state}, &output, &diagnostic); code != 0 {
		t.Fatal(code, output.String(), diagnostic.String())
	}
	var page claudeConnectionPage
	if err := json.Unmarshal(output.Bytes(), &page); err != nil || page.Outcome != "page" || page.Connections == nil || len(page.Connections) != 0 {
		t.Fatal(page, err)
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("inventory created first-run state", err)
	}
	if err := os.WriteFile(state, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := claudeConnectionsCommand([]string{"connections", "claude", "--state-dir", state}, &output, &diagnostic); code != 1 {
		t.Fatal("foreign state admitted", code)
	}
}

func TestClaudeConnectionsUserCompanionPreservesSavedInventoryAndCLILocator(t *testing.T) {
	setup, self, native := claudeSetupFixture(t)
	setup.project, setup.userRoot, setup.name = "", native.project, "OpenAbstractions"
	native.scope = "user"
	if err := applyClaudeSetup(context.Background(), setup, self, native.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	plugin := claudePluginOptions{action: "install", scope: "user", state: setup.state, cli: setup.cli}
	vendor := newPluginVendorFixture(t, setup.state, setup.userRoot)
	vendor.scope = "user"
	if err := applyClaudePlugin(context.Background(), plugin, self, vendor.run); err != nil {
		t.Fatal(err)
	}
	for _, locator := range []bool{false, true} {
		t.Run(fmt.Sprint(locator), func(t *testing.T) {
			page, err := readClaudeConnectionsWithOptions(context.Background(), setup.state, func() (string, error) { return setup.userRoot, nil }, locator)
			if err != nil {
				t.Fatal("USER companion made the normal C1 inventory unavailable", err)
			}
			if len(page.Connections) != 1 || page.Connections[0].Kind != "user" || page.Connections[0].Registration != "saved" {
				t.Fatal(page)
			}
			if (page.Connections[0].CLIIdentity != "") != locator {
				t.Fatal("CLI hint/default compatibility changed", page)
			}
		})
	}
}

func TestClaudeConnectionsPluginInventoryIsReadOnlyAndScoped(t *testing.T) {
	o, self, vendor := pluginFixture(t)
	ctx := context.Background()
	if err := applyClaudePlugin(ctx, o, self, vendor.run); err != nil {
		t.Fatal(err)
	}
	calls := len(vendor.calls)
	read := func(want string) {
		t.Helper()
		page, err := readClaudeConnections(ctx, o.state)
		if err != nil || len(page.Connections) != 1 {
			t.Fatal(page, err)
		}
		row := page.Connections[0]
		if row.Kind != "plugin" || row.Registration != want || row.Project != o.project || row.Program != self || row.Name != "openabstractions@"+vendor.market || len(vendor.calls) != calls {
			t.Fatal(row, vendor.calls)
		}
	}
	read("saved")
	legacy := claudeSetupOptions{action: "connect", project: o.project, state: o.state, cli: o.cli, name: "legacy-oa"}
	bare := &claudeVendorFixture{t: t, project: o.project}
	if err := applyClaudeSetup(ctx, legacy, self, bare.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	page, err := readClaudeConnections(ctx, o.state)
	if err != nil || len(page.Connections) != 2 || page.Connections[0].Kind != "" || page.Connections[0].Name != legacy.name || page.Connections[1].Kind != "plugin" {
		t.Fatal(page, err)
	}
	// Remove only the legacy marker from this isolated fixture so subsequent
	// payload-state assertions keep their single-row expectation.
	for _, path := range claudeMarkers(t, o.state) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	payload := filepath.Join(vendor.root, "plugin", "hooks", "register.js")
	original, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("foreign edit"), 0600); err != nil {
		t.Fatal(err)
	}
	read("changed")
	if err := os.WriteFile(payload, original, 0600); err != nil {
		t.Fatal(err)
	}
	vendor.known, vendor.installed = false, false
	vendor.save()
	read("absent")
}

func TestClaudeConnectionsReadOnlySavedChangedAndAbsent(t *testing.T) {
	options, self, vendor := claudeSetupFixture(t)
	ctx := context.Background()
	page, err := readClaudeConnections(ctx, options.state)
	if err != nil || page.Outcome != "page" || len(page.Connections) != 0 {
		t.Fatal(page, err)
	}
	if err := applyClaudeSetup(ctx, options, self, vendor.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	calls := len(vendor.calls)
	page, err = readClaudeConnections(ctx, options.state)
	if err != nil || len(page.Connections) != 1 {
		t.Fatal(page, err)
	}
	row := page.Connections[0]
	if row.Project != options.project || row.Program != self || row.Name != options.name || row.Registration != "saved" || len(vendor.calls) != calls {
		t.Fatal(row, vendor.calls)
	}
	if row.CLIIdentity != "" {
		t.Fatal("default inventory disclosed CLI locator")
	}
	withCLI, err := readClaudeConnectionsWithOptions(ctx, options.state, claudeUserConfigurationHome, true)
	if err != nil || len(withCLI.Connections) != 1 {
		t.Fatal(withCLI, err)
	}
	cli, err := fileid.ParseStoredFileID(withCLI.Connections[0].CLIIdentity)
	if err != nil || cli.Path() != options.cli || len(vendor.calls) != calls {
		t.Fatal(cli, err, vendor.calls)
	}
	document := vendor.document()
	document["mcpServers"].(map[string]any)[options.name] = claudeEntry{Type: "stdio", Command: "foreign", Args: []string{}, Env: map[string]string{}}
	vendor.save(document)
	page, err = readClaudeConnections(ctx, options.state)
	if err != nil || page.Connections[0].Registration != "changed" {
		t.Fatal(page, err)
	}
	withCLI, err = readClaudeConnectionsWithOptions(ctx, options.state, claudeUserConfigurationHome, true)
	if err != nil || withCLI.Connections[0].CLIIdentity != "" {
		t.Fatal("changed entry offered a CLI locator", withCLI, err)
	}
	delete(document["mcpServers"].(map[string]any), options.name)
	vendor.save(document)
	page, err = readClaudeConnections(ctx, options.state)
	if err != nil || page.Connections[0].Registration != "absent" {
		t.Fatal(page, err)
	}
}

func TestClaudeConnectionsMissingImageRetainsConfigurationOnly(t *testing.T) {
	options, self, vendor := claudeSetupFixture(t)
	if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(self); err != nil {
		t.Fatal(err)
	}
	calls := len(vendor.calls)
	page, err := readClaudeConnections(context.Background(), options.state)
	if err != nil || len(page.Connections) != 1 || page.Connections[0].Program != self || page.Connections[0].ProgramKey != "" || page.Connections[0].Registration != "saved" || len(vendor.calls) != calls {
		t.Fatalf("configuration-only=%+v err=%v", page, err)
	}
}

func TestClaudeConnectionsPreservesExactNamesAndInactiveHistory(t *testing.T) {
	options, self, vendor := claudeSetupFixture(t)
	ctx := context.Background()
	for _, name := range []string{"second", "first"} {
		options.name = name
		if err := applyClaudeSetup(ctx, options, self, vendor.run, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	options.action = "disconnect"
	if err := applyClaudeSetup(ctx, options, self, vendor.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	page, err := readClaudeConnections(ctx, options.state)
	if err != nil || len(page.Connections) != 2 || page.Connections[0].Name != "first" || page.Connections[0].Registration != "absent" || page.Connections[1].Name != "second" || page.Connections[1].Registration != "saved" {
		t.Fatal(page, err)
	}
}

func TestClaudeConnectionsUnknownOwnershipAndBoundsRefuse(t *testing.T) {
	for _, broken := range []string{"corrupt", "renamed", "other-state", "many-entries"} {
		t.Run(broken, func(t *testing.T) {
			options, self, vendor := claudeSetupFixture(t)
			if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			marker := claudeMarkers(t, options.state)[0]
			switch broken {
			case "corrupt":
				if err := os.WriteFile(marker, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "renamed":
				if err := os.Rename(marker, filepath.Join(options.state, "claude-setup-foreign.json")); err != nil {
					t.Fatal(err)
				}
			case "other-state":
				raw, err := os.ReadFile(marker)
				if err != nil {
					t.Fatal(err)
				}
				var value claudeSetupMarker
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				value.State = value.Project
				raw, err = json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "many-entries":
				for i := 0; i < 2*claudeConnectionLimit+1; i++ {
					if err := os.WriteFile(filepath.Join(options.state, fmt.Sprintf("unrelated-%03d", i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			var output, diagnostics bytes.Buffer
			if exit := claudeConnectionsCommand([]string{"connections", "claude", "--state-dir", options.state}, &output, &diagnostics); exit != 1 {
				t.Fatal(exit, output.String(), diagnostics.String())
			}
			var page claudeConnectionPage
			if err := json.Unmarshal(output.Bytes(), &page); err != nil || page.Outcome != "unavailable" || len(page.Connections) != 0 {
				t.Fatal(page, err)
			}
		})
	}
}

func TestClaudeConnectionsNoStateCreationAndCancellation(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if page, err := readClaudeConnections(context.Background(), missing); err != nil || page.Outcome != "page" || len(page.Connections) != 0 {
		t.Fatal(page, err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("read created state", err)
	}
	options, _, _ := claudeSetupFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readClaudeConnections(ctx, options.state); err == nil {
		t.Fatal("cancelled inventory succeeded")
	}
	for _, args := range [][]string{{"connections"}, {"connections", "other"}, {"connections", "claude", "--state-dir", "relative"}, {"connections", "claude", "--state-dir", options.state, "extra"}} {
		if exit := claudeConnectionsCommand(args, &bytes.Buffer{}, &bytes.Buffer{}); exit != 2 {
			t.Fatal(args, exit)
		}
	}
	if exit := claudeConnectionsCommand([]string{"connections", "claude", "--help"}, &bytes.Buffer{}, &bytes.Buffer{}); exit != 0 {
		t.Fatal(exit)
	}
}
