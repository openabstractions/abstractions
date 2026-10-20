package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func selectAgentStartCLI(ctx context.Context, p startprofile.Profile, probe func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
	switch p.Name {
	case "claude-v1":
		selected, err := nativecli.SelectClaudeSavedCLI(ctx, []string{p.CLIFileID}, probe)
		if err != nil || selected == nil {
			return nil, errors.New("saved_native_cli_unavailable")
		}
		return selected, nil
	case "codex-v1":
		selected, err := nativecli.SelectCodexSavedCLI(ctx, []string{p.CLIFileID}, probe)
		if err != nil || selected == nil {
			return nil, errors.New("saved_native_cli_unavailable")
		}
		return selected, nil
	default:
		return nil, errors.New("unsupported_profile")
	}
}
