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
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
)

type CopyKind string

const ClaudeProducer = "mcp-gateway/claude"
const CodexProducer = "mcp-gateway/codex"

const (
	SourceCopy CopyKind = "source"
	VendorCopy CopyKind = "vendor-cache"
)

type CopyState string

const (
	CopyCurrent     CopyState = "current"
	CopyStale       CopyState = "stale"
	CopyAbsent      CopyState = "absent"
	CopyForeign     CopyState = "foreign"
	CopyUnreadable  CopyState = "unreadable"
	CopyHumanNeeded CopyState = "human-needed"
	CopyUnknown     CopyState = "unknown"
)

type CopyReading struct {
	State  CopyState
	Reason string
}

// Copy is a read-time projection of one producer-owned receipt. Functions and
// expected bytes stay with the producer. The runtime receives no hash authority.
type Copy struct {
	Kind                     CopyKind
	Owner, Location, Receipt string
	observe                  func(context.Context) (CopyReading, error)
	reconcile                func(context.Context) error
	reconcileAuthorized      func(context.Context, func(context.Context) error) error
}

func (c Copy) Observe(ctx context.Context) (CopyReading, error) {
	if c.observe == nil {
		return CopyReading{State: CopyUnknown}, errors.New("copy producer unavailable")
	}
	return c.observe(ctx)
}

func (c Copy) CanReconcile() bool { return c.reconcile != nil }
func (c Copy) Reconcile(ctx context.Context) error {
	if c.reconcile == nil {
		return errors.New("copy needs its existing setup owner")
	}
	return c.reconcile(ctx)
}

// ReconcileAuthorized retains the producer's exact ownership/readback owner
// and rechecks native caller authority at every write or vendor dispatch.
func (c Copy) ReconcileAuthorized(ctx context.Context, authorize func(context.Context) error) error {
	if authorize == nil || c.reconcileAuthorized == nil {
		return errors.New("copy has no authority-bound repair owner")
	}
	if err := authorize(ctx); err != nil {
		return err
	}
	return c.reconcileAuthorized(ctx, authorize)
}

// InstalledCopies resolves the same account config root as the installed Setup
// step. It reads gateway-owned markers only. Isolated runtimes use no installed
// copy inventory; their caller supplies its own explicit test/config root.
func InstalledCopies(ctx context.Context) ([]Copy, error) {
	roots, err := layout.Resolve(layout.Current())
	if err != nil {
		return nil, err
	}
	return CopiesInConfig(ctx, roots.Config)
}

// CopiesInConfig performs read-only discovery. Missing receipts authorize no
// creation, and unrelated state entries are never treated as copy locations.
func CopiesInConfig(ctx context.Context, config string) ([]Copy, error) {
	return copiesInConfig(ctx, config, RunClaude, RunCodex)
}

func copiesInConfig(ctx context.Context, config string, claudeRun ClaudeRunner, codexRun CodexRunner) ([]Copy, error) {
	var copies []Copy
	for _, directory := range []struct {
		name, prefix, owner string
		keyCharacters       int
		userKey             bool
		read                func(context.Context, string, string) ([]Copy, error)
	}{
		{"claude-connections", "claude-plugin-", ClaudeProducer, claudeSourceKeyCharacters, true, func(ctx context.Context, state, path string) ([]Copy, error) {
			return claudeCopies(ctx, state, path, claudeRun)
		}},
		{"codex-connections", "codex-session-start-", CodexProducer, 2 * codexSourceKeyBytes, false, func(ctx context.Context, state, path string) ([]Copy, error) {
			return codexCopies(ctx, state, path, codexRun)
		}},
	} {
		state := filepath.Join(config, directory.name)
		if _, err := os.Lstat(state); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := privatedir.CheckDir(state); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(state)
		if err != nil {
			return nil, err
		}
		count := 0
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !strings.HasPrefix(entry.Name(), directory.prefix) {
				continue
			}
			if !strings.HasSuffix(entry.Name(), ".json") {
				key := strings.TrimPrefix(entry.Name(), directory.prefix)
				_, hexErr := hex.DecodeString(key)
				if !(directory.userKey && key == "user") && (len(key) != directory.keyCharacters || hexErr != nil || strings.ToLower(key) != key) {
					continue
				}
				location := filepath.Join(state, entry.Name())
				receipt := location + ".json"
				if _, err := os.Lstat(receipt); errors.Is(err, os.ErrNotExist) {
					count++
					if count > 64 {
						return nil, errors.New("gateway copy receipt count exceeds connection limit")
					}
					copies = append(copies, refusedCopy(SourceCopy, directory.owner, location, receipt, CopyForeign, "copy has no positive ownership receipt; preserved"))
				} else if err != nil {
					return nil, err
				}
				continue
			}
			count++
			// Reuse the gateway's existing saved-connection limit.
			if count > 64 {
				return nil, errors.New("gateway copy receipt count exceeds connection limit")
			}
			path := filepath.Join(state, entry.Name())
			rows, err := directory.read(ctx, state, path)
			if err != nil {
				rows = []Copy{refusedCopy(SourceCopy, directory.owner, path, path, CopyUnreadable, err.Error())}
			}
			copies = append(copies, rows...)
		}
	}
	return copies, nil
}

