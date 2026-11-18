package cacheproducer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/claude-plugin/hooks"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type ClaudeRunner func(context.Context, string, string, []string) ([]byte, error)
type claudeSetupRunner = ClaudeRunner

// The ownership marker is a small JSON record. Its plugin payload members
// carry the projected Claude Mod, which grows with the hook; they have their
// own bound. Both owners (cacheproducer and Setup) keep the same values.
const claudeMarkerLimit = 32 << 10
const claudePayloadLimit = 128 << 10
const claudeProjectLimit = 1 << 20

type claudeEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// Plugin configuration uses the vendor CLI exclusively. These private files
// establish ownership; they confer no enrollment or session authority.
type ClaudeOptions struct{ Action, Project, State, CLI, RegisteredProgram, Scope string }

var ErrClaudeAbsent = errors.New("user message companion is not installed")
var ErrClaudeUpdate = errors.New("user message companion requires update")

// ClaudeInvocationError is a failed vendor CLI call. Only closed operation and
// reason tokens cross the diagnostic boundary. Vendor output and error text can
// contain profile data and remain private.
type ClaudeInvocationError struct {
	operation, reason string
	mutating          bool
	cause             error
}

func (e *ClaudeInvocationError) Error() string {
	message := "native " + e.operation + " failed: " + e.reason
	if e.mutating {
		message += "; outcome unknown; no automatic retry"
	}
	return message
}
func (e *ClaudeInvocationError) Unwrap() error { return e.cause }

// ClaudeInvocationFailure classifies a failed vendor CLI call by its context
// and cause: timeout, cancelled, exit or unavailable.
func ClaudeInvocationFailure(ctx context.Context, operation string, mutating bool, cause error) error {
	reason := "unavailable"
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(cause, context.DeadlineExceeded):
		reason = "timeout"
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(cause, context.Canceled):
		reason = "cancelled"
	case errors.As(cause, &exit):
		reason = "exit"
	}
	return &ClaudeInvocationError{operation, reason, mutating, errors.Join(ctx.Err(), cause)}
}

// claudeInvocationFailureFrom classifies a failed vendor call as
// ClaudeInvocationFailure does. An exit is refined by the vendor's own output
// into one closed reason; the output itself stays in this process.
func claudeInvocationFailureFrom(ctx context.Context, operation string, mutating bool, cause error, output []byte) error {
	err := ClaudeInvocationFailure(ctx, operation, mutating, cause)
	var invocation *ClaudeInvocationError
	if errors.As(err, &invocation) && invocation.reason == "exit" {
		if reason := ClaudeVendorReason(output); reason != "" {
			invocation.reason = reason
		}
	}
	return err
}

// ClaudeVendorReason maps Claude Code's output from a failed plugin command
// to one closed reason: folder_missing, marketplace_missing, plugin_missing,
// name_conflict, invalid_plugin or access_denied. It answers "" when the
// output matches none.
func ClaudeVendorReason(output []byte) string {
	text := strings.ToLower(string(output))
	has := func(words ...string) bool {
		for _, word := range words {
			if strings.Contains(text, word) {
				return true
			}
		}
		return false
	}
	switch {
	case claudeVendorPluginMissing.MatchString(text):
		return "plugin_missing"
	case claudeVendorMarketplaceMissing.MatchString(text):
		return "marketplace_missing"
	case has("enoent", "no such file", "does not exist", "path not found", "directory not found", "folder not found", "cannot find the path", "cannot find the file"):
		return "folder_missing"
	case has("already exists", "already installed", "already added", "already registered", "conflict"):
		return "name_conflict"
	case has("validation failed", "invalid manifest", "invalid plugin", "failed to parse", "schema"):
		return "invalid_plugin"
	case has("eperm", "eacces", "ebusy", "permission denied", "access is denied", "operation not permitted"):
		return "access_denied"
	}
	return ""
}

// A quoted or bare name between a noun and its verdict, as in
// `Plugin "openabstractions@oa-local-1" not found`.
const claudeVendorName = `\s+["'“‘]?[^\s"'”’]*["'”’]?\s+`

var (
	claudeVendorPluginMissing      = regexp.MustCompile(`plugin` + claudeVendorName + `(?:not found|is not installed|not installed)|not found in (?:any )?marketplace`)
	claudeVendorMarketplaceMissing = regexp.MustCompile(`marketplace` + claudeVendorName + `(?:not found|does not exist|is not (?:installed|known|configured))|unknown marketplace|no marketplace named`)
)

