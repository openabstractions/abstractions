package cacheproducer

import (
	"context"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
)

type setupIdentityRef = setupio.IdentityRef

func setupIdentity(ctx context.Context, path string, directory bool) (fileid.Handle, string, error) {
	return setupio.Open(ctx, path, directory)
}
func setupIdentitiesCurrent(ctx context.Context, handles []setupIdentityRef) bool {
	return setupio.Current(ctx, handles)
}
func setupCodexCLIIdentity(ctx context.Context, program string) (setupIdentityRef, string, error) {
	return setupio.CodexCLIIdentity(ctx, program)
}