func refusedCopy(kind CopyKind, owner, location, receipt string, state CopyState, reason string) Copy {
	return Copy{Kind: kind, Owner: owner, Location: location, Receipt: receipt,
		observe: func(context.Context) (CopyReading, error) { return CopyReading{State: state, Reason: reason}, nil }}
}

func claudeCopies(ctx context.Context, state, path string, run ClaudeRunner) ([]Copy, error) {
	_, marker, err := ReadClaudeMarker(ctx, path)
	if err != nil || marker == nil {
		return nil, errors.Join(err, errors.New("plugin receipt unavailable"))
	}
	project, err := fileid.ParseStoredFileID(marker.Project)
	if err != nil {
		return nil, err
	}
	self, err := fileid.ParseStoredFileID(marker.Self)
	if err != nil {
		return nil, err
	}
	cli, err := fileid.ParseStoredFileID(marker.CLI)
	if err != nil {
		return nil, err
	}
	_, stateID, err := setupIdentityClosed(ctx, state, true)
	if err != nil || marker.State != stateID {
		return nil, errors.New("plugin receipt state differs")
	}
	projectHandle, projectID, err := setupIdentity(ctx, project.Path(), true)
	if err != nil {
		return nil, err
	}
	key := PluginHash([]byte(projectHandle.FileID().Key()))[:claudeSourceKeyCharacters]
	projectHandle.Close()
	if marker.Scope == "user" {
		key = "user"
	} else if marker.Scope != "local" {
		return nil, errors.New("plugin scope unavailable")
	}
	root, receipt := claudePluginPaths(state, key)
	if marker.Project != projectID || filepath.Base(path) != filepath.Base(receipt) || marker.Marketplace != "oa-local-"+key {
		return nil, errors.New("plugin receipt location differs")
	}
	options := ClaudeOptions{Action: "status", Scope: marker.Scope, State: state, CLI: cli.Path()}
	if marker.Scope == "local" {
		options.Project = project.Path()
	}
	owner := ClaudeProducer
	if !marker.MarketplaceOwned || !marker.PluginOwned {
		return []Copy{
			refusedCopy(SourceCopy, owner, root, path, CopyHumanNeeded, "native plugin ownership needs Setup"),
			refusedCopy(VendorCopy, owner, path+"#vendor-cache", path, CopyHumanNeeded, "native plugin ownership needs Setup"),
		}, nil
	}
	readSource := func(ctx context.Context) (CopyReading, error) {
		return observeClaudeSource(ctx, path, root, options, self.Path())
	}
	readCache := func(ctx context.Context) (CopyReading, error) {
		err := ApplyClaude(ctx, options, self.Path(), run)
		switch {
		case err == nil:
			return CopyReading{State: CopyCurrent}, nil
		case errors.Is(err, ErrClaudeUpdate):
			return CopyReading{State: CopyStale, Reason: err.Error()}, nil
		case errors.Is(err, ErrClaudeAbsent):
			return CopyReading{State: CopyAbsent, Reason: err.Error()}, nil
		default:
			return CopyReading{State: CopyForeign, Reason: err.Error()}, nil
		}
	}
	// Only the existing producer performs vendor operations and receipt CAS.
	repair := func(ctx context.Context) error {
		update := options
		update.Action = "update"
		return applyClaude(ctx, update, self.Path(), run, true)
	}
	repairAuthorized := func(ctx context.Context, authorize func(context.Context) error) error {
		update := options
		update.Action = "update"
		return applyClaude(ctx, update, self.Path(), run, true, authorize)
	}
	version := marker.InstalledVersion
	if version == "" {
		raw, err := cas.ReadLimitContext(ctx, filepath.Join(root, "plugin", ".claude-plugin", "plugin.json"), claudeMarkerLimit)
		if err == nil {
			var manifest struct{ Version string }
			if json.Unmarshal(raw, &manifest) == nil {
				version = manifest.Version
			}
		}
	}
	selectedProject := options.Project
	cache, locateErr := ClaudeInstalledFolder(ctx, nil, "openabstractions@"+marker.Marketplace, marker.Scope, selectedProject, version, filepath.Join(root, "plugin"))
	cacheCopy := Copy{Kind: VendorCopy, Owner: owner, Location: cache, Receipt: path, observe: readCache, reconcile: repair, reconcileAuthorized: repairAuthorized}
	if locateErr != nil {
		cacheCopy = refusedCopy(VendorCopy, owner, path+"#vendor-cache", path, CopyUnreadable, locateErr.Error())
	}
	return []Copy{{Kind: SourceCopy, Owner: owner, Location: root, Receipt: path, observe: readSource, reconcile: repair, reconcileAuthorized: repairAuthorized}, cacheCopy}, nil
}

