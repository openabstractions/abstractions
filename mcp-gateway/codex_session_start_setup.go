package main

import (
	"context"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
)

const codexHookPlugin = "oa-codex-session-start"
const codexHookVersion = "0.8.0"

var errCodexHookPreserved = cacheproducer.ErrCodexPreserved

func codexHookFiles(server string) map[string][]byte { return cacheproducer.CodexFiles(server) }
func applyCodexSessionStartSetup(ctx context.Context, o codexSetupOptions, home string, run codexSetupRunner) error {
	return applyCodexSessionStartSetupStatus(ctx, o, home, run, new(string))
}
func applyCodexSessionStartSetupStatus(ctx context.Context, o codexSetupOptions, home string, run codexSetupRunner, status *string) error {
	return cacheproducer.ApplyCodexStatus(ctx, cacheproducer.CodexOptions{Action: o.action, State: o.state, CLI: o.cli, Name: o.name, Program: o.helperProgram, FileKey: o.helperKey}, home, func(ctx context.Context, program, directory string, args []string) (cacheproducer.CodexResult, error) {
		r, e := run(ctx, program, directory, args)
		return cacheproducer.CodexResult{Output: r.Output, Diagnostic: r.Diagnostic, Code: r.Code}, e
	}, status)
}
