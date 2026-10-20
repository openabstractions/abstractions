package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

type claudeVendorFixture struct {
	t                                              *testing.T
	project                                        string
	scope                                          string
	calls                                          [][]string
	failAdd, failRemove, wrongReadback, raceRemove bool
}

func (f *claudeVendorFixture) document() map[string]any {
	raw, err := os.ReadFile(filepath.Join(f.project, f.filename()))
	if os.IsNotExist(err) {
		return map[string]any{"mcpServers": map[string]any{}}
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		f.t.Fatal(err)
	}
	return document
}
func (f *claudeVendorFixture) save(document map[string]any) {
	raw, err := json.Marshal(document)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.project, f.filename()), raw, 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *claudeVendorFixture) filename() string {
	if f.scope == "user" {
		return ".claude.json"
	}
	return ".mcp.json"
}
func (f *claudeVendorFixture) scopeName() string {
	if f.scope == "user" {
		return "user"
	}
	return "project"
}
func (f *claudeVendorFixture) run(_ context.Context, _ string, project string, args []string) ([]byte, error) {
	if project != f.project {
		f.t.Fatalf("wrong project %q", project)
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	if reflect.DeepEqual(args, []string{"--version"}) {
		return []byte("2.1.308 (Claude Code)"), nil
	}
	document := f.document()
	servers := document["mcpServers"].(map[string]any)
	switch {
	case len(args) == 11 && reflect.DeepEqual(args[:6], []string{"mcp", "add", "--scope", f.scopeName(), "--transport", "stdio"}) && args[7] == "--" && args[9] == "--state-dir":
		entry := claudeEntry{Type: "stdio", Command: args[8], Args: []string{"--state-dir", args[10]}, Env: map[string]string{}}
		if f.wrongReadback {
			entry.Command = "foreign"
		}
		servers[args[6]] = entry
		f.save(document)
		if f.failAdd {
			return nil, errors.New("lost add reply")
		}
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"mcp", "remove", "--scope", f.scopeName()}):
		if f.raceRemove {
			servers[args[4]] = map[string]any{"type": "stdio", "command": "external-replacement", "args": []any{}, "env": map[string]any{}}
			f.save(document)
		}
		delete(servers, args[4])
		f.save(document)
		if f.failRemove {
			return nil, errors.New("lost remove reply")
		}
	default:
		f.t.Fatalf("unexpected vendor argv %#v", args)
	}
	return []byte("vendor prose is not a machine status contract"), nil
}

