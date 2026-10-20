package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeUserModRemoveNeverInstalledWithSavedConnection(t *testing.T) {
	for _, foreign := range []string{"", "marketplace", "plugin", "wrong-scope", "malformed"} {
		t.Run(foreign, func(t *testing.T) {
			setup, self, native := claudeSetupFixture(t)
			setup.project, setup.userRoot, setup.name = "", native.project, "OpenAbstractions"
			native.scope = "user"
			if err := applyClaudeSetup(context.Background(), setup, self, native.run, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			connectionPath := filepath.Join(setup.userRoot, ".claude.json")
			before, err := os.ReadFile(connectionPath)
			if err != nil {
				t.Fatal(err)
			}
			o := claudePluginOptions{action: "remove", scope: "user", state: setup.state, cli: setup.cli}
			v := newPluginVendorFixture(t, setup.state, setup.userRoot)
			v.scope, v.market, v.root = "user", "oa-local-user", filepath.Join(setup.state, "claude-plugin-user")
			v.known = foreign == "marketplace"
			v.installed = foreign == "plugin" || foreign == "wrong-scope"
			v.malformed = foreign == "malformed"
			run := v.run
			if foreign == "wrong-scope" {
				run = func(ctx context.Context, cli, project string, args []string) ([]byte, error) {
					if strings.Join(args, " ") == "plugin list --json" {
						v.calls = append(v.calls, args)
						return pluginJSON([]any{map[string]any{"id": "openabstractions@oa-local-user", "scope": "unknown"}}), nil
					}
					return v.run(ctx, cli, project, args)
				}
			}
			err = applyClaudePlugin(context.Background(), o, self, run)
			if foreign == "" && err != nil {
				t.Fatal("verified absence refused", err)
			}
			if foreign != "" && err == nil {
				t.Fatal("foreign or unknown native companion accepted")
			}
			marketRead, pluginRead := false, false
			for _, args := range v.calls {
				call := strings.Join(args, " ")
				if call == "plugin marketplace list --json" {
					marketRead = true
				} else if call == "plugin list --json" {
					pluginRead = true
				} else if call != "--version" {
					t.Fatal("absence check mutated vendor", args)
				}
			}
			if foreign == "" && (!marketRead || !pluginRead) {
				t.Fatal("absence lacks guarded native readbacks")
			}
			after, _ := os.ReadFile(connectionPath)
			if !bytes.Equal(before, after) {
				t.Fatal("saved C1 changed")
			}
			if _, err := os.Stat(filepath.Join(setup.state, "claude-plugin-user.json")); !os.IsNotExist(err) {
				t.Fatal("absence created marker", err)
			}
			if _, err := os.Stat(v.root); !os.IsNotExist(err) {
				t.Fatal("absence created payload", err)
			}
		})
	}
}

func TestClaudeUserModLifecycleUsesNativeUserScopeAndNoSecondMCP(t *testing.T) {
	o, self, v := pluginFixture(t)
	o.scope = "user"
	o.project = ""
	v.scope = "user"
	v.project = o.state
	for _, action := range []string{"install", "status", "update", "status", "remove"} {
		o.action = action
		if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
			t.Fatal(action, err)
		}
	}
	o.action = "status"
	if err := applyClaudePlugin(context.Background(), o, self, v.run); !errors.Is(err, errClaudeCompanionAbsent) {
		t.Fatal("removed companion claimed ready", err)
	}
	for _, args := range v.calls {
		if strings.Contains(strings.Join(args, " "), "--scope local") || strings.Contains(strings.Join(args, " "), "--project") {
			t.Fatal(args)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(v.root, "plugin/.claude-plugin/plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(manifest, &value) != nil {
		t.Fatal("manifest")
	}
	if _, exists := value["mcpServers"]; exists {
		t.Fatal("user mod starts a second MCP server")
	}
	module, err := os.ReadFile(filepath.Join(v.root, "plugin/hooks/register.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(module), "const server = 'OpenAbstractions';") || strings.Contains(string(module), "$.mcp.connect") || strings.Contains(string(module), "asUser") {
		t.Fatal("wrong user MCP or attribution")
	}
	if _, err := os.Stat(filepath.Join(o.state, ".claude")); !os.IsNotExist(err) {
		t.Fatal("user setup touched project settings", err)
	}
}

func TestClaudeUserModForeignReadbackAndReadOnlyMissingState(t *testing.T) {
	o, self, v := pluginFixture(t)
	o.scope = "user"
	o.project = ""
	o.action = "status"
	v.scope = "user"
	v.project = o.state
	if err := applyClaudePlugin(context.Background(), o, self, v.run); !errors.Is(err, errClaudeCompanionAbsent) {
		t.Fatal(err)
	}
	if len(v.calls) != 0 {
		t.Fatal("absent status ran vendor commands", v.calls)
	}
	missing := o
	missing.state = filepath.Join(t.TempDir(), "absent-state")
	if err := applyClaudePlugin(context.Background(), missing, self, v.run); !errors.Is(err, errClaudeCompanionAbsent) {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing.state); !os.IsNotExist(err) {
		t.Fatal("status created state", err)
	}
	o.action = "install"
	v.wrongReadback = "source"
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err == nil {
		t.Fatal("foreign user plugin source accepted")
	}
}

func TestClaudeUserModProducerFixture(t *testing.T) {
	files := claudePluginFilesForScope("oa-native-validator", "inert-never-executed", "inert-state", "user")
	output := os.Getenv("OA_CLAUDE_MOD_FIXTURE_OUTPUT")
	if output == "" {
		output = filepath.Join(t.TempDir(), "user-mod")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		path := filepath.Join(output, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if len(files) != 5 || !strings.Contains(string(files["plugin/hooks/register.js"]), "const server = 'OpenAbstractions';") {
		t.Fatal("producer fixture differs")
	}
}
