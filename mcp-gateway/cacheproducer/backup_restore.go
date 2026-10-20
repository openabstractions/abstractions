package cacheproducer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// Restore is called only after the child committed the old helper. CURRENT is
// the child-captured pre-rollback snapshot, so its helper key is historical.
// Its exact bytes are CAS preconditions; they provide no native authority.
func RestoreCompanionBackup(ctx context.Context, prior, current, goal []byte, authorize func() error) error {
	return restoreCompanionBackup(ctx, prior, current, goal, authorize, RunClaude, RunCodex)
}
func restoreCompanionBackup(ctx context.Context, priorRaw, currentRaw, goalRaw []byte, authorize func() error, claudeRun ClaudeRunner, codexRun CodexRunner) error {
	prior, old, err := parseCompanionBackup(priorRaw)
	if err != nil {
		return err
	}
	current, now, err := parseCompanionBackup(currentRaw)
	if err != nil {
		return err
	}
	goal, err := parseCompanion(goalRaw)
	if err != nil {
		return err
	}
	if authorize == nil || prior.Parent == current.Parent || len(old.Members) != len(now.Members) || len(old.Members) != len(goal.Members) || old.Program != now.Program || old.Program != goal.Program {
		return backupRefused("relationship")
	}
	guard := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := authorize(); err != nil {
			return err
		}
		if err := companionImage(ctx, goal.Program, goal.FileKey); err != nil {
			return err
		}
		hash, err := companionHelperHash(ctx, goal.Program, goal.FileKey)
		if err != nil || hash != prior.HelperHash {
			return backupRefused("restored-helper-hash")
		}
		return nil
	}
	if err = guard(); err != nil {
		return err
	}
	for i, m := range old.Members {
		if !sameCompanionScope(m, now.Members[i]) || !reflect.DeepEqual(m, goal.Members[i]) || !reflect.DeepEqual(prior.Members[i].Preferences, current.Members[i].Preferences) {
			return backupRefused("scope-or-preference-changed")
		}
		if len(m.Files) != len(now.Members[i].Files) {
			return backupRefused("recipe-roster")
		}
		for name := range m.Files {
			if _, ok := now.Members[i].Files[name]; !ok {
				return backupRefused("recipe-roster")
			}
		}
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
	// Exact already-restored state is the sole successful no-op path.
	if reading, err := observeCompanionDeclaration(ctx, goalRaw, cr, dr); err == nil && reading.State == CopyCurrent {
		hostsCurrent := true
		for i, m := range goal.Members {
			want, e := reboundHostReceipt(ctx, prior.Members[i].HostReceipt, goal, m)
			if e != nil {
				return e
			}
			actual, e := cas.ReadLimitContext(ctx, m.Receipt, claudeMarkerLimit)
			if e != nil || !bytes.Equal(actual, want) {
				hostsCurrent = false
			}
		}
		if hostsCurrent {
			return guard()
		}
	}
	states := make([]*companionRestoreState, len(old.Members))
	defer func() {
		for _, state := range states {
			if state != nil {
				state.close()
			}
		}
	}()
	for i, m := range old.Members {
		goalNative := false
		if m.Requirement == "required" && m.Owner == CodexProducer {
			goalNative = observeDeclaredCodex(ctx, goal, m, dr) == nil
		}
		state, err := newCompanionRestoreState(ctx, m, now.Members[i], prior.Members[i], current.Members[i], goal, guard, goalNative)
		if err != nil {
			return err
		}
		states[i] = state
	}
	// Admit every member before the first source or vendor write.
	for _, state := range states {
		if err = state.check(ctx); err != nil {
			return err
		}
	}
	for i, state := range states {
		if err = state.admitNative(ctx, prior.Members[i], current.Members[i], cr, dr); err != nil {
			return err
		}
	}
	for i, state := range states {
		if state.member.Requirement == "none" {
			if err = state.replace(ctx, state.member.Receipt, state.hostGoal); err != nil {
				return err
			}
			continue
		}
		if state.member.Owner == ClaudeProducer {
			err = state.restoreClaude(ctx, goal, prior.Members[i], current.Members[i], cr)
		} else {
			err = state.restoreCodex(ctx, goal, prior.Members[i], current.Members[i], dr)
		}
		if err != nil {
			return err
		}
		if err = state.replace(ctx, state.member.Receipt, state.hostGoal); err != nil {
			return err
		}
	}
	if reading, err := observeCompanionDeclaration(ctx, goalRaw, cr, dr); err != nil || reading.State != CopyCurrent {
		return backupRefused("restore-readback")
	}
	return guard()
}

