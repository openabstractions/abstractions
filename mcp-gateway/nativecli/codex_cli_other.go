//go:build !windows

package nativecli

import (
	"context"
	"errors"
)

type CodexCLI struct{ *Image }

func (c *CodexCLI) Close() error {
	if c == nil || c.Image == nil {
		return nil
	}
	return c.Image.Close()
}

func (*CodexCLI) Version() string { return "" }
func SelectCodexCLI(context.Context, func(context.Context, string) ([]byte, error)) (*CodexCLI, error) {
	return nil, errors.New("native Codex CLI selection is unavailable on this platform")
}

func SelectCodexSavedCLI(context.Context, []string, func(context.Context, string) ([]byte, error)) (*CodexCLI, error) {
	return nil, errors.New("native Codex CLI selection is unavailable on this platform")
}

func CodexDesktopRunning(context.Context) (bool, error) {
	return false, errors.New("Codex Desktop detection is unavailable on this platform")
}

// FollowCodexCLI returns the path unchanged: Codex Desktop's package layout
// exists only on Windows.
func FollowCodexCLI(_ context.Context, program string) (string, error) { return program, nil }

func HoldCodexSavedCLI(context.Context, []string) (*CodexCLI, error) {
	return nil, errors.New("native Codex CLI selection is unavailable on this platform")
}
