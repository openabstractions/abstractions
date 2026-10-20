package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudePluginFailureDiagnosticPreservesCausePrivately(t *testing.T) {
	secret := errors.New("secret profile token and vendor output")
	for _, tc := range []struct {
		name, reason string
		cause        error
	}{
		{"unavailable", "unavailable", secret},
		{"timeout", "timeout", errors.Join(context.DeadlineExceeded, secret)},
		{"cancelled", "cancelled", errors.Join(context.Canceled, secret)},
		{"exit", "exit", &exec.ExitError{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := claudePluginInvocationFailure(context.Background(), "plugin_list", false, tc.cause)
			if !errors.Is(err, tc.cause) || err.Error() != "native plugin_list failed: "+tc.reason {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err = claudePluginInvocationFailure(ctx, "install", true, tc.cause)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, tc.cause) || !strings.Contains(err.Error(), "outcome unknown; no automatic retry") || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
		})
	}
}

func TestClaudePluginVendorFailureNamesOperation(t *testing.T) {
	for _, operation := range []string{"version", "marketplace_list", "plugin_list", "validate", "marketplace_add", "install", "marketplace_update", "update", "uninstall", "marketplace_remove"} {
		t.Run(operation, func(t *testing.T) {
			o, self, v := pluginFixture(t)
			if operation == "marketplace_update" || operation == "update" || operation == "uninstall" || operation == "marketplace_remove" {
				if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
					t.Fatal(err)
				}
				o.action = "update"
				if operation == "uninstall" || operation == "marketplace_remove" {
					o.action = "remove"
				}
			}
			cause := errors.New("secret profile token")
			calls := 0
			run := func(ctx context.Context, cli, project string, args []string) ([]byte, error) {
				stage := "version"
				if len(args) > 1 {
					stage = args[1]
					if stage == "marketplace" {
						stage += "_" + args[2]
					}
					if stage == "list" {
						stage = "plugin_list"
					}
				}
				if stage == operation {
					calls++
					return []byte("secret vendor output"), cause
				}
				return v.run(ctx, cli, project, args)
			}
			err := applyClaudePlugin(context.Background(), o, self, run)
			var invocation *claudePluginInvocationError
			if !errors.As(err, &invocation) || invocation.Operation() != operation || !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			mutating := operation == "marketplace_add" || operation == "install" || operation == "marketplace_update" || operation == "update" || operation == "uninstall" || operation == "marketplace_remove"
			if strings.Contains(err.Error(), "outcome unknown") != mutating {
				t.Fatal(err)
			}
		})
	}
}

type pluginVendorFixture struct {
	t                     *testing.T
	project, root, market string
	known, installed      bool
	calls                 [][]string
	lost, malformed       bool
	wrongReadback         string
	failValidate          bool
	editOnValidate        bool
	scope                 string
	version               string
	configRoot            string
}

func newPluginVendorFixture(t *testing.T, project, configRoot string) *pluginVendorFixture {
	t.Helper()
	if !filepath.IsAbs(configRoot) {
		t.Fatal("plugin fixture requires an absolute disposable configuration root")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configRoot)
	return &pluginVendorFixture{t: t, project: project, configRoot: configRoot}
}

func (v *pluginVendorFixture) pluginScope() string {
	if v.scope == "user" {
		return "user"
	}
	return "local"
}

