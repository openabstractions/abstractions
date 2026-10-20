package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

const claudeProjectLimit = 1 << 20

// The ownership marker is a small JSON record. Its plugin payload members
// carry the projected Claude Mod, which grows with the hook; they have their
// own bound. Both owners (cacheproducer and Setup) keep the same values.
const claudeMarkerLimit = 32 << 10
const claudePayloadLimit = 128 << 10

type claudeSetupOptions struct{ action, project, state, cli, name, registeredProgram, userRoot, companionRequirement string }
type claudeEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}
type claudeSetupMarker struct {
	Companion json.RawMessage `json:"companion_declaration,omitempty"`
	Previous  *agentPrevious  `json:"previous,omitempty"`
	Scope     string          `json:"scope,omitempty"`
	Project   string          `json:"project"`
	Self      string          `json:"self"`
	CLI       string          `json:"cli"`
	State     string          `json:"state"`
	Name      string          `json:"name"`
	Active    bool            `json:"active"`
	Entry     claudeEntry     `json:"entry"`
}

func decodeClaudeMarker(raw []byte) (claudeSetupMarker, error) {
	var marker claudeSetupMarker
	fields, err := claudeConfigObject(raw)
	if err != nil || len(fields) < 7 || len(fields) > 10 {
		return marker, errors.New("unknown setup ownership record")
	}
	for key := range fields {
		switch key {
		case "scope", "project", "self", "cli", "state", "name", "active", "entry", "previous", "companion_declaration":
		default:
			return marker, errors.New("unknown setup ownership field")
		}
	}
	if raw, ok := fields[cacheproducer.CompanionDeclarationField]; ok {
		if err := cacheproducer.ValidateCompanionDeclaration(raw); err != nil {
			return marker, err
		}
	}
	if raw, ok := fields["previous"]; ok {
		if _, err := decodeAgentPrevious(raw); err != nil {
			return marker, err
		}
	}
	if _, err := decodeClaudeEntry(fields["entry"]); err != nil {
		return marker, errors.New("unknown setup ownership entry")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	legacyFields := len(fields)
	if _, ok := fields[cacheproducer.CompanionDeclarationField]; ok {
		legacyFields--
	}
	if decoder.Decode(&marker) != nil || (marker.Scope != "" && marker.Scope != "user") || (legacyFields >= 8 && marker.Scope != "user") || (legacyFields == 9 && marker.Previous == nil) || (marker.Scope == "user" && marker.Name != "OpenAbstractions") {
		return marker, errors.New("unknown setup ownership record")
	}
	return marker, nil
}

func claudeSetupCommand(args []string, output, diagnostics io.Writer) int {
	options, err := parseClaudeSetup(args, diagnostics)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		checkLine(diagnostics, "Claude setup: invalid command; use connect claude --help or disconnect claude --help")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	self, err := os.Executable()
	if err != nil {
		checkLine(diagnostics, "Claude setup: gateway executable unavailable")
		return 1
	}
	if err := applyClaudeSetup(ctx, options, self, runClaudeSetupCLI, output); err != nil {
		checkLine(diagnostics, "Claude setup: "+err.Error())
		claudeSetupRecovery(diagnostics, options)
		return 1
	}
	return 0
}

func claudeSetupRecovery(output io.Writer, options claudeSetupOptions) {
	cli := options.cli
	if cli == "" {
		cli = "claude from PATH"
	}
	checkLine(output, "Recovery: preserve foreign, changed or unproven registrations. No OA jobs, state or rights were removed.")
	checkLine(output, fmt.Sprintf("Review the named connection %q with the selected CLI %q before any manual change. Its get command can start the configured server.", options.name, cli))
}

func parseClaudeSetup(args []string, output io.Writer) (claudeSetupOptions, error) {
	var options claudeSetupOptions
	if len(args) < 2 || (args[0] != "connect" && args[0] != "disconnect") || args[1] != "claude" {
		return options, errors.New("unsupported setup target")
	}
	options.action = args[0]
	flags := flag.NewFlagSet(args[0]+" claude", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&options.project, "project", "", "explicit absolute Claude Code project directory (project scope only)")
	flags.StringVar(&options.state, "state-dir", "", "explicit absolute private gateway state directory")
	flags.StringVar(&options.cli, "claude", "", "absolute Claude Code CLI executable; default resolves PATH claude")
	flags.StringVar(&options.name, "name", "OpenAbstractions", "MCP name: 1..64 ASCII letters, numbers, hyphens or underscores")
	flags.StringVar(&options.registeredProgram, "registered-program", "", "disconnect only: exact configured command owned by the existing setup record; never executed")
	if err := flags.Parse(args[2:]); err != nil {
		return options, err
	}
	if options.project != "" {
		named := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "name" {
				named = true
			}
		})
		if !named {
			options.name = "openabstractions"
		}
	}
	if flags.NArg() != 0 || (options.project != "" && !filepath.IsAbs(options.project)) || !filepath.IsAbs(options.state) || (options.cli != "" && !filepath.IsAbs(options.cli)) || !claudeSetupName(options.name) || (options.registeredProgram != "" && (options.action != "disconnect" || !filepath.IsAbs(options.registeredProgram))) {
		return options, errors.New("invalid setup flags")
	}
	if options.project == "" && options.name != "OpenAbstractions" {
		return options, errors.New("ordinary user connection name must be OpenAbstractions")
	}
	return options, nil
}