func setupIdentityClosed(ctx context.Context, path string, directory bool) (fileid.FileID, string, error) {
	h, stored, err := setupIdentity(ctx, path, directory)
	if err != nil {
		return fileid.FileID{}, "", err
	}
	id := h.FileID()
	return id, stored, h.Close()
}

func observeClaudeSource(ctx context.Context, markerPath, root string, options ClaudeOptions, self string) (CopyReading, error) {
	_, marker, err := ReadClaudeMarker(ctx, markerPath)
	if err != nil || marker == nil {
		return CopyReading{}, errors.Join(err, errors.New("plugin ownership unavailable"))
	}
	if !marker.MarketplaceOwned || !marker.PluginOwned {
		return CopyReading{State: CopyHumanNeeded, Reason: "native plugin ownership needs Setup"}, nil
	}
	_, stateID, err := setupIdentityClosed(ctx, options.State, true)
	if err != nil || marker.State != stateID || marker.Scope != options.Scope {
		return CopyReading{State: CopyForeign, Reason: "source receipt identity changed"}, nil
	}
	selected, err := fileid.ParseStoredFileID(marker.Self)
	if err != nil || selected.Path() != self {
		return CopyReading{State: CopyForeign, Reason: "source program locator changed"}, nil
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return CopyReading{State: CopyAbsent, Reason: "source needs its existing setup owner; preserved"}, nil
	} else if err != nil {
		return CopyReading{}, err
	}
	directory, _, err := setupIdentity(ctx, root, true)
	if err != nil {
		return CopyReading{}, err
	}
	defer directory.Close()
	files := ClaudeFilesForScope(marker.Marketplace, self, options.State, options.Scope)
	if options.Scope == "user" {
		files["plugin/hooks/register.js"], err = ClaudeBackgroundModule(ctx, options.State, options.CLI, stateID)
		if err != nil {
			return CopyReading{State: CopyHumanNeeded, Reason: err.Error()}, nil
		}
	}
	SetClaudeGeneration(files)
	state := CopyCurrent
	for name, expected := range files {
		want, owned := marker.Files[name]
		if !owned {
			return CopyReading{State: CopyForeign, Reason: "unlisted source member"}, nil
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := privatedir.CheckFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return CopyReading{}, err
		}
		raw, err := cas.ReadLimitContext(ctx, path, claudePayloadLimit)
		if err != nil {
			return CopyReading{}, err
		}
		if raw == nil {
			state = CopyAbsent
			continue
		}
		if PluginHash(raw) != want {
			return CopyReading{State: CopyForeign, Reason: "owned source bytes changed"}, nil
		}
		if !bytes.Equal(raw, expected) && state != CopyAbsent {
			state = CopyStale
		}
	}
	if !setupIdentitiesCurrent(ctx, []setupIdentityRef{directory}) {
		return CopyReading{State: CopyUnknown, Reason: "source directory changed during check"}, nil
	}
	return CopyReading{State: state}, nil
}

