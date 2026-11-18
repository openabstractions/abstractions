package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// The existing process-tree owner contains only this explicit configuration or
// inventory child. It never adopts a running product or starts a model session.
func runClaudePanelCommand(parent context.Context, program string, args ...string) ([]byte, error) {
	return runPanelConfigurationCommand(parent, program, args, false)
}

func runCodexPanelCommand(parent context.Context, program string, args ...string) ([]byte, error) {
	return runPanelConfigurationCommand(parent, program, args, true)
}

type panelConfigurationError struct {
	stage           string
	cause           error
	exitCode        int
	pluginOperation string
	pluginReason    string
}

func (e *panelConfigurationError) Error() string {
	message := "connection command could not be completed"
	if errors.Is(e.cause, context.DeadlineExceeded) {
		message = "connection command timed out"
	} else if errors.Is(e.cause, context.Canceled) {
		message = "connection command was canceled"
	}
	message += " (stage: " + e.stage
	if e.exitCode != 0 {
		message += fmt.Sprintf("; exit: %d", e.exitCode)
	}
	if e.pluginOperation == "connections" {
		message += "; Claude connections; reason: " + e.pluginReason
	} else if e.pluginOperation != "" {
		message += "; Claude plugin: " + e.pluginOperation + "; reason: " + e.pluginReason
	}
	return message + ")"
}
func (e *panelConfigurationError) Unwrap() error { return e.cause }

// The plugin owner has a thirty-second deadline. Let it report and join before
// the outer command owner expires; ordinary inventory commands keep their budget.
func panelConfigurationBudget(args []string) time.Duration {
	if len(args) >= 3 && args[0] == "plugin" && args[1] == "claude" {
		switch args[2] {
		case "install", "update", "remove":
			return 35 * time.Second
		}
	}
	return 25 * time.Second
}

// Never forward raw vendor stderr, which may contain profile or credential
// values. Accept only the gateway's closed diagnostic vocabulary:
// "OA_CLAUDE_PLUGIN_ERROR <operation> <reason>" from a plugin command and
// "OA_CLAUDE_CONNECTIONS_ERROR <reason>" from the connection inventory. The
// operation of an inventory failure reads "connections".
func panelPluginDiagnostic(raw []byte) (operation, reason string) {
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		var op, why string
		switch {
		case len(fields) == 3 && fields[0] == "OA_CLAUDE_PLUGIN_ERROR":
			op, why = fields[1], fields[2]
			switch op {
			case "version", "marketplace_list", "plugin_list", "validate", "install", "update", "uninstall", "marketplace_add", "marketplace_update", "marketplace_remove":
			default:
				continue
			}
			switch why {
			case "timeout", "cancelled", "exit", "unavailable", "folder_missing", "marketplace_missing", "plugin_missing", "name_conflict", "invalid_plugin", "access_denied":
			default:
				continue
			}
		case len(fields) == 2 && fields[0] == "OA_CLAUDE_CONNECTIONS_ERROR":
			op, why = "connections", fields[1]
			switch why {
			case "state_folder", "bound", "marker", "settings_file", "changed", "timeout", "other":
			default:
				continue
			}
		default:
			continue
		}
		if operation != "" { // An ambiguous diagnostic is not a current verdict.
			return "", ""
		}
		operation, reason = op, why
	}
	return operation, reason
}

// panelConfigurationSentence says in one plain sentence why a connection
// command failed, from its closed diagnostic. It answers "" when the command
// gave no diagnostic.
func panelConfigurationSentence(err error) string {
	var command *panelConfigurationError
	if !errors.As(err, &command) {
		return ""
	}
	if command.pluginOperation == "connections" {
		return map[string]string{
			"state_folder":  "OpenAbstractions couldn't read its Claude connection folder. Check again.",
			"bound":         "OpenAbstractions found more saved Claude connections than it can list.",
			"marker":        "A saved Claude connection record is damaged. Connect again to replace it.",
			"settings_file": "OpenAbstractions couldn't read Claude Code's settings file. Check again.",
			"changed":       "Claude connection settings changed while OpenAbstractions read them. Check again.",
			"timeout":       "Reading the Claude connections took too long. Check again.",
			"other":         "OpenAbstractions couldn't read the saved Claude connections. Check again.",
		}[command.pluginReason]
	}
	if command.pluginOperation == "" {
		return ""
	}
	return map[string]string{
		"folder_missing":      "Claude Code couldn't find the OpenAbstractions plugin folder. Connect again to restore it.",
		"marketplace_missing": "Claude Code doesn't list the OpenAbstractions plugin source. Connect again to add it.",
		"plugin_missing":      "Claude Code doesn't have the OpenAbstractions plugin installed. Connect again to install it.",
		"name_conflict":       "Another plugin source in Claude Code already uses the OpenAbstractions name.",
		"invalid_plugin":      "Claude Code rejected the OpenAbstractions plugin files. Connect again to replace them.",
		"access_denied":       "Claude Code couldn't change its plugin settings. Close Claude, then connect again.",
		"timeout":             "Claude Code took too long to answer. Check again.",
		"cancelled":           "The Claude Code change was canceled.",
		"unavailable":         "Claude Code couldn't be started. Open Claude, then check again.",
		"exit":                "Claude Code stopped with an error while OpenAbstractions changed its plugin settings.",
	}[command.pluginReason]
}