func (v *pluginVendorFixture) save() {
	v.t.Helper()
	if v.scope == "user" {
		return
	}
	dir := filepath.Join(v.project, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		v.t.Fatal(err)
	}
	doc := map[string]any{"unrelated": "preserved"}
	if b, err := os.ReadFile(filepath.Join(dir, "settings.local.json")); err == nil {
		if json.Unmarshal(b, &doc) != nil {
			v.t.Fatal("invalid fixture local settings")
		}
	} else if !os.IsNotExist(err) {
		v.t.Fatal(err)
	}
	for _, key := range []string{"enabledPlugins", "extraKnownMarketplaces"} {
		if doc[key] == nil {
			doc[key] = map[string]any{}
		}
	}
	delete(doc["extraKnownMarketplaces"].(map[string]any), v.market)
	delete(doc["enabledPlugins"].(map[string]any), "openabstractions@"+v.market)
	if v.known {
		doc["extraKnownMarketplaces"].(map[string]any)[v.market] = map[string]any{"source": map[string]string{"source": "directory", "path": v.root}}
	}
	if v.installed {
		doc["enabledPlugins"].(map[string]any)["openabstractions@"+v.market] = true
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.local.json"), pluginJSON(doc), 0600); err != nil {
		v.t.Fatal(err)
	}
}
func (v *pluginVendorFixture) run(_ context.Context, _ string, project string, args []string) ([]byte, error) {
	// Every consumer uses the same explicit fixture profile. Refuse an omitted
	// or changed profile before even recording a fake vendor effect.
	if !filepath.IsAbs(v.configRoot) || os.Getenv("CLAUDE_CONFIG_DIR") != v.configRoot {
		return nil, errors.New("plugin fixture configuration root is unavailable or changed")
	}
	if project != v.project {
		v.t.Fatal("wrong project")
	}
	v.calls = append(v.calls, append([]string(nil), args...))
	if reflect.DeepEqual(args, []string{"--version"}) {
		return []byte("2.1.308 (Claude Code)"), nil
	}
	if reflect.DeepEqual(args, []string{"plugin", "marketplace", "list", "--json"}) {
		if !v.known {
			return []byte("[]"), nil
		}
		return pluginJSON([]any{map[string]string{"name": v.market, "source": "directory", "path": v.root, "scope": v.pluginScope()}}), nil
	}
	if reflect.DeepEqual(args, []string{"plugin", "list", "--json"}) {
		if v.malformed {
			return []byte("[null]"), nil
		}
		if !v.installed {
			return []byte("[]"), nil
		}
		row := map[string]any{"id": "openabstractions@" + v.market, "scope": "local", "projectPath": v.project, "readFromFolder": filepath.Join(v.root, "plugin"), "version": v.version, "enabled": true}
		if v.scope == "user" {
			row["scope"] = "user"
			delete(row, "projectPath")
		}
		switch v.wrongReadback {
		case "source":
			row["readFromFolder"] = "foreign"
		case "version":
			row["version"] = "0.0.0"
		case "disabled":
			row["enabled"] = false
		}
		return pluginJSON([]any{row}), nil
	}
	if len(args) == 4 && args[0] == "plugin" && args[1] == "validate" && args[3] == "--json" {
		v.root = filepath.Dir(args[2])
		var m struct {
			Name string `json:"name"`
		}
		b, err := os.ReadFile(filepath.Join(v.root, ".claude-plugin", "marketplace.json"))
		if err != nil {
			v.t.Fatal(err)
		}
		if json.Unmarshal(b, &m) != nil {
			v.t.Fatal("marketplace")
		}
		v.market = m.Name
		if v.failValidate {
			return nil, errors.New("invalid fixture manifest")
		}
		if v.editOnValidate {
			if err := os.WriteFile(filepath.Join(v.root, "plugin", "hooks", "register.js"), []byte("external after-validation edit"), 0600); err != nil {
				v.t.Fatal(err)
			}
		}
		return []byte("{}"), nil
	}
	switch {
	case len(args) == 7 && reflect.DeepEqual(args[:3], []string{"plugin", "marketplace", "add"}):
		if !reflect.DeepEqual(args[4:], []string{"--scope", v.pluginScope(), "--json"}) {
			v.t.Fatal(args)
		}
		v.root = args[3]
		var market struct {
			Name string `json:"name"`
		}
		b, err := os.ReadFile(filepath.Join(v.root, ".claude-plugin", "marketplace.json"))
		if err != nil {
			v.t.Fatal(err)
		}
		if json.Unmarshal(b, &market) != nil {
			v.t.Fatal("marketplace")
		}
		v.market = market.Name
		v.known = true
	case len(args) == 5 && reflect.DeepEqual(args[:3], []string{"plugin", "marketplace", "update"}):
	case len(args) == 7 && reflect.DeepEqual(args[:3], []string{"plugin", "marketplace", "remove"}):
		v.known = false
	case len(args) == 6 && args[0] == "plugin" && (args[1] == "install" || args[1] == "update" || args[1] == "uninstall"):
		if !reflect.DeepEqual(args[3:], []string{"--scope", v.pluginScope(), "--json"}) {
			v.t.Fatal(args)
		}
		v.installed = args[1] != "uninstall"
		if v.installed {
			b, err := os.ReadFile(filepath.Join(v.root, "plugin", ".claude-plugin", "plugin.json"))
			var manifest struct {
				Version string `json:"version"`
			}
			if err != nil || json.Unmarshal(b, &manifest) != nil {
				v.t.Fatal("fixture plugin version")
			}
			v.version = manifest.Version
			home := v.configRoot
			if err := os.MkdirAll(filepath.Join(home, "plugins"), 0700); err != nil {
				v.t.Fatal(err)
			}
			row := map[string]any{"scope": v.pluginScope(), "installPath": filepath.Join(v.root, "plugin"), "version": v.version}
			if v.pluginScope() == "local" {
				row["projectPath"] = v.project
			}
			registry := map[string]any{"version": 2, "plugins": map[string]any{"openabstractions@" + v.market: []any{row}}}
			if err := os.WriteFile(filepath.Join(home, "plugins", "installed_plugins.json"), pluginJSON(registry), 0600); err != nil {
				v.t.Fatal(err)
			}
		}
	default:
		v.t.Fatalf("unexpected native command %v", args)
	}
	v.save()
	if v.lost {
		return nil, errors.New("lost native reply")
	}
	return []byte("{}"), nil
}
func pluginFixture(t *testing.T) (claudePluginOptions, string, *pluginVendorFixture) {
	o, self, _ := claudeSetupFixture(t)
	return claudePluginOptions{action: "install", project: o.project, state: o.state, cli: o.cli}, self, newPluginVendorFixture(t, o.project, t.TempDir())
}

