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
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

type agentTransitionOptions struct {
	product, state, cli, scope string
	previous                   agentPrevious
}

func agentTransitionCommand(args []string, out, diagnostic io.Writer) int {
	if len(args) < 2 || args[0] != "update" || (args[1] != "claude" && args[1] != "codex") {
		return 2
	}
	o := agentTransitionOptions{product: args[1]}
	f := flag.NewFlagSet("update "+o.product, flag.ContinueOnError)
	f.SetOutput(diagnostic)
	f.StringVar(&o.state, "state-dir", "", "absolute existing C1 state directory")
	f.StringVar(&o.cli, o.product, "", "selected absolute native vendor CLI")
	f.StringVar(&o.previous.Program, "previous-program", "", "exact positively owned previous helper")
	f.StringVar(&o.previous.Application, "previous-application", "", "captured prior native application")
	f.StringVar(&o.previous.FileKey, "previous-file-key", "", "captured prior native file identity")
	f.StringVar(&o.previous.Revision, "previous-revision", "", "captured prior native enrollment revision")
	if err := f.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !immutableHelperTransitions() {
		checkLine(diagnostic, "immutable helper transitions are available on Darwin")
		return 1
	}
	if f.NArg() != 0 || !filepath.IsAbs(o.state) || !filepath.IsAbs(o.cli) || !filepath.IsAbs(o.previous.Program) || o.previous.Application == "" || o.previous.FileKey == "" || o.previous.Revision == "" {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	self, err := os.Executable()
	if err != nil {
		return 1
	}
	if o.product == "claude" {
		o.scope, err = claudeUserConfigurationHome()
	} else {
		o.scope, err = codexConfigurationHome()
	}
	if err == nil {
		err = applyAgentTransition(ctx, o, self, runClaudeSetupCLI, runCodexSetupCLI)
	}
	if err != nil {
		checkLine(diagnostic, "Connection transition stopped; prior and new images retained; "+err.Error())
		return 1
	}
	checkLine(out, "Owned user connection updated to the kept helper; reopen the client. Prior enrollment cleanup remains with the native operator.")
	return 0
}

func applyAgentTransition(ctx context.Context, o agentTransitionOptions, self string, claudeRun claudeSetupRunner, codexRun codexSetupRunner) error {
	if o.product != "claude" && o.product != "codex" {
		return errors.New("unsupported connection product")
	}
	if err := privatedir.CheckDir(o.state); err != nil {
		return errors.New("existing connection state is not private")
	}
	scope, _, err := setupIdentity(ctx, o.scope, true)
	if err != nil {
		return err
	}
	defer scope.Close()
	newImage, newID, err := setupIdentity(ctx, self, false)
	if err != nil {
		return err
	}
	defer newImage.Close()
	oldImage, _, err := setupIdentity(ctx, o.previous.Program, false)
	if err != nil {
		return err
	}
	defer oldImage.Close()
	cli, cliID, err := setupIdentity(ctx, o.cli, false)
	if err != nil {
		return err
	}
	defer cli.Close()
	state, _, err := setupIdentity(ctx, o.state, true)
	if err != nil {
		return err
	}
	defer state.Close()
	if oldImage.FileID().Key() != o.previous.FileKey || newImage.FileID().Equal(oldImage.FileID()) {
		return errors.New("previous or new helper identity differs")
	}
	if nativecli.DarwinAgentApplicationName(filepath.Base(o.previous.Program)) != o.previous.Application {
		return errors.New("prior application routing capture differs")
	}
	handles := []setupIdentityRef{scope, newImage, cli, state, oldImage}
	check := func() error {
		if !setupIdentitiesCurrent(ctx, handles) {
			return errors.New("connection identity changed")
		}
		return nil
	}
	var path string
	var base, next []byte
	var oldValue, newValue any
	var read func() (any, error)
	var write func(any) error
	if o.product == "claude" {
		key := sha256.Sum256([]byte(claudeSetupKey(scope.FileID().Key(), "OpenAbstractions", "user")))
		path = filepath.Join(state.FileID().Path(), "claude-setup-"+hex.EncodeToString(key[:])+".json")
		if err := privatedir.CheckFile(path); err != nil {
			return err
		}
		base, err = cas.ReadLimitContext(ctx, path, claudeMarkerLimit)
		if err != nil {
			return err
		}
		marker, err := decodeClaudeMarker(base)
		if err != nil {
			return err
		}
		if !marker.Active || marker.Scope != "user" || marker.Name != "OpenAbstractions" || marker.Entry.Command != o.previous.Program || !claudeMarkerIdentitiesMatch(ctx, marker, handles, true) {
			return errors.New("prior Claude connection is not positively owned")
		}
		if !reflect.DeepEqual(marker.Entry.Args, []string{"--state-dir", state.FileID().Path()}) || len(marker.Entry.Env) != 0 {
			return errors.New("unsupported owned Claude arguments")
		}
		captured, err := fileid.ParseStoredFileID(marker.Self)
		if err != nil {
			return err
		}
		actual, status, err := fileid.Revalidate(ctx, captured)
		if err != nil || status != fileid.StateSame || !actual.Equal(oldImage.FileID()) {
			return errors.New("prior Claude image capture changed")
		}
		version, err := claudeRun(ctx, o.cli, scope.FileID().Path(), []string{"--version"})
		if err != nil || !bytes.Contains(version, []byte("(Claude Code)")) {
			return errors.New("selected Claude CLI unavailable")
		}
		oldEntry := marker.Entry
		newEntry := oldEntry
		newEntry.Command = newImage.FileID().Path()
		oldValue, newValue = oldEntry, newEntry
		marker.Self, marker.CLI, marker.Entry, marker.Previous = newID, cliID, newEntry, &o.previous
		next, err = json.Marshal(marker)
		if err != nil {
			return err
		}
		read = func() (any, error) {
			entry, err := readClaudeNamedEntry(ctx, scope, ".claude.json", "OpenAbstractions")
			if err != nil || entry == nil {
				return nil, err
			}
			return *entry, nil
		}
		write = func(value any) error {
			args := []string{"mcp", "remove", "--scope", "user", "OpenAbstractions"}
			if value != nil {
				e := value.(claudeEntry)
				args = []string{"mcp", "add", "--scope", "user", "--transport", "stdio", "OpenAbstractions", "--", e.Command, "--state-dir", e.Args[1]}
			}
			_, err := claudeRun(ctx, o.cli, scope.FileID().Path(), args)
			return err
		}
	} else {
		key := sha256.Sum256([]byte(scope.FileID().Key() + "\x00OpenAbstractions"))
		path = filepath.Join(state.FileID().Path(), "codex-setup-"+hex.EncodeToString(key[:])+".json")
		if err := privatedir.CheckFile(path); err != nil {
			return err
		}
		base, err = cas.ReadLimitContext(ctx, path, claudeMarkerLimit)
		if err != nil {
			return err
		}
		marker, err := decodeCodexSetupMarker(base)
		if err != nil {
			return errors.New("unknown prior Codex ownership")
		}
		compatible := claudeSetupMarker{Project: marker.Scope, Self: marker.Self, CLI: marker.CLI, State: marker.State, Entry: claudeEntry{Command: marker.Entry.Transport.Command}}
		if !marker.Active || marker.Name != "OpenAbstractions" || marker.Entry.Transport.Command != o.previous.Program || !claudeMarkerIdentitiesMatch(ctx, compatible, handles, true) {
			return errors.New("prior Codex connection is not positively owned")
		}
		captured, err := fileid.ParseStoredFileID(marker.Self)
		if err != nil {
			return err
		}
		actual, status, err := fileid.Revalidate(ctx, captured)
		if err != nil || status != fileid.StateSame || !actual.Equal(oldImage.FileID()) {
			return errors.New("prior Codex image capture changed")
		}
		version, err := codexRun(ctx, o.cli, state.FileID().Path(), []string{"--version"})
		if err != nil || version.Code != 0 || !strings.HasPrefix(strings.TrimSpace(string(version.Output)), "codex-cli ") {
			return errors.New("selected Codex CLI unavailable")
		}
		// OA's own tool timeout and none compare as the same entry; write
		// re-adds the timeout after codex mcp add.
		oldEntry := marker.Entry
		oldEntry.ToolTimeout = codexOwnedTimeout(oldEntry.ToolTimeout)
		newEntry := oldEntry
		newEntry.Transport.Command = newImage.FileID().Path()
		oldValue, newValue = oldEntry, newEntry
		marker.Self, marker.CLI, marker.Entry, marker.Previous = newID, cliID, newEntry, &o.previous
		next, err = json.Marshal(marker)
		if err != nil {
			return err
		}
		read = func() (any, error) {
			entry, err := readCodexEntry(ctx, codexRun, o.cli, state.FileID().Path(), "OpenAbstractions")
			if err != nil || entry == nil {
				return nil, err
			}
			entry.ToolTimeout = codexOwnedTimeout(entry.ToolTimeout)
			return *entry, nil
		}
		write = func(value any) error {
			args := []string{"mcp", "remove", "OpenAbstractions"}
			if value != nil {
				e := value.(codexEntry)
				if !reflect.DeepEqual(e.Transport.Args, []string{"--state-dir", state.FileID().Path()}) || len(e.Transport.Env) != 0 || len(e.Transport.EnvVars) != 0 {
					return errors.New("unsupported owned Codex arguments")
				}
				args = []string{"mcp", "add", "OpenAbstractions", "--", e.Transport.Command, "--state-dir", e.Transport.Args[1]}
			}
			reply, err := codexRun(ctx, o.cli, state.FileID().Path(), args)
			if err != nil || reply.Code != 0 {
				return errors.New("Codex mutation reply unavailable")
			}
			if value != nil {
				ensureCodexToolTimeout(scope.FileID().Path(), "OpenAbstractions")
			}
			return nil
		}
	}
	return transitionOwnedConfiguration(ctx, path, base, next, oldValue, newValue, read, write, check)
}