func codexCopies(ctx context.Context, state, path string, run CodexRunner) ([]Copy, error) {
	if err := privatedir.CheckFile(path); err != nil {
		return nil, err
	}
	raw, err := cas.ReadLimitContext(ctx, path, claudeMarkerLimit)
	if err != nil {
		return nil, err
	}
	var marker codexHookOwnership
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return nil, err
	}
	home, err := codexHome()
	if err != nil {
		return nil, err
	}
	scope, _, err := setupIdentityClosed(ctx, home, true)
	if err != nil {
		return nil, err
	}
	stateID, _, err := setupIdentityClosed(ctx, state, true)
	if err != nil || marker.Scope != scope.Key() || marker.State != stateID.Key() {
		return nil, errors.New("hook receipt identity differs")
	}
	sum := sha256.Sum256([]byte(scope.Key() + "\x00" + marker.Name))
	key := hex.EncodeToString(sum[:codexSourceKeyBytes])
	root, _, cache, receipt, _ := codexHookPaths(state, home, key)
	if filepath.Base(path) != filepath.Base(receipt) {
		return nil, errors.New("hook receipt name differs")
	}
	owner := CodexProducer
	cli, err := codexReceiptCLI(ctx, state, marker, hex.EncodeToString(sum[:]))
	present := func(raw json.RawMessage) bool {
		return len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	if err != nil || !present(marker.Plugin) || !present(marker.Market) || marker.Cache == "" {
		return []Copy{
			refusedCopy(SourceCopy, owner, root, path, CopyHumanNeeded, "selected native setup needs confirmation"),
			refusedCopy(VendorCopy, owner, cache, path, CopyHumanNeeded, "selected native setup needs confirmation"),
		}, nil
	}
	options := CodexOptions{Action: "connections", State: state, CLI: cli, Name: marker.Name}
	observe := func(ctx context.Context) (CopyReading, error) {
		status := ""
		if err := applyCodexStatus(ctx, options, home, run, &status, VendorCopy, true); err != nil {
			// The existing native status checks source, vendor metadata and
			// cache together. Its refusal cannot establish which copy failed.
			return CopyReading{State: CopyUnknown, Reason: err.Error()}, nil
		}
		if status == "partial" {
			return CopyReading{State: CopyAbsent, Reason: "recorded native cache has a missing recipe member"}, nil
		}
		if status != "installed" {
			return CopyReading{State: CopyHumanNeeded, Reason: "hook installation needs Setup"}, nil
		}
		return CopyReading{State: CopyCurrent}, nil
	}
	reconcile := func(kind CopyKind) func(context.Context) error {
		return func(ctx context.Context) error {
			selected, err := codexReceiptCLI(ctx, state, marker, hex.EncodeToString(sum[:]))
			if err != nil || selected != cli {
				return errors.Join(err, errors.New("selected native CLI changed; preserved"))
			}
			status := ""
			return applyCodexStatus(ctx, options, home, run, &status, kind, false)
		}
	}
	// A partial cache is repairable only under its recorded directory identity
	// and exact surviving recipe members. The vendor owns the cache writes.
	// Directory replacement and host hook trust retain their own admission.
	return []Copy{
		{Kind: SourceCopy, Owner: owner, Location: root, Receipt: path, observe: func(ctx context.Context) (CopyReading, error) {
			return observeCodexSource(ctx, path, root, raw, CodexFiles(marker.Name), codexHookCatalog("oa-session-"+key))
		}, reconcile: reconcile(SourceCopy)},
		{Kind: VendorCopy, Owner: owner, Location: cache, Receipt: path, observe: observe, reconcile: reconcile(VendorCopy)},
	}, nil
}