// logPanelConfigurationFailure writes one category line for a failed
// connection command: its stage, exit code and closed diagnostic only.
func logPanelConfigurationFailure(e *panelConfigurationError) {
	operation, reason := e.pluginOperation, e.pluginReason
	if operation == "" {
		operation, reason = "none", "none"
	}
	log.Printf("connection command failed: stage=%s exit=%d operation=%s reason=%s", e.reason(), e.exitCode, operation, reason)
}

func (e *panelConfigurationError) reason() string {
	var refusal *layout.Refusal
	if e.stage == "command_layout" && errors.As(e.cause, &refusal) {
		switch refusal.Reason {
		case layout.VirtualizedProfile:
			return "command_virtualized_profile"
		case layout.UnknownProfileView:
			return "command_unknown_profile"
		case layout.NoProfileFolder:
			return "command_profile_folder"
		case layout.Sandboxed:
			return "command_sandboxed"
		}
	}
	return e.stage
}

func panelConfigurationEnvironment(temporary string, codex bool) ([]string, error) {
	names := []string{"APPDATA", "LOCALAPPDATA", "USERPROFILE", "HOMEDRIVE", "HOMEPATH"}
	set := []string{"DISABLE_UPDATES=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "ENABLE_CLAUDEAI_MCP_SERVERS=false"}
	if codex {
		if home := os.Getenv("CODEX_HOME"); home != "" {
			if !filepath.IsAbs(home) {
				return nil, errors.New("Codex settings home must be absolute")
			}
			set = append(set, "CODEX_HOME="+home)
		}
	} else if home := os.Getenv("CLAUDE_CONFIG_DIR"); home != "" {
		if !filepath.IsAbs(home) {
			return nil, errors.New("Claude settings directory must be absolute")
		}
		set = append(set, "CLAUDE_CONFIG_DIR="+home)
	}
	return (supervision.CleanEnvironment{Temp: temporary, Names: names, Set: set}).Build()
}

func runPanelConfigurationCommand(parent context.Context, program string, args []string, codex bool) (raw []byte, err error) {
	stage := "command_layout"
	exitCode := 0
	var pluginOperation, pluginReason string
	defer func() {
		if err != nil {
			failure := &panelConfigurationError{stage: stage, cause: err, exitCode: exitCode, pluginOperation: pluginOperation, pluginReason: pluginReason}
			logPanelConfigurationFailure(failure)
			err = failure
		}
	}()
	ctx, cancel := context.WithTimeout(parent, panelConfigurationBudget(args))
	defer cancel()
	platform := layout.Current()
	roots, err := layout.Resolve(platform)
	if err != nil {
		return nil, err
	}
	stage = "command_tmp_root"
	tmpRoot, err := layout.EnsureTmp(roots, platform)
	if err != nil {
		return nil, err
	}
	stage = "command_temp"
	temporary, err := os.MkdirTemp(tmpRoot, "oa-claude-config-")
	if err != nil {
		return nil, err
	}
	stage = "command_identity"
	temporaryID, err := fileid.Resolve(ctx, fileid.Self, temporary, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	closed := true // no child exists yet
	defer func() {
		if !closed {
			err = errors.Join(err, errors.New("connection command completion is unconfirmed; temporary files retained"))
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		current, checkErr := fileid.Resolve(cleanup, fileid.Self, temporary, fileid.Options{Links: fileid.RefuseLinks})
		if checkErr != nil || !current.Equal(temporaryID) {
			err = errors.Join(err, errors.New("connection temporary folder changed; preserved"))
			return
		}
		err = errors.Join(err, os.RemoveAll(temporary))
	}()
	stage = "command_environment"
	env, err := panelConfigurationEnvironment(temporary, codex)
	if err != nil {
		return nil, err
	}
	stage = "command_handles"
	input, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer outRead.Close()
	defer outWrite.Close()
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer errRead.Close()
	defer errWrite.Close()
	stage = "command_start"
	child, err := supervision.Start(ctx, supervision.Options{Executable: program, Args: args,
		Dir: temporary, Env: env, Stdin: input, Stdout: outWrite, Stderr: errWrite,
		ForceTimeout: 3 * time.Second, ProcessMemoryLimit: 1 << 30})
	if err != nil {
		return nil, err
	}
	closed = false
	if err := errors.Join(outWrite.Close(), errWrite.Close()); err != nil {
		cancel()
	}
	type result struct {
		raw []byte
		err error
	}
	output, diagnostic := make(chan result, 1), make(chan result, 1)
	read := func(f *os.File, limit int64, done chan result) {
		value, readErr := io.ReadAll(io.LimitReader(f, limit+1))
		if int64(len(value)) > limit {
			readErr = errors.New("connection command output exceeds its bound")
			cancel()
		}
		done <- result{value, readErr}
	}
	go read(outRead, 1<<20, output)
	go read(errRead, 32<<10, diagnostic)
	stage = "command_wait"
	status, waitErr := child.Wait()
	exit, known := child.Exit()
	closed = known && exit.Unknown == ""
	if waitErr != nil || !closed {
		_ = outRead.Close()
		_ = errRead.Close()
	}
	out, failure := <-output, <-diagnostic
	pluginOperation, pluginReason = panelPluginDiagnostic(failure.raw)
	if err := errors.Join(waitErr, out.err, failure.err, ctx.Err()); err != nil {
		return nil, err
	}
	stage = "command_exit"
	if !closed || exit.MemoryLimitHit {
		return nil, errors.New("connection command completion is unconfirmed or resource-limited")
	}
	if status != 0 {
		exitCode = status
		return nil, fmt.Errorf("connection command refused (exit %d)", status)
	}
	stage = "command_cleanup"
	return out.raw, nil
}
