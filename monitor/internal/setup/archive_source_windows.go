package setup

import (
	"context"
	"os"

	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

func openArchiveSource(ctx context.Context, name string) (*os.File, error) {
	return nativecli.OpenSource(ctx, name)
}