func (s *companionRestoreState) admitNative(ctx context.Context, prior, current companionBackupMember, cr ClaudeRunner, dr CodexRunner) error {
	if s.member.Requirement == "none" {
		return nil
	}
	if err := s.admitPriorCache(ctx, prior); err != nil {
		return err
	}
	state, project, cli, err := declaredContext(ctx, s.member)
	if err != nil {
		return err
	}
	var native companionNative
	if s.member.Owner == ClaudeProducer {
		native, err = readClaudeNativeSnapshot(ctx, s.member, cr, cli.Path(), project.Path())
	} else {
		key := PluginHash([]byte(project.Key() + "\x00" + s.member.Name))[:2*codexSourceKeyBytes]
		native, err = readCodexNativeSnapshot(ctx, dr, cli.Path(), state.Path(), "oa-session-"+key)
	}
	if err != nil {
		return err
	}
	if sameNative(native, current.Native) || sameNative(native, prior.Native) {
		return nil
	}
	if s.member.Owner == CodexProducer && len(native.Plugin) == 0 && nativeMarketEqual(native, current.Native) && bytes.Equal(s.expected[s.member.Root+".json"], s.receiptIntermediate) {
		return nil
	}
	return backupRefused("person-changed-native-entry")
}

func (s *companionRestoreState) admitPriorCache(ctx context.Context, prior companionBackupMember) error {
	stored, err := fileid.ParseStoredFileID(prior.CacheID)
	if err != nil {
		return err
	}
	if s.member.Owner == ClaudeProducer && fileid.SamePathName(stored.Path(), filepath.Join(s.member.Root, "plugin")) {
		return nil
	}
	_, status, err := fileid.Revalidate(ctx, stored)
	if err != nil {
		return err
	}
	if status == fileid.StateGone {
		if _, err := os.Lstat(stored.Path()); os.IsNotExist(err) {
			return nil
		}
		return backupRefused("old-cache-identity")
	}
	if status != fileid.StateSame {
		return backupRefused("old-cache-identity")
	}
	files := map[string]string{}
	if s.member.Owner == ClaudeProducer {
		for name, body := range prior.Files {
			if strings.HasPrefix(name, "plugin/") {
				files[strings.TrimPrefix(name, "plugin/")] = PluginHash(body)
			}
		}
	} else {
		var receipt codexHookOwnership
		if json.Unmarshal(prior.Receipt, &receipt) != nil {
			return backupRefused("receipt")
		}
		files = receipt.CacheFiles
	}
	for name, digest := range files {
		body, err := readOwnedCompanionFile(ctx, stored.Path(), name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || PluginHash(body) != digest {
			return backupRefused("person-changed-old-cache")
		}
	}
	return nil
}

type companionRestoreState struct {
	member, current                            companionMember
	expected                                   map[string][]byte
	goals                                      map[string][]byte
	hostGoal, receiptGoal, receiptIntermediate []byte
	guard                                      func() error
	handles                                    []setupIdentityRef
}

func newCompanionRestoreState(ctx context.Context, m, current companionMember, prior, now companionBackupMember, goal companionDeclaration, guard func() error, goalNative bool) (*companionRestoreState, error) {
	s := &companionRestoreState{member: m, current: current, expected: map[string][]byte{}, goals: map[string][]byte{}, guard: guard}
	complete := false
	defer func() {
		if !complete {
			s.close()
		}
	}()
	if m.Requirement == "required" {
		stored, err := fileid.ParseStoredFileID(now.RootID)
		if err != nil {
			return nil, err
		}
		id, status, err := fileid.Revalidate(ctx, stored)
		if err != nil || status != fileid.StateSame || id.Path() != m.Root {
			return nil, backupRefused("source-directory-changed")
		}
	}
	hostGoal, err := reboundHostReceipt(ctx, prior.HostReceipt, goal, m)
	if err != nil {
		return nil, err
	}
	s.hostGoal = hostGoal
	for name, body := range prior.Files {
		s.goals[filepath.Join(m.Root, filepath.FromSlash(name))] = body
	}
	s.goals[m.Receipt] = hostGoal
	if m.Requirement == "required" {
		s.receiptGoal, err = reboundProducerReceipt(ctx, prior.Receipt, goal, m)
		if err != nil {
			return nil, err
		}
		s.receiptIntermediate, err = intermediateProducerReceipt(ctx, prior.Receipt, now.Receipt, goal, m)
		if err != nil {
			return nil, err
		}
		s.goals[m.Root+".json"] = s.receiptGoal
	}
	for path, target := range s.goals {
		if err := privatedir.CheckFile(path); err != nil {
			return nil, err
		}
		actual, err := cas.ReadLimitContext(ctx, path, claudePayloadLimit)
		if err != nil {
			return nil, err
		}
		var before []byte
		switch path {
		case m.Receipt:
			before = now.HostReceipt
		case m.Root + ".json":
			before = now.Receipt
		default:
			relative, err := filepath.Rel(m.Root, path)
			if err != nil {
				return nil, err
			}
			before = now.Files[filepath.ToSlash(relative)]
		}
		allowed := bytes.Equal(actual, before) || bytes.Equal(actual, target) || path == m.Root+".json" && bytes.Equal(actual, s.receiptIntermediate)
		if !allowed && path == m.Root+".json" && m.Owner == CodexProducer && goalNative {
			allowed = equivalentCodexRestoreReceipt(actual, target)
		}
		if !allowed {
			return nil, backupRefused("person-changed-owned-file")
		}
		s.expected[path] = actual
		parent, _, err := setupIdentity(ctx, filepath.Dir(path), true)
		if err != nil {
			return nil, err
		}
		s.handles = append(s.handles, parent)
	}
	if m.Owner == ClaudeProducer && m.Scope == "user" {
		prefs, err := companionPreferenceDigests(ctx, m)
		if err != nil || !reflect.DeepEqual(prefs, now.Preferences) {
			return nil, backupRefused("preference-changed")
		}
	}
	complete = true
	return s, nil
}
func (s *companionRestoreState) close() {
	for _, h := range s.handles {
		_ = h.Close()
	}
}
func (s *companionRestoreState) check(ctx context.Context) error {
	if err := s.guard(); err != nil {
		return err
	}
	if !setupIdentitiesCurrent(ctx, s.handles) {
		return backupRefused("source-parent-changed")
	}
	for path, want := range s.expected {
		if err := privatedir.CheckFile(path); err != nil {
			return err
		}
		raw, err := cas.ReadLimitContext(ctx, path, claudePayloadLimit)
		if err != nil || !bytes.Equal(raw, want) {
			return backupRefused("person-changed-owned-file")
		}
	}
	return nil
}
func (s *companionRestoreState) replace(ctx context.Context, path string, body []byte) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	base, ok := s.expected[path]
	if !ok {
		return backupRefused("unowned-write")
	}
	if bytes.Equal(base, body) {
		return nil
	}
	if err := (casapi.BoundedFileStore{MaxBytes: claudePayloadLimit}).WriteContext(ctx, path, casapi.Value{Data: base}, body); err != nil {
		return err
	}
	s.expected[path] = append([]byte(nil), body...)
	return nil
}
func (s *companionRestoreState) source(ctx context.Context) error {
	paths := make([]string, 0, len(s.member.Files))
	for name := range s.member.Files {
		paths = append(paths, filepath.Join(s.member.Root, filepath.FromSlash(name)))
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := s.replace(ctx, path, s.goals[path]); err != nil {
			return err
		}
	}
	return nil
}
func reboundHostReceipt(ctx context.Context, raw []byte, goal companionDeclaration, m companionMember) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, backupRefused("host-receipt")
	}
	stored, err := actualHelperStored(ctx, goal.Program, goal.FileKey)
	if err != nil {
		return nil, err
	}
	self, _ := json.Marshal(stored)
	if _, ok := fields["self"]; ok {
		fields["self"] = self
	} else {
		fields["Self"] = self
	}
	member, err := canonicalCompanion(companionDeclaration{Version: 1, Program: goal.Program, FileKey: goal.FileKey, Members: []companionMember{m}})
	if err != nil {
		return nil, err
	}
	fields[CompanionDeclarationField] = member
	return canonicalCompanion(fields)
}
func actualHelperStored(ctx context.Context, program, key string) (string, error) {
	h, stored, err := setupIdentity(ctx, program, false)
	if err != nil {
		return "", err
	}
	defer h.Close()
	if h.FileID().Key() != key {
		return "", backupRefused("helper-identity")
	}
	return stored, nil
}
func reboundProducerReceipt(ctx context.Context, raw []byte, goal companionDeclaration, m companionMember) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, backupRefused("producer-receipt")
	}
	if m.Owner == ClaudeProducer {
		stored, err := actualHelperStored(ctx, goal.Program, goal.FileKey)
		if err != nil {
			return nil, err
		}
		fields["Self"], _ = json.Marshal(stored)
	} else {
		fields["Program"], _ = json.Marshal(goal.Program)
		fields["FileKey"], _ = json.Marshal(goal.FileKey)
	}
	return canonicalCompanion(fields)
}
func intermediateProducerReceipt(ctx context.Context, prior, current []byte, goal companionDeclaration, m companionMember) ([]byte, error) {
	raw, err := reboundProducerReceipt(ctx, prior, goal, m)
	if err != nil {
		return nil, err
	}
	var old, new map[string]json.RawMessage
	if json.Unmarshal(raw, &old) != nil || json.Unmarshal(current, &new) != nil {
		return nil, backupRefused("producer-receipt")
	}
	if m.Owner == ClaudeProducer {
		old["InstalledVersion"] = new["InstalledVersion"]
	} else {
		old["Plugin"] = json.RawMessage("null")
		old["Cache"] = json.RawMessage(`""`)
		delete(old, "CacheFiles")
	}
	return canonicalCompanion(old)
}

