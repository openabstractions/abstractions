package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeWatchDisabledProjectionPreservesPreexistingApprovals(t *testing.T) {
	for _, disabledRule := range []string{claudeWatchRule, claudeAsksWatchRule} {
		t.Run(disabledRule, func(t *testing.T) {
			settings := string(pluginJSON(map[string]any{"permissions": map[string]any{"allow": []string{claudeWatchRule, claudeAsksWatchRule}}, "foreign": true}))
			root, state, cli, settingsPath := watchFixture(t, settings)
			for _, rule := range []string{claudeWatchRule, claudeAsksWatchRule} {
				if got, _, err := applyClaudeWatchFor(context.Background(), "enable", state, cli, root, rule); err != nil || got != "preexisting" {
					t.Fatal(got, err)
				}
			}
			o := claudePluginOptions{action: "install", scope: "user", state: state, cli: cli}
			vendor := newPluginVendorFixture(t, state, root)
			vendor.scope, vendor.market, vendor.root = "user", "oa-local-user", filepath.Join(state, "claude-plugin-user")
			if err := applyClaudePlugin(context.Background(), o, cli, vendor.run); err != nil {
				t.Fatal(err)
			}
			if got, preserved, err := applyClaudeWatchFor(context.Background(), "disable", state, cli, root, disabledRule); err != nil || got != "disabled" || !preserved {
				t.Fatal(got, preserved, err)
			}
			_, receipt, err := readClaudeWatchReceipt(context.Background(), claudeWatchReceiptFile(state, disabledRule))
			if err != nil {
				t.Fatal(err)
			}
			module, err := claudeBackgroundModule(context.Background(), state, cli, receipt.State)
			if err != nil {
				t.Fatal(err)
			}
			want := `const backgroundDisabled = {"asks":false,"messages":true};`
			if disabledRule == claudeAsksWatchRule {
				want = `const backgroundDisabled = {"asks":true,"messages":false};`
			}
			if !bytes.Contains(module, []byte(want)) {
				t.Fatal("disabled scope lost in reload projection")
			}
			if err := applyClaudePlugin(context.Background(), o, cli, vendor.run); err != nil {
				t.Fatal("owned projection refresh", err)
			}
			installed, err := os.ReadFile(filepath.Join(vendor.root, "plugin", "hooks", "register.js"))
			if err != nil || !bytes.Equal(module, installed) {
				t.Fatal("installed reload projection differs", err)
			}
			after, err := os.ReadFile(settingsPath)
			if err != nil || string(after) != settings {
				t.Fatal("person approval or foreign settings changed", err)
			}
		})
	}
}

// ASK-R14: an unreviewed Asks scope rides the Messages watch, so only the
// Messages Disable holds the module's wakes.
func TestClaudeWatchUnreviewedAsksAndAbsentDisableProjection(t *testing.T) {
	root, state, cli, _ := watchFixture(t, `{}`)
	if got, _, err := applyClaudeWatch(context.Background(), "disable", state, cli, root); err != nil || got != "disabled" {
		t.Fatal(got, err)
	}
	_, receipt, err := readClaudeWatchReceipt(context.Background(), claudeWatchReceiptFile(state, claudeWatchRule))
	if err != nil {
		t.Fatal(err)
	}
	module, err := claudeBackgroundModule(context.Background(), state, cli, receipt.State)
	if err != nil || !bytes.Contains(module, []byte(`const backgroundDisabled = {"asks":false,"messages":true};`)) {
		t.Fatal("Messages Disable lost or unreviewed Asks held", err)
	}
}