func TestClaudePluginFixtureRefusesMissingOrChangedConfigurationWithoutCWDOutput(t *testing.T) {
	for _, bad := range []string{"", "relative", "other-absolute"} {
		t.Run(bad, func(t *testing.T) {
			o, self, v := pluginFixture(t)
			if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
				t.Fatal(err)
			}
			cwd := t.TempDir()
			t.Chdir(cwd)
			value := bad
			if bad == "other-absolute" {
				value = t.TempDir()
			}
			t.Setenv("CLAUDE_CONFIG_DIR", value)
			calls := len(v.calls)
			if _, err := v.run(context.Background(), o.cli, o.project, []string{"plugin", "update", "openabstractions@" + v.market, "--scope", "local", "--json"}); err == nil {
				t.Fatal("unbound fixture profile accepted")
			}
			if len(v.calls) != calls {
				t.Fatal("fake vendor effect recorded before profile refusal")
			}
			entries, err := os.ReadDir(cwd)
			if err != nil || len(entries) != 0 {
				t.Fatal("fixture wrote into current directory", entries, err)
			}
		})
	}
}
func TestClaudePluginLifecycle(t *testing.T) {
	o, self, v := pluginFixture(t)
	for _, action := range []string{"install", "update", "remove", "remove"} {
		o.action = action
		if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if v.known || v.installed {
		t.Fatal("configuration retained")
	}
	if _, err := os.Stat(filepath.Join(v.root, "plugin", "hooks", "register.js")); err != nil {
		t.Fatal("retained payload", err)
	}
	for _, args := range v.calls {
		if strings.Contains(strings.Join(args, " "), "--scope user") || strings.Contains(strings.Join(args, " "), "--scope project") {
			t.Fatal("non-local scope")
		}
		if len(args) > 2 && args[0] == "plugin" && args[1] == "marketplace" && args[2] == "remove" && !strings.Contains(strings.Join(args, " "), "--scope local") {
			t.Fatal("unscoped removal")
		}
	}
}

func TestClaudePluginManagerRemovalUsesOwnedLocatorWithoutHelperExecution(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			o, helper, v := pluginFixture(t)
			ctx := context.Background()
			if err := applyClaudePlugin(ctx, o, helper, v.run); err != nil {
				t.Fatal(err)
			}
			manager := filepath.Join(filepath.Dir(helper), "installed-manager.exe")
			if err := os.WriteFile(manager, []byte("inert manager; never executed"), 0600); err != nil {
				t.Fatal(err)
			}
			if missing {
				if err := os.Remove(helper); err != nil {
					t.Fatal(err)
				}
			}
			// The owner has no enrollment dependency; revocation leaves removal
			// under the installed manager and the recorded configuration locator.
			o.action, o.registeredProgram = "remove", manager
			calls := len(v.calls)
			if err := applyClaudePlugin(ctx, o, manager, v.run); err == nil || len(v.calls) != calls || !v.installed {
				t.Fatal("foreign locator changed configuration", err)
			}
			o.registeredProgram = helper
			if err := applyClaudePlugin(ctx, o, manager, v.run); err != nil {
				t.Fatal(err)
			}
			if v.known || v.installed {
				t.Fatal("owned configuration retained")
			}
		})
	}
}

