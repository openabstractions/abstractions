package cacheproducer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
)

const CompanionBackupMaxBytes = 256 << 10

var ErrCompanionBackupRefused = errors.New("companion backup refused")

func backupRefused(word string) error {
	return errors.Join(ErrCompanionBackupRefused, errors.New(word))
}

type companionNative struct{ Market, Plugin, Registry json.RawMessage }
type companionBackup struct {
	Version     int
	Parent      string
	Declaration json.RawMessage
	HelperHash  string
	Members     []companionBackupMember
}
type companionBackupMember struct {
	Binding              string
	RootID               string
	CacheID              string
	HostReceipt, Receipt []byte
	Files                map[string][]byte
	Native               companionNative
	Preferences          map[string]string
}

func backupParent(ref string) bool {
	_, err := hex.DecodeString(ref)
	return len(ref) == 48 && err == nil && strings.ToLower(ref) == ref
}
func parseCompanionBackup(raw []byte) (companionBackup, companionDeclaration, error) {
	var b companionBackup
	var d companionDeclaration
	if len(raw) == 0 || len(raw) > CompanionBackupMaxBytes {
		return b, d, backupRefused("size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&b) != nil || decoder.Decode(new(any)) != io.EOF {
		return b, d, backupRefused("shape")
	}
	canonical, err := canonicalCompanion(b)
	if err != nil || !bytes.Equal(raw, canonical) || b.Version != 1 || !backupParent(b.Parent) || len(b.HelperHash) != 64 {
		return b, d, backupRefused("identity")
	}
	if _, err := hex.DecodeString(b.HelperHash); err != nil || strings.ToLower(b.HelperHash) != b.HelperHash {
		return b, d, backupRefused("helper-hash")
	}
	d, err = parseCompanion(b.Declaration)
	if err != nil || len(d.Members) != len(b.Members) {
		return b, d, backupRefused("declaration")
	}
	for i, m := range d.Members {
		saved := b.Members[i]
		if saved.Binding != m.Binding || saved.Files == nil || len(saved.Files) != len(m.Files) || len(saved.HostReceipt) == 0 || len(saved.HostReceipt) > claudeMarkerLimit {
			return b, d, backupRefused("member")
		}
		if err := closedHostReceipt(saved.HostReceipt); err != nil {
			return b, d, err
		}
		for name, want := range m.Files {
			if PluginHash(saved.Files[name]) != want {
				return b, d, backupRefused("file-hash")
			}
		}
		if m.Requirement == "none" {
			if len(saved.Receipt) != 0 || len(saved.Native.Market) != 0 || len(saved.Native.Plugin) != 0 || len(saved.Native.Registry) != 0 {
				return b, d, backupRefused("none")
			}
			continue
		}
		if len(saved.Receipt) == 0 || len(saved.Receipt) > claudeMarkerLimit {
			return b, d, backupRefused("receipt")
		}
		if _, err := fileid.ParseStoredFileID(saved.RootID); err != nil {
			return b, d, backupRefused("root-identity")
		}
		if _, err := fileid.ParseStoredFileID(saved.CacheID); err != nil {
			return b, d, backupRefused("cache-identity")
		}
		if err := closedProducerReceipt(m.Owner, saved.Receipt); err != nil {
			return b, d, err
		}
		if err := closedNative(m.Owner, saved.Native); err != nil {
			return b, d, err
		}
	}
	return b, d, nil
}
func ValidateCompanionBackup(raw []byte) error { _, _, err := parseCompanionBackup(raw); return err }

// Capture returns bytes for the existing parent intent's bounded CAS. It
// creates no files or archive and retains no unrelated vendor profile entries.
func CaptureCompanionBackup(ctx context.Context, declaration []byte, parentRef string, authorize func() error) ([]byte, error) {
	return captureCompanionBackup(ctx, declaration, parentRef, authorize, RunClaude, RunCodex)
}
func captureCompanionBackup(ctx context.Context, declaration []byte, parentRef string, authorize func() error, claudeRun ClaudeRunner, codexRun CodexRunner) ([]byte, error) {
	if !backupParent(parentRef) || authorize == nil {
		return nil, backupRefused("authority")
	}
	if err := authorize(); err != nil {
		return nil, err
	}
	d, err := parseCompanion(declaration)
	if err != nil {
		return nil, err
	}
	guard := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := authorize(); err != nil {
			return err
		}
		return companionImage(ctx, d.Program, d.FileKey)
	}
	cr := func(c context.Context, p, dir string, args []string) ([]byte, error) {
		if err := guard(); err != nil {
			return nil, err
		}
		out, err := claudeRun(c, p, dir, args)
		if err != nil {
			return nil, err
		}
		return out, guard()
	}
	dr := func(c context.Context, p, dir string, args []string) (CodexResult, error) {
		if err := guard(); err != nil {
			return CodexResult{}, err
		}
		out, err := codexRun(c, p, dir, args)
		if err != nil {
			return out, err
		}
		return out, guard()
	}
	if reading, err := observeCompanionDeclaration(ctx, declaration, cr, dr); err != nil || reading.State != CopyCurrent {
		return nil, backupRefused("not-current")
	}
	// The bounded restore owner supports the same closed source roster. A new
	// roster requires fresh producer qualification before it can promise Undo.
	successor, err := SuccessorCompanionDeclaration(ctx, declaration, d.Program, d.FileKey)
	if err != nil {
		return nil, err
	}
	next, err := parseCompanion(successor)
	if err != nil {
		return nil, err
	}
	for i, m := range d.Members {
		if len(m.Files) != len(next.Members[i].Files) {
			return nil, backupRefused("recipe-roster")
		}
		for name := range m.Files {
			if _, ok := next.Members[i].Files[name]; !ok {
				return nil, backupRefused("recipe-roster")
			}
		}
	}
	hash, err := companionHelperHash(ctx, d.Program, d.FileKey)
	if err != nil {
		return nil, err
	}
	b := companionBackup{Version: 1, Parent: parentRef, Declaration: append([]byte(nil), declaration...), HelperHash: hash, Members: []companionBackupMember{}}
	for _, m := range d.Members {
		saved := companionBackupMember{Binding: m.Binding, Files: map[string][]byte{}, Preferences: map[string]string{}}
		saved.HostReceipt, err = cas.ReadLimitContext(ctx, m.Receipt, claudeMarkerLimit)
		if err != nil {
			return nil, err
		}
		if err = closedHostReceipt(saved.HostReceipt); err != nil {
			return nil, err
		}
		for name, want := range m.Files {
			body, err := readOwnedCompanionFile(ctx, m.Root, name)
			if err != nil || PluginHash(body) != want {
				return nil, backupRefused("source-changed")
			}
			saved.Files[name] = body
		}
		if m.Requirement == "required" {
			_, saved.RootID, err = setupIdentityClosed(ctx, m.Root, true)
			if err != nil {
				return nil, err
			}
			saved.Receipt, err = cas.ReadLimitContext(ctx, m.Root+".json", claudeMarkerLimit)
			if err != nil {
				return nil, err
			}
			if err = closedProducerReceipt(m.Owner, saved.Receipt); err != nil {
				return nil, err
			}
			if m.Owner == ClaudeProducer {
				err = observeDeclaredClaudeSnapshot(ctx, d, m, cr, &saved.Native)
			} else {
				err = observeDeclaredCodexSnapshot(ctx, d, m, dr, &saved.Native)
			}
			if err != nil {
				return nil, err
			}
			if err = closedNative(m.Owner, saved.Native); err != nil {
				return nil, err
			}
			var cache string
			if m.Owner == ClaudeProducer {
				var entry struct{ InstallPath string }
				if json.Unmarshal(saved.Native.Registry, &entry) != nil {
					return nil, backupRefused("cache-path")
				}
				cache = entry.InstallPath
			} else {
				var receipt codexHookOwnership
				if json.Unmarshal(saved.Receipt, &receipt) != nil {
					return nil, backupRefused("receipt")
				}
				stored, err := fileid.ParseStoredFileID(receipt.Cache)
				if err != nil {
					return nil, err
				}
				cache = stored.Path()
			}
			_, saved.CacheID, err = setupIdentityClosed(ctx, cache, true)
			if err != nil {
				return nil, err
			}
		}
		if m.Owner == ClaudeProducer && m.Scope == "user" {
			saved.Preferences, err = companionPreferenceDigests(ctx, m)
			if err != nil {
				return nil, err
			}
		}
		b.Members = append(b.Members, saved)
	}
	if err = guard(); err != nil {
		return nil, err
	}
	raw, err := canonicalCompanion(b)
	if err != nil {
		return nil, err
	}
	if err = ValidateCompanionBackup(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func companionHelperHash(ctx context.Context, program, key string) (string, error) {
	h, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return "", err
	}
	defer h.Close()
	if h.FileID().Key() != key {
		return "", backupRefused("helper-identity")
	}
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(h.File(), 256<<20))
	if err != nil || n <= 0 || n == 256<<20 {
		return "", backupRefused("helper-hash")
	}
	current, err := fileid.Resolve(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !current.Equal(h.FileID()) {
		return "", backupRefused("helper-identity")
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
func readOwnedCompanionFile(ctx context.Context, root, name string) ([]byte, error) {
	directory, _, err := setupIdentity(ctx, root, true)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	h, err := directory.Beneath(filepath.FromSlash(name), fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return nil, err
	}
	defer h.Close()
	body, err := readClaudePluginHeld(ctx, h)
	if err != nil {
		return nil, err
	}
	again, err := directory.Beneath(filepath.FromSlash(name), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	defer again.Close()
	if !again.FileID().Equal(h.FileID()) || !setupIdentitiesCurrent(ctx, []setupIdentityRef{directory}) {
		return nil, backupRefused("source-identity")
	}
	return body, nil
}
func companionPreferenceDigests(ctx context.Context, m companionMember) (map[string]string, error) {
	state, err := fileid.ParseStoredFileID(m.State)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, rule := range []string{claudeWatchRule, claudeAsksWatchRule} {
		raw, _, err := ReadWatchReceipt(ctx, WatchReceiptFile(state.Path(), rule))
		if err != nil {
			return nil, err
		}
		if raw == nil {
			out[rule] = "absent"
		} else {
			out[rule] = PluginHash(raw)
		}
	}
	return out, nil
}
func closedObject(raw []byte, allowed map[string]bool) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || len(raw) > claudeMarkerLimit || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, backupRefused("metadata")
	}
	for name := range fields {
		if !allowed[name] {
			return nil, backupRefused("unowned-metadata")
		}
	}
	return fields, nil
}
func names(values ...string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		out[v] = true
	}
	return out
}
func closedHostReceipt(raw []byte) error {
	fields, err := closedObject(raw, names("companion_declaration", "previous", "Previous", "Scope", "Self", "CLI", "State", "Name", "Active", "Entry", "scope", "project", "self", "cli", "state", "name", "active", "entry"))
	if err != nil {
		return err
	}
	entry := fields["entry"]
	if len(entry) == 0 {
		entry = fields["Entry"]
	}
	if len(entry) == 0 {
		return backupRefused("host-entry")
	}
	e, err := closedObject(entry, names("type", "command", "args", "env", "name", "enabled", "disabled_reason", "transport", "enabled_tools", "disabled_tools", "startup_timeout_sec", "tool_timeout_sec"))
	if err != nil {
		return err
	}
	if env := e["env"]; len(env) > 0 && string(env) != "{}" && string(env) != "null" {
		return backupRefused("host-env")
	}
	if tr := e["transport"]; len(tr) > 0 {
		transport, err := closedObject(tr, names("type", "command", "args", "env", "env_vars", "cwd"))
		if err != nil {
			return err
		}
		if env := transport["env"]; len(env) > 0 && string(env) != "{}" && string(env) != "null" {
			return backupRefused("host-env")
		}
		if env := transport["env_vars"]; len(env) > 0 && string(env) != "[]" && string(env) != "null" {
			return backupRefused("host-env")
		}
	}
	for _, field := range []string{"previous", "Previous"} {
		if previous := fields[field]; len(previous) > 0 {
			if _, err := closedObject(previous, names("application", "program", "file_key", "revision")); err != nil {
				return err
			}
		}
	}
	return nil
}
func closedProducerReceipt(owner string, raw []byte) error {
	allowed := names("Project", "State", "Self", "CLI", "Marketplace", "Scope", "Files", "MarketplaceOwned", "PluginOwned", "InstalledVersion")
	if owner == CodexProducer {
		allowed = names("Scope", "State", "Name", "Cache", "Market", "Plugin", "Program", "FileKey", "Version", "Files", "CacheFiles")
	}
	fields, err := closedObject(raw, allowed)
	if err != nil {
		return err
	}
	if owner == CodexProducer {
		return closedNative(owner, companionNative{Market: fields["Market"], Plugin: fields["Plugin"]})
	}
	return nil
}
func closedNative(owner string, s companionNative) error {
	market, plugin := names("name", "source", "path", "scope"), names("id", "scope", "projectPath", "readFromFolder", "version", "folderVersion", "enabled", "installPath")
	if owner == CodexProducer {
		market = names("name", "root")
		plugin = names("pluginId", "name", "marketplaceName", "version", "installed", "enabled", "source")
	}
	if _, err := closedObject(s.Market, market); err != nil {
		return err
	}
	p, err := closedObject(s.Plugin, plugin)
	if err != nil {
		return err
	}
	if owner == CodexProducer {
		if _, err := closedObject(p["source"], names("source", "path")); err != nil {
			return err
		}
	} else {
		if _, err := closedObject(s.Registry, names("scope", "projectPath", "installPath", "version")); err != nil {
			return err
		}
	}
	return nil
}
func selectedClaudeRegistry(ctx context.Context, m companionMember, market string) (json.RawMessage, error) {
	home, err := setupio.ClaudeConfigDir()
	if err != nil {
		return nil, err
	}
	directory, _, err := setupIdentity(ctx, home, true)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	h, err := directory.Beneath(filepath.Join("plugins", "installed_plugins.json"), fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return nil, err
	}
	defer h.Close()
	raw, err := readClaudePluginHeld(ctx, h)
	if err != nil {
		return nil, err
	}
	var record struct {
		Version int
		Plugins map[string][]json.RawMessage
	}
	if json.Unmarshal(raw, &record) != nil || record.Version != 2 {
		return nil, backupRefused("registry")
	}
	var selected json.RawMessage
	project, err := fileid.ParseStoredFileID(m.Project)
	if err != nil {
		return nil, err
	}
	for _, entry := range record.Plugins["openabstractions@"+market] {
		var row struct{ Scope, ProjectPath string }
		if json.Unmarshal(entry, &row) != nil {
			return nil, backupRefused("registry")
		}
		wantProject := ""
		if m.Scope == "local" {
			wantProject = project.Path()
		}
		if row.Scope == m.Scope && row.ProjectPath == wantProject {
			if selected != nil {
				return nil, backupRefused("registry-ambiguous")
			}
			selected = append([]byte(nil), entry...)
		}
	}
	if selected == nil {
		return nil, backupRefused("registry-absent")
	}
	return selected, nil
}

func sameNative(a, b companionNative) bool {
	for _, pair := range [][2]json.RawMessage{{a.Market, b.Market}, {a.Plugin, b.Plugin}, {a.Registry, b.Registry}} {
		x, _ := canonicalCompanion(pair[0])
		y, _ := canonicalCompanion(pair[1])
		if !bytes.Equal(x, y) {
			return false
		}
	}
	return true
}
func sameCompanionScope(a, b companionMember) bool {
	a.Version, b.Version = "", ""
	a.Files, b.Files = nil, nil
	return reflect.DeepEqual(a, b)
}