func TestClaudeAsksWatchApprovalIsSeparateAndUndoPreservesMessages(t *testing.T) {
	root, state, cli, path := watchFixture(t, `{"permissions":{"allow":["mcp__OpenAbstractions__oa_messages_watch"]},"foreign":true}`)
	if got, _, err := applyClaudeWatch(context.Background(), "enable", state, cli, root); err != nil || got != "preexisting" {
		t.Fatal(got, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte(claudeAsksWatchRule)) {
		t.Fatal("header approval silently expanded")
	}
	if got, _, err := applyClaudeWatchFor(context.Background(), "status", state, cli, root, claudeAsksWatchRule); err != nil || got != "unreviewed" {
		t.Fatal(got, err)
	}
	if got, _, err := applyClaudeWatchFor(context.Background(), "enable", state, cli, root, claudeAsksWatchRule); err != nil || got != "owned" {
		t.Fatal(got, err)
	}
	if got, _, err := applyClaudeWatchFor(context.Background(), "disable", state, cli, root, claudeAsksWatchRule); err != nil || got != "disabled" {
		t.Fatal(got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("Asks undo changed message/foreign settings", string(after), err)
	}
	if _, _, _, err := claudeWatchInsertionFor([]byte(`{"permissions":{"deny":["mcp__OpenAbstractions__oa_asks_watch"]}}`), claudeAsksWatchRule); err == nil {
		t.Fatal("existing Asks denial overridden")
	}
}

func TestClaudeWatchInterruptedOwnershipRecovery(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-write", true: "after-write"}[applied], func(t *testing.T) {
			root, state, cli, path := watchFixture(t, `{"foreign":true}`)
			if _, _, err := applyClaudeWatch(context.Background(), "enable", state, cli, root); err != nil {
				t.Fatal(err)
			}
			receiptPath := filepath.Join(state, "claude-watch-permission.json")
			encoded, _ := os.ReadFile(receiptPath)
			var receipt claudeWatchReceipt
			if err := json.Unmarshal(encoded, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt.Status = "pending"
			encoded = pluginJSON(receipt)
			if err := os.WriteFile(receiptPath, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			if !applied {
				if err := os.WriteFile(path, []byte(`{"foreign":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := applyClaudeWatch(context.Background(), "disable", state, cli, root); err != nil {
				t.Fatal(err)
			}
			actual, _ := os.ReadFile(path)
			if string(actual) != `{"foreign":true}` {
				t.Fatal("interrupted write undo changed foreign bytes")
			}
		})
	}
}

func watchFixture(t *testing.T, raw string) (string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	state := filepath.Join(t.TempDir(), "state")
	cli := filepath.Join(t.TempDir(), "claude.exe")
	if err := os.WriteFile(cli, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "settings.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return root, state, cli, path
}

func TestClaudeWatchOwnedUndoPreservesBytes(t *testing.T) {
	for _, raw := range []string{"{\n  \"foreign\": {\"secret\":\"opaque\"}\n}\n", `{"permissions":{"allow":["Read"],"foreign":42},"x":true}`, `{"permissions":{}}`, `{}`} {
		t.Run(raw, func(t *testing.T) {
			root, state, cli, path := watchFixture(t, raw)
			got, _, err := applyClaudeWatch(context.Background(), "enable", state, cli, root)
			if err != nil || got != "owned" {
				t.Fatalf("enable %s %v", got, err)
			}
			enabled, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(enabled, []byte(claudeWatchRule)) {
				t.Fatal("missing rule")
			}
			got, _, err = applyClaudeWatch(context.Background(), "disable", state, cli, root)
			if err != nil || got != "disabled" {
				t.Fatalf("disable %s %v", got, err)
			}
			restored, _ := os.ReadFile(path)
			if string(restored) != raw {
				t.Fatalf("foreign bytes changed: %s", restored)
			}
		})
	}
}

func TestClaudeWatchPreexistingPreserved(t *testing.T) {
	raw := `{"permissions":{"allow":["` + claudeWatchRule + `"]},"private":"unchanged"}`
	root, state, cli, path := watchFixture(t, raw)
	status, preserved, err := applyClaudeWatch(context.Background(), "enable", state, cli, root)
	if err != nil || status != "preexisting" || !preserved {
		t.Fatalf("%s %v %v", status, preserved, err)
	}
	status, preserved, err = applyClaudeWatch(context.Background(), "disable", state, cli, root)
	if err != nil || status != "disabled" || !preserved {
		t.Fatalf("%s %v %v", status, preserved, err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != raw {
		t.Fatal("preexisting rule changed")
	}
}

func TestClaudeWatchRefusesAmbiguousAndRestrictedSettings(t *testing.T) {
	for _, raw := range []string{`{broken}`, `{"permissions":{},"permissions":{}}`, `{"permissions":{"allow":[],"allow":[]}}`, `{"permissions":{"deny":["mcp__OpenAbstractions__*"]}}`, `{"permissions":{"ask":["mcp__OpenAbstractions"]}}`, `{"permissions":{"allow":null}}`, `{/*comment*/}`} {
		root, state, cli, path := watchFixture(t, raw)
		if _, _, err := applyClaudeWatch(context.Background(), "enable", state, cli, root); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		actual, _ := os.ReadFile(path)
		if string(actual) != raw {
			t.Fatal("rejected settings changed")
		}
	}
}

func TestClaudeWatchRestrictionsRetainUnrelatedRules(t *testing.T) {
	for _, rule := range []string{"Bash", "Bash(rm *)", "Read(./.env)", "B*", "mcp__other", "mcp__other__*", "mcp__OpenAbstractions__oa_messages_send"} {
		for _, kind := range []string{"ask", "deny"} {
			t.Run(kind+rule, func(t *testing.T) {
				encoded, _ := json.Marshal(rule)
				raw := `{"permissions":{"` + kind + `":[` + string(encoded) + `]},"foreign":true}`
				root, state, cli, p := watchFixture(t, raw)
				status, _, err := applyClaudeWatch(context.Background(), "enable", state, cli, root)
				if err != nil || status != "owned" {
					t.Fatalf("unrelated rule blocked: %s %v", status, err)
				}
				if _, _, err := applyClaudeWatch(context.Background(), "disable", state, cli, root); err != nil {
					t.Fatal(err)
				}
				actual, _ := os.ReadFile(p)
				if string(actual) != raw {
					t.Fatal("unrelated rule bytes changed")
				}
			})
		}
	}
}

func TestClaudeWatchRestrictionsRefuseMatchingRules(t *testing.T) {
	for _, rule := range []string{claudeWatchRule, "mcp__OpenAbstractions", "mcp__OpenAbstractions__*", "mcp__OpenAbstractions__oa_messages_*", "mcp__*", "*", "*watch", "mcp__Open*__*", "mcp__OpenAbstractions__oa_messages_?", "mcp__[OpenAbstractions]__*"} {
		for _, kind := range []string{"ask", "deny"} {
			t.Run(kind+rule, func(t *testing.T) {
				encoded, _ := json.Marshal(rule)
				raw := `{"permissions":{"` + kind + `":[` + string(encoded) + `]}}`
				root, state, cli, p := watchFixture(t, raw)
				if _, _, err := applyClaudeWatch(context.Background(), "enable", state, cli, root); err == nil {
					t.Fatal("matching or undecidable rule accepted")
				}
				actual, _ := os.ReadFile(p)
				if string(actual) != raw {
					t.Fatal("restriction changed")
				}
			})
		}
	}
}

func TestClaudeWatchRefusesForeignEditUndoAndCancelledEnable(t *testing.T) {
	root, state, cli, path := watchFixture(t, `{}`)
	if _, _, err := applyClaudeWatch(context.Background(), "enable", state, cli, root); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	changed := append(raw, ' ')
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := applyClaudeWatch(context.Background(), "disable", state, cli, root); err == nil {
		t.Fatal("foreign edit undo accepted")
	}
	actual, _ := os.ReadFile(path)
	if !bytes.Equal(actual, changed) {
		t.Fatal("foreign edit changed")
	}
	root, state, cli, path = watchFixture(t, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := applyClaudeWatch(ctx, "enable", state, cli, root); err == nil {
		t.Fatal("cancel accepted")
	}
	actual, _ = os.ReadFile(path)
	if string(actual) != `{}` {
		t.Fatal("cancel mutated settings")
	}
}

func TestClaudeWatchStatusDoesNotReadProfile(t *testing.T) {
	root, state, cli, _ := watchFixture(t, `{broken}`)
	status, _, err := applyClaudeWatch(context.Background(), "status", state, cli, filepath.Join(root, "absent"))
	if err != nil || status != "unreviewed" {
		t.Fatalf("%s %v", status, err)
	}
}

func TestClaudeWatchCommandFixtureEnableDisable(t *testing.T) {
	root, state, cli, path := watchFixture(t, `{"fixture":"retained"}`)
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	for _, action := range []string{"status", "enable", "disable", "status"} {
		var output, diagnostics bytes.Buffer
		code := claudeWatchCommand([]string{"watch-permission", "claude", action, "--state-dir", state, "--claude", cli}, &output, &diagnostics)
		if code != 0 {
			t.Fatalf("%s exit %d: %s", action, code, diagnostics.String())
		}
		var result struct{ Outcome, State string }
		if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Outcome != "page" {
			t.Fatalf("invalid reply %s: %v", output.String(), err)
		}
		if action == "disable" && result.State != "disabled" {
			t.Fatalf("undo state %q", result.State)
		}
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != `{"fixture":"retained"}` {
		t.Fatal("command fixture did not undo owned change")
	}
}

func TestClaudeWatchCombinedScopesUndoEitherOrder(t *testing.T) {
	for _, raw := range []string{`{}`, `{"permissions":{}}`, "{\n  \"foreign\": true\n}\n", `{"permissions":{"allow":["Read"]}}`} {
		for _, first := range []string{claudeWatchRule, claudeAsksWatchRule} {
			for _, reverse := range []bool{false, true} {
				t.Run(raw+first+map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
					root, state, cli, p := watchFixture(t, raw)
					second := claudeWatchRule
					if first == second {
						second = claudeAsksWatchRule
					}
					for _, rule := range []string{first, second} {
						if _, _, err := applyClaudeWatchFor(context.Background(), "enable", state, cli, root, rule); err != nil {
							t.Fatal(err)
						}
					}
					for _, rule := range []string{first, second} {
						if got, _, err := applyClaudeWatchFor(context.Background(), "enable", state, cli, root, rule); err != nil || got != "owned" {
							t.Fatal(got, err)
						}
					}
					remove, keep := first, second
					if reverse {
						remove, keep = second, first
					}
					if _, _, err := applyClaudeWatchFor(context.Background(), "disable", state, cli, root, remove); err != nil {
						t.Fatal(err)
					}
					current, _ := os.ReadFile(p)
					if bytes.Contains(current, []byte(remove)) || !bytes.Contains(current, []byte(keep)) {
						t.Fatal(string(current))
					}
					if _, _, err := applyClaudeWatchFor(context.Background(), "disable", state, cli, root, keep); err != nil {
						t.Fatal(err)
					}
					current, _ = os.ReadFile(p)
					if string(current) != raw {
						t.Fatalf("foreign bytes changed: %s", current)
					}
				})
			}
		}
	}
}

func TestClaudeWatchCombinedScopesForeignEditPreserved(t *testing.T) {
	root, state, cli, p := watchFixture(t, `{}`)
	for _, rule := range []string{claudeWatchRule, claudeAsksWatchRule} {
		if _, _, err := applyClaudeWatchFor(context.Background(), "enable", state, cli, root, rule); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(p)
	raw = append(raw, ' ')
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{claudeWatchRule, claudeAsksWatchRule} {
		if _, _, err := applyClaudeWatchFor(context.Background(), "disable", state, cli, root, rule); err == nil {
			t.Fatal("foreign edit accepted")
		}
	}
	current, _ := os.ReadFile(p)
	if !bytes.Equal(current, raw) {
		t.Fatal("foreign edit changed")
	}
}

func TestClaudeWatchCombinedUndoRecoversReceiptBoundaries(t *testing.T) {
	for _, boundary := range []string{"intent", "settings", "sibling"} {
		for _, recoverRule := range []string{claudeWatchRule, claudeAsksWatchRule} {
			t.Run(boundary+recoverRule, func(t *testing.T) {
				root, state, cli, p := watchFixture(t, `{"foreign":true}`)
				for _, rule := range []string{claudeWatchRule, claudeAsksWatchRule} {
					if _, _, err := applyClaudeWatchFor(context.Background(), "enable", state, cli, root, rule); err != nil {
						t.Fatal(err)
					}
				}
				settingsBefore, _ := os.ReadFile(p)
				targetPath, siblingPath := claudeWatchReceiptFile(state, claudeWatchRule), claudeWatchReceiptFile(state, claudeAsksWatchRule)
				targetBefore, _ := os.ReadFile(targetPath)
				siblingBefore, _ := os.ReadFile(siblingPath)
				if _, _, err := applyClaudeWatchFor(context.Background(), "disable", state, cli, root, claudeWatchRule); err != nil {
					t.Fatal(err)
				}
				settingsAfter, _ := os.ReadFile(p)
				siblingAfter, _ := os.ReadFile(siblingPath)
				var target, sibling claudeWatchReceipt
				if err := json.Unmarshal(targetBefore, &target); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(siblingAfter, &sibling); err != nil {
					t.Fatal(err)
				}
				target.Status, target.Undo, target.Rebased, target.SiblingBefore = "undo-pending", pluginHash(settingsAfter), &sibling, pluginHash(siblingBefore)
				if err := os.WriteFile(targetPath, pluginJSON(target), 0600); err != nil {
					t.Fatal(err)
				}
				if boundary != "sibling" {
					if err := os.WriteFile(siblingPath, siblingBefore, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if boundary == "intent" {
					if err := os.WriteFile(p, settingsBefore, 0600); err != nil {
						t.Fatal(err)
					}
					recoverRule = claudeWatchRule
				}
				if _, _, err := applyClaudeWatchFor(context.Background(), "disable", state, cli, root, recoverRule); err != nil {
					t.Fatal(err)
				}
				if recoverRule == claudeWatchRule {
					if _, _, err := applyClaudeWatchFor(context.Background(), "disable", state, cli, root, claudeAsksWatchRule); err != nil {
						t.Fatal(err)
					}
				}
				current, _ := os.ReadFile(p)
				if string(current) != `{"foreign":true}` {
					t.Fatal(string(current))
				}
			})
		}
	}
}

func TestClaudeWatchPreexistingScopeDisablePreservesOwnedSibling(t *testing.T) {
	for _, preexisting := range []string{claudeWatchRule, claudeAsksWatchRule} {
		for _, reverse := range []bool{false, true} {
			t.Run(preexisting+map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
				raw := `{"permissions":{"allow":["` + preexisting + `"]},"foreign":true}`
				root, state, cli, p := watchFixture(t, raw)
				owned := claudeWatchRule
				if owned == preexisting {
					owned = claudeAsksWatchRule
				}
				for _, rule := range []string{preexisting, owned} {
					if _, _, err := applyClaudeWatchFor(context.Background(), "enable", state, cli, root, rule); err != nil {
						t.Fatal(err)
					}
				}
				order := []string{preexisting, owned}
				if reverse {
					order = []string{owned, preexisting}
				}
				for _, rule := range order {
					if _, _, err := applyClaudeWatchFor(context.Background(), "disable", state, cli, root, rule); err != nil {
						t.Fatal(err)
					}
				}
				current, _ := os.ReadFile(p)
				if string(current) != raw {
					t.Fatal(string(current))
				}
			})
		}
	}
}
