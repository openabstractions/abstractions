package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

const claudeConnectionLimit = 64
const claudeConnectionOutputLimit = 1 << 20

// These are saved configuration facts, not host approval or live OA authority.
type claudeConnection struct {
	Previous     *agentPrevious `json:"previous,omitempty"`
	Project      string         `json:"project"`
	ProjectKey   string         `json:"project_key"`
	Program      string         `json:"program"`
	ProgramKey   string         `json:"program_key"`
	Name         string         `json:"name"`
	Registration string         `json:"registration"`
	Kind         string         `json:"kind,omitempty"`
	CLIIdentity  string         `json:"cli_identity,omitempty"` // discovery only; caller must freshly authenticate the executable
}

type claudeConnectionPage struct {
	HelperLifecycle []agentDiagnosticRecord `json:"helper_lifecycle,omitempty"`
	Outcome         string                  `json:"outcome"`
	Connections     []claudeConnection      `json:"connections"`
}

func claudeConnectionsCommand(args []string, output, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("connections claude", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	state := flags.String("state-dir", "", "absolute private gateway state directory (read only)")
	lifecycle := flags.Bool("helper-lifecycle", false, "include bounded OA helper lifecycle diagnostics (read only)")
	cliLocator := flags.Bool("cli-locator", false, "include owned CLI location hints for fresh native authentication (read only)")
	if len(args) < 2 || args[0] != "connections" || args[1] != "claude" {
		return 2
	}
	if err := flags.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*state) {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	page, err := readClaudeConnectionsWithOptions(ctx, *state, claudeUserConfigurationHome, *cliLocator)
	if err == nil && *lifecycle {
		page.HelperLifecycle = readAgentDiagnostics(ctx, *state)
	}
	if err != nil {
		page = claudeConnectionPage{Outcome: "unavailable", Connections: []claudeConnection{}}
	}
	raw, encodeErr := json.Marshal(page)
	if encodeErr != nil || len(raw) > claudeConnectionOutputLimit {
		raw = []byte(`{"outcome":"unavailable","connections":[]}`)
		err = errors.New("connection inventory output bound")
	}
	if _, writeErr := output.Write(append(raw, '\n')); writeErr != nil {
		return 1
	}
	if err != nil {
		checkLine(diagnostics, "Claude saved connections are unavailable; no current connection or permission claim was established.")
		return 1
	}
	return 0
}

func readClaudeConnections(ctx context.Context, statePath string) (claudeConnectionPage, error) {
	return readClaudeConnectionsFor(ctx, statePath, claudeUserConfigurationHome)
}

func readClaudeConnectionsFor(ctx context.Context, statePath string, userHome func() (string, error)) (claudeConnectionPage, error) {
	return readClaudeConnectionsWithOptions(ctx, statePath, userHome, false)
}

func readClaudeConnectionsWithOptions(ctx context.Context, statePath string, userHome func() (string, error), cliLocator bool) (claudeConnectionPage, error) {
	page := claudeConnectionPage{Outcome: "page", Connections: []claudeConnection{}}
	if !filepath.IsAbs(statePath) {
		return page, errors.New("connection state must be absolute")
	}
	// An absent first-run subtree is a read-only empty inventory. Hold the
	// nearest existing ancestor before observing its first absent child;
	// links and inaccessible ancestors
	// retain their ordinary refusal and no private state is created here.
	if _, err := os.Lstat(statePath); errors.Is(err, os.ErrNotExist) {
		ancestor, missing := filepath.Dir(statePath), filepath.Base(statePath)
		for {
			if err := ctx.Err(); err != nil {
				return page, err
			}
			_, err := os.Lstat(ancestor)
			if err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return page, err
			}
			missing = filepath.Base(ancestor)
			next := filepath.Dir(ancestor)
			if next == ancestor {
				return page, errors.New("connection state ancestor unavailable")
			}
			ancestor = next
		}
		hold, holdErr := programfiles.HoldDirectory(ancestor)
		if holdErr != nil {
			return page, holdErr
		}
		defer hold.Release()
		parent, _, openErr := setupIdentity(ctx, ancestor, true)
		if openErr != nil {
			return page, openErr
		}
		defer parent.Close()
		child, childErr := parent.Beneath(missing, fileid.Options{Links: fileid.RefuseLinks})
		if errors.Is(childErr, os.ErrNotExist) {
			return page, ctx.Err()
		}
		if childErr != nil {
			return page, childErr
		}
		if err := child.Close(); err != nil {
			return page, err
		}
		return page, errors.New("connection state ancestor changed during absence check")
	}
	if err := privatedir.CheckDir(statePath); err != nil {
		return page, err
	}
	state, _, err := setupIdentity(ctx, statePath, true)
	if err != nil {
		return page, err
	}
	defer state.Close()
	// Enumerate this one existing owned marker directory, never projects or an
	// agent profile. ReadDir's finite count also bounds unrelated entries here.
	directory, err := os.Open(state.FileID().Path())
	if err != nil {
		return page, err
	}
	defer directory.Close()
	directoryKey, err := fileid.FileKeyOf(directory)
	if err != nil || directoryKey != state.FileID().Key() {
		return page, errors.New("connection directory identity changed")
	}
	entries, err := directory.ReadDir(2*claudeConnectionLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return page, err
	}
	if len(entries) > 2*claudeConnectionLimit || !setupIdentitiesCurrent(ctx, []setupIdentityRef{state}) {
		return page, errors.New("connection inventory unavailable or exceeds its bound")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		name := entry.Name()
		// The user-scoped companion has no MCP server of its own. Its marker
		// belongs to the separate companion owner, not the project-plugin
		// connection reader below. A saved user MCP remains independently readable.
		if name == "claude-plugin-user.json" {
			continue
		}
		plugin := strings.HasPrefix(name, "claude-plugin-")
		if (!strings.HasPrefix(name, "claude-setup-") && !plugin) || !strings.HasSuffix(name, ".json") {
			continue
		}
		if len(page.Connections) == claudeConnectionLimit {
			return page, errors.New("connection inventory exceeds its bound")
		}
		markerPath := filepath.Join(state.FileID().Path(), name)
		if err := privatedir.CheckFile(markerPath); err != nil {
			return page, err
		}
		markerFile, err := state.Beneath(name, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			return page, err
		}
		raw, readErr := cas.ReadLimitContext(ctx, markerPath, claudeMarkerLimit)
		current, currentErr := state.Beneath(name, fileid.Options{Links: fileid.RefuseLinks})
		same := currentErr == nil && current.FileID().Equal(markerFile.FileID())
		markerFile.Close()
		if currentErr == nil {
			current.Close()
		}
		if readErr != nil || !same {
			return page, errors.New("connection ownership changed during read")
		}
		var row claudeConnection
		if plugin {
			row, err = readClaudePluginConnection(ctx, state, name, markerPath)
		} else {
			var marker claudeSetupMarker
			marker, err = decodeClaudeMarker(raw)
			if err == nil && !claudeSetupName(marker.Name) {
				err = errors.New("unknown connection ownership")
			}
			if err == nil {
				row, err = readClaudeConnection(ctx, state, name, marker, userHome)
			}
		}
		if err != nil {
			return page, err
		}
		after, err := cas.ReadLimitContext(ctx, markerPath, claudeMarkerLimit)
		if err != nil || !bytes.Equal(raw, after) {
			return page, errors.New("connection ownership changed during public readback")
		}
		if !cliLocator {
			row.CLIIdentity = ""
		}
		page.Connections = append(page.Connections, row)
	}
	if !setupIdentitiesCurrent(ctx, []setupIdentityRef{state}) {
		return page, errors.New("connection state changed during read")
	}
	sort.Slice(page.Connections, func(i, j int) bool {
		if page.Connections[i].Project == page.Connections[j].Project {
			return page.Connections[i].Name < page.Connections[j].Name
		}
		return page.Connections[i].Project < page.Connections[j].Project
	})
	return page, nil
}