func equivalentCodexRestoreReceipt(actual, goal []byte) bool {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(actual, &a) != nil || json.Unmarshal(goal, &b) != nil {
		return false
	}
	for _, fields := range []map[string]json.RawMessage{a, b} {
		delete(fields, "Cache")
		delete(fields, "CacheFiles")
	}
	x, _ := canonicalCompanion(a)
	y, _ := canonicalCompanion(b)
	return bytes.Equal(x, y)
}

func (s *companionRestoreState) restoreClaude(ctx context.Context, goal companionDeclaration, prior, current companionBackupMember, run ClaudeRunner) error {
	state, project, cli, err := declaredContext(ctx, s.member)
	if err != nil {
		return err
	}
	_ = state
	read := func() (companionNative, error) {
		return readClaudeNativeSnapshot(ctx, s.member, run, cli.Path(), project.Path())
	}
	native, err := read()
	if err != nil {
		return err
	}
	if !sameNative(native, current.Native) && !sameNative(native, prior.Native) {
		return backupRefused("person-changed-native-entry")
	}
	if err = s.source(ctx); err != nil {
		return err
	}
	if !sameNative(native, prior.Native) {
		if err = s.replace(ctx, s.member.Root+".json", s.receiptIntermediate); err != nil {
			return err
		}
		var marker ClaudeMarker
		if json.Unmarshal(prior.Receipt, &marker) != nil {
			return backupRefused("receipt")
		}
		if err = s.check(ctx); err != nil {
			return err
		}
		if err = s.admitPriorCache(ctx, prior); err != nil {
			return err
		}
		if _, err = run(ctx, cli.Path(), project.Path(), []string{"plugin", "marketplace", "update", marker.Marketplace, "--json"}); err != nil {
			return err
		}
		if err = s.check(ctx); err != nil {
			return err
		}
		if err = s.admitPriorCache(ctx, prior); err != nil {
			return err
		}
		if _, err = run(ctx, cli.Path(), project.Path(), []string{"plugin", "update", "openabstractions@" + marker.Marketplace, "--scope", s.member.Scope, "--json"}); err != nil {
			return err
		}
	}
	native, err = read()
	if err != nil || !sameNative(native, prior.Native) {
		return backupRefused("native-old-version-unconfirmed")
	}
	if err = s.replace(ctx, s.member.Root+".json", s.receiptGoal); err != nil {
		return err
	}
	return nil
}
func readClaudeNativeSnapshot(ctx context.Context, m companionMember, run ClaudeRunner, cli, project string) (companionNative, error) {
	var snapshot companionNative
	raw, err := run(ctx, cli, project, []string{"plugin", "marketplace", "list", "--json"})
	if err != nil {
		return snapshot, err
	}
	markets, err := PluginList(raw)
	if err != nil {
		return snapshot, err
	}
	marketName := ""
	for _, row := range markets {
		if PluginString(row, "path") == m.Root {
			if snapshot.Market != nil {
				return snapshot, backupRefused("native-ambiguous")
			}
			snapshot.Market = PluginJSON(row)
			marketName = PluginString(row, "name")
		}
	}
	raw, err = run(ctx, cli, project, []string{"plugin", "list", "--json"})
	if err != nil {
		return snapshot, err
	}
	plugins, err := PluginList(raw)
	if err != nil {
		return snapshot, err
	}
	for _, row := range plugins {
		if PluginString(row, "id") == "openabstractions@"+marketName && PluginString(row, "scope") == m.Scope {
			if snapshot.Plugin != nil {
				return snapshot, backupRefused("native-ambiguous")
			}
			snapshot.Plugin = PluginJSON(row)
		}
	}
	snapshot.Registry, err = selectedClaudeRegistry(ctx, m, marketName)
	if err != nil {
		return snapshot, err
	}
	return snapshot, closedNative(m.Owner, snapshot)
}

