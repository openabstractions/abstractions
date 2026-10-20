package main

import (
	"context"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
)

func setupCodexCLIIdentity(ctx context.Context, program string) (setupIdentityRef, string, error) {
	return setupio.CodexCLIIdentity(ctx, program)
}
