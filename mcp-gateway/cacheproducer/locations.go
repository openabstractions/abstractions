package cacheproducer

import (
	"encoding/json"
	"path/filepath"
)

const claudeSourceKeyCharacters = 24
const codexSourceKeyBytes = 8

// Producer placement and receipt discovery use the same location owner.
func claudePluginPaths(state, key string) (root, receipt string) {
	root = filepath.Join(state, "claude-plugin-"+key)
	return root, root + ".json"
}

func codexHookCatalog(market string) []byte {
	catalog, _ := json.Marshal(map[string]any{"name": market, "plugins": []any{map[string]any{"name": codexHookPlugin, "source": map[string]any{"source": "local", "path": "./plugins/" + codexHookPlugin}, "policy": map[string]any{"installation": "AVAILABLE", "authentication": "ON_INSTALL"}, "category": "Productivity"}}})
	return catalog
}

func codexHookPaths(state, home, key string) (root, plugin, cache, receipt, catalog string) {
	root = filepath.Join(state, "codex-session-start-"+key)
	plugin = filepath.Join(root, "plugins", codexHookPlugin)
	cache = filepath.Join(home, "plugins", "cache", "oa-session-"+key, codexHookPlugin, codexHookVersion)
	return root, plugin, cache, root + ".json", filepath.Join(root, ".agents", "plugins", "marketplace.json")
}