func claudeSetupFixture(t *testing.T) (claudeSetupOptions, string, *claudeVendorFixture) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project with spaces")
	state := filepath.Join(root, "private state")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := privatedir.Make(state); err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(root, "gateway with spaces.exe")
	cli := filepath.Join(root, "vendor cli.exe")
	for _, p := range []string{self, cli} {
		if err := os.WriteFile(p, []byte("identity fixture; never executed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	options := claudeSetupOptions{action: "connect", project: project, state: state, cli: cli, name: "oa-fixture"}
	return options, self, &claudeVendorFixture{t: t, project: project}
}
func claudeMarkers(t *testing.T, state string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(state, "claude-setup-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestClaudeUserSetupOwnsOnlySelectedUserEntry(t *testing.T) {
	o, self, vendor := claudeSetupFixture(t)
	o.project, o.userRoot, o.name = "", vendor.project, "OpenAbstractions"
	vendor.scope = "user"
	vendor.save(map[string]any{"opaque-account-value": map[string]any{"secret": "never-output"}, "mcpServers": map[string]any{"Other": map[string]any{"unknownForeignShape": true}}})
	projectBytes := []byte(`{"mcpServers":{"OpenAbstractions":{"command":"project-override"}}}`)
	if err := os.WriteFile(filepath.Join(o.userRoot, ".mcp.json"), projectBytes, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for range 2 {
		if err := applyClaudeSetup(context.Background(), o, self, vendor.run, &out); err != nil {
			t.Fatal(err)
		}
	}
	if len(vendor.calls) != 2 {
		t.Fatal("idempotent setup ran extra vendor operation", vendor.calls)
	}
	page, err := readClaudeConnectionsFor(context.Background(), o.state, func() (string, error) { return o.userRoot, nil })
	if err != nil || len(page.Connections) != 1 || page.Connections[0].Kind != "user" || page.Connections[0].Registration != "saved" {
		t.Fatal(page, err)
	}
	if _, err := readClaudeConnectionsFor(context.Background(), o.state, func() (string, error) { return "", errors.New("unknown user profile") }); err == nil {
		t.Fatal("unknown user context admitted")
	}
	o.action = "disconnect"
	if err := applyClaudeSetup(context.Background(), o, self, vendor.run, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := vendor.document()["mcpServers"].(map[string]any)["Other"]; !ok {
		t.Fatal("foreign entry removed")
	}
	if raw, err := os.ReadFile(filepath.Join(o.userRoot, ".mcp.json")); err != nil || !bytes.Equal(raw, projectBytes) {
		t.Fatal("project registration changed", err)
	}
	if strings.Contains(out.String(), "never-output") || strings.Contains(out.String(), "unknownForeignShape") {
		t.Fatal("unrelated content exposed")
	}
	for _, call := range vendor.calls {
		if len(call) > 1 && call[0] == "mcp" && call[1] == "get" {
			t.Fatal("health preflight invoked")
		}
	}
}

func TestClaudeUserSetupRefusesForeignAmbiguousAndLostOwnership(t *testing.T) {
	for _, mode := range []string{"foreign", "identical-unowned", "duplicate", "malformed", "lost-reply", "wrong-readback", "changed-owned"} {
		t.Run(mode, func(t *testing.T) {
			o, self, v := claudeSetupFixture(t)
			o.project, o.userRoot, o.name = "", v.project, "OpenAbstractions"
			v.scope = "user"
			entry := claudeEntry{Type: "stdio", Command: self, Args: []string{"--state-dir", o.state}, Env: map[string]string{}}
			switch mode {
			case "foreign":
				entry.Command = "foreign"
				v.save(map[string]any{"mcpServers": map[string]any{o.name: entry}})
			case "identical-unowned":
				v.save(map[string]any{"mcpServers": map[string]any{o.name: entry}})
			case "duplicate":
				os.WriteFile(filepath.Join(o.userRoot, ".claude.json"), []byte(`{"mcpServers":{},"mcpServers":{}}`), 0600)
			case "malformed":
				os.WriteFile(filepath.Join(o.userRoot, ".claude.json"), []byte(`{"mcpServers":`), 0600)
			case "lost-reply":
				v.failAdd = true
			case "wrong-readback":
				v.wrongReadback = true
			case "changed-owned":
				if err := applyClaudeSetup(context.Background(), o, self, v.run, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
				entry.Env = map[string]string{"CHANGED": "value"}
				v.save(map[string]any{"mcpServers": map[string]any{o.name: entry}})
				v.calls = nil
				o.action = "disconnect"
			}
			before, _ := os.ReadFile(filepath.Join(o.userRoot, ".claude.json"))
			if err := applyClaudeSetup(context.Background(), o, self, v.run, &bytes.Buffer{}); err == nil {
				t.Fatal("uncertain registration admitted")
			}
			if mode != "lost-reply" && mode != "wrong-readback" {
				after, _ := os.ReadFile(filepath.Join(o.userRoot, ".claude.json"))
				if !bytes.Equal(before, after) || len(v.calls) != 0 {
					t.Fatal("refusal changed configuration", v.calls)
				}
			}
		})
	}
}

func TestClaudeUserActualCLIIsolatedAddReadbackRemove(t *testing.T) {
	cli := os.Getenv("OA_CLAUDE_USER_CLI")
	if cli == "" {
		t.Skip("explicit isolated native configuration receiving")
	}
	o, self, _ := claudeSetupFixture(t)
	o.userRoot, o.project, o.name, o.cli = o.project, "", "OpenAbstractions", cli
	t.Setenv("CLAUDE_CONFIG_DIR", o.userRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := applyClaudeSetup(ctx, o, self, runClaudeSetupCLI, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	projectOptions := o
	projectOptions.project = o.userRoot
	if err := applyClaudeSetup(ctx, projectOptions, self, runClaudeSetupCLI, &bytes.Buffer{}); err != nil {
		t.Fatal("same-name project setup", err)
	}
	page, err := readClaudeConnectionsFor(ctx, o.state, func() (string, error) { return o.userRoot, nil })
	if err != nil || len(page.Connections) != 2 {
		t.Fatal(page, err)
	}
	for _, row := range page.Connections {
		if row.Registration != "saved" {
			t.Fatal(row)
		}
	}
	// The selected command is an inert non-executable identity fixture. Native
	// add/readback/remove succeeds without ever health-launching that target.
	if err := applyClaudeSetup(ctx, o, self, runClaudeSetupCLI, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	o.action = "disconnect"
	if err := applyClaudeSetup(ctx, o, self, runClaudeSetupCLI, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	page, err = readClaudeConnectionsFor(ctx, o.state, func() (string, error) { return o.userRoot, nil })
	if err != nil || len(page.Connections) != 2 {
		t.Fatal(page, err)
	}
	for _, row := range page.Connections {
		want := "saved"
		if row.Kind == "user" {
			want = "absent"
		}
		if row.Registration != want {
			t.Fatal("wrong scope removed", row)
		}
	}
	if _, err := runClaudeSetupCLI(ctx, cli, o.userRoot, []string{"mcp", "add", "--scope", "user", "--transport", "stdio", o.name, "--", filepath.Join(o.userRoot, "foreign-not-executed.exe")}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(o.userRoot, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	o.action = "connect"
	called := false
	if err := applyClaudeSetup(ctx, o, self, func(context.Context, string, string, []string) ([]byte, error) { called = true; return nil, nil }, &bytes.Buffer{}); err == nil || called {
		t.Fatal("foreign native entry reached a configuration/health command", err, called)
	}
	after, err := os.ReadFile(filepath.Join(o.userRoot, ".claude.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("foreign native entry changed", err)
	}
	t.Log("native_user_add_exact_readback_idempotence_remove=true; no MCP target launch or model session")
}

func TestClaudeUserConfigurationRefusesVirtualizedProfileBeforeSettings(t *testing.T) {
	called := false
	platform := layout.Platform{GOOS: "windows", Profile: func() (string, error) { return "packaged-view", nil }}
	_, err := claudeUserConfigurationHomeFor(platform, t.TempDir(), func() (string, error) { called = true; return "", nil })
	if layout.ReasonOf(err) != layout.VirtualizedProfile || called {
		t.Fatal("virtualized profile reached settings location", err, called)
	}
}

func TestClaudeUserChangedOwnedCaseAliasesRefuseBeforeVendor(t *testing.T) {
	for _, key := range []string{"type", "command", "args", "env", "marker"} {
		t.Run(key, func(t *testing.T) {
			o, self, v := claudeSetupFixture(t)
			o.project, o.userRoot, o.name = "", v.project, "OpenAbstractions"
			v.scope = "user"
			if err := applyClaudeSetup(context.Background(), o, self, v.run, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(o.userRoot, ".claude.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]json.RawMessage
			if err = json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			var servers map[string]json.RawMessage
			if err = json.Unmarshal(document["mcpServers"], &servers); err != nil {
				t.Fatal(err)
			}
			var entry map[string]json.RawMessage
			if err = json.Unmarshal(servers[o.name], &entry); err != nil {
				t.Fatal(err)
			}
			if key == "marker" {
				path = claudeMarkers(t, o.state)[0]
				raw, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var marker map[string]json.RawMessage
				if err = json.Unmarshal(raw, &marker); err != nil {
					t.Fatal(err)
				}
				marker["Name"] = marker["name"]
				delete(marker, "name")
				raw, err = json.Marshal(marker)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var expected, legacy claudeEntry
				if err := json.Unmarshal(servers[o.name], &expected); err != nil {
					t.Fatal(err)
				}
				alias := strings.ToUpper(key[:1]) + key[1:]
				aliasValue := entry[key]
				entry[key] = json.RawMessage(`"changed"`)
				if key == "args" {
					entry[key] = json.RawMessage(`["changed"]`)
				}
				if key == "env" {
					entry[key] = json.RawMessage(`null`)
				}
				lowercase, err := json.Marshal(entry)
				if err != nil {
					t.Fatal(err)
				}
				// The expected uppercase alias comes LAST, reproducing the old
				// Go decoder's acceptance while JavaScript retains changed lowercase.
				servers[o.name] = append(append(append(lowercase[:len(lowercase)-1], []byte(`,"`+alias+`":`)...), aliasValue...), '}')
				if err := json.Unmarshal(servers[o.name], &legacy); err != nil || !reflect.DeepEqual(legacy, expected) {
					t.Fatal("fixture does not expose former case-alias acceptance", err)
				}
				document["mcpServers"], err = json.Marshal(servers)
				if err != nil {
					t.Fatal(err)
				}
				raw, err = json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			v.calls = nil
			o.action = "disconnect"
			if err := applyClaudeSetup(context.Background(), o, self, v.run, &bytes.Buffer{}); err == nil || len(v.calls) != 0 {
				t.Fatal("case alias reached vendor", err, v.calls)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, raw) {
				t.Fatal("changed entry was not preserved", err)
			}
		})
	}
}

func TestClaudeSetupFlagsAndUnknownCommands(t *testing.T) {
	root := t.TempDir()
	user, err := parseClaudeSetup([]string{"connect", "claude", "--state-dir", root}, &bytes.Buffer{})
	if err != nil || user.project != "" || user.name != "OpenAbstractions" {
		t.Fatal(user, err)
	}
	options, err := parseClaudeSetup([]string{"connect", "claude", "--project", root, "--state-dir", root, "--claude", filepath.Join(root, "cli with spaces.exe"), "--name", "fresh-alias"}, &bytes.Buffer{})
	if err != nil || options.name != "fresh-alias" || options.action != "connect" {
		t.Fatal(options, err)
	}
	for _, args := range [][]string{{"connect"}, {"connect", "other"}, {"disconnect", "claude"}, {"connect", "claude", "--project", "relative", "--state-dir", root}, {"connect", "claude", "--project", root, "--state-dir", root, "--scope", "user"}, {"connect", "claude", "--project", root, "--state-dir", root, "--name", "bad/name"}} {
		if _, err := parseClaudeSetup(args, &bytes.Buffer{}); err == nil {
			t.Fatal("accepted", args)
		}
	}
	if exit := claudeSetupCommand([]string{"connect", "other"}, &bytes.Buffer{}, &bytes.Buffer{}); exit != 2 {
		t.Fatal(exit)
	}
	for _, name := range []string{"dotted.name", ".", ".."} {
		if _, err := parseClaudeSetup([]string{"connect", "claude", "--project", root, "--state-dir", root, "--name", name}, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid vendor registration name accepted", name)
		}
	}
}

func TestClaudeSetupConnectIdempotenceAndOwnedRemoval(t *testing.T) {
	options, self, vendor := claudeSetupFixture(t)
	vendor.save(map[string]any{"mcpServers": map[string]any{"other": map[string]any{"type": "http", "url": "https://example.invalid"}}, "otherRoot": true})
	var output bytes.Buffer
	if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &output); err != nil {
		t.Fatal(err)
	}
	if len(claudeMarkers(t, options.state)) != 1 || len(vendor.calls) != 2 {
		t.Fatal(vendor.calls)
	}
	if !strings.Contains(output.String(), "approval: pending or unverified") || !strings.Contains(output.String(), "OA authorization: unchanged") {
		t.Fatal(output.String())
	}
	before := len(vendor.calls)
	if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &output); err != nil || len(vendor.calls) != before {
		t.Fatal(err, vendor.calls)
	}
	job := filepath.Join(options.state, "job-kept.json")
	if err := os.WriteFile(job, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	options.action = "disconnect"
	if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &output); err != nil {
		t.Fatal(err)
	}
	document := vendor.document()
	servers := document["mcpServers"].(map[string]any)
	if _, ok := servers[options.name]; ok {
		t.Fatal("entry retained")
	}
	if _, ok := servers["other"]; !ok || document["otherRoot"] != true {
		t.Fatal("foreign entry changed")
	}
	if _, err := os.Stat(job); err != nil {
		t.Fatal("job/state removed", err)
	}
	if len(claudeMarkers(t, options.state)) != 1 {
		t.Fatal("ownership history removed")
	}
	if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &output); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeSetupEqualUnownedOrChangedRegistrationIsPreserved(t *testing.T) {
	for _, kind := range []string{"equal-unowned", "foreign", "changed-owned"} {
		t.Run(kind, func(t *testing.T) {
			options, self, vendor := claudeSetupFixture(t)
			if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if kind == "equal-unowned" {
				if err := os.Remove(claudeMarkers(t, options.state)[0]); err != nil {
					t.Fatal(err)
				}
			} else {
				doc := vendor.document()
				doc["mcpServers"].(map[string]any)[options.name] = map[string]any{"type": "stdio", "command": "foreign", "args": []any{}, "env": map[string]any{}}
				vendor.save(doc)
			}
			before, err := os.ReadFile(filepath.Join(options.project, ".mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			calls := len(vendor.calls)
			for _, action := range []string{"connect", "disconnect"} {
				options.action = action
				if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err == nil {
					t.Fatal("unowned entry accepted")
				}
			}
			after, err := os.ReadFile(filepath.Join(options.project, ".mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || len(vendor.calls) != calls {
				t.Fatal("foreign configuration mutated")
			}
		})
	}
}

func TestClaudeSetupLostRepliesAndUnknownReadbackNeverEstablishOwnership(t *testing.T) {
	for _, which := range []string{"add-reply", "readback", "remove-reply"} {
		t.Run(which, func(t *testing.T) {
			options, self, vendor := claudeSetupFixture(t)
			if which == "remove-reply" {
				if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
				marker := claudeMarkers(t, options.state)[0]
				before, err := os.ReadFile(marker)
				if err != nil {
					t.Fatal(err)
				}
				vendor.failRemove = true
				options.action = "disconnect"
				if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err == nil {
					t.Fatal("lost reply accepted")
				}
				after, err := os.ReadFile(marker)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("uncertain ownership modified", err)
				}
			} else {
				vendor.failAdd = which == "add-reply"
				vendor.wrongReadback = which == "readback"
				if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err == nil {
					t.Fatal("unknown add accepted")
				}
				if len(claudeMarkers(t, options.state)) != 0 {
					t.Fatal("fabricated ownership")
				}
			}
		})
	}
}

func TestClaudeSetupVendorRemoveHasNoAtomicExternalEditProtection(t *testing.T) {
	options, self, vendor := claudeSetupFixture(t)
	if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	vendor.raceRemove = true
	options.action = "disconnect"
	var output bytes.Buffer
	if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "not protected by compare-and-delete") {
		t.Fatal("atomicity overclaim", output.String())
	}
}

func TestClaudeSetupUnknownProjectRecordsRefuseBeforeVendor(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`null`), []byte(`{"mcpServers":null}`), []byte(`{"mcpServers":{"oa-fixture":{"type":"stdio","command":"other","args":[],"env":{},"future":true}}}`), bytes.Repeat([]byte(" "), claudeProjectLimit+1)} {
		options, self, vendor := claudeSetupFixture(t)
		if err := os.WriteFile(filepath.Join(options.project, ".mcp.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err == nil || len(vendor.calls) != 0 {
			t.Fatal("unknown record accepted", err)
		}
		if len(claudeMarkers(t, options.state)) != 0 {
			t.Fatal("unknown record adopted")
		}
	}
}

func TestClaudeSetupIdentityChangeAndMarkerCASConflictRefuse(t *testing.T) {
	for _, which := range []string{"self-before-add", "marker-after-add"} {
		t.Run(which, func(t *testing.T) {
			options, self, vendor := claudeSetupFixture(t)
			run := func(ctx context.Context, cli, project string, args []string) ([]byte, error) {
				out, err := vendor.run(ctx, cli, project, args)
				if which == "self-before-add" && reflect.DeepEqual(args, []string{"--version"}) {
					if err := os.Rename(self, self+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(self, []byte("replaced"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if which == "marker-after-add" && len(args) > 1 && args[1] == "add" {
					// A second setup writer wins the private CAS name before this
					// operation can establish ownership. Preserve its exact bytes.
					projectHandle, _, err := setupIdentity(ctx, options.project, true)
					if err != nil {
						t.Fatal(err)
					}
					defer projectHandle.Close()
					key := sha256.Sum256([]byte(projectHandle.FileID().Key() + "\x00" + options.name))
					path := filepath.Join(options.state, "claude-setup-"+hex.EncodeToString(key[:])+".json")
					if err := os.WriteFile(path, []byte("foreign marker"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return out, err
			}
			if err := applyClaudeSetup(context.Background(), options, self, run, &bytes.Buffer{}); err == nil {
				t.Fatal("changed identity/marker accepted")
			}
			if which == "self-before-add" && len(vendor.calls) != 1 {
				t.Fatal("vendor mutation followed identity change", vendor.calls)
			}
			if which == "marker-after-add" {
				raw, err := os.ReadFile(claudeMarkers(t, options.state)[0])
				if err != nil || string(raw) != "foreign marker" {
					t.Fatal("foreign marker overwritten", err)
				}
			}
		})
	}
}

func TestClaudeSetupArgumentChild(t *testing.T) {
	if os.Getenv("OA_CLAUDE_SETUP_ARGV_CHILD") != "1" {
		return
	}
	for _, key := range []string{"DISABLE_UPDATES", "DISABLE_TELEMETRY", "DISABLE_ERROR_REPORTING", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"} {
		if os.Getenv(key) != "1" {
			os.Exit(2)
		}
	}
	if os.Getenv("ENABLE_CLAUDEAI_MCP_SERVERS") != "false" {
		os.Exit(2)
	}
	for i, arg := range os.Args {
		if arg == "--" {
			raw, _ := json.Marshal(os.Args[i+1:])
			os.Stdout.Write(raw)
			os.Exit(0)
		}
	}
	os.Exit(1)
}

func TestClaudeSetupNativeArgvKeepsSpacesAndBoundsOutput(t *testing.T) {
	t.Setenv("OA_CLAUDE_SETUP_ARGV_CHILD", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	want := []string{"project with spaces", "--state-dir", "state with spaces"}
	args := append([]string{"-test.run=^TestClaudeSetupArgumentChild$", "--"}, want...)
	raw, err := runClaudeSetupCLI(ctx, os.Args[0], t.TempDir(), args)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(string(raw), err)
	}
	var output claudeSetupOutput
	if _, err := output.Write(bytes.Repeat([]byte("x"), (32<<10)+1)); err == nil {
		t.Fatal("unbounded vendor output")
	}
}

func replaceClaudeFixtureImage(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ordinary executable upgrade fixture"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeSetupConfigurationOwnershipSurvivesExecutableUpgrade(t *testing.T) {
	for _, which := range []string{"gateway-same-path", "cli-same-path", "cli-explicit-new-path"} {
		t.Run(which, func(t *testing.T) {
			options, self, vendor := claudeSetupFixture(t)
			if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			switch which {
			case "gateway-same-path":
				replaceClaudeFixtureImage(t, self)
			case "cli-same-path":
				replaceClaudeFixtureImage(t, options.cli)
			case "cli-explicit-new-path":
				options.cli = filepath.Join(filepath.Dir(options.cli), "new selected CLI.exe")
				if err := os.WriteFile(options.cli, []byte("explicitly selected CLI fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := len(vendor.calls)
			if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
				t.Fatal("owned configuration stranded by upgrade", err)
			}
			if len(vendor.calls) != before {
				t.Fatal("idempotence rewrote registration")
			}
			options.action = "disconnect"
			if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
				t.Fatal("owned removal stranded by upgrade", err)
			}
		})
	}
}

func TestClaudeSetupForeignRecoveryWarnsThatNamedGetExecutes(t *testing.T) {
	for _, cli := range []string{"", filepath.Join(t.TempDir(), "selected CLI with spaces.exe")} {
		options := claudeSetupOptions{project: filepath.Join(t.TempDir(), "selected project with spaces"), cli: cli, name: "owned-alias"}
		var output bytes.Buffer
		claudeSetupRecovery(&output, options)
		text := output.String()
		if strings.Contains(text, "mcp remove") || !strings.Contains(text, "preserve foreign, changed or unproven") || !strings.Contains(text, "get command can start") || !strings.Contains(text, fmt.Sprintf("%q", options.name)) {
			t.Fatal(text)
		}
		if cli != "" && !strings.Contains(text, fmt.Sprintf("%q", cli)) {
			t.Fatal(text)
		}
		if cli == "" && !strings.Contains(text, "claude from PATH") {
			t.Fatal(text)
		}
	}
	options, _, vendor := claudeSetupFixture(t)
	vendor.save(map[string]any{"mcpServers": map[string]any{options.name: claudeEntry{Type: "stdio", Command: "foreign executable", Args: []string{}, Env: map[string]string{}}}})
	before, err := os.ReadFile(filepath.Join(options.project, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	exit := claudeSetupCommand([]string{"connect", "claude", "--project", options.project, "--state-dir", options.state, "--claude", options.cli, "--name", options.name}, &output, &output)
	if exit != 1 || strings.Contains(output.String(), "mcp remove") || !strings.Contains(output.String(), "preserve foreign, changed or unproven") {
		t.Fatal(exit, output.String())
	}
	if !strings.Contains(output.String(), fmt.Sprintf("%q", options.name)) || !strings.Contains(output.String(), fmt.Sprintf("%q", options.cli)) {
		t.Fatal(output.String())
	}
	after, err := os.ReadFile(filepath.Join(options.project, ".mcp.json"))
	if err != nil || !bytes.Equal(before, after) || len(claudeMarkers(t, options.state)) != 0 {
		t.Fatal("foreign registration changed", err)
	}
}

func TestClaudeSetupChangedMarkerDuringVersionPreventsMutation(t *testing.T) {
	for _, which := range []string{"removed", "replaced"} {
		t.Run(which, func(t *testing.T) {
			options, self, vendor := claudeSetupFixture(t)
			if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			marker := claudeMarkers(t, options.state)[0]
			before, err := os.ReadFile(filepath.Join(options.project, ".mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			start := len(vendor.calls)
			options.action = "disconnect"
			run := func(ctx context.Context, cli, project string, args []string) ([]byte, error) {
				out, err := vendor.run(ctx, cli, project, args)
				if reflect.DeepEqual(args, []string{"--version"}) {
					if which == "removed" {
						if err := os.Remove(marker); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.WriteFile(marker, []byte("changed marker"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				return out, err
			}
			if err := applyClaudeSetup(context.Background(), options, self, run, &bytes.Buffer{}); err == nil {
				t.Fatal("stale setup ownership used")
			}
			if len(vendor.calls) != start+1 {
				t.Fatal("vendor mutation followed changed marker", vendor.calls)
			}
			after, err := os.ReadFile(filepath.Join(options.project, ".mcp.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("registration changed", err)
			}
		})
	}
}

func TestClaudeSetupDifferentGatewayPathDoesNotAdoptOwnedConfiguration(t *testing.T) {
	options, self, vendor := claudeSetupFixture(t)
	if err := applyClaudeSetup(context.Background(), options, self, vendor.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(self), "different gateway.exe")
	if err := os.WriteFile(other, []byte("different selected gateway"), 0600); err != nil {
		t.Fatal(err)
	}
	before := len(vendor.calls)
	if err := applyClaudeSetup(context.Background(), options, other, vendor.run, &bytes.Buffer{}); err == nil || len(vendor.calls) != before {
		t.Fatal("configuration transferred to new gateway path", err)
	}
}
