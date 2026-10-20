package cacheproducer

import (
	"context"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
)

func RunClaude(ctx context.Context, program, directory string, args []string) ([]byte, error) {
	return setupio.RunClaude(ctx, program, directory, args)
}
func RunCodex(ctx context.Context, program, directory string, args []string) (CodexResult, error) {
	r, e := setupio.RunCodex(ctx, program, directory, args)
	return CodexResult{Output: r.Output, Diagnostic: r.Diagnostic, Code: r.Code}, e
}
