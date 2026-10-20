package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// seedGuestDependencies records, in a sandboxed guest's own config folder,
// each peer dependency the installed plugin package already carries, so the
// guest's start-up installer finds them present and never reaches for a
// registry the sandbox cannot reach.
//
// The guest's installer (live run 2026-10-06) skips a folder when it holds
// node_modules and every dependency its package.json declares is listed in
// package-lock.json's root package. A plugin package installed by npm carries
// its peers in its own node_modules, where the plugin resolves them; nothing
// in the guest's config folder imports them. The record therefore names what
// the plugin package holds, at the versions it holds, and adds no code.
//
// pluginRoot is the folder holding the plugin's node_modules; pluginManifest
// is the plugin's own package.json. A peer the package does not carry is not
// recorded, and the guest's installer still tries the network for it.
func seedGuestDependencies(configDir, pluginRoot, pluginManifest string) ([]string, error) {
	var plugin struct {
		Peers map[string]string `json:"peerDependencies"`
	}
	raw, err := os.ReadFile(pluginManifest)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &plugin); err != nil {
		return nil, fmt.Errorf("%s: %w", pluginManifest, err)
	}
	carried := map[string]string{}
	for name := range plugin.Peers {
		var peer struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		raw, err := os.ReadFile(filepath.Join(pluginRoot, "node_modules", filepath.FromSlash(name), "package.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &peer) != nil || peer.Name != name || peer.Version == "" {
			return nil, fmt.Errorf("the plugin package's %s has no matching name and version", name)
		}
		carried[name] = peer.Version
	}
	if len(carried) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Join(configDir, "node_modules"), 0o700); err != nil {
		return nil, err
	}
	manifest := map[string]any{"private": true, "dependencies": carried}
	lock := map[string]any{"lockfileVersion": 3, "requires": true,
		"packages": map[string]any{"": map[string]any{"dependencies": carried}}}
	for name, value := range map[string]any{"package.json": manifest, "package-lock.json": lock} {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(configDir, name), append(data, '\n'), 0o600); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(carried))
	for name, version := range carried {
		names = append(names, name+"@"+version)
	}
	sort.Strings(names)
	return names, nil
}
