//go:build !windows

package nativecli

import (
	"context"
	"os"

	identity "github.com/openabstractions/abstraction-identity"
)

// Portable DATA checks do not establish a native executable hold.
func OpenSource(ctx context.Context, name string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := identity.LocalPath(name); err != nil {
		return nil, err
	}
	return os.Open(name)
}
