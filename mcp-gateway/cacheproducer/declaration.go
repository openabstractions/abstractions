package cacheproducer

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

const CompanionDeclarationField = "companion_declaration"
const CompanionDeclarationMaxBytes = 256 << 10
const companionDeclarationLimit = CompanionDeclarationMaxBytes

func ValidateCompanionDeclaration(raw []byte) error { _, err := parseCompanion(raw); return err }

// Selection is the installed setup producer's explicit companion choice. It
// grants no native enrollment and performs no vendor operation.
type CompanionSelection struct{ Host, Requirement, State, Project, Scope, CLI, Name string }

type companionDeclaration struct {
	Version int               `json:"version"`
	Program string            `json:"program"`
	FileKey string            `json:"file_key"`
	Members []companionMember `json:"members"`
}
type companionMember struct {
	Owner       string            `json:"owner"`
	Requirement string            `json:"requirement"`
	Receipt     string            `json:"receipt"`
	Binding     string            `json:"binding"`
	State       string            `json:"state"`
	Project     string            `json:"project"`
	CLI         string            `json:"cli"`
	Scope       string            `json:"scope"`
	Name        string            `json:"name"`
	Root        string            `json:"root"`
	Version     string            `json:"version"`
	Files       map[string]string `json:"files"`
}

func canonicalCompanion(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err = d.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func parseCompanion(raw []byte) (companionDeclaration, error) {
	var d companionDeclaration
	if len(raw) == 0 || len(raw) > companionDeclarationLimit {
		return d, errors.New("companion declaration unavailable or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil || decoder.Decode(new(any)) != io.EOF {
		return d, errors.New("unknown companion declaration")
	}
	canonical, err := canonicalCompanion(d)
	if err != nil || !bytes.Equal(raw, canonical) || d.Version != 1 || !filepath.IsAbs(d.Program) || d.FileKey == "" || len(d.Members) == 0 || len(d.Members) > 64 {
		return d, errors.New("invalid companion declaration")
	}
	seen := map[string]bool{}
	for _, m := range d.Members {
		if (m.Owner != ClaudeProducer && m.Owner != CodexProducer) || (m.Requirement != "required" && m.Requirement != "none") || m.Binding == "" || seen[m.Binding] || !filepath.IsAbs(m.Receipt) || !filepath.IsAbs(m.Root) || m.Files == nil {
			return d, errors.New("invalid companion member")
		}
		seen[m.Binding] = true
		if (m.Scope != "user" && m.Scope != "local") || m.Owner == CodexProducer && m.Scope != "user" || m.Name == "" || len(m.Name) > 64 || m.Binding != PluginHash([]byte(m.Owner+"\x00"+m.State+"\x00"+m.Project+"\x00"+m.Scope+"\x00"+m.Name)) {
			return d, errors.New("invalid companion scope binding")
		}
		for _, stored := range []string{m.State, m.Project, m.CLI} {
			if _, err := fileid.ParseStoredFileID(stored); err != nil {
				return d, err
			}
		}
		if m.Requirement == "required" && (m.Version == "" || len(m.Files) == 0 || len(m.Files) > 32) {
			return d, errors.New("required companion recipe missing")
		}
		if m.Requirement == "none" && (m.Version != "" || len(m.Files) != 0) {
			return d, errors.New("none companion carries a recipe")
		}
		if m.Requirement == "required" {
			if m.Owner == ClaudeProducer {
				digest := strings.TrimPrefix(m.Version, "oa-cache-")
				if len(digest) != 64 || m.Version == digest {
					return d, errors.New("unknown Claude recipe version")
				}
				if _, e := hex.DecodeString(digest); e != nil {
					return d, e
				}
			} else {
				parts := strings.Split(m.Version, ".")
				if len(parts) != 3 {
					return d, errors.New("unknown Codex recipe version")
				}
				for _, part := range parts {
					v, e := strconv.ParseUint(part, 10, 16)
					if e != nil || strconv.FormatUint(v, 10) != part {
						return d, errors.New("unknown Codex recipe version")
					}
				}
			}
		}
		for name, digest := range m.Files {
			if filepath.IsAbs(name) || strings.Contains(name, "\\") || strings.Contains(name, "..") || len(digest) != 64 {
				return d, errors.New("invalid companion file")
			}
			if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
				return d, errors.New("invalid companion digest")
			}
		}
	}
	return d, nil
}
func companionImage(ctx context.Context, program, key string) error {
	h, _, err := setupIdentity(ctx, program, false)
	if err != nil {
		return err
	}
	defer h.Close()
	if key == "" || h.FileID().Key() != key {
		return errors.New("companion helper identity differs")
	}
	return nil
}

// DesiredCompanion is inert declaration generation before Keep. Receipt is the
// existing host setup receipt that will carry these private bytes.
func DesiredCompanion(ctx context.Context, s CompanionSelection, receipt, program, key string) ([]byte, error) {
	return desiredCompanion(ctx, s, receipt, program, key, true)
}
func desiredCompanion(ctx context.Context, s CompanionSelection, receipt, program, key string, verify bool) ([]byte, error) {
	if verify {
		if err := companionImage(ctx, program, key); err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(program) || key == "" {
		return nil, errors.New("prospective companion image is incomplete")
	}
	if s.Requirement != "required" && s.Requirement != "none" {
		return nil, errors.New("explicit companion membership required")
	}
	state, stateID, err := setupIdentity(ctx, s.State, true)
	if err != nil {
		return nil, err
	}
	defer state.Close()
	cli, cliID, err := setupIdentity(ctx, s.CLI, false)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	if s.Name == "" {
		s.Name = "OpenAbstractions"
	}
	m := companionMember{Requirement: s.Requirement, Receipt: receipt, State: stateID, CLI: cliID, Scope: s.Scope, Name: s.Name, Files: map[string]string{}}
	var files map[string][]byte
	switch s.Host {
	case "claude":
		m.Owner = ClaudeProducer
		if s.Scope == "" {
			s.Scope = "user"
			m.Scope = s.Scope
		}
		anchor := s.Project
		if s.Scope == "user" {
			anchor = s.State
		}
		project, pid, err := setupIdentity(ctx, anchor, true)
		if err != nil {
			return nil, err
		}
		defer project.Close()
		m.Project = pid
		projectKey := PluginHash([]byte(project.FileID().Key()))[:claudeSourceKeyCharacters]
		if s.Scope == "user" {
			projectKey = "user"
		}
		m.Root, _ = claudePluginPaths(s.State, projectKey)
		if s.Requirement == "required" {
			files = ClaudeFilesForScope("oa-local-"+projectKey, program, s.State, s.Scope)
			if s.Scope == "user" {
				files["plugin/hooks/register.js"], err = ClaudeBackgroundModule(ctx, s.State, s.CLI, stateID)
				if err != nil {
					return nil, err
				}
			}
			m.Version = SetClaudeGeneration(files)
		}
	case "codex":
		m.Owner = CodexProducer
		m.Scope = "user"
		home, err := codexHome()
		if err != nil {
			return nil, err
		}
		scope, pid, err := setupIdentity(ctx, home, true)
		if err != nil {
			return nil, err
		}
		defer scope.Close()
		m.Project = pid
		key := PluginHash([]byte(scope.FileID().Key() + "\x00" + s.Name))[:2*codexSourceKeyBytes]
		m.Root, _, _, _, _ = codexHookPaths(s.State, home, key)
		if s.Requirement == "required" {
			files = map[string][]byte{}
			for name, body := range CodexFiles(s.Name) {
				files["plugins/"+codexHookPlugin+"/"+name] = body
			}
			files[".agents/plugins/marketplace.json"] = codexHookCatalog("oa-session-" + key)
			m.Version = codexHookVersion
		}
	default:
		return nil, errors.New("unknown companion producer")
	}
	for name, body := range files {
		m.Files[name] = PluginHash(body)
	}
	m.Binding = PluginHash([]byte(m.Owner + "\x00" + m.State + "\x00" + m.Project + "\x00" + m.Scope + "\x00" + m.Name))
	return canonicalCompanion(companionDeclaration{Version: 1, Program: program, FileKey: key, Members: []companionMember{m}})
}

func ReadCompanionDeclaration(ctx context.Context, program, key string) ([]byte, error) {
	roots, err := layout.Resolve(layout.Current())
	if err != nil {
		return nil, err
	}
	return ReadCompanionDeclarationInConfig(ctx, roots.Config, program, key)
}

// All matching user/project records participate. A legacy record for this
// helper with no declaration keeps the aggregate unknown.
func ReadCompanionDeclarationInConfig(ctx context.Context, config, program, key string) ([]byte, error) {
	return readCompanionDeclarationInConfig(ctx, config, program, key, true)
}
func readCompanionDeclarationInConfig(ctx context.Context, config, program, key string, strict bool) ([]byte, error) {
	if err := companionImage(ctx, program, key); err != nil {
		return nil, err
	}
	aggregate := companionDeclaration{Version: 1, Program: program, FileKey: key, Members: []companionMember{}}
	for _, host := range []string{"claude", "codex"} {
		state := filepath.Join(config, host+"-connections")
		entries, err := os.ReadDir(state)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err = privatedir.CheckDir(state); err != nil {
			return nil, err
		}
		count := 0
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), host+"-setup-") || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			count++
			if count > 64 {
				return nil, errors.New("companion receipt count bound")
			}
			path := filepath.Join(state, entry.Name())
			if err = privatedir.CheckFile(path); err != nil {
				return nil, err
			}
			raw, err := cas.ReadLimitContext(ctx, path, claudeMarkerLimit)
			if err != nil {
				return nil, err
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil {
				return nil, errors.New("invalid setup receipt")
			}
			decl := fields[CompanionDeclarationField]
			if len(decl) == 0 {
				var self string
				_ = json.Unmarshal(fields["self"], &self)
				if self == "" {
					_ = json.Unmarshal(fields["Self"], &self)
				}
				id, e := fileid.ParseStoredFileID(self)
				if e == nil && id.Path() == program {
					return nil, errors.New("matching setup receipt has no explicit companion declaration")
				}
				continue
			}
			d, e := parseCompanion(decl)
			if e != nil {
				return nil, e
			}
			if d.Program != program {
				continue
			}
			if strict && d.FileKey != key {
				return nil, errors.New("matching companion helper generation differs")
			}
			for _, m := range d.Members {
				if m.Receipt != path {
					return nil, errors.New("companion receipt binding differs")
				}
				aggregate.Members = append(aggregate.Members, m)
			}
		}
	}
	sort.Slice(aggregate.Members, func(i, j int) bool { return aggregate.Members[i].Binding < aggregate.Members[j].Binding })
	if err := unlistedCompanions(ctx, config, program, aggregate.Members); err != nil {
		return nil, err
	}
	raw, err := canonicalCompanion(aggregate)
	if err != nil {
		return nil, err
	}
	if _, err = parseCompanion(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func unlistedCompanions(ctx context.Context, config, program string, members []companionMember) error {
	required := map[string]bool{}
	for _, m := range members {
		if m.Requirement == "required" {
			required[m.Root+".json"] = true
		}
	}
	for _, host := range []string{"claude", "codex"} {
		state := filepath.Join(config, host+"-connections")
		entries, err := os.ReadDir(state)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		count := 0
		prefix := "claude-plugin-"
		if host == "codex" {
			prefix = "codex-session-start-"
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			count++
			if count > 64 {
				return errors.New("companion receipt count bound")
			}
			path := filepath.Join(state, entry.Name())
			raw, err := cas.ReadLimitContext(ctx, path, claudeMarkerLimit)
			if err != nil {
				return err
			}
			matches := false
			if host == "claude" {
				var marker ClaudeMarker
				if json.Unmarshal(raw, &marker) != nil {
					return errors.New("invalid companion receipt")
				}
				self, e := fileid.ParseStoredFileID(marker.Self)
				matches = e == nil && self.Path() == program && marker.MarketplaceOwned && marker.PluginOwned
			} else {
				var marker codexHookOwnership
				if json.Unmarshal(raw, &marker) != nil {
					return errors.New("invalid companion receipt")
				}
				matches = marker.Program == program && marker.Cache != ""
			}
			if matches && !required[path] {
				return errors.New("installed companion has no matching required declaration")
			}
		}
	}
	return nil
}

func RebindCompanionDeclaration(ctx context.Context, raw []byte, program, key string) ([]byte, error) {
	d, err := parseCompanion(raw)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(program) || key == "" {
		return nil, errors.New("prospective companion image is incomplete")
	}
	if d.Program != program {
		return nil, errors.New("retained companion program locator differs")
	}
	d.FileKey = key
	return canonicalCompanion(d)
}
func SuccessorCompanionDeclaration(ctx context.Context, raw []byte, program, key string) ([]byte, error) {
	d, err := parseCompanion(raw)
	if err != nil {
		return nil, err
	}
	if d.Program != program {
		return nil, errors.New("successor companion program locator differs")
	}
	next := companionDeclaration{Version: 1, Program: program, FileKey: key, Members: []companionMember{}}
	for _, m := range d.Members {
		state, e := fileid.ParseStoredFileID(m.State)
		if e != nil {
			return nil, e
		}
		project, e := fileid.ParseStoredFileID(m.Project)
		if e != nil {
			return nil, e
		}
		cli, e := fileid.ParseStoredFileID(m.CLI)
		if e != nil {
			return nil, e
		}
		host := "claude"
		if m.Owner == CodexProducer {
			host = "codex"
		}
		selection := CompanionSelection{Host: host, Requirement: m.Requirement, State: state.Path(), Project: project.Path(), Scope: m.Scope, CLI: cli.Path(), Name: m.Name}
		if host == "claude" && m.Scope == "user" {
			selection.Project = ""
		}
		encoded, e := desiredCompanion(ctx, selection, m.Receipt, program, key, false)
		if e != nil {
			return nil, e
		}
		fresh, e := parseCompanion(encoded)
		if e != nil || len(fresh.Members) != 1 || fresh.Members[0].Binding != m.Binding {
			return nil, errors.New("companion scope or member set changed")
		}
		next.Members = append(next.Members, fresh.Members[0])
	}
	return canonicalCompanion(next)
}