// The CLI's documented CLAUDE_CONFIG_DIR selects its user configuration; the
// ordinary default is .claude.json in the native account's home. Admission of
// the account view precedes reading either location.
func claudeUserConfigurationHome() (string, error) { return setupio.ClaudeHome() }

func claudeUserConfigurationHomeFor(platform layout.Platform, override string, home func() (string, error)) (string, error) {
	return setupio.ClaudeHomeFor(platform, override, home)
}

func claudeSetupName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

type claudeSetupRunner func(context.Context, string, string, []string) ([]byte, error)

type claudeSetupOutput = setupio.Output

func runClaudeSetupCLI(ctx context.Context, executable, project string, args []string) ([]byte, error) {
	return setupio.RunClaude(ctx, executable, project, args)
}

type setupIdentityRef = setupio.IdentityRef

func setupIdentity(ctx context.Context, path string, directory bool) (fileid.Handle, string, error) {
	return setupio.Open(ctx, path, directory)
}

func describeSetupIdentity(h setupIdentityRef, directory bool) (string, error) {
	return setupio.Describe(h, directory)
}

func setupIdentitiesCurrent(ctx context.Context, handles []setupIdentityRef) bool {
	return setupio.Current(ctx, handles)
}

func claudeMarkerIdentitiesMatch(ctx context.Context, marker claudeSetupMarker, handles []setupIdentityRef, explicitCLI bool) bool {
	for _, binding := range []struct {
		index int
		text  string
	}{{0, marker.Project}, {3, marker.State}} {
		i, text := binding.index, binding.text
		stored, err := fileid.ParseStoredFileID(text)
		if err != nil {
			return false
		}
		current, state, err := fileid.Revalidate(ctx, stored)
		if err != nil || state != fileid.StateSame || !current.Equal(handles[i].FileID()) {
			return false
		}
	}
	self, selfErr := fileid.ParseStoredFileID(marker.Self)
	cli, cliErr := fileid.ParseStoredFileID(marker.CLI)
	if selfErr != nil || cliErr != nil {
		return false
	}
	// These are exact configured command strings, not claims that two paths
	// identify one executable. The private marker owns a configuration created
	// by setup; it grants no image-bound OA rights. An ordinary image upgrade
	// at that configured path preserves ownership. Current image identities
	// remain held and checked throughout each operation separately.
	return self.Path() == marker.Entry.Command && (nativecli.SameClaudeCLIInstall(cli.Path(), handles[2].FileID().Path()) || explicitCLI)
}