func readClaudeConnection(ctx context.Context, state fileid.Handle, filename string, marker claudeSetupMarker, userHome func() (string, error)) (claudeConnection, error) {
	var row claudeConnection
	storedState, err := fileid.ParseStoredFileID(marker.State)
	if err != nil {
		return row, err
	}
	currentState, stateStatus, err := fileid.Revalidate(ctx, storedState)
	if err != nil || stateStatus != fileid.StateSame || !currentState.Equal(state.FileID()) {
		return row, errors.New("connection belongs to a different state directory")
	}
	storedProject, err := fileid.ParseStoredFileID(marker.Project)
	if err != nil {
		return row, err
	}
	currentProject, projectStatus, err := fileid.Revalidate(ctx, storedProject)
	if err != nil || projectStatus != fileid.StateSame {
		return row, errors.New("connection project identity unavailable")
	}
	project, _, err := setupIdentity(ctx, currentProject.Path(), true)
	if err != nil {
		return row, err
	}
	defer project.Close()
	if !project.FileID().Equal(currentProject) {
		return row, errors.New("connection project changed")
	}
	configName := ".mcp.json"
	if marker.Scope == "user" {
		home, err := userHome()
		if err != nil {
			return row, errors.New("user configuration scope unavailable")
		}
		scope, err := fileid.Resolve(ctx, fileid.Self, home, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil || !scope.Equal(project.FileID()) {
			return row, errors.New("user configuration scope changed")
		}
		configName = ".claude.json"
	}
	self, err := fileid.ParseStoredFileID(marker.Self)
	if err != nil || marker.Entry.Type != "stdio" || marker.Entry.Command != self.Path() ||
		!reflect.DeepEqual(marker.Entry.Args, []string{"--state-dir", state.FileID().Path()}) || len(marker.Entry.Env) != 0 {
		return row, errors.New("unknown connection configuration")
	}
	key := sha256.Sum256([]byte(claudeSetupKey(project.FileID().Key(), marker.Name, marker.Scope)))
	if filename != "claude-setup-"+hex.EncodeToString(key[:])+".json" {
		return row, errors.New("connection ownership name mismatch")
	}
	current, err := readClaudeNamedEntry(ctx, project, configName, marker.Name)
	if err != nil || !setupIdentitiesCurrent(ctx, []setupIdentityRef{state, project}) {
		return row, errors.New("connection public readback unavailable")
	}
	program, _, err := setupIdentity(ctx, marker.Entry.Command, false)
	programKey := ""
	if err == nil {
		defer program.Close()
		if !setupIdentitiesCurrent(ctx, []setupIdentityRef{state, project, program}) {
			return row, errors.New("connection identity changed during read")
		}
		programKey = program.FileID().Key()
	}
	// Ownership of saved configuration survives loss of the target image.
	// An empty key supplies no native image identity or execution authority.
	row = claudeConnection{Project: project.FileID().Path(), ProjectKey: project.FileID().Key(), Program: marker.Entry.Command, ProgramKey: programKey, Name: marker.Name, Registration: "changed"}
	row.Previous = marker.Previous
	if marker.Scope == "user" {
		row.Kind = "user"
	}
	if current == nil {
		row.Registration = "absent"
	} else if marker.Active && reflect.DeepEqual(*current, marker.Entry) {
		row.Registration = "saved"
		if _, err := fileid.ParseStoredFileID(marker.CLI); err == nil {
			row.CLIIdentity = marker.CLI
		}
	}
	return row, nil
}

func readClaudePluginConnection(ctx context.Context, state fileid.Handle, filename, markerPath string) (claudeConnection, error) {
	var row claudeConnection
	_, marker, err := readPluginMarker(ctx, markerPath)
	if err != nil || marker == nil {
		return row, errors.New("unknown plugin ownership")
	}
	storedState, err := fileid.ParseStoredFileID(marker.State)
	if err != nil {
		return row, err
	}
	currentState, stateStatus, err := fileid.Revalidate(ctx, storedState)
	if err != nil || stateStatus != fileid.StateSame || !currentState.Equal(state.FileID()) {
		return row, errors.New("plugin state identity changed")
	}
	storedProject, err := fileid.ParseStoredFileID(marker.Project)
	if err != nil {
		return row, err
	}
	current, status, err := fileid.Revalidate(ctx, storedProject)
	if err != nil || status != fileid.StateSame {
		return row, errors.New("plugin project identity changed")
	}
	project, _, err := setupIdentity(ctx, current.Path(), true)
	if err != nil {
		return row, err
	}
	defer project.Close()
	if !project.FileID().Equal(current) {
		return row, errors.New("plugin project changed")
	}
	key := pluginHash([]byte(project.FileID().Key()))[:24]
	market := "oa-local-" + key
	if filename != "claude-plugin-"+key+".json" || marker.Marketplace != market || marker.Scope != "local" {
		return row, errors.New("plugin ownership name mismatch")
	}
	self, err := fileid.ParseStoredFileID(marker.Self)
	if err != nil {
		return row, err
	}
	root := filepath.Join(state.FileID().Path(), "claude-plugin-"+key)
	row = claudeConnection{Project: project.FileID().Path(), ProjectKey: project.FileID().Key(), Program: self.Path(), Name: "openabstractions@" + market, Kind: "plugin", Registration: "changed"}
	program, _, programErr := setupIdentity(ctx, self.Path(), false)
	if programErr == nil {
		defer program.Close()
		row.ProgramKey = program.FileID().Key()
	}
	known, installed, declarationErr := readProjectPlugin(ctx, project, market, root)
	if declarationErr == nil && !known && !installed {
		row.Registration = "absent"
	}
	payloadCurrent := true
	for name := range claudePluginFiles(market, self.Path(), state.FileID().Path()) {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := privatedir.CheckFile(path); err != nil {
			payloadCurrent = false
			break
		}
		b, err := cas.ReadLimitContext(ctx, path, claudePayloadLimit)
		if err != nil || b == nil || pluginHash(b) != marker.Files[name] {
			payloadCurrent = false
			break
		}
	}
	if declarationErr == nil && known && installed && marker.MarketplaceOwned && marker.PluginOwned && payloadCurrent {
		row.Registration = "saved"
		if _, err := fileid.ParseStoredFileID(marker.CLI); err == nil {
			row.CLIIdentity = marker.CLI
		}
	}
	if !setupIdentitiesCurrent(ctx, []setupIdentityRef{state, project}) {
		return row, errors.New("plugin inventory identity changed")
	}
	return row, nil
}
