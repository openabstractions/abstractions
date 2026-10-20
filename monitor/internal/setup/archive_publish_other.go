//go:build !windows

package setup

import (
	"context"
	"os"

	"github.com/openabstractions/abstraction-identity/fileid"
)

func publishArchiveDirectory(_ context.Context, stage, destination, _ string) error {
	return os.Rename(stage, destination)
}

func cleanArchiveStage(stage, original string) {
	if held, err := fileid.Open(context.Background(), fileid.Self, stage, fileid.Options{Links: fileid.RefuseLinks}); err == nil {
		defer held.Close()
		current, err := fileid.FileKeyOf(held.File())
		if err != nil || current != original {
			return
		}
		_ = os.RemoveAll(stage)
	}
}
