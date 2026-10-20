package cacheproducer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// ObserveCompanionDeclaration observes the retained recipe version. It never
// compares an older receipt with today's compiled template and makes no write.
func ObserveCompanionDeclaration(ctx context.Context, raw []byte) (CopyReading, error) {
	return observeCompanionDeclaration(ctx, raw, RunClaude, RunCodex)
}
func observeCompanionDeclaration(ctx context.Context, raw []byte, claudeRun ClaudeRunner, codexRun CodexRunner) (CopyReading, error) {
	d, err := parseCompanion(raw)
	if err != nil {
		return CopyReading{State: CopyUnknown}, err
	}
	if err = companionImage(ctx, d.Program, d.FileKey); err != nil {
		return CopyReading{State: CopyUnknown}, err
	}
	// The carrier's generation may precede an automatic update; exact scoped
	// membership remains required, while installed receipts prove the new bytes.
	config := ""
	for _, m := range d.Members {
		state, e := fileid.ParseStoredFileID(m.State)
		if e != nil {
			return CopyReading{State: CopyUnknown}, e
		}
		parent := filepath.Dir(state.Path())
		if config != "" && config != parent {
			return CopyReading{State: CopyUnknown}, errors.New("mixed companion configuration roots")
		}
		config = parent
	}
	liveRaw, err := readCompanionDeclarationInConfig(ctx, config, d.Program, d.FileKey, false)
	if err != nil {
		return CopyReading{State: CopyUnknown}, err
	}
	live, err := parseCompanion(liveRaw)
	if err != nil || len(live.Members) != len(d.Members) {
		return CopyReading{State: CopyUnknown}, errors.New("companion member set changed")
	}
	for i, m := range d.Members {
		a, b := m, live.Members[i]
		a.Version, b.Version = "", ""
		a.Files, b.Files = nil, nil
		if !reflect.DeepEqual(a, b) {
			return CopyReading{State: CopyUnknown}, errors.New("companion membership or scope changed")
		}
	}
	for _, m := range d.Members {
		if m.Requirement == "none" {
			continue
		}
		if m.Owner == ClaudeProducer {
			err = observeDeclaredClaude(ctx, d, m, claudeRun)
		} else {
			err = observeDeclaredCodex(ctx, d, m, codexRun)
		}
		if err != nil {
			return CopyReading{State: CopyUnknown, Reason: err.Error()}, err
		}
	}
	if err = companionImage(ctx, d.Program, d.FileKey); err != nil {
		return CopyReading{State: CopyUnknown}, err
	}
	return CopyReading{State: CopyCurrent}, nil
}
func verifyDeclaredFiles(ctx context.Context, root string, files map[string]string, prefix string) error {
	directory, _, err := setupIdentity(ctx, root, true)
	if err != nil {
		return err
	}
	defer directory.Close()
	for name, digest := range files {
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		relative := strings.TrimPrefix(name, prefix)
		h, err := directory.Beneath(filepath.FromSlash(relative), fileid.Options{Links: fileid.RefuseLinks, Read: true})
		if err != nil {
			return err
		}
		body, readErr := readClaudePluginHeld(ctx, h)
		current, currentErr := directory.Beneath(filepath.FromSlash(relative), fileid.Options{Links: fileid.RefuseLinks})
		same := currentErr == nil && current.FileID().Equal(h.FileID())
		if currentErr == nil {
			_ = current.Close()
		}
		_ = h.Close()
		if readErr != nil || !same || PluginHash(body) != digest {
			return errors.New("recorded companion file digest differs")
		}
	}
	if !setupIdentitiesCurrent(ctx, []setupIdentityRef{directory}) {
		return errors.New("companion directory changed")
	}
	return nil
}
func declaredContext(ctx context.Context, m companionMember) (state, project, cli fileid.FileID, err error) {
	for _, item := range []struct {
		raw       string
		out       *fileid.FileID
		directory bool
	}{{m.State, &state, true}, {m.Project, &project, true}, {m.CLI, &cli, false}} {
		stored, e := fileid.ParseStoredFileID(item.raw)
		if e != nil {
			return state, project, cli, e
		}
		current, status, e := fileid.Revalidate(ctx, stored)
		if e != nil || status != fileid.StateSame {
			return state, project, cli, errors.New("companion binding identity changed")
		}
		*item.out = current
	}
	return
}
func observeDeclaredClaude(ctx context.Context, d companionDeclaration, m companionMember, run ClaudeRunner) error {
	return observeDeclaredClaudeSnapshot(ctx, d, m, run, nil)
}
func observeDeclaredClaudeSnapshot(ctx context.Context, d companionDeclaration, m companionMember, run ClaudeRunner, snapshot *companionNative) error {
	_, project, cli, err := declaredContext(ctx, m)
	if err != nil {
		return err
	}
	_, marker, err := ReadClaudeMarker(ctx, m.Root+".json")
	if err != nil || marker == nil {
		return errors.New("required Claude receipt unavailable")
	}
	self, err := fileid.ParseStoredFileID(marker.Self)
	if err != nil {
		return err
	}
	current, nativeState, err := fileid.Revalidate(ctx, self)
	if err != nil || nativeState != fileid.StateSame || current.Key() != d.FileKey || self.Path() != d.Program || marker.State != m.State || marker.Project != m.Project || marker.CLI != m.CLI || marker.Scope != m.Scope || !marker.PluginOwned || !marker.MarketplaceOwned || marker.InstalledVersion != m.Version || !reflect.DeepEqual(marker.Files, m.Files) {
		return errors.New("Claude installed receipt differs from declaration")
	}
	if err = verifyDeclaredFiles(ctx, m.Root, m.Files, ""); err != nil {
		return err
	}
	invoke := func(args ...string) ([]byte, error) {
		if _, _, _, err := declaredContext(ctx, m); err != nil {
			return nil, err
		}
		body, err := run(ctx, cli.Path(), project.Path(), args)
		if err != nil {
			return nil, err
		}
		if _, _, _, err := declaredContext(ctx, m); err != nil {
			return nil, err
		}
		return body, nil
	}
	marketRaw, err := invoke("plugin", "marketplace", "list", "--json")
	if err != nil {
		return err
	}
	markets, err := PluginList(marketRaw)
	if err != nil {
		return err
	}
	found := 0
	for _, row := range markets {
		if PluginString(row, "name") == marker.Marketplace {
			if PluginString(row, "source") != "directory" || PluginString(row, "path") != m.Root {
				return errors.New("Claude native marketplace differs")
			}
			found++
			if snapshot != nil {
				snapshot.Market = PluginJSON(row)
			}
		}
	}
	if found != 1 {
		return errors.New("Claude native marketplace missing or ambiguous")
	}
	raw, err := invoke("plugin", "list", "--json")
	if err != nil {
		return err
	}
	entries, err := PluginList(raw)
	if err != nil {
		return err
	}
	found = 0
	for _, row := range entries {
		if PluginString(row, "id") != "openabstractions@"+marker.Marketplace || PluginString(row, "scope") != m.Scope {
			continue
		}
		selectedProject := ""
		if m.Scope == "local" {
			selectedProject = project.Path()
		}
		if PluginString(row, "projectPath") != selectedProject {
			continue
		}
		var enabled bool
		if json.Unmarshal(row["enabled"], &enabled) != nil || !enabled || PluginString(row, "version") != m.Version {
			return errors.New("Claude native recorded version differs")
		}
		cache, err := ClaudeInstalledFolder(ctx, row, "openabstractions@"+marker.Marketplace, m.Scope, selectedProject, m.Version, filepath.Join(m.Root, "plugin"))
		if err != nil {
			return err
		}
		if err = verifyDeclaredFiles(ctx, cache, m.Files, "plugin/"); err != nil {
			return err
		}
		if snapshot != nil {
			snapshot.Plugin = PluginJSON(row)
			snapshot.Registry, err = selectedClaudeRegistry(ctx, m, marker.Marketplace)
			if err != nil {
				return err
			}
		}
		found++
	}
	if found != 1 {
		return errors.New("Claude native plugin missing or ambiguous")
	}
	return nil
}
func observeDeclaredCodex(ctx context.Context, d companionDeclaration, m companionMember, run CodexRunner) error {
	return observeDeclaredCodexSnapshot(ctx, d, m, run, nil)
}
func observeDeclaredCodexSnapshot(ctx context.Context, d companionDeclaration, m companionMember, run CodexRunner, snapshot *companionNative) error {
	state, project, cli, err := declaredContext(ctx, m)
	if err != nil {
		return err
	}
	raw, err := cas.ReadLimitContext(ctx, m.Root+".json", claudeMarkerLimit)
	if err != nil {
		return err
	}
	var receipt codexHookOwnership
	if json.Unmarshal(raw, &receipt) != nil || receipt.Program != d.Program || receipt.FileKey != d.FileKey || receipt.Version != m.Version || receipt.Scope != project.Key() || receipt.State != state.Key() || receipt.Name != m.Name || receipt.Cache == "" || !reflect.DeepEqual(receipt.Files, m.Files) {
		return errors.New("required Codex receipt differs from declaration")
	}
	if err = verifyDeclaredFiles(ctx, m.Root, m.Files, ""); err != nil {
		return err
	}
	if len(receipt.CacheFiles) == 0 {
		return errors.New("Codex cache has no recorded digests")
	}
	key := PluginHash([]byte(project.Key() + "\x00" + m.Name))[:2*codexSourceKeyBytes]
	market := "oa-session-" + key
	cache := filepath.Join(project.Path(), "plugins", "cache", market, codexHookPlugin, m.Version)
	_, cacheStored, err := setupIdentityClosed(ctx, cache, true)
	if err != nil || cacheStored != receipt.Cache {
		return errors.New("Codex cache identity differs")
	}
	if err = verifyDeclaredFiles(ctx, cache, receipt.CacheFiles, ""); err != nil {
		return err
	}
	invoke := func(args ...string) ([]byte, error) {
		if _, _, _, err := declaredContext(ctx, m); err != nil {
			return nil, err
		}
		result, err := run(ctx, cli.Path(), state.Path(), args)
		if err != nil || result.Code != 0 {
			return nil, errors.New("Codex native readback unavailable")
		}
		if _, _, _, err := declaredContext(ctx, m); err != nil {
			return nil, err
		}
		return result.Output, nil
	}
	marketRaw, err := invoke("plugin", "marketplace", "list", "--json")
	if err != nil {
		return err
	}
	var markets struct {
		Marketplaces []json.RawMessage `json:"marketplaces"`
	}
	if json.Unmarshal(marketRaw, &markets) != nil {
		return errors.New("Codex native marketplace reply invalid")
	}
	found := 0
	for _, row := range markets.Marketplaces {
		a, _ := canonicalCompanion(json.RawMessage(row))
		b, _ := canonicalCompanion(receipt.Market)
		if bytes.Equal(a, b) {
			found++
			if snapshot != nil {
				snapshot.Market = append([]byte(nil), row...)
			}
		}
	}
	if found != 1 {
		return errors.New("Codex native marketplace differs")
	}
	pluginRaw, err := invoke("plugin", "list", "--marketplace", market, "--json")
	if err != nil {
		return err
	}
	var plugins struct {
		Installed []json.RawMessage `json:"installed"`
	}
	if json.Unmarshal(pluginRaw, &plugins) != nil {
		return errors.New("Codex native plugin reply invalid")
	}
	found = 0
	for _, row := range plugins.Installed {
		a, _ := canonicalCompanion(row)
		b, _ := canonicalCompanion(receipt.Plugin)
		if bytes.Equal(a, b) {
			found++
			if snapshot != nil {
				snapshot.Plugin = append([]byte(nil), row...)
			}
		}
	}
	if found != 1 {
		return errors.New("Codex native plugin differs")
	}
	return nil
}