func (s *companionRestoreState) restoreCodex(ctx context.Context, goal companionDeclaration, prior, current companionBackupMember, run CodexRunner) error {
	state, project, cli, err := declaredContext(ctx, s.member)
	if err != nil {
		return err
	}
	key := PluginHash([]byte(project.Key() + "\x00" + s.member.Name))[:2*codexSourceKeyBytes]
	market := "oa-session-" + key
	read := func() (companionNative, error) {
		return readCodexNativeSnapshot(ctx, run, cli.Path(), state.Path(), market)
	}
	native, err := read()
	if err != nil {
		return err
	}
	intermediate := bytes.Equal(s.expected[s.member.Root+".json"], s.receiptIntermediate)
	if !sameNative(native, current.Native) && !sameNative(native, prior.Native) && !(intermediate && len(native.Plugin) == 0 && nativeMarketEqual(native, current.Native)) {
		return backupRefused("person-changed-native-entry")
	}
	if err = s.source(ctx); err != nil {
		return err
	}
	if !sameNative(native, prior.Native) {
		if len(native.Plugin) > 0 {
			if err = s.check(ctx); err != nil {
				return err
			}
			if err = s.admitPriorCache(ctx, prior); err != nil {
				return err
			}
			result, err := run(ctx, cli.Path(), state.Path(), []string{"plugin", "remove", codexHookPlugin + "@" + market, "--json"})
			if err != nil || result.Code != 0 {
				return backupRefused("native-remove-unknown")
			}
			native, err = read()
			if err != nil || len(native.Plugin) != 0 || !nativeMarketEqual(native, current.Native) {
				return backupRefused("native-remove-unconfirmed")
			}
		}
		if err = s.replace(ctx, s.member.Root+".json", s.receiptIntermediate); err != nil {
			return err
		}
		if err = s.check(ctx); err != nil {
			return err
		}
		if err = s.admitPriorCache(ctx, prior); err != nil {
			return err
		}
		result, err := run(ctx, cli.Path(), state.Path(), []string{"plugin", "add", codexHookPlugin + "@" + market, "--json"})
		if err != nil || result.Code != 0 {
			return backupRefused("native-add-unknown")
		}
	}
	native, err = read()
	if err != nil || !sameNative(native, prior.Native) {
		return backupRefused("native-old-version-unconfirmed")
	}
	// The vendor may recreate its directory or normalize JSON. Record only
	// actual cache identity/digests after checking the retained source recipe.
	cache := filepath.Join(project.Path(), "plugins", "cache", market, codexHookPlugin, s.member.Version)
	_, cacheID, err := setupIdentityClosed(ctx, cache, true)
	if err != nil {
		return err
	}
	var receipt codexHookOwnership
	if json.Unmarshal(s.receiptGoal, &receipt) != nil {
		return backupRefused("receipt")
	}
	receipt.Cache = cacheID
	receipt.Market = native.Market
	receipt.Plugin = native.Plugin
	receipt.CacheFiles = map[string]string{}
	for name, body := range prior.Files {
		prefix := "plugins/" + codexHookPlugin + "/"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		relative := strings.TrimPrefix(name, prefix)
		actual, err := readOwnedCompanionFile(ctx, cache, relative)
		if err != nil {
			return err
		}
		var a, b any
		if json.Unmarshal(actual, &a) != nil || json.Unmarshal(body, &b) != nil || !reflect.DeepEqual(a, b) {
			return backupRefused("native-cache-differs")
		}
		receipt.CacheFiles[relative] = PluginHash(actual)
	}
	raw, err := canonicalCompanion(receipt)
	if err != nil {
		return err
	}
	return s.replace(ctx, s.member.Root+".json", raw)
}
func nativeMarketEqual(a, b companionNative) bool {
	x, _ := canonicalCompanion(a.Market)
	y, _ := canonicalCompanion(b.Market)
	return bytes.Equal(x, y)
}
func readCodexNativeSnapshot(ctx context.Context, run CodexRunner, cli, state, market string) (companionNative, error) {
	var out companionNative
	result, err := run(ctx, cli, state, []string{"plugin", "marketplace", "list", "--json"})
	if err != nil || result.Code != 0 {
		return out, backupRefused("native-read")
	}
	var markets struct {
		Marketplaces []json.RawMessage `json:"marketplaces"`
	}
	if json.Unmarshal(result.Output, &markets) != nil {
		return out, backupRefused("native-read")
	}
	for _, row := range markets.Marketplaces {
		var entry struct{ Name string }
		if json.Unmarshal(row, &entry) != nil {
			return out, backupRefused("native-read")
		}
		if entry.Name == market {
			if out.Market != nil {
				return out, backupRefused("native-ambiguous")
			}
			out.Market = append([]byte(nil), row...)
		}
	}
	result, err = run(ctx, cli, state, []string{"plugin", "list", "--marketplace", market, "--json"})
	if err != nil || result.Code != 0 {
		return out, backupRefused("native-read")
	}
	var plugins struct {
		Installed []json.RawMessage `json:"installed"`
	}
	if json.Unmarshal(result.Output, &plugins) != nil || len(plugins.Installed) > 1 {
		return out, backupRefused("native-read")
	}
	if len(plugins.Installed) == 1 {
		out.Plugin = append([]byte(nil), plugins.Installed[0]...)
	}
	if len(out.Plugin) > 0 {
		if err := closedNative(CodexProducer, out); err != nil {
			return out, err
		}
	}
	return out, nil
}