func observeCodexSource(ctx context.Context, receipt, root string, base []byte, files map[string][]byte, catalog []byte) (CopyReading, error) {
	current, err := cas.ReadLimitContext(ctx, receipt, claudeMarkerLimit)
	if err != nil {
		return CopyReading{}, err
	}
	if !bytes.Equal(current, base) {
		return CopyReading{State: CopyUnknown, Reason: "source receipt changed; rescan required"}, nil
	}
	plugin := filepath.Join(root, "plugins", codexHookPlugin)
	if _, err := os.Lstat(plugin); errors.Is(err, os.ErrNotExist) {
		return CopyReading{State: CopyAbsent, Reason: "source needs its existing setup owner; preserved"}, nil
	} else if err != nil {
		return CopyReading{}, err
	}
	directory, _, err := setupIdentity(ctx, plugin, true)
	if err != nil {
		return CopyReading{}, err
	}
	defer directory.Close()
	if err := codexHookInventory(plugin, true); err != nil {
		return CopyReading{State: CopyForeign, Reason: err.Error()}, nil
	}
	state := CopyCurrent
	for name, expected := range files {
		path := filepath.Join(plugin, filepath.FromSlash(name))
		if err := privatedir.CheckFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return CopyReading{}, err
		}
		raw, err := cas.ReadLimitContext(ctx, path, claudePayloadLimit)
		if err != nil {
			return CopyReading{}, err
		}
		if raw == nil {
			state = CopyAbsent
		} else if !bytes.Equal(raw, expected) {
			return CopyReading{State: CopyForeign, Reason: "source content changed; preserved"}, nil
		}
	}
	raw, err := cas.ReadLimitContext(ctx, filepath.Join(root, ".agents", "plugins", "marketplace.json"), claudeMarkerLimit)
	if err != nil {
		return CopyReading{}, err
	}
	if raw == nil {
		state = CopyAbsent
	} else if !bytes.Equal(raw, catalog) {
		return CopyReading{State: CopyForeign, Reason: "source catalog changed; preserved"}, nil
	}
	if state == CopyAbsent {
		return CopyReading{State: state, Reason: "source needs its existing setup owner; preserved"}, nil
	}
	if !setupIdentitiesCurrent(ctx, []setupIdentityRef{directory}) {
		return CopyReading{State: CopyUnknown, Reason: "source directory changed during check"}, nil
	}
	return CopyReading{State: state}, nil
}

func codexReceiptCLI(ctx context.Context, state string, hook codexHookOwnership, key string) (string, error) {
	path := filepath.Join(state, "codex-setup-"+key+".json")
	if err := privatedir.CheckFile(path); err != nil {
		return "", err
	}
	raw, err := cas.ReadLimitContext(ctx, path, claudeMarkerLimit)
	if err != nil {
		return "", err
	}
	fields, err := claudeConfigObject(raw)
	if err != nil {
		return "", err
	}
	// These existing setup-receipt fields are location hints only. ApplyCodex
	// freshly holds the selected CLI, authenticates the hook's own receipt and
	// performs its existing vendor readbacks before making any current claim.
	var marker struct {
		Scope, State, Name, CLI string
		Active                  bool
	}
	if json.Unmarshal(raw, &marker) != nil || !marker.Active || marker.Name != hook.Name || len(fields) < 7 {
		return "", errors.New("native setup locator unavailable")
	}
	for _, value := range []struct{ stored, key string }{{marker.Scope, hook.Scope}, {marker.State, hook.State}} {
		stored, err := fileid.ParseStoredFileID(value.stored)
		if err != nil {
			return "", err
		}
		current, status, err := fileid.Revalidate(ctx, stored)
		if err != nil || status != fileid.StateSame || current.Key() != value.key {
			return "", errors.New("native setup locator identity differs")
		}
	}
	cli, err := fileid.ParseStoredFileID(marker.CLI)
	if err != nil {
		return "", fmt.Errorf("native setup CLI locator: %w", err)
	}
	return cli.Path(), nil
}

func codexHome() (string, error) { return setupio.CodexHome() }