var claudePluginModule = hooks.Register()

type ClaudeMarker struct {
	Project, State, Self, CLI, Marketplace string
	Scope                                  string
	Files                                  map[string]string
	MarketplaceOwned, PluginOwned          bool
	InstalledVersion                       string `json:",omitempty"`
}

// PluginJSON is v as one JSON line, the form plugin files and receipts take.
func PluginJSON(v any) []byte { b, _ := json.Marshal(v); return append(b, '\n') }

// PluginHash is the lowercase hex SHA-256 that markers and receipts record.
func PluginHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Reuse the C1 receipts as the durable preference owner. The projection changes
// only our authenticated module payload; it never changes vendor approval.
func ClaudeBackgroundModule(ctx context.Context, state, cli, stateID string) ([]byte, error) {
	// ASK-R14: the Asks wake rides the Messages watch and the person's
	// answering enrollment; only an explicit Disable of that scope holds it.
	disabled := map[string]bool{"messages": false, "asks": false}
	for scope, rule := range map[string]string{"messages": claudeWatchRule, "asks": claudeAsksWatchRule} {
		base, receipt, err := ReadWatchReceipt(ctx, WatchReceiptFile(state, rule))
		if err != nil {
			return nil, err
		}
		if base != nil {
			if !nativecli.SameClaudeCLIInstall(receipt.CLI, cli) || receipt.State != stateID || receipt.Status == "pending" || receipt.Status == "undo-pending" {
				return nil, errors.New("background preference unavailable; preserved")
			}
			disabled[scope] = receipt.Status == "disabled"
		}
	}
	projection, _ := json.Marshal(disabled)
	module, err := hooks.ProjectRegister(claudePluginModule)
	if err != nil {
		return nil, err
	}
	return bytes.Replace(module, []byte("const backgroundDisabled = {messages: false, asks: false};"), append(append([]byte("const backgroundDisabled = "), projection...), ';'), 1), nil
}

func claudePluginFiles(market, self, state string) map[string][]byte {
	return ClaudeFilesForScope(market, self, state, "local")
}

func ClaudeFilesForScope(market, self, state, scope string) map[string][]byte {
	module, _ := hooks.ProjectRegister(claudePluginModule)
	if scope != "user" {
		module = bytes.Replace(module, []byte("const server = 'OpenAbstractions';"), []byte("const server = 'plugin:openabstractions:OpenAbstractions';"), 1)
	}
	files := map[string][]byte{
		".claude-plugin/marketplace.json":   PluginJSON(map[string]any{"name": market, "owner": map[string]string{"name": "OpenAbstractions"}, "plugins": []any{map[string]string{"name": "openabstractions", "source": "./plugin"}}}),
		"plugin/.claude-plugin/plugin.json": PluginJSON(map[string]any{"name": "openabstractions", "displayName": "OpenAbstractions", "version": "0.8.0", "description": "OpenAbstractions native MCP integration", "author": map[string]string{"name": "OpenAbstractions"}, "mcpServers": map[string]any{"OpenAbstractions": claudeEntry{Type: "stdio", Command: self, Args: []string{"--state-dir", state}, Env: map[string]string{}}}}),
		"plugin/README.md":                  []byte("# OpenAbstractions\n\nPersonal project native MCP integration (local scope). The native module observes its own admitted inbox and submits only a fixed OA pointer. Actual Desktop session association and idle wake require separate receiving.\n"),
		"plugin/hooks/hooks.json":           []byte("{\"modules\":[\"./register.js\"]}\n"),
		// Module source acceptance and native configuration are separate from
		// actual Desktop loading, association and idle-wake qualification.
		"plugin/hooks/register.js": module,
	}
	if scope == "user" {
		files["plugin/.claude-plugin/plugin.json"] = PluginJSON(map[string]any{"name": "openabstractions", "displayName": "OpenAbstractions", "version": "0.8.0", "description": "OpenAbstractions asynchronous message companion", "author": map[string]string{"name": "OpenAbstractions"}})
		files["plugin/README.md"] = []byte("# OpenAbstractions\n\nGlobal personal message companion for the existing user OpenAbstractions MCP connection. This mod starts no MCP server. Claude Code Desktop 2.1.309 validator receiving and actual idle delivery are separate. Reload plugins or reopen Claude Code after installation. Peer messages remain untrusted assistant content and supply no person consent.\n")
	}
	return files
}