func TestClaudePluginPersonalSetupPreservesSharedAndUnrelatedLocalSettings(t *testing.T) {
	o, self, v := pluginFixture(t)
	dir := filepath.Join(o.project, ".claude")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	shared := []byte("{\"sharedSentinel\":\"must remain byte-exact\"}\n")
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), shared, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.local.json"), []byte("{\"unrelated\":{\"keep\":17},\"enabledPlugins\":{\"foreign@other\":true}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"install", "update", "remove"} {
		o.action = action
		if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || string(b) != string(shared) {
		t.Fatal("shared settings changed", err)
	}
	b, err = os.ReadFile(filepath.Join(dir, "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil || doc["unrelated"].(map[string]any)["keep"] != float64(17) || doc["enabledPlugins"].(map[string]any)["foreign@other"] != true {
		t.Fatal("unrelated local settings changed")
	}
}

func TestClaudePluginPayloadUsesHeldDedicatedCommand(t *testing.T) {
	files := claudePluginFiles("owned-market", `C:\owned\oa-claude-integration.exe`, `C:\owned\state`)
	var manifest struct {
		Name, DisplayName string
		MCP               map[string]claudeEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(files["plugin/.claude-plugin/plugin.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	entry := manifest.MCP["OpenAbstractions"]
	if manifest.Name != "openabstractions" || manifest.DisplayName != "OpenAbstractions" || entry.Command != `C:\owned\oa-claude-integration.exe` || !reflect.DeepEqual(entry.Args, []string{"--state-dir", `C:\owned\state`}) {
		t.Fatalf("%+v", manifest)
	}
}
func TestClaudePluginLostReplyRequiresReadback(t *testing.T) {
	o, self, v := pluginFixture(t)
	v.lost = true
	err := applyClaudePlugin(context.Background(), o, self, v.run)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatal(err)
	}
	adds := 0
	for _, a := range v.calls {
		if len(a) > 2 && a[0] == "plugin" && a[1] == "marketplace" && a[2] == "add" {
			adds++
		}
	}
	if adds != 1 {
		t.Fatalf("automatic retry: %v", v.calls)
	}
	v.lost = false
	count := len(v.calls)
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err == nil {
		t.Fatal("unconfirmed lost-reply configuration adopted")
	}
	if len(v.calls) != count {
		t.Fatal("unconfirmed configuration dispatched")
	}
}
func TestClaudePluginChangedPayloadRefusesBeforeVendor(t *testing.T) {
	o, self, v := pluginFixture(t)
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.root, "plugin", "hooks", "register.js"), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	v.calls = nil
	o.action = "remove"
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err == nil {
		t.Fatal("changed payload accepted")
	}
	if len(v.calls) != 0 {
		t.Fatal("vendor started")
	}
}
func TestClaudePluginMalformedReadbackStopsRemoval(t *testing.T) {
	o, self, v := pluginFixture(t)
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
		t.Fatal(err)
	}
	v.calls = nil
	v.malformed = true
	o.action = "remove"
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err == nil {
		t.Fatal("malformed readback accepted")
	}
	if !v.known {
		t.Fatal("marketplace removed after uncertain readback")
	}
}
func TestClaudePluginForeignProjectPreserved(t *testing.T) {
	o, self, v := pluginFixture(t)
	// An unrelated project marketplace is left intact. A same-name foreign
	// declaration is tested after discovering the deterministic owned name.
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
		t.Fatal(err)
	}
	v.root += "-foreign"
	v.save()
	v.calls = nil
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err == nil {
		t.Fatal("foreign source accepted")
	}
	if len(v.calls) != 0 {
		t.Fatal("foreign source dispatched")
	}
}

func TestClaudePluginNativeReadbackRequired(t *testing.T) {
	for _, kind := range []string{"source", "version", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			o, self, v := pluginFixture(t)
			v.wrongReadback = kind
			if err := applyClaudePlugin(context.Background(), o, self, v.run); err == nil {
				t.Fatal("unproven native configuration accepted")
			}
			if !v.known || !v.installed {
				t.Fatal("uncertain configuration was destructively cleaned up")
			}
		})
	}
}

func TestClaudePluginIntentCannotOwnExternalEqualConfiguration(t *testing.T) {
	o, self, v := pluginFixture(t)
	v.failValidate = true
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err == nil {
		t.Fatal("validation failure accepted")
	}
	v.failValidate = false
	v.known = true
	v.installed = true
	v.save()
	v.calls = nil
	o.action = "remove"
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err == nil {
		t.Fatal("intent adopted external configuration")
	}
	if len(v.calls) != 0 || !v.known || !v.installed {
		t.Fatal("external configuration mutated")
	}
}

