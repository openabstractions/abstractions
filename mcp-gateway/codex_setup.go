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

	"path/filepath"
	"reflect"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
)

type codexSetupOptions struct {
	action, state, cli, name, registeredProgram string
	helperLifecycle                             bool
	helperProgram, helperKey                    string
	companionRequirement                        string
}
type codexTransport struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	EnvVars []string          `json:"env_vars"`
	Cwd     *string           `json:"cwd"`
}
type codexEntry struct {
	Name           string         `json:"name"`
	Enabled        bool           `json:"enabled"`
	DisabledReason *string        `json:"disabled_reason"`
	Transport      codexTransport `json:"transport"`
	EnabledTools   []string       `json:"enabled_tools"`
	DisabledTools  []string       `json:"disabled_tools"`
	StartupTimeout *float64       `json:"startup_timeout_sec"`
	ToolTimeout    *float64       `json:"tool_timeout_sec"`
}
type codexSetupMarker struct {
	Companion                     json.RawMessage `json:"companion_declaration,omitempty"`
	Previous                      *agentPrevious  `json:"Previous,omitempty"`
	Scope, Self, CLI, State, Name string
	Active                        bool
	Entry                         codexEntry
}

func decodeCodexSetupMarker(raw []byte) (codexSetupMarker, error) {
	var marker codexSetupMarker
	fields, err := claudeConfigObject(raw)
	if err != nil || len(fields) < 7 || len(fields) > 9 {
		return marker, errors.New("unknown Codex setup ownership")
	}
	for key, value := range fields {
		switch key {
		case "Scope", "Self", "CLI", "State", "Name":
			var text string
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &text) != nil || text == "" {
				return marker, errors.New("unknown Codex setup ownership field")
			}
		case "Active":
			if !bytes.Equal(bytes.TrimSpace(value), []byte("true")) && !bytes.Equal(bytes.TrimSpace(value), []byte("false")) {
				return marker, errors.New("unknown Codex setup ownership state")
			}
		case "Entry":
		case "Previous":
			if _, err := decodeAgentPrevious(value); err != nil {
				return marker, err
			}
		case "companion_declaration":
			if err := cacheproducer.ValidateCompanionDeclaration(value); err != nil {
				return marker, err
			}
		default:
			return marker, errors.New("unknown Codex setup ownership field")
		}
	}
	for _, key := range []string{"Scope", "Self", "CLI", "State", "Name", "Active", "Entry"} {
		if _, ok := fields[key]; !ok {
			return marker, errors.New("incomplete Codex setup ownership")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil || decoder.Decode(new(any)) != io.EOF {
		return marker, errors.New("unknown Codex setup ownership")
	}
	return marker, nil
}

type codexCLIResult = setupio.CLIResult
type codexSetupRunner func(context.Context, string, string, []string) (codexCLIResult, error)

func runCodexSetupCLI(ctx context.Context, program, directory string, args []string) (codexCLIResult, error) {
	return setupio.RunCodex(ctx, program, directory, args)
}

func codexConfigurationHome() (string, error) { return setupio.CodexHome() }

func codexSetupCommand(args []string, output, diagnostics io.Writer) int {
	options, err := parseCodexSetup(args, diagnostics)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		checkLine(diagnostics, "Codex setup: invalid command; use connect codex --help")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	home, err := codexConfigurationHome()
	if err != nil {
		checkLine(diagnostics, "Codex configuration scope unavailable")
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		return 1
	}
	page, err := applyCodexSetup(ctx, options, self, home, runCodexSetupCLI)
	if err != nil {
		checkLine(diagnostics, "Codex setup: "+err.Error()+". Preserve changed or unowned entries; inspect the named entry with codex mcp get --json.")
		return 1
	}
	if writeCodexSetupPage(ctx, options, page, output) != nil {
		return 1
	}
	return 0
}

func writeCodexSetupPage(ctx context.Context, options codexSetupOptions, page codexSetupPage, output io.Writer) error {
	if options.action == "connections" && options.helperLifecycle {
		page.HelperLifecycle = readAgentDiagnostics(ctx, options.state)
	}
	return json.NewEncoder(output).Encode(page)
}

func parseCodexSetup(args []string, output io.Writer) (codexSetupOptions, error) {
	options := codexSetupOptions{name: "OpenAbstractions"}
	if len(args) < 2 || args[1] != "codex" || (args[0] != "connect" && args[0] != "disconnect" && args[0] != "connections") {
		return options, errors.New("command")
	}
	options.action = args[0]
	flags := flag.NewFlagSet(options.action+" codex", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&options.state, "state-dir", "", "absolute private OA state directory")
	flags.StringVar(&options.cli, "codex", "", "selected absolute Codex CLI")
	flags.StringVar(&options.name, "name", options.name, "saved server name")
	if options.action == "connections" {
		flags.BoolVar(&options.helperLifecycle, "helper-lifecycle", false, "include bounded OA helper lifecycle diagnostics (read only)")
	}
	if options.action == "disconnect" {
		flags.StringVar(&options.registeredProgram, "registered-program", "", "owned saved target locator; never executed")
	}
	flags.Usage = func() {
		fmt.Fprintln(output, "Codex setup uses the account's shared Codex configuration for desktop, CLI and IDE. Connect installs and disconnect removes the owned oa-codex-session-start hook plugin through Codex. Review and trust it once in Codex /hooks; startup receiving is best effort. Hook status is separate from MCP setup. This grants no OA permission and does not check a live connection.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args[2:]); err != nil {
		return options, err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(options.state) || !filepath.IsAbs(options.cli) || !claudeSetupName(options.name) || (options.registeredProgram != "" && !filepath.IsAbs(options.registeredProgram)) {
		return options, errors.New("options")
	}
	return options, nil
}

type codexSetupPage struct {
	Previous        *agentPrevious          `json:"previous,omitempty"`
	HelperLifecycle []agentDiagnosticRecord `json:"helper_lifecycle,omitempty"`
	Outcome         string                  `json:"outcome"`
	Scope           string                  `json:"scope"`
	Registration    string                  `json:"registration"`
	Name            string                  `json:"name"`
	Program         string                  `json:"program"`
	Owned           bool                    `json:"owned"`
	SessionStart    string                  `json:"session_start,omitempty"`
}

func readCodexEntry(ctx context.Context, run codexSetupRunner, cli, directory, name string) (*codexEntry, error) {
	reply, err := run(ctx, cli, directory, []string{"mcp", "get", name, "--json"})
	if err != nil {
		return nil, err
	}
	if reply.Code == 1 && len(bytes.TrimSpace(reply.Output)) == 0 && strings.HasSuffix(strings.TrimSpace(string(reply.Diagnostic)), "Error: No MCP server named '"+name+"' found.") {
		return nil, nil
	}
	if reply.Code != 0 {
		return nil, errors.New("selected Codex registration unavailable")
	}
	var entry codexEntry
	decoder := json.NewDecoder(bytes.NewReader(reply.Output))
	decoder.DisallowUnknownFields()
	if len(reply.Output) > claudeMarkerLimit || decoder.Decode(&entry) != nil || decoder.Decode(new(any)) != io.EOF || entry.Name != name {
		return nil, errors.New("unsupported Codex registration readback")
	}
	return &entry, nil
}

// The vendor CLI is the sole Codex configuration writer. The private marker
// owns only the exact entry OA created; it conveys no native enrollment/rights.
func applyCodexSetup(ctx context.Context, options codexSetupOptions, self, home string, run codexSetupRunner) (codexSetupPage, error) {
	if options.helperProgram == "" {
		image, _, err := setupIdentity(ctx, self, false)
		if err != nil {
			return codexSetupPage{}, err
		}
		options.helperProgram, options.helperKey = self, image.FileID().Key()
		if err := image.Close(); err != nil {
			return codexSetupPage{}, err
		}
	}
	page, err := applyCodexMCPSetup(ctx, options, self, home, run)
	if err != nil {
		return page, err
	}
	if err := applyCodexSessionStartSetupStatus(ctx, options, home, run, &page.SessionStart); err != nil {
		page.SessionStart = "unavailable"
		if errors.Is(err, errCodexHookPreserved) {
			page.SessionStart = "preserved"
		}
	}
	return page, nil
}

func applyCodexMCPSetup(ctx context.Context, options codexSetupOptions, self, home string, run codexSetupRunner) (codexSetupPage, error) {
	page := codexSetupPage{Outcome: "page", Scope: "user", Name: options.name, Registration: "absent"}
	if err := privatedir.Ensure(options.state); err != nil {
		return page, err
	}
	scope, scopeID, err := setupIdentity(ctx, home, true)
	if err != nil {
		return page, errors.New("open Codex once, then try again")
	}
	defer scope.Close()
	gateway, selfID, err := setupIdentity(ctx, self, false)
	if err != nil {
		return page, err
	}
	defer gateway.Close()
	cli, cliID, err := setupCodexCLIIdentity(ctx, options.cli)
	if err != nil {
		return page, err
	}
	defer cli.Close()
	cliProgram := cli.FileID().Path()
	if selected, ok := cli.(interface{ Program() string }); ok {
		cliProgram = selected.Program()
	}
	state, stateID, err := setupIdentity(ctx, options.state, true)
	if err != nil {
		return page, err
	}
	defer state.Close()
	handles := []setupIdentityRef{scope, gateway, cli, state}
	command := gateway.FileID().Path()
	if options.registeredProgram != "" {
		command = options.registeredProgram
	}
	entry := codexEntry{Name: options.name, Enabled: true, Transport: codexTransport{Type: "stdio", Command: command, Args: []string{"--state-dir", state.FileID().Path()}, EnvVars: []string{}}}
	expected := codexSetupMarker{Scope: scopeID, Self: selfID, CLI: cliID, State: stateID, Name: options.name, Active: true, Entry: entry}
	sum := sha256.Sum256([]byte(scope.FileID().Key() + "\x00" + options.name))
	markerPath := filepath.Join(state.FileID().Path(), "codex-setup-"+hex.EncodeToString(sum[:])+".json")
	if err := privatedir.CheckFile(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return page, errors.New("Codex setup ownership is not private")
	}
	base, err := cas.ReadLimitContext(ctx, markerPath, claudeMarkerLimit)
	if err != nil {
		return page, err
	}
	var marker codexSetupMarker
	if base != nil {
		marker, err = decodeCodexSetupMarker(base)
		if err != nil {
			return page, errors.New("unknown Codex setup ownership")
		}
		// Reuse C1's held-directory and configured-command ownership checks.
		compatible := claudeSetupMarker{Project: marker.Scope, Self: marker.Self, CLI: marker.CLI, State: marker.State, Entry: claudeEntry{Command: marker.Entry.Transport.Command}}
		if !claudeMarkerIdentitiesMatch(ctx, compatible, handles, true) || marker.Name != options.name {
			return page, errors.New("Codex setup ownership belongs to another scope")
		}
		if options.action == "connect" && !codexEntriesMatch(marker.Entry, entry) {
			return page, errors.New("Codex setup belongs to a different integration; preserved")
		}
		if options.registeredProgram != "" && marker.Entry.Transport.Command != options.registeredProgram {
			return page, errors.New("saved Codex target differs; preserved")
		}
		if options.action == "disconnect" {
			entry = marker.Entry
			expected.Entry = entry
			expected.Self = marker.Self
			page.Program = entry.Transport.Command
		}
		page.Previous = marker.Previous
	}
	if base != nil {
		expected.Companion = marker.Companion
	}
	if options.action == "declare" {
		raw, e := cacheproducer.DesiredCompanion(ctx, cacheproducer.CompanionSelection{Host: "codex", Requirement: options.companionRequirement, State: options.state, CLI: options.cli, Name: options.name}, markerPath, self, gateway.FileID().Key())
		if e != nil {
			return page, e
		}
		expected.Companion = raw
		expected.Active = base != nil && marker.Active
		expected.Previous = marker.Previous
		if !setupIdentitiesCurrent(ctx, handles) {
			return page, errors.New("declaration identities changed")
		}
		encoded, e := json.Marshal(expected)
		if e != nil {
			return page, e
		}
		e = (casapi.BoundedFileStore{MaxBytes: claudeMarkerLimit}).WriteContext(ctx, markerPath, casapi.Value{Data: base}, encoded)
		return page, e
	}
	nativeRun := run
	run = func(call context.Context, program, directory string, args []string) (codexCLIResult, error) {
		check := func() error {
			if !setupIdentitiesCurrent(call, handles) {
				return errors.New("selected identity changed before or during Codex command")
			}
			current, err := cas.ReadLimitContext(call, markerPath, claudeMarkerLimit)
			if err != nil || (current == nil) != (base == nil) || !bytes.Equal(current, base) {
				return errors.New("Codex ownership changed before or during command")
			}
			return nil
		}
		if err := check(); err != nil {
			return codexCLIResult{}, err
		}
		result, err := nativeRun(call, program, directory, args)
		if err != nil {
			return result, err
		}
		if err := check(); err != nil {
			return result, err
		}
		return result, nil
	}
	version, err := run(ctx, cliProgram, state.FileID().Path(), []string{"--version"})
	if err != nil || version.Code != 0 || !bytes.HasPrefix(bytes.TrimSpace(version.Output), []byte("codex-cli ")) {
		return page, errors.New("selected executable is not the supported Codex CLI")
	}
	current, err := readCodexEntry(ctx, run, cliProgram, state.FileID().Path(), options.name)
	if err != nil {
		return page, err
	}
	page.Program = command
	page.Owned = base != nil && marker.Active
	if current != nil {
		page.Registration = "changed"
		if page.Owned && codexEntriesMatch(*current, marker.Entry) {
			page.Registration = "saved"
			page.Program = marker.Entry.Transport.Command
		}
	}
	if options.action == "connections" {
		return page, nil
	}
	if current != nil && (base == nil || !marker.Active || !codexEntriesMatch(*current, marker.Entry) || options.action == "connect" && !codexEntriesMatch(*current, entry)) {
		return page, errors.New("same-name Codex entry is foreign or changed; preserved")
	}
	if options.action == "disconnect" && base == nil {
		return page, errors.New("no positive Codex setup ownership; preserved")
	}
	if options.action == "connect" && current != nil {
		if current.ToolTimeout != nil {
			return page, nil
		}
		// An entry saved before OA set the tool timeout gets it now.
		return page, saveCodexToolTimeout(ctx, run, cliProgram, state.FileID().Path(), scope.FileID().Path(), markerPath, base, marker, *current, handles)
	}
	if options.action == "disconnect" && current == nil {
		return page, nil
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return page, errors.New("selected identity changed")
	}
	after, err := cas.ReadLimitContext(ctx, markerPath, claudeMarkerLimit)
	if err != nil || !bytes.Equal(base, after) {
		return page, errors.New("Codex ownership changed before operation")
	}
	immediate, err := readCodexEntry(ctx, run, cliProgram, state.FileID().Path(), options.name)
	if err != nil || !reflect.DeepEqual(immediate, current) {
		return page, errors.New("Codex registration changed before operation")
	}
	var args []string
	if options.action == "connect" {
		args = []string{"mcp", "add", options.name, "--", entry.Transport.Command, "--state-dir", entry.Transport.Args[1]}
	} else {
		args = []string{"mcp", "remove", options.name}
	}
	reply, err := run(ctx, cliProgram, state.FileID().Path(), args)
	if err != nil || reply.Code != 0 {
		return page, errors.New("vendor operation reply unavailable; outcome unknown")
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return page, errors.New("selected identity changed during operation; outcome unknown")
	}
	observed, err := readCodexEntry(ctx, run, cliProgram, state.FileID().Path(), options.name)
	if err != nil {
		return page, errors.New("vendor readback unavailable; outcome unknown")
	}
	if options.action == "connect" {
		if observed == nil || !reflect.DeepEqual(*observed, entry) {
			return page, errors.New("Codex saved entry differs; ownership not established")
		}
		ensureCodexToolTimeout(scope.FileID().Path(), options.name)
		if timed, err := readCodexEntry(ctx, run, cliProgram, state.FileID().Path(), options.name); err == nil && timed != nil && codexEntriesMatch(*timed, entry) {
			expected.Entry = *timed
		} else {
			codexSetupLog("Codex setup: tool timeout readback differs; the entry keeps Codex's default")
		}
		page.Registration = "saved"
		page.Owned = true
	} else {
		if observed != nil {
			return page, errors.New("Codex entry remains or changed after removal")
		}
		expected.Active = false
		page.Registration = "absent"
		page.Owned = false
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		return page, err
	}
	store := casapi.BoundedFileStore{MaxBytes: claudeMarkerLimit}
	if err := store.WriteContext(ctx, markerPath, casapi.Value{Data: base}, encoded); err != nil {
		return page, errors.New("vendor operation completed but ownership save is unknown")
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return page, errors.New("selected identity changed during ownership save")
	}
	return page, nil
}

// saveCodexToolTimeout adds the tool timeout to an owned entry saved before OA
// set it, and records the entry Codex reads back. Any failure leaves the entry
// working with Codex's default timeout and costs one log line.
func saveCodexToolTimeout(ctx context.Context, run codexSetupRunner, cli, directory, home, markerPath string, base []byte, marker codexSetupMarker, current codexEntry, handles []setupIdentityRef) error {
	ensureCodexToolTimeout(home, marker.Name)
	timed, err := readCodexEntry(ctx, run, cli, directory, marker.Name)
	if err != nil || timed == nil || !codexEntriesMatch(*timed, current) {
		codexSetupLog("Codex setup: tool timeout readback differs; the entry keeps Codex's default")
		return nil
	}
	if reflect.DeepEqual(*timed, marker.Entry) {
		return nil
	}
	marker.Entry = *timed
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if !setupIdentitiesCurrent(ctx, handles) {
		return errors.New("selected identity changed during ownership save")
	}
	if err := (casapi.BoundedFileStore{MaxBytes: claudeMarkerLimit}).WriteContext(ctx, markerPath, casapi.Value{Data: base}, encoded); err != nil {
		codexSetupLog("Codex setup: tool timeout saved; ownership record unchanged")
	}
	return nil
}