func ReadClaudeMarker(ctx context.Context, path string) ([]byte, *ClaudeMarker, error) {
	if err := privatedir.CheckFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	b, err := cas.ReadLimitContext(ctx, path, claudeMarkerLimit)
	if err != nil || b == nil {
		return b, nil, err
	}
	var m ClaudeMarker
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || len(m.Files) != 5 {
		return nil, nil, errors.New("unknown plugin ownership record")
	}
	return b, &m, nil
}

// ReadClaudeProject proves the personal local declaration independently of the
// CLI's merged list. Shared project and user settings remain outside this owner.
func ReadClaudeProject(ctx context.Context, project fileid.Handle, market, root string) (bool, bool, error) {
	h, err := project.Beneath(".claude", fileid.Options{Links: fileid.RefuseLinks})
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	defer h.Close()
	f, err := h.Beneath("settings.local.json", fileid.Options{Links: fileid.RefuseLinks})
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	defer f.Close()
	info, err := f.File().Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false, false, errors.New("project settings must be regular")
	}
	b, err := cas.ReadLimitContext(ctx, f.FileID().Path(), claudeProjectLimit)
	if err != nil || len(b) > claudeProjectLimit {
		return false, false, errors.New("project plugin settings unavailable")
	}
	var doc struct {
		Enabled map[string]bool `json:"enabledPlugins"`
		Markets map[string]struct {
			Source struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
		} `json:"extraKnownMarketplaces"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return false, false, errors.New("unknown project settings")
	}
	if !setupIdentitiesCurrent(ctx, []setupIdentityRef{project, h, f}) {
		return false, false, errors.New("project settings identity changed")
	}
	entry, known := doc.Markets[market]
	if known && (entry.Source.Source != "directory" || entry.Source.Path != root) {
		return false, false, errors.New("foreign project marketplace")
	}
	enabled, present := doc.Enabled["openabstractions@"+market]
	if present && !enabled {
		return false, false, errors.New("project plugin disabled; preserved")
	}
	return known, present, nil
}

// PluginList reads a vendor CLI's JSON list of plugin or marketplace rows and
// refuses null or an empty row.
func PluginList(raw []byte) ([]map[string]json.RawMessage, error) {
	var entries []map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("unknown native plugin list")
	}
	for _, e := range entries {
		if e == nil {
			return nil, errors.New("unknown native plugin entry")
		}
	}
	return entries, nil
}

// PluginString is row field k as a string, empty when absent or not a string.
func PluginString(e map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(e[k], &s)
	return s
}

func ApplyClaude(ctx context.Context, o ClaudeOptions, self string, run claudeSetupRunner) error {
	return applyClaude(ctx, o, self, run, false)
}

func applyClaude(ctx context.Context, o ClaudeOptions, self string, run claudeSetupRunner, ownedOnly bool, authority ...func(context.Context) error) error {
	var companionCurrent func() error
	checkAuthority := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, authorize := range authority {
			if authorize == nil {
				return errors.New("repair authority unavailable")
			}
			if err := authorize(ctx); err != nil {
				return err
			}
		}
		if companionCurrent != nil {
			return companionCurrent()
		}
		return nil
	}
	if o.Scope == "" {
		o.Scope = "local"
	}
	if o.Scope != "user" && o.Scope != "local" {
		return errors.New("unknown personal plugin scope")
	}
	if o.RegisteredProgram != "" && ((o.Action != "remove" && o.Action != "status") || !filepath.IsAbs(o.RegisteredProgram)) {
		return errors.New("registered program is a removal locator only")
	}
	anchor := o.Project
	if o.Scope == "user" {
		if o.Project != "" {
			return errors.New("user companion requires no project")
		}
		if o.Action == "status" {
			if _, err := os.Lstat(o.State); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return ErrClaudeAbsent
				}
				return err
			}
			if err := privatedir.CheckDir(o.State); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return ErrClaudeAbsent
				}
				return err
			}
		} else if err := privatedir.Ensure(o.State); err != nil {
			return err
		}
		anchor = o.State
	}
	project, pid, err := setupIdentity(ctx, anchor, true)
	if err != nil {
		return err
	}
	defer project.Close()
	image, sid, err := setupIdentity(ctx, self, false)
	if err != nil {
		return err
	}
	defer image.Close()
	cli, cid, err := setupIdentity(ctx, o.CLI, false)
	if err != nil {
		return err
	}
	defer cli.Close()
	if o.Action != "status" {
		if err = privatedir.Ensure(o.State); err != nil {
			return err
		}
	} else if err = privatedir.CheckDir(o.State); err != nil {
		return err
	}
	state, stid, err := setupIdentity(ctx, o.State, true)
	if err != nil {
		return err
	}
	defer state.Close()
	handles := []setupIdentityRef{project, image, cli, state}
	key := PluginHash([]byte(project.FileID().Key()))[:claudeSourceKeyCharacters]
	if o.Scope == "user" {
		key = "user"
	}
	market := "oa-local-" + key
	root, markerPath := claudePluginPaths(state.FileID().Path(), key)
	base, marker, err := ReadClaudeMarker(ctx, markerPath)
	if err != nil {
		return err
	}
	if ownedOnly && (o.Action != "update" || marker == nil || !marker.MarketplaceOwned || !marker.PluginOwned) {
		return errors.New("native plugin ownership needs Setup; preserved")
	}
	if ownedOnly {
		selected, err := fileid.ParseStoredFileID(marker.CLI)
		// Claude Code updates move its CLI to a new version folder of the
		// same installation; the selected CLI is held and checked below.
		if err != nil || !nativecli.SameClaudeCLIInstall(selected.Path(), o.CLI) {
			return errors.New("selected plugin CLI changed; preserved")
		}
	}
	if o.Action == "status" && marker == nil {
		return ErrClaudeAbsent
	}
	files := ClaudeFilesForScope(market, image.FileID().Path(), state.FileID().Path(), o.Scope)
	if len(files["plugin/hooks/register.js"]) == 0 {
		return errors.New("generated wake prompt unavailable")
	}
	if o.Scope == "user" && o.Action != "remove" {
		module, err := ClaudeBackgroundModule(ctx, state.FileID().Path(), cli.FileID().Path(), stid)
		if err != nil {
			return err
		}
		files["plugin/hooks/register.js"] = module
		if ownedOnly {
			expectedModule := append([]byte(nil), module...)
			companionCurrent = func() error {
				current, err := ClaudeBackgroundModule(ctx, state.FileID().Path(), cli.FileID().Path(), stid)
				if err != nil {
					return err
				}
				if !bytes.Equal(current, expectedModule) {
					return errors.New("background approval changed; companion preserved")
				}
				return nil
			}
		}
	}
	wantVersion := SetClaudeGeneration(files)
	expected := ClaudeMarker{Project: pid, Self: sid, CLI: cid, State: stid, Marketplace: market, Scope: o.Scope, Files: map[string]string{}}
	for name, b := range files {
		expected.Files[name] = PluginHash(b)
	}
	if marker != nil {
		expected.InstalledVersion = marker.InstalledVersion
		// As in bare MCP setup, an upgrade at the exact configured executable
		// path preserves configuration ownership. Actual images are held and
		// revalidated throughout this operation; this grants no native rights.
		oldSelf, e1 := fileid.ParseStoredFileID(marker.Self)
		ownedProgram := image.FileID().Path()
		if o.RegisteredProgram != "" {
			ownedProgram = o.RegisteredProgram
		}
		if marker.Scope != o.Scope || marker.Project != pid || marker.State != stid || marker.Marketplace != market || e1 != nil || oldSelf.Path() != ownedProgram {
			return errors.New("plugin ownership identity changed")
		}
		// An explicitly selected current CLI may move during a vendor update.
		// Its historical location is provenance, not configuration authority.
		expected.MarketplaceOwned, expected.PluginOwned = marker.MarketplaceOwned, marker.PluginOwned
		if o.Action == "remove" {
			expected.Files = marker.Files
			expected.Self = marker.Self
		}
	}
	known, installed := expected.MarketplaceOwned, expected.PluginOwned
	if o.Scope == "local" {
		known, installed, err = ReadClaudeProject(ctx, project, market, root)
	}
	if err != nil {
		return err
	}
	if (known && !expected.MarketplaceOwned) || (installed && !expected.PluginOwned) {
		return errors.New("project plugin configuration lacks confirmed ownership; preserved")
	}
	absentUserRemoval := o.Scope == "user" && o.Action == "remove" && marker == nil
	if o.Action != "install" && marker == nil && !absentUserRemoval {
		return errors.New("no positive plugin ownership")
	}
	// Every existing payload member must match the last owned bytes, including
	// during update/removal. Unknown contents are never overwritten or removed.
	if ownedOnly {
		if err := restoreOwnedClaudeMembers(ctx, root, markerPath, base, marker, files, handles, checkAuthority); err != nil {
			return err
		}
	}
	admitted := map[string][]byte{}
	for name := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := privatedir.CheckFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		b, err := cas.ReadLimitContext(ctx, path, claudePayloadLimit)
		if err != nil {
			return err
		}
		if b != nil && (marker == nil || PluginHash(b) != marker.Files[name]) {
			return errors.New("plugin payload changed; preserved")
		}
		if marker != nil && b == nil {
			return errors.New("owned plugin payload missing")
		}
		admitted[name] = b
	}
	ownedVersion := ""
	if marker != nil {
		var manifest struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(admitted["plugin/.claude-plugin/plugin.json"], &manifest) != nil {
			return errors.New("owned manifest unavailable")
		}
		ownedVersion = manifest.Version
	}
	ownedPayloadVersion := ownedVersion
	if marker != nil && marker.InstalledVersion != "" {
		ownedVersion = marker.InstalledVersion
	}
	if expected.InstalledVersion == "" {
		expected.InstalledVersion = ownedVersion
	}
	invoke := func(operation string, mutating bool, args ...string) ([]byte, error) {
		if mutating {
			if err := checkAuthority(); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, ClaudeInvocationFailure(ctx, operation, false, err)
		}
		if !setupIdentitiesCurrent(ctx, handles) {
			return nil, errors.New("selected identity changed")
		}
		current, ownership, err := ReadClaudeMarker(ctx, markerPath)
		if err != nil || !bytes.Equal(current, base) {
			return nil, errors.New("plugin ownership changed")
		}
		if ownership != nil {
			for name, want := range ownership.Files {
				// Only the closed payload roster is interpreted as a path.
				if _, ok := files[name]; !ok {
					return nil, errors.New("unknown owned payload member")
				}
				path := filepath.Join(root, filepath.FromSlash(name))
				if err := privatedir.CheckFile(path); err != nil {
					return nil, err
				}
				b, err := cas.ReadLimitContext(ctx, path, claudePayloadLimit)
				if err != nil || b == nil || PluginHash(b) != want {
					return nil, errors.New("plugin payload changed before vendor dispatch; preserved")
				}
			}
		}
		b, err := run(ctx, cli.FileID().Path(), project.FileID().Path(), args)
		if err != nil {
			return nil, claudeInvocationFailureFrom(ctx, operation, mutating, err, b)
		}
		if !setupIdentitiesCurrent(ctx, handles) {
			if err := ctx.Err(); err != nil {
				return nil, ClaudeInvocationFailure(ctx, operation, mutating, err)
			}
			return nil, errors.New("selected identity changed; outcome unknown")
		}
		return b, nil
	}
	version, err := invoke("version", false, "--version")
	if err != nil {
		return err
	}
	if !bytes.Contains(version, []byte("(Claude Code)")) {
		return errors.New("selected CLI is not Claude Code")
	}
	marketReadback := func(present bool) error {
		raw, err := invoke("marketplace_list", false, "plugin", "marketplace", "list", "--json")
		if err != nil {
			return err
		}
		entries, err := PluginList(raw)
		if err != nil {
			return err
		}
		count := 0
		for _, entry := range entries {
			if PluginString(entry, "name") == market {
				count++
				if PluginString(entry, "source") != "directory" || PluginString(entry, "path") != root || (PluginString(entry, "scope") != "" && PluginString(entry, "scope") != o.Scope) {
					return errors.New("foreign native marketplace; preserved")
				}
			}
		}
		if (present && count != 1) || (!present && count != 0) {
			return errors.New("native marketplace readback missing or ambiguous")
		}
		return nil
	}
	if err := marketReadback(known); err != nil {
		return err
	}
	pluginID := "openabstractions@" + market
	var installedFolders []string
	desiredVersion := wantVersion
	pluginReadback := func(present bool, wantVersion string) error {
		raw, err := invoke("plugin_list", false, "plugin", "list", "--json")
		if err != nil {
			return err
		}
		entries, err := PluginList(raw)
		if err != nil {
			return err
		}
		matches := 0
		for _, e := range entries {
			if absentUserRemoval && PluginString(e, "id") == pluginID {
				return errors.New("unowned native companion remains; preserved")
			}
			matchesScope := PluginString(e, "scope") == o.Scope && (o.Scope == "user" && PluginString(e, "projectPath") == "" || o.Scope == "local" && PluginString(e, "projectPath") == project.FileID().Path())
			if PluginString(e, "id") == pluginID && matchesScope {
				matches++
				var enabled bool
				matchesVersion := PluginString(e, "version") == wantVersion || PluginString(e, "folderVersion") == wantVersion
				if marker != nil && marker.PluginOwned {
					matchesVersion = matchesVersion || PluginString(e, "version") == ownedPayloadVersion || PluginString(e, "folderVersion") == ownedPayloadVersion
				}
				if (o.Action == "update" || o.Action == "install") && marker != nil && marker.PluginOwned {
					matchesVersion = matchesVersion || PluginString(e, "version") == desiredVersion || PluginString(e, "folderVersion") == desiredVersion
				}
				if present && (json.Unmarshal(e["enabled"], &enabled) != nil || !enabled || !matchesVersion) {
					return errors.New("native plugin source/version/enabled differs")
				}
				if present {
					installedFolders = nil
					if folder := PluginString(e, "readFromFolder"); folder != "" {
						if folder != filepath.Join(root, "plugin") {
							return errors.New("foreign native plugin source; preserved")
						}
						installedFolders = append(installedFolders, folder)
					}
					selectedProject := ""
					if o.Scope == "local" {
						selectedProject = project.FileID().Path()
					}
					folder, err := ClaudeInstalledFolder(ctx, e, pluginID, o.Scope, selectedProject, PluginString(e, "version"), filepath.Join(root, "plugin"))
					if err != nil {
						return err
					}
					if len(installedFolders) == 0 || installedFolders[0] != folder {
						installedFolders = append(installedFolders, folder)
					}
				}
			}
		}
		if (present && matches != 1) || (!present && matches != 0) {
			return errors.New("native plugin readback missing or ambiguous")
		}
		return nil
	}
	compareCurrent := func(wantKnown, wantInstalled bool, wantVersion string) error {
		// Vendor mutation is name-based. This final comparison narrows the
		// external-edit window; the vendor offers no compare-and-delete API.
		if o.Scope == "local" {
			k, i, err := ReadClaudeProject(ctx, project, market, root)
			if err != nil {
				return err
			}
			if k != wantKnown || i != wantInstalled {
				return errors.New("project plugin changed before vendor mutation; preserved")
			}
		}
		if err := marketReadback(wantKnown); err != nil {
			return err
		}
		return pluginReadback(wantInstalled, wantVersion)
	}
	if err := compareCurrent(known, installed, ownedVersion); err != nil {
		return err
	}
	if absentUserRemoval {
		// The held CLI and guarded list readbacks positively establish absence.
		// No payload, vendor entry, or ownership record needs a mutation.
		return nil
	}
	if o.Action == "status" {
		if !known || !installed {
			return ErrClaudeAbsent
		}
		for name, want := range expected.Files {
			if PluginHash(admitted[name]) != want {
				return ErrClaudeUpdate
			}
		}
		return verifyClaudePluginCopies(ctx, installedFolders, files)
	}
	store := casapi.BoundedFileStore{MaxBytes: claudeMarkerLimit}
	saveMarker := func() error {
		if err := checkAuthority(); err != nil {
			return err
		}
		if !setupIdentitiesCurrent(ctx, handles) {
			return errors.New("selected identity changed before ownership save")
		}
		encoded := PluginJSON(expected)
		if err := store.WriteContext(ctx, markerPath, casapi.Value{Data: base}, encoded); err != nil {
			return err
		}
		base = encoded
		return nil
	}
	if o.Action != "remove" {
		for _, dir := range []string{root, filepath.Join(root, ".claude-plugin"), filepath.Join(root, "plugin"), filepath.Join(root, "plugin", ".claude-plugin"), filepath.Join(root, "plugin", "hooks")} {
			if err := checkAuthority(); err != nil {
				return err
			}
			if err := privatedir.Ensure(dir); err != nil {
				return err
			}
		}
		for name, b := range files {
			if err := checkAuthority(); err != nil {
				return err
			}
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := privatedir.Ensure(filepath.Dir(path)); err != nil {
				return err
			}
			if err := (casapi.BoundedFileStore{MaxBytes: claudePayloadLimit}).WriteContext(ctx, path, casapi.Value{Data: admitted[name]}, b); err != nil {
				return err
			}
		}
		// Payload provenance is durable before the first vendor effect. The two
		// ownership bits remain false until independently confirmed readback.
		if err := saveMarker(); err != nil {
			return err
		}
		if _, err := invoke("validate", false, "plugin", "validate", filepath.Join(root, "plugin"), "--json"); err != nil {
			return err
		}
	}
	if o.Action == "remove" {
		if installed {
			if err := compareCurrent(known, true, ownedVersion); err != nil {
				return err
			}
			if _, err = invoke("uninstall", true, "plugin", "uninstall", pluginID, "--scope", o.Scope, "--json"); err != nil {
				return err
			}
		}
		if err := compareCurrent(known, false, ""); err != nil {
			return err
		}
		expected.PluginOwned = false
		if err := saveMarker(); err != nil {
			return err
		}
	} else {
		if !known {
			if err := compareCurrent(false, installed, ownedVersion); err != nil {
				return err
			}
			if _, err = invoke("marketplace_add", true, "plugin", "marketplace", "add", root, "--scope", o.Scope, "--json"); err != nil {
				return err
			}
			if err := compareCurrent(true, installed, ownedVersion); err != nil {
				return err
			}
			expected.MarketplaceOwned = true
			if err := saveMarker(); err != nil {
				return err
			}
			known = true
		}
		if o.Action == "update" || installed {
			if err := compareCurrent(known, installed, ownedVersion); err != nil {
				return err
			}
			if _, err = invoke("marketplace_update", true, "plugin", "marketplace", "update", market, "--json"); err != nil {
				return err
			}
		}
		action := "install"
		if installed {
			action = "update"
		}
		if err := compareCurrent(known, installed, ownedVersion); err != nil {
			return err
		}
		if _, err = invoke(action, true, "plugin", action, pluginID, "--scope", o.Scope, "--json"); err != nil {
			return err
		}
		if err := compareCurrent(true, true, wantVersion); err != nil {
			return err
		}
		if err := verifyClaudePluginCopies(ctx, installedFolders, files); err != nil {
			return err
		}
		expected.PluginOwned = true
		expected.InstalledVersion = wantVersion
		if err := saveMarker(); err != nil {
			return err
		}
	}
	if o.Action == "remove" && known {
		if err := compareCurrent(true, false, ""); err != nil {
			return err
		}
		if _, err = invoke("marketplace_remove", true, "plugin", "marketplace", "remove", market, "--scope", o.Scope, "--json"); err != nil {
			return err
		}
		if err := compareCurrent(false, false, ""); err != nil {
			return err
		}
		expected.MarketplaceOwned = false
		if err := saveMarker(); err != nil {
			return err
		}
	}
	if err := marketReadback(o.Action != "remove"); err != nil {
		return err
	}
	if o.Scope == "user" {
		if err := pluginReadback(o.Action != "remove", wantVersion); err != nil {
			return err
		}
		if o.Action != "remove" {
			return verifyClaudePluginCopies(ctx, installedFolders, files)
		}
		return nil
	}
	known, installed, err = ReadClaudeProject(ctx, project, market, root)
	if err != nil {
		return err
	}
	if o.Action == "remove" {
		if known || installed {
			return errors.New("project plugin configuration remains")
		}
	} else if !known || !installed {
		return errors.New("project plugin configuration not established")
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return errors.New("selected identity changed after readback")
	}
	if o.Action != "remove" {
		return verifyClaudePluginCopies(ctx, installedFolders, files)
	}
	return nil
}

// Invocation describes a bounded vendor diagnostic without exposing output.
type Invocation interface {
	error
	Operation() string
	Reason() string
	Mutating() bool
}

func (e *ClaudeInvocationError) Operation() string { return e.operation }
func (e *ClaudeInvocationError) Reason() string    { return e.reason }
func (e *ClaudeInvocationError) Mutating() bool    { return e.mutating }

// ClaudeInvocationDiagnostic words an error that carries a vendor invocation
// failure as that failure alone, keeping err as its cause. Any other error
// returns unchanged.
func ClaudeInvocationDiagnostic(err error) error {
	var invocation Invocation
	if errors.As(err, &invocation) {
		return &ClaudeInvocationError{invocation.Operation(), invocation.Reason(), invocation.Mutating(), err}
	}
	return err
}