func TestClaudePluginPayloadCASDoesNotAdoptConcurrentEdit(t *testing.T) {
	o, self, v := pluginFixture(t)
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(v.root, "plugin", "hooks", "register.js")
	o.action = "update"
	run := func(ctx context.Context, cli, project string, args []string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"--version"}) {
			if err := os.WriteFile(p, []byte("external edit"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return v.run(ctx, cli, project, args)
	}
	if err := applyClaudePlugin(context.Background(), o, self, run); err == nil {
		t.Fatal("concurrent edit adopted")
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "external edit" {
		t.Fatal("external edit overwritten", err)
	}
}

func TestClaudePluginRechecksBeforeUninstallAndUpdate(t *testing.T) {
	for _, action := range []string{"remove", "update"} {
		t.Run(action, func(t *testing.T) {
			o, self, v := pluginFixture(t)
			if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
				t.Fatal(err)
			}
			o.action = action
			v.calls = nil
			run := func(ctx context.Context, cli, project string, args []string) ([]byte, error) {
				if reflect.DeepEqual(args, []string{"--version"}) {
					v.root += "-foreign"
					v.save()
				}
				return v.run(ctx, cli, project, args)
			}
			if err := applyClaudePlugin(context.Background(), o, self, run); err == nil {
				t.Fatal("changed project source accepted")
			}
			for _, a := range v.calls {
				if len(a) > 1 && (a[1] == "update" || a[1] == "uninstall") {
					t.Fatal("mutation dispatched", a)
				}
			}
		})
	}
}

func TestClaudePluginExplicitRelocatedCLIUpgrade(t *testing.T) {
	o, self, v := pluginFixture(t)
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
		t.Fatal(err)
	}
	o.cli = filepath.Join(filepath.Dir(o.cli), "new CLI.exe")
	if err := os.WriteFile(o.cli, []byte("new selected CLI; never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	o.action = "update"
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
		t.Fatal(err)
	}
}

func TestClaudePluginRechecksNativeStateBeforeMutation(t *testing.T) {
	for _, kind := range []string{"disabled", "source", "version"} {
		t.Run(kind, func(t *testing.T) {
			o, self, v := pluginFixture(t)
			if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
				t.Fatal(err)
			}
			o.action = "remove"
			v.calls = nil
			run := func(ctx context.Context, cli, project string, args []string) ([]byte, error) {
				if reflect.DeepEqual(args, []string{"--version"}) {
					v.wrongReadback = kind
				}
				return v.run(ctx, cli, project, args)
			}
			if err := applyClaudePlugin(context.Background(), o, self, run); err == nil {
				t.Fatal("native change accepted")
			}
			for _, a := range v.calls {
				if len(a) > 1 && a[1] == "uninstall" {
					t.Fatal("uninstall dispatched")
				}
			}
		})
	}
}

func TestClaudePluginValidationCannotChangePayloadBeforeInstall(t *testing.T) {
	o, self, v := pluginFixture(t)
	v.editOnValidate = true
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err == nil {
		t.Fatal("post-validation payload change accepted")
	}
	if v.known || v.installed {
		t.Fatal("changed payload registered")
	}
	b, err := os.ReadFile(filepath.Join(v.root, "plugin", "hooks", "register.js"))
	if err != nil || string(b) != "external after-validation edit" {
		t.Fatal("external edit not preserved", err)
	}
}

func TestClaudePluginRemoveRetainsOlderPayloadProvenance(t *testing.T) {
	o, self, v := pluginFixture(t)
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(v.root, "plugin", "README.md")
	older := []byte("older installed OA payload\n")
	if err := os.WriteFile(path, older, 0600); err != nil {
		t.Fatal(err)
	}
	markers, err := filepath.Glob(filepath.Join(o.state, "claude-plugin-*.json"))
	if err != nil || len(markers) != 1 {
		t.Fatal(markers, err)
	}
	_, m, err := readPluginMarker(context.Background(), markers[0])
	if err != nil {
		t.Fatal(err)
	}
	m.Files["plugin/README.md"] = pluginHash(older)
	if err := os.WriteFile(markers[0], pluginJSON(m), 0600); err != nil {
		t.Fatal(err)
	}
	o.action = "remove"
	if err := applyClaudePlugin(context.Background(), o, self, v.run); err != nil {
		t.Fatal(err)
	}
	_, after, err := readPluginMarker(context.Background(), markers[0])
	if err != nil || after.Files["plugin/README.md"] != pluginHash(older) {
		t.Fatal("older provenance rewritten", err)
	}
}
