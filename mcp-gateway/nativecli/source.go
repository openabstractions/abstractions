package nativecli

import (
	"context"
	"os"
)

func openArchiveSource(ctx context.Context, name string) (*os.File, error) {
	return OpenSource(ctx, name)
}