func readClaudeProjectEntry(ctx context.Context, project fileid.Handle, name string) (*claudeEntry, error) {
	return readClaudeNamedEntry(ctx, project, ".mcp.json", name)
}

func readClaudeNamedEntry(ctx context.Context, project fileid.Handle, filename, name string) (*claudeEntry, error) {
	h, err := project.Beneath(filename, fileid.Options{Links: fileid.RefuseLinks})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer h.Close()
	info, err := h.File().Stat()
	if err != nil || !info.Mode().IsRegular() || h.FileID().Links() != 1 {
		return nil, errors.New("project record must be regular")
	}
	raw, err := cas.ReadLimitContext(ctx, h.FileID().Path(), claudeProjectLimit)
	if err != nil || len(raw) > claudeProjectLimit {
		return nil, errors.New("project record unavailable or oversized")
	}
	current, err := project.Beneath(filename, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	defer current.Close()
	if !current.FileID().Equal(h.FileID()) {
		return nil, errors.New("project record changed during read")
	}
	document, err := claudeConfigObject(raw)
	if err != nil {
		return nil, errors.New("invalid project record")
	}
	serversRaw, ok := document["mcpServers"]
	if !ok {
		return nil, nil
	}
	servers, err := claudeConfigObject(serversRaw)
	if err != nil {
		return nil, errors.New("invalid project server record")
	}
	entryRaw, ok := servers[name]
	if !ok {
		return nil, nil
	}
	entry, err := decodeClaudeEntry(entryRaw)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func decodeClaudeEntry(entryRaw []byte) (claudeEntry, error) {
	var entry claudeEntry
	entryFields, err := claudeConfigObject(entryRaw)
	if err != nil {
		return entry, errors.New("ambiguous selected server entry")
	}
	for key := range entryFields {
		switch key {
		case "type", "command", "args", "env":
		default:
			return entry, errors.New("unknown selected server field")
		}
	}
	if env, ok := entryFields["env"]; ok {
		if _, err := claudeConfigObject(env); err != nil {
			return entry, errors.New("ambiguous selected server environment")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(entryRaw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&entry) != nil || entry.Type != "stdio" || entry.Command == "" || entry.Args == nil {
		return entry, errors.New("unknown selected server entry shape")
	}
	if entry.Env == nil {
		entry.Env = map[string]string{}
	}
	return entry, nil
}

// Other values stay opaque. Duplicate names make a selected scope ambiguous.
func claudeConfigObject(raw []byte) (map[string]json.RawMessage, error) {
	return setupio.ConfigObject(raw)
}

func applyClaudeSetup(ctx context.Context, options claudeSetupOptions, self string, run claudeSetupRunner, output io.Writer) error {
	scope, scopePath, filename := "project", options.project, ".mcp.json"
	if scopePath == "" {
		scope, filename = "user", ".claude.json"
		scopePath = options.userRoot
		if scopePath == "" {
			var err error
			scopePath, err = claudeUserConfigurationHome()
			if err != nil {
				return errors.New("Claude user configuration location unavailable")
			}
		}
		if options.name != "OpenAbstractions" || fileid.LocalPath(scopePath) != nil {
			return errors.New("unsupported user configuration scope")
		}
	}
	if options.registeredProgram != "" {
		if options.action != "disconnect" || !filepath.IsAbs(options.registeredProgram) {
			return errors.New("registered command selection is removal-only")
		}
		if err := fileid.LocalPath(options.registeredProgram); err != nil {
			return errors.New("registered command must be a local absolute path")
		}
	}
	explicitCLI := options.cli != ""
	if options.cli == "" {
		var err error
		options.cli, err = exec.LookPath("claude")
		if err != nil || !filepath.IsAbs(options.cli) {
			return errors.New("Claude Code CLI unavailable; select its absolute executable with --claude")
		}
	}
	project, projectID, err := setupIdentity(ctx, scopePath, true)
	if err != nil {
		return errors.New("selected project identity unavailable")
	}
	defer project.Close()
	gateway, selfID, err := setupIdentity(ctx, self, false)
	if err != nil {
		return errors.New("gateway executable identity unavailable")
	}
	defer gateway.Close()
	cli, cliID, err := setupIdentity(ctx, options.cli, false)
	if err != nil {
		return errors.New("selected CLI identity unavailable")
	}
	defer cli.Close()
	if err := privatedir.Ensure(options.state); err != nil {
		return errors.New("state directory is not private to this account")
	}
	state, stateID, err := setupIdentity(ctx, options.state, true)
	if err != nil {
		return errors.New("state directory identity unavailable")
	}
	defer state.Close()
	handles := []setupIdentityRef{project, gateway, cli, state}
	entry := claudeEntry{Type: "stdio", Command: gateway.FileID().Path(), Args: []string{"--state-dir", state.FileID().Path()}, Env: map[string]string{}}
	if options.registeredProgram != "" {
		entry.Command = options.registeredProgram
	}
	expected := claudeSetupMarker{Project: projectID, Self: selfID, CLI: cliID, State: stateID, Name: options.name, Active: true, Entry: entry}
	if scope == "user" {
		expected.Scope = scope
	}
	key := sha256.Sum256([]byte(claudeSetupKey(project.FileID().Key(), options.name, expected.Scope)))
	markerPath := filepath.Join(state.FileID().Path(), "claude-setup-"+hex.EncodeToString(key[:])+".json")
	if err := privatedir.CheckFile(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("setup ownership record is not private; preserved")
	}
	base, err := cas.ReadLimitContext(ctx, markerPath, claudeMarkerLimit)
	if err != nil {
		return errors.New("setup ownership record unavailable")
	}
	var marker claudeSetupMarker
	if base != nil {
		marker, err = decodeClaudeMarker(base)
		if err != nil {
			return errors.New("setup ownership record is unknown; preserved")
		}
		if !claudeMarkerIdentitiesMatch(ctx, marker, handles, explicitCLI) || marker.Scope != expected.Scope || marker.Name != options.name || !reflect.DeepEqual(marker.Entry, entry) {
			return errors.New("setup ownership belongs to different project, executable, CLI or state; preserved")
		}
	}
	if base != nil {
		expected.Companion = marker.Companion
	}
	if options.action == "declare" {
		selection := cacheproducer.CompanionSelection{Host: "claude", Requirement: options.companionRequirement, State: options.state, CLI: options.cli, Project: options.project, Scope: "user", Name: options.name}
		if scope == "project" {
			selection.Scope = "local"
		}
		raw, e := cacheproducer.DesiredCompanion(ctx, selection, markerPath, self, gateway.FileID().Key())
		if e != nil {
			return e
		}
		expected.Companion = raw
		expected.Active = base != nil && marker.Active
		expected.Previous = marker.Previous
		if !setupIdentitiesCurrent(ctx, handles) {
			return errors.New("declaration identities changed")
		}
		encoded, e := json.Marshal(expected)
		if e != nil {
			return e
		}
		return (casapi.BoundedFileStore{MaxBytes: claudeMarkerLimit}).WriteContext(ctx, markerPath, casapi.Value{Data: base}, encoded)
	}
	current, err := readClaudeNamedEntry(ctx, project, filename, options.name)
	if err != nil {
		return errors.New("selected named registration cannot be admitted; preserved")
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return errors.New("selected identity changed during preflight; preserved")
	}
	if current != nil && (base == nil || !marker.Active || !reflect.DeepEqual(*current, entry)) {
		return errors.New("same-name " + scope + " registration is foreign or changed; preserved")
	}
	if options.action == "connect" && current != nil {
		checkLine(output, "Claude "+scope+" registration: already saved and owned ("+options.name+")")
		claudeSetupStatus(output, scope)
		return nil
	}
	if options.action == "disconnect" {
		if base == nil {
			return errors.New("no positive setup ownership; registration and state preserved")
		}
		// Removal through a trusted management image preserves the original
		// configured command identity in history. That target is never opened
		// or executed; the positive marker and exact current entry own removal.
		if options.registeredProgram != "" {
			expected.Self = marker.Self
		}
		if current == nil {
			checkLine(output, "Claude "+scope+" registration: already absent; setup history and OA state retained")
			return nil
		}
	}
	version, err := run(ctx, cli.FileID().Path(), project.FileID().Path(), []string{"--version"})
	if err != nil || !bytes.Contains(version, []byte("(Claude Code)")) {
		return errors.New("selected program did not identify as the Claude Code CLI")
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return errors.New("selected identity changed before vendor operation; preserved")
	}
	immediateMarker, err := cas.ReadLimitContext(ctx, markerPath, claudeMarkerLimit)
	if err != nil || (immediateMarker == nil) != (base == nil) || !bytes.Equal(immediateMarker, base) {
		return errors.New("setup ownership record changed before vendor operation; preserved")
	}
	immediate, err := readClaudeNamedEntry(ctx, project, filename, options.name)
	if err != nil || !reflect.DeepEqual(immediate, current) {
		return errors.New("project registration changed before vendor operation; preserved")
	}
	var args []string
	if options.action == "connect" {
		args = []string{"mcp", "add", "--scope", scope, "--transport", "stdio", options.name, "--", entry.Command, "--state-dir", entry.Args[1]}
	} else {
		args = []string{"mcp", "remove", "--scope", scope, options.name}
	}
	_, err = run(ctx, cli.FileID().Path(), project.FileID().Path(), args)
	if err != nil {
		return errors.New("vendor reply failed or was lost; registration outcome unknown and ownership record preserved")
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return errors.New("selected identity changed during vendor operation; outcome unknown and ownership record preserved")
	}
	observed, err := readClaudeNamedEntry(ctx, project, filename, options.name)
	if err != nil {
		return errors.New("vendor readback unavailable; outcome unknown and ownership record preserved")
	}
	if options.action == "connect" {
		if observed == nil || !reflect.DeepEqual(*observed, entry) {
			return errors.New("vendor saved entry does not match; ownership not established")
		}
	} else {
		if observed != nil {
			return errors.New("project registration remains or changed after vendor removal; ownership record preserved")
		}
		expected.Active = false
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		return errors.New("setup ownership encoding failed")
	}
	store := casapi.BoundedFileStore{MaxBytes: claudeMarkerLimit}
	if err := store.WriteContext(ctx, markerPath, casapi.Value{Data: base}, encoded); err != nil {
		return errors.New("vendor operation completed but ownership record changed or could not be saved; inspect the selected project")
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return errors.New("selected identity changed during ownership save; inspect the selected project")
	}
	if options.action == "connect" {
		checkLine(output, "Claude "+scope+" registration: saved and owned ("+options.name+")")
		claudeSetupStatus(output, scope)
	} else {
		checkLine(output, "Claude "+scope+" registration: removed ("+options.name+"); jobs, state and rights retained")
		checkLine(output, "Vendor removal is name-based; an external edit between preflight and removal is not protected by compare-and-delete.")
	}
	return nil
}

func claudeSetupKey(directoryKey, name, scope string) string {
	if scope == "user" {
		return "user\x00" + directoryKey + "\x00" + name
	}
	return directoryKey + "\x00" + name
}

func claudeSetupStatus(output io.Writer, scope string) {
	if scope == "project" {
		checkLine(output, "Claude approval: pending or unverified; approve this project registration in Claude Code.")
	} else {
		checkLine(output, "Claude user registration applies across projects; reopen Claude Code to load it. Project or local entries can take precedence.")
	}
	checkLine(output, "MCP connection: not checked; OA authorization: unchanged. Use --check separately for read-only diagnostics.")
}
