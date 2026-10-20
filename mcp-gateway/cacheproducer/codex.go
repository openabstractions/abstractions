package cacheproducer

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

	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

type CodexOptions struct{ Action, State, CLI, Name, Program, FileKey string }
type CodexResult struct {
	Output, Diagnostic []byte
	Code               int
}
type CodexRunner func(context.Context, string, string, []string) (CodexResult, error)

const codexSessionStartTool = "oa_codex_session_start"
const codexHookPlugin = "oa-codex-session-start"
const codexHookVersion = "0.8.0"

var ErrCodexPreserved = errors.New("Codex SessionStart plugin " + codexHookPlugin + " preserved")

func codexHookPreserved(reason string) error {
	return fmt.Errorf("%w: %s", ErrCodexPreserved, reason)
}

type codexHookOwnership struct {
	Scope, State, Name, Cache string
	Market, Plugin            json.RawMessage
	Program, FileKey, Version string            `json:",omitempty"`
	Files, CacheFiles         map[string]string `json:",omitempty"`
}

func CodexFiles(server string) map[string][]byte {
	manifest, _ := json.Marshal(map[string]any{"name": codexHookPlugin, "version": codexHookVersion, "description": "OpenAbstractions native Codex session receiving", "category": "Productivity", "interface": map[string]any{"category": "Productivity"}, "hooks": "./hooks/hooks.json"})
	hooks, _ := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"matcher": "^(startup|resume)$", "hooks": []any{map[string]any{"type": "mcp_tool", "server": server, "tool": codexSessionStartTool, "input": map[string]any{}}}}}}})
	return map[string][]byte{".codex-plugin/plugin.json": manifest, "hooks/hooks.json": hooks}
}

func codexHookTree(root string, files map[string][]byte, cached bool) error {
	if err := codexHookInventory(root, false); err != nil {
		return err
	}
	if _, err := codexHookMembers(context.Background(), root, files, cached, false); err != nil {
		return codexHookPreserved("Codex hook tree changed; preserved")
	}
	return nil
}

func codexHookMembers(ctx context.Context, root string, files map[string][]byte, cached, allowMissing bool) (missing bool, err error) {
	for name, expected := range files {
		raw, err := cas.ReadLimitContext(ctx, filepath.Join(root, filepath.FromSlash(name)), claudeMarkerLimit)
		if err != nil {
			return false, err
		}
		if raw == nil && allowMissing {
			missing = true
			continue
		}
		var actual, want any
		matches := bytes.Equal(raw, expected)
		if cached {
			matches = json.Unmarshal(raw, &actual) == nil && json.Unmarshal(expected, &want) == nil && reflect.DeepEqual(actual, want)
		}
		if !matches {
			return false, codexHookPreserved("Codex hook tree changed; preserved")
		}
	}
	return missing, nil
}

func codexHookInventory(root string, allowMissing bool) error {
	// Fixed component inventories avoid interpreting path spelling as identity.
	inventory := map[string]map[string]bool{"": {".codex-plugin": true, "hooks": true}, "hooks": {"hooks.json": false}, ".codex-plugin": {"plugin.json": false}}
	for directory, names := range inventory {
		entries, err := os.ReadDir(filepath.Join(root, directory))
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || len(entries) > len(names) || !allowMissing && len(entries) != len(names) {
			return codexHookPreserved("Codex hook tree changed; preserved")
		}
		for _, entry := range entries {
			dir, ok := names[entry.Name()]
			if !ok || entry.IsDir() != dir || entry.Type()&os.ModeSymlink != 0 {
				return codexHookPreserved("Codex hook tree changed; preserved")
			}
		}
	}
	return nil
}

