package main

import (
	"context"
	"errors"
	"flag"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
	"io"
	"os"
	"path/filepath"
	"time"
)

type claudePluginOptions struct{ action, project, state, cli, registeredProgram, scope string }
type claudePluginMarker = cacheproducer.ClaudeMarker

var errClaudeCompanionAbsent = cacheproducer.ErrClaudeAbsent
var errClaudeCompanionUpdate = cacheproducer.ErrClaudeUpdate

func applyClaudePlugin(ctx context.Context, o claudePluginOptions, self string, run claudeSetupRunner) error {
	err := cacheproducer.ApplyClaude(ctx, cacheproducer.ClaudeOptions{Action: o.action, Project: o.project, State: o.state, CLI: o.cli, RegisteredProgram: o.registeredProgram, Scope: o.scope}, self, cacheproducer.ClaudeRunner(run))
	return cacheproducer.ClaudeInvocationDiagnostic(err)
}
func claudePluginFiles(market, self, state string) map[string][]byte {
	return claudePluginFilesForScope(market, self, state, "local")
}
func claudePluginFilesForScope(market, self, state, scope string) map[string][]byte {
	return cacheproducer.ClaudeFilesForScope(market, self, state, scope)
}
func claudeBackgroundModule(ctx context.Context, state, cli, stateID string) ([]byte, error) {
	return cacheproducer.ClaudeBackgroundModule(ctx, state, cli, stateID)
}
func readPluginMarker(ctx context.Context, path string) ([]byte, *claudePluginMarker, error) {
	return cacheproducer.ReadClaudeMarker(ctx, path)
}
func readProjectPlugin(ctx context.Context, project fileid.Handle, market, root string) (bool, bool, error) {
	return cacheproducer.ReadClaudeProject(ctx, project, market, root)
}

// cacheproducer owns the vendor invocation diagnostic and the plugin JSON
// helpers; the command and its tests read them under their local names.
type claudePluginInvocationError = cacheproducer.ClaudeInvocationError

var claudePluginInvocationFailure = cacheproducer.ClaudeInvocationFailure
var pluginJSON = cacheproducer.PluginJSON
var pluginHash = cacheproducer.PluginHash
var pluginList = cacheproducer.PluginList
var pluginString = cacheproducer.PluginString

func claudePluginCommand(args []string, output, diagnostics io.Writer) int {
	if len(args) < 3 || args[0] != "plugin" || args[1] != "claude" {
		return 2
	}
	if args[2] == "--help" {
		checkLine(output, "plugin claude install|update|remove --scope user|local --state-dir ABS --claude ABS (local scope also requires --project ABS); status --scope user is read-only")
		return 0
	}
	o := claudePluginOptions{action: args[2], scope: "local"}
	f := flag.NewFlagSet("plugin claude "+o.action, flag.ContinueOnError)
	f.SetOutput(diagnostics)
	f.StringVar(&o.project, "project", "", "absolute project directory; personal local-scope setup")
	f.StringVar(&o.scope, "scope", "local", "personal plugin scope: user or local")
	f.StringVar(&o.state, "state-dir", "", "absolute private integration state directory")
	f.StringVar(&o.cli, "claude", "", "absolute selected Claude Code executable")
	f.StringVar(&o.registeredProgram, "registered-program", "", "owned registered helper locator for removal/read-only status; never executed")
	if err := f.Parse(args[3:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || (o.scope != "user" && o.scope != "local") || (o.scope == "local" && !filepath.IsAbs(o.project)) || (o.scope == "user" && o.project != "") || !filepath.IsAbs(o.state) || !filepath.IsAbs(o.cli) || (o.action != "install" && o.action != "update" && o.action != "remove" && !(o.action == "status" && o.scope == "user")) {
		return 2
	}
	if o.registeredProgram != "" && ((o.action != "remove" && o.action != "status") || !filepath.IsAbs(o.registeredProgram)) {
		return 2
	}
	self, err := os.Executable()
	if err != nil {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = applyClaudePlugin(ctx, o, self, runClaudeSetupCLI)
	if o.action == "status" {
		state := "unknown"
		switch {
		case err == nil:
			state = "current"
		case errors.Is(err, errClaudeCompanionAbsent):
			state = "absent"
		case errors.Is(err, errClaudeCompanionUpdate):
			state = "update"
		}
		if _, writeErr := output.Write(pluginJSON(map[string]string{"outcome": "page", "state": state})); writeErr != nil {
			return 1
		}
		return 0
	}
	if err != nil {
		var invocation *claudePluginInvocationError
		if errors.As(err, &invocation) {
			checkLine(diagnostics, "OA_CLAUDE_PLUGIN_ERROR "+invocation.Operation()+" "+invocation.Reason())
		}
		checkLine(diagnostics, "Claude plugin: "+err.Error()+"; preserve owned configuration and inspect with the selected CLI")
		return 1
	}
	checkLine(output, "Claude personal "+o.scope+" plugin: "+o.action+" verified; reload/load and actual asynchronous reception remain unchecked")
	return 0
}
