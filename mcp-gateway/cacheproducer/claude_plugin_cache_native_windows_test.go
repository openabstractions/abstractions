package cacheproducer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

const claudeNativePhaseTimeout = 45 * time.Second

func TestClaudePluginCacheNativeManifest(t *testing.T) {
	program := os.Getenv("OA_CLAUDE_CACHE_VALIDATION_CLI")
	if program == "" {
		if os.Getenv("OA_CLAUDE_CACHE_VALIDATION_REQUIRED") == "1" {
			t.Fatal("required Claude Mod validation needs an admitted CLI path and SHA256")
		}
		t.Skip("explicit held-CLI disposable manifest validation")
	}
	expected := os.Getenv("OA_CLAUDE_CACHE_VALIDATION_SHA256")
	if os.Getenv("OA_CLAUDE_CACHE_VALIDATION_REQUIRED") == "1" && expected == "" {
		t.Fatal("required Claude Mod validation needs an admitted CLI SHA256")
	}
	if !filepath.IsAbs(program) {
		t.Fatal("Claude validator path must be absolute")
	}
	ctx, cancel := context.WithTimeout(t.Context(), claudeNativePhaseTimeout)
	defer cancel()
	image, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	if expected != "" {
		digest := sha256.New()
		if _, err := io.Copy(digest, image.File()); err != nil || hex.EncodeToString(digest.Sum(nil)) != expected {
			t.Fatal("selected Claude validator differs from its admitted SHA256", err)
		}
	}
	id := image.FileID()
	stored, err := id.Stored()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	cli, err := nativecli.SelectClaudeSavedCLI(ctx, []string{stored.String()}, func(ctx context.Context, path string) ([]byte, error) {
		return RunClaude(ctx, path, root, []string{"--version"})
	})
	if err != nil || cli == nil {
		t.Fatal("held CLI unavailable", err)
	}
	defer cli.Close()
	if err := privatedir.Ensure(filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "state", "claude-plugin-user")
	for _, dir := range []string{payload, filepath.Join(payload, ".claude-plugin"), filepath.Join(payload, "plugin"), filepath.Join(payload, "plugin", ".claude-plugin"), filepath.Join(payload, "plugin", "hooks")} {
		if err := privatedir.Ensure(dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "gateway.exe"), []byte("inert setup identity; never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	files := ClaudeFilesForScope("oa-local-user", filepath.Join(root, "gateway.exe"), filepath.Join(root, "state"), "user")
	state, stateID, err := setupIdentity(ctx, filepath.Join(root, "state"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	// Validate the canonical user-scope projection that the setup owner ships.
	// The native validator enforces module guards beyond the Node test mocks.
	files["plugin/hooks/register.js"], err = ClaudeBackgroundModule(ctx, filepath.Join(root, "state"), cli.Program(), stateID)
	if err != nil {
		t.Fatal(err)
	}
	generation := SetClaudeGeneration(files)
	for name, b := range files {
		path := filepath.Join(root, "state", "claude-plugin-user", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cli.Check(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := RunClaude(ctx, cli.Program(), root, []string{"plugin", "validate", filepath.Join(root, "state", "claude-plugin-user", "plugin"), "--json"})
	if err != nil {
		t.Fatalf("native generation validation: %v %s", err, raw)
	}
	if !claudeNativeManifestHasHooks(raw) {
		t.Fatalf("native generation refused: %s", raw)
	}
	if err := cli.Check(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("held Claude Code %s validated canonical rendered module sha256=%s generation=%s with reported hooks; disposable validation only", cli.Version(), PluginHash(files["plugin/hooks/register.js"]), generation)
	if os.Getenv("OA_CLAUDE_CACHE_VALIDATE_ONLY") == "1" {
		return
	}
	exerciseClaudePluginNativeCacheUpdate(t, ctx, cli.Program(), root, files, generation)
}

func claudeNativeManifestHasHooks(raw []byte) bool {
	var result struct {
		Success  bool `json:"success"`
		Contents []struct {
			Type        string `json:"type"`
			GatingHooks []struct {
				Pattern string `json:"pattern"`
			} `json:"gatingHooks"`
		} `json:"contents"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Success {
		return false
	}
	for _, content := range result.Contents {
		if content.Type == "hooks" {
			for _, hook := range content.GatingHooks {
				if hook.Pattern != "" {
					return true
				}
			}
		}
	}
	return false
}

func TestClaudeNativeManifestRequiresReportedHooks(t *testing.T) {
	for _, raw := range []string{`{"success":true}`, `{"success":true,"contents":[{"type":"hooks","gatingHooks":[]}]}`, `{"success":false,"contents":[{"type":"hooks","gatingHooks":[{"pattern":"prompt.submit"}]}]}`} {
		if claudeNativeManifestHasHooks([]byte(raw)) {
			t.Fatal("empty or failed hook validation accepted", raw)
		}
	}
	if !claudeNativeManifestHasHooks([]byte(`{"success":true,"contents":[{"type":"hooks","gatingHooks":[{"pattern":"prompt.submit"}]}]}`)) {
		t.Fatal("reported native hook refused")
	}
}

func exerciseClaudePluginNativeCacheUpdate(t *testing.T, ctx context.Context, cli, root string, files map[string][]byte, generation string) {
	t.Helper()
	run := func(args ...string) []byte {
		raw, err := RunClaude(ctx, cli, root, args)
		if err != nil {
			t.Fatalf("disposable native %v: %v %s", args[:2], err, raw)
		}
		return raw
	}
	const market = "oa-local-user"
	const pluginID = "openabstractions@oa-local-user"
	run("plugin", "marketplace", "add", filepath.Join(root, "state", "claude-plugin-user"), "--scope", "user", "--json")
	old := filepath.Join(root, "plugins", "cache", market, "openabstractions", "0.8.0")
	for name, b := range files {
		if len(name) < len("plugin/") || name[:len("plugin/")] != "plugin/" {
			continue
		}
		if name == "plugin/.claude-plugin/plugin.json" {
			var manifest map[string]any
			if json.Unmarshal(b, &manifest) != nil {
				t.Fatal("old manifest")
			}
			manifest["version"] = "0.8.0"
			b = PluginJSON(manifest)
		}
		if name == "plugin/hooks/register.js" {
			b = append(append([]byte(nil), b...), []byte("\n// stale cached predecessor\n")...)
		}
		path := filepath.Join(old, filepath.FromSlash(name[len("plugin/"):]))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	registry := map[string]any{"version": 2, "plugins": map[string]any{pluginID: []any{map[string]any{"scope": "user", "installPath": old, "version": "0.8.0", "installedAt": "2026-10-28T00:00:00.000Z", "lastUpdated": "2026-10-28T00:00:00.000Z"}}}}
	if err := os.WriteFile(filepath.Join(root, "plugins", "installed_plugins.json"), PluginJSON(registry), 0600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(root, "settings.json")
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if json.Unmarshal(b, &settings) != nil {
		t.Fatal("fixture settings")
	}
	settings["enabledPlugins"] = map[string]bool{pluginID: true}
	if err := os.WriteFile(settingsPath, PluginJSON(settings), 0600); err != nil {
		t.Fatal(err)
	}
	run("plugin", "marketplace", "update", market, "--json")
	run("plugin", "update", pluginID, "--scope", "user", "--json")
	verifyReadback := func() {
		entries, err := PluginList(run("plugin", "list", "--json"))
		if err != nil {
			t.Fatal(err)
		}
		matched := false
		for _, entry := range entries {
			if PluginString(entry, "id") != pluginID || PluginString(entry, "scope") != "user" {
				continue
			}
			matched = true
			t.Logf("disposable native update version=%s folderVersion=%s inPlace=%t", PluginString(entry, "version"), PluginString(entry, "folderVersion"), PluginString(entry, "readFromFolder") != "")
			if PluginString(entry, "version") != generation {
				t.Fatalf("native update retained old cache generation: %s", PluginString(entry, "version"))
			}
			folder, err := ClaudeInstalledFolder(ctx, entry, pluginID, "user", "", generation, filepath.Join(root, "state", "claude-plugin-user", "plugin"))
			if err != nil {
				t.Fatal("actual installed copy provenance", err)
			}
			if err := VerifyClaudeCopy(ctx, folder, files); err != nil {
				t.Fatal("actual installed cache bytes", err)
			}
		}
		if !matched {
			t.Fatal("native updated plugin absent")
		}
	}
	verifyReadback()
	// An equal-generation second update must preserve the same verified bytes.
	run("plugin", "update", pluginID, "--scope", "user", "--json")
	verifyReadback()
	t.Log("disposable native old0.8.0 cache refreshed to opaque generation; second same-generation update completed without a session")
	// Give the normal setup owner custody of this exact disposable installation.
	// A successfully published update loses its response before marker commit.
	// Its recovery has a separate deadline from the CLI cache exercise above.
	ownerCtx, cancel := context.WithTimeout(t.Context(), claudeNativePhaseTimeout)
	defer cancel()
	state := filepath.Join(root, "state")
	self := filepath.Join(root, "gateway.exe")
	identity := func(path string, directory bool) string {
		h, stored, err := setupIdentity(ownerCtx, path, directory)
		if err != nil {
			t.Fatal(err)
		}
		h.Close()
		return stored
	}
	stateID := identity(state, true)
	marker := ClaudeMarker{Project: stateID, State: stateID, Self: identity(self, false), CLI: identity(cli, false), Marketplace: market, Scope: "user", Files: map[string]string{}, MarketplaceOwned: true, PluginOwned: true, InstalledVersion: generation}
	for name, b := range files {
		marker.Files[name] = PluginHash(b)
	}
	if err := os.WriteFile(filepath.Join(state, "claude-plugin-user.json"), PluginJSON(marker), 0600); err != nil {
		t.Fatal(err)
	}
	lost := false
	loseUpdate := func(ctx context.Context, command, project string, args []string) ([]byte, error) {
		raw, err := RunClaude(ctx, command, project, args)
		if err == nil && !lost && len(args) > 1 && args[0] == "plugin" && args[1] == "update" {
			lost = true
			return nil, errors.New("fixture lost successful vendor update response")
		}
		return raw, err
	}
	options := ClaudeOptions{Action: "update", Scope: "user", State: state, CLI: cli}
	if err := ApplyClaude(ownerCtx, options, self, loseUpdate); err == nil || !lost {
		t.Fatal("native lost-response fixture did not stop", err)
	}
	if err := ApplyClaude(ownerCtx, options, self, RunClaude); err != nil {
		t.Fatal("normal setup owner recovery", err)
	}
	options.Action = "status"
	if err := ApplyClaude(ownerCtx, options, self, RunClaude); err != nil {
		t.Fatal("actual native copy after retained recovery", err)
	}
	t.Log("normal setup owner retained prior generation across actual vendor publication/lost reply; retry verified both actual copies without another approval")
}
