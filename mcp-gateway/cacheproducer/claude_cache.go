package cacheproducer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
)

// Claude's cache key is an opaque manifest version, separate from OA's public
// release train. Build metadata is deliberately not used as an update signal.
func ClaudeGeneration(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var content []byte
	for _, name := range names {
		content = append(content, []byte(name+"\x00"+PluginHash(files[name])+"\n")...)
	}
	return "oa-cache-" + PluginHash(content)
}

func SetClaudeGeneration(files map[string][]byte) string {
	version := ClaudeGeneration(files)
	var manifest map[string]any
	if json.Unmarshal(files["plugin/.claude-plugin/plugin.json"], &manifest) != nil {
		panic("invalid embedded Claude manifest")
	}
	manifest["version"] = version
	manifest["metadata"] = map[string]string{"oaRelease": "0.8.0"}
	files["plugin/.claude-plugin/plugin.json"] = PluginJSON(manifest)
	return version
}

// The vendor owns this registry and cache. Observe only the exact owned entry;
// never repair cache files or adopt an arbitrary path reported by the CLI.
func ClaudeInstalledFolder(ctx context.Context, entry map[string]json.RawMessage, id, scope, project, version, source string) (string, error) {
	home, err := setupio.ClaudeConfigDir()
	if err != nil {
		return "", err
	}
	if version == "" || version == "." || version == ".." || len(version) > 128 || filepath.Base(version) != version || strings.ContainsAny(version, `/\\:`) {
		return "", errors.New("invalid installed plugin generation")
	}
	directory, _, err := setupIdentity(ctx, home, true)
	if err != nil {
		return "", errors.Join(ErrClaudeUpdate, errors.New("Claude plugin registry unavailable"))
	}
	defer directory.Close()
	registry, err := directory.Beneath(filepath.Join("plugins", "installed_plugins.json"), fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return "", errors.Join(ErrClaudeUpdate, errors.New("Claude plugin registry unavailable"))
	}
	defer registry.Close()
	raw, err := readClaudePluginHeld(ctx, registry)
	if err != nil {
		return "", err
	}
	var record struct {
		Version int `json:"version"`
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			ProjectPath string `json:"projectPath"`
			InstallPath string `json:"installPath"`
			Version     string `json:"version"`
		} `json:"plugins"`
	}
	if json.Unmarshal(raw, &record) != nil || record.Version != 2 {
		return "", errors.New("Claude plugin registry format unavailable; update required")
	}
	market := strings.TrimPrefix(id, "openabstractions@")
	expected := filepath.Join(home, "plugins", "cache", market, "openabstractions", version)
	matches := 0
	selected := ""
	for _, row := range record.Plugins[id] {
		if row.Scope != scope || row.ProjectPath != project {
			continue
		}
		matches++
		selected = filepath.Clean(row.InstallPath)
		if row.Version != version || selected != expected && selected != source {
			return "", errors.New("Claude installed plugin provenance differs; preserved")
		}
	}
	if matches != 1 || PluginString(entry, "installPath") != "" && filepath.Clean(PluginString(entry, "installPath")) != selected {
		return "", errors.New("Claude installed plugin path missing or ambiguous; update required")
	}
	current, err := directory.Beneath(filepath.Join("plugins", "installed_plugins.json"), fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return "", errors.New("Claude plugin registry changed")
	}
	defer current.Close()
	currentRaw, readErr := readClaudePluginHeld(ctx, current)
	if readErr != nil || PluginHash(currentRaw) != PluginHash(raw) || current.FileID().Key() != registry.FileID().Key() {
		return "", errors.New("Claude plugin registry changed")
	}
	return selected, nil
}

func verifyClaudePluginCopies(ctx context.Context, folders []string, files map[string][]byte) error {
	if len(folders) == 0 {
		return ErrClaudeUpdate
	}
	for _, folder := range folders {
		if err := VerifyClaudeCopy(ctx, folder, files); err != nil {
			return err
		}
	}
	return nil
}

func readClaudePluginHeld(ctx context.Context, h fileid.Handle) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := h.File().Stat()
	if err != nil || !info.Mode().IsRegular() || h.FileID().Links() != 1 {
		return nil, errors.New("Claude plugin file must be regular and single-link")
	}
	b, err := io.ReadAll(io.LimitReader(h.File(), claudePayloadLimit+1))
	if err != nil || len(b) > claudePayloadLimit {
		return nil, errors.New("Claude plugin file unavailable or oversized")
	}
	return b, ctx.Err()
}

func VerifyClaudeCopy(ctx context.Context, folder string, files map[string][]byte) error {
	directory, _, err := setupIdentity(ctx, folder, true)
	if err != nil {
		return ErrClaudeUpdate
	}
	defer directory.Close()
	for name, expected := range files {
		if !strings.HasPrefix(name, "plugin/") {
			continue
		}
		rel := filepath.FromSlash(strings.TrimPrefix(name, "plugin/"))
		h, err := directory.Beneath(rel, fileid.Options{Links: fileid.RefuseLinks, Read: true})
		if err != nil {
			return ErrClaudeUpdate
		}
		actual, readErr := readClaudePluginHeld(ctx, h)
		current, currentErr := directory.Beneath(rel, fileid.Options{Links: fileid.RefuseLinks})
		same := currentErr == nil && current.FileID().Equal(h.FileID())
		if currentErr == nil {
			current.Close()
		}
		h.Close()
		if readErr != nil || !same || PluginHash(actual) != PluginHash(expected) {
			return ErrClaudeUpdate
		}
	}
	if !setupIdentitiesCurrent(ctx, []setupIdentityRef{directory}) {
		return ErrClaudeUpdate
	}
	return nil
}