// The plugin payload is immutable first-created content. A partial or changed
// file is preserved for inspection; setup never overwrites it.
func codexHookCreate(path string, raw []byte) error {
	old, err := cas.ReadLimit(path, claudeMarkerLimit)
	if err != nil {
		return err
	}
	if old != nil {
		if bytes.Equal(old, raw) {
			return nil
		}
		return codexHookPreserved("changed Codex hook source; preserved")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func codexHookJSON(raw []byte, into any) error {
	if len(raw) > claudeMarkerLimit {
		return errors.New("Codex plugin reply too large")
	}
	return json.Unmarshal(raw, into)
}

// Only the vendor CLI writes vendor settings. Private source and ownership
// state remain under the OA state directory; neither records a chat identity.
func applyCodexSessionStartSetup(ctx context.Context, options CodexOptions, home string, run CodexRunner) error {
	return ApplyCodexStatus(ctx, options, home, run, new(string))
}

func ApplyCodexStatus(ctx context.Context, options CodexOptions, home string, run CodexRunner, status *string) error {
	return applyCodexStatus(ctx, options, home, run, status, "", false)
}

func applyCodexStatus(ctx context.Context, options CodexOptions, home string, run CodexRunner, status *string, repair CopyKind, inspectOnly bool) error {
	*status = "absent"
	scope, _, err := setupIdentity(ctx, home, true)
	if err != nil {
		return err
	}
	defer scope.Close()
	state, _, err := setupIdentity(ctx, options.State, true)
	if err != nil {
		return err
	}
	defer state.Close()
	cli, _, err := setupCodexCLIIdentity(ctx, options.CLI)
	if err != nil {
		return err
	}
	defer cli.Close()
	handles := []setupIdentityRef{scope, state, cli}
	nativeRun := run
	run = func(call context.Context, program, directory string, args []string) (CodexResult, error) {
		if !setupIdentitiesCurrent(call, handles) {
			return CodexResult{}, errors.New("Codex hook setup identity changed")
		}
		if selected, ok := cli.(interface{ Program() string }); ok {
			program = selected.Program()
		} else {
			program = cli.FileID().Path()
		}
		result, err := nativeRun(call, program, directory, args)
		if !setupIdentitiesCurrent(call, handles) {
			return CodexResult{}, errors.New("Codex hook setup identity changed")
		}
		return result, err
	}
	sum := sha256.Sum256([]byte(scope.FileID().Key() + "\x00" + options.Name))
	marketName := "oa-session-" + hex.EncodeToString(sum[:codexSourceKeyBytes])
	root, pluginRoot, cache, markerPath, catalogPath := codexHookPaths(options.State, home, hex.EncodeToString(sum[:codexSourceKeyBytes]))
	files := CodexFiles(options.Name)
	catalog := codexHookCatalog(marketName)
	checkSource := func() error {
		if err := codexHookTree(pluginRoot, files, false); err != nil {
			return err
		}
		raw, err := cas.ReadLimitContext(ctx, catalogPath, claudeMarkerLimit)
		if err != nil || !bytes.Equal(raw, catalog) {
			return codexHookPreserved("changed Codex hook catalog; preserved")
		}
		return nil
	}
	cacheKey := func() (string, error) {
		h, key, err := setupIdentity(ctx, cache, true)
		if err != nil {
			return "", err
		}
		defer h.Close()
		return key, nil
	}
	if err := privatedir.CheckFile(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	base, err := cas.ReadLimitContext(ctx, markerPath, claudeMarkerLimit)
	if err != nil {
		return err
	}
	if base == nil && options.Action == "connections" {
		return nil
	}
	owner := codexHookOwnership{Scope: scope.FileID().Key(), State: state.FileID().Key(), Name: options.Name}
	if base != nil {
		decoder := json.NewDecoder(bytes.NewReader(base))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&owner) != nil || owner.Scope != scope.FileID().Key() || owner.State != state.FileID().Key() || owner.Name != options.Name {
			return codexHookPreserved("unknown Codex hook ownership; preserved")
		}
	}
	read := func() (json.RawMessage, json.RawMessage, error) {
		result, err := run(ctx, options.CLI, options.State, []string{"plugin", "marketplace", "list", "--json"})
		if err != nil || result.Code != 0 {
			return nil, nil, errors.New("Codex marketplace readback unavailable")
		}
		var markets struct {
			Marketplaces []json.RawMessage `json:"marketplaces"`
		}
		if codexHookJSON(result.Output, &markets) != nil {
			return nil, nil, errors.New("unsupported Codex marketplace readback")
		}
		var found json.RawMessage
		for _, raw := range markets.Marketplaces {
			var row struct{ Name, Root string }
			if json.Unmarshal(raw, &row) != nil {
				return nil, nil, errors.New("unsupported Codex marketplace row")
			}
			if row.Name == marketName {
				if found != nil || !fileid.SamePathName(row.Root, root) {
					return nil, nil, codexHookPreserved("foreign Codex hook marketplace; preserved")
				}
				found = raw
			}
		}
		if found == nil {
			return nil, nil, nil
		}
		result, err = run(ctx, options.CLI, options.State, []string{"plugin", "list", "--marketplace", marketName, "--json"})
		if err != nil || result.Code != 0 {
			return nil, nil, errors.New("Codex plugin readback unavailable")
		}
		var plugins struct {
			Installed []json.RawMessage `json:"installed"`
		}
		if codexHookJSON(result.Output, &plugins) != nil {
			return nil, nil, errors.New("unsupported Codex plugin readback")
		}
		var installed json.RawMessage
		for _, raw := range plugins.Installed {
			var row struct {
				PluginID           string `json:"pluginId"`
				Name               string
				MarketplaceName    string `json:"marketplaceName"`
				Version            string
				Installed, Enabled bool
				Source             struct{ Source, Path string }
			}
			if json.Unmarshal(raw, &row) != nil {
				return nil, nil, errors.New("unsupported Codex plugin row")
			}
			if row.PluginID != codexHookPlugin+"@"+marketName || row.Name != codexHookPlugin || row.MarketplaceName != marketName || row.Version != codexHookVersion || !row.Installed || !row.Enabled || row.Source.Source != "local" || !fileid.SamePathName(row.Source.Path, pluginRoot) || installed != nil {
				return nil, nil, codexHookPreserved("foreign or changed Codex hook plugin; preserved")
			}
			installed = raw
		}
		return found, installed, nil
	}
	equal := func(a, b json.RawMessage) bool {
		if bytes.Equal(bytes.TrimSpace(a), []byte("null")) {
			a = nil
		}
		if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
			b = nil
		}
		var av, bv any
		if len(a) == 0 || len(b) == 0 {
			return len(a) == len(b)
		}
		return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
	}
	market, plugin, err := read()
	if err != nil {
		return err
	}
	if plugin != nil {
		*status = "installed"
	}
	if base == nil && (market != nil || plugin != nil) {
		return codexHookPreserved("unowned Codex hook installation; preserved")
	}
	if base != nil && (!equal(market, owner.Market) || !equal(plugin, owner.Plugin)) {
		return codexHookPreserved("changed Codex hook installation; preserved")
	}
	if repair != "" && (options.Action != "connections" || base == nil || market == nil || plugin == nil || owner.Cache == "") {
		return codexHookPreserved("copy repair needs confirmed native plugin ownership")
	}
	partialCache := false
	if base != nil {
		if err := checkSource(); err != nil {
			if repair != SourceCopy || inspectOnly {
				return err
			}
			if err := restoreCodexSource(ctx, root, pluginRoot, catalogPath, markerPath, base, files, catalog, handles); err != nil {
				return err
			}
			if err := checkSource(); err != nil {
				return err
			}
		}
		if repair == SourceCopy {
			return nil
		}
		if plugin != nil {
			key, err := cacheKey()
			if err != nil || key != owner.Cache {
				return codexHookPreserved("Codex hook cache identity changed; preserved")
			}
			if err := codexHookTree(cache, files, true); err != nil {
				if repair != VendorCopy {
					return err
				}
				if err := codexHookInventory(cache, true); err != nil {
					return err
				}
				partialCache, err = codexHookMembers(ctx, cache, files, true, true)
				if err != nil || !partialCache {
					return errors.Join(err, codexHookPreserved("cache has no admitted missing member"))
				}
			}
		}
	}
	if partialCache && inspectOnly {
		*status = "partial"
		return nil
	}
	if options.Action == "connections" && !partialCache {
		return nil
	}
	if options.Action == "disconnect" && base == nil {
		return nil
	}
	store := casapi.BoundedFileStore{MaxBytes: claudeMarkerLimit}
	save := func() error {
		owner.Market, owner.Plugin = market, plugin
		owner.Cache = ""
		if plugin != nil {
			key, err := cacheKey()
			if err != nil {
				return err
			}
			owner.Cache = key
			if options.Program != "" {
				if err := companionImage(ctx, options.Program, options.FileKey); err != nil {
					return err
				}
				owner.Program, owner.FileKey, owner.Version = options.Program, options.FileKey, codexHookVersion
				owner.Files = map[string]string{".agents/plugins/marketplace.json": PluginHash(catalog)}
				owner.CacheFiles = map[string]string{}
				for name, body := range files {
					owner.Files["plugins/"+codexHookPlugin+"/"+name] = PluginHash(body)
					cached, err := cas.ReadLimitContext(ctx, filepath.Join(cache, filepath.FromSlash(name)), claudeMarkerLimit)
					if err != nil || cached == nil {
						return errors.New("Codex cache digest unavailable")
					}
					owner.CacheFiles[name] = PluginHash(cached)
				}
			}
		}
		encoded, _ := json.Marshal(owner)
		if err := store.WriteContext(ctx, markerPath, casapi.Value{Data: base}, encoded); err != nil {
			return err
		}
		base = encoded
		return nil
	}
	mutate := func(args []string) error {
		var held []setupIdentityRef
		for _, path := range []string{root, pluginRoot, filepath.Join(pluginRoot, "hooks"), filepath.Join(root, ".agents", "plugins")} {
			h, _, err := setupIdentity(ctx, path, true)
			if err != nil {
				return err
			}
			defer h.Close()
			held = append(held, h)
		}
		current, err := cas.ReadLimitContext(ctx, markerPath, claudeMarkerLimit)
		if err != nil || !bytes.Equal(current, base) {
			return codexHookPreserved("Codex hook ownership changed; preserved")
		}
		m, p, err := read()
		if err != nil || !equal(m, market) || !equal(p, plugin) {
			return codexHookPreserved("Codex hook installation changed; preserved")
		}
		if err := checkSource(); err != nil {
			return err
		}
		if plugin != nil {
			key, err := cacheKey()
			if err != nil || key != owner.Cache {
				return codexHookPreserved("Codex hook cache identity changed; preserved")
			}
			if partialCache {
				if err := codexHookInventory(cache, true); err != nil {
					return err
				}
				if _, err := codexHookMembers(ctx, cache, files, true, true); err != nil {
					return err
				}
			} else if err := codexHookTree(cache, files, true); err != nil {
				return err
			}
		}
		result, err := run(ctx, options.CLI, options.State, args)
		if err != nil || result.Code != 0 {
			return errors.New("Codex hook operation outcome unknown; preserve and inspect")
		}
		if !setupIdentitiesCurrent(ctx, held) {
			return errors.New("Codex hook source identity changed during operation; outcome unknown")
		}
		if err := checkSource(); err != nil {
			return errors.New("Codex hook source changed during operation; outcome unknown")
		}
		market, plugin, err = read()
		*status = "absent"
		if plugin != nil {
			*status = "installed"
		}
		return err
	}
	if partialCache {
		if err := mutate([]string{"plugin", "add", codexHookPlugin + "@" + marketName, "--json"}); err != nil {
			return err
		}
		if err := codexHookTree(cache, files, true); err != nil {
			return err
		}
		return save()
	}
	if options.Action == "connect" {
		if base == nil {
			if err := privatedir.Ensure(filepath.Join(root, "plugins")); err != nil {
				return err
			}
			if err := privatedir.Ensure(filepath.Join(pluginRoot, "hooks")); err != nil {
				return err
			}
			if err := privatedir.Ensure(filepath.Join(pluginRoot, ".codex-plugin")); err != nil {
				return err
			}
			for rel, raw := range files {
				path := filepath.Join(pluginRoot, filepath.FromSlash(rel))
				if err := codexHookCreate(path, raw); err != nil {
					return err
				}
			}
			if err := privatedir.Ensure(filepath.Join(root, ".agents", "plugins")); err != nil {
				return err
			}
			if err := codexHookCreate(catalogPath, catalog); err != nil {
				return err
			}
		}
		if market == nil {
			if err := mutate([]string{"plugin", "marketplace", "add", root, "--json"}); err != nil {
				return err
			}
			if market == nil || plugin != nil {
				return errors.New("Codex marketplace differs after add")
			}
			if err := save(); err != nil {
				return err
			}
		}
		if plugin == nil {
			if err := mutate([]string{"plugin", "add", codexHookPlugin + "@" + marketName, "--json"}); err != nil {
				return err
			}
			if plugin == nil {
				return errors.New("Codex plugin absent after add")
			}
			if err := codexHookTree(cache, files, true); err != nil {
				return err
			}
			return save()
		}
		if options.Program != "" {
			return save()
		}
		return nil
	}
	if plugin != nil {
		if err := mutate([]string{"plugin", "remove", codexHookPlugin + "@" + marketName, "--json"}); err != nil {
			return err
		}
		if plugin != nil {
			return errors.New("Codex hook plugin remains after remove")
		}
		if err := save(); err != nil {
			return err
		}
	}
	if market != nil {
		if err := mutate([]string{"plugin", "marketplace", "remove", marketName, "--json"}); err != nil {
			return err
		}
		if market != nil {
			return errors.New("Codex hook marketplace remains after remove")
		}
		return save()
	}
	return nil
}
