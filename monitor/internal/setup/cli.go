package setup

import (
	"context"
	"errors"

	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

type ClaudeCLI struct{ *nativecli.ClaudeCLI }
type CodexCLI struct{ *nativecli.CodexCLI }

func (c *ClaudeCLI) Check(ctx context.Context) error {
	if c == nil {
		return errors.New("Code selection unavailable")
	}
	return c.ClaudeCLI.Check(ctx)
}
func (c *CodexCLI) Check(ctx context.Context) error {
	if c == nil {
		return errors.New("Codex selection unavailable")
	}
	return c.CodexCLI.Check(ctx)
}

func (c *ClaudeCLI) Close() error {
	if c == nil {
		return nil
	}
	return c.ClaudeCLI.Close()
}
func (c *CodexCLI) Close() error {
	if c == nil {
		return nil
	}
	return c.CodexCLI.Close()
}
func SelectClaudeCLI(ctx context.Context, probe func(context.Context, string) ([]byte, error)) (*ClaudeCLI, error) {
	selected, err := nativecli.SelectClaudeCLI(ctx, probe)
	if err != nil {
		return nil, err
	}
	return &ClaudeCLI{selected}, nil
}
func SelectClaudeSavedCLI(ctx context.Context, stored []string, probe func(context.Context, string) ([]byte, error)) (*ClaudeCLI, error) {
	selected, err := nativecli.SelectClaudeSavedCLI(ctx, stored, probe)
	if err != nil {
		return nil, err
	}
	return &ClaudeCLI{selected}, nil
}
func SelectCodexCLI(ctx context.Context, probe func(context.Context, string) ([]byte, error)) (*CodexCLI, error) {
	selected, err := nativecli.SelectCodexCLI(ctx, probe)
	if err != nil {
		return nil, err
	}
	return &CodexCLI{selected}, nil
}
func OpenClaudeCode(ctx context.Context) error { return nativecli.OpenClaudeCode(ctx) }
