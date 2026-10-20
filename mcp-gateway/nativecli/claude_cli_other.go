//go:build !windows

package nativecli

import (
	"context"
	"errors"
)

type ClaudeCLI struct{ *Image }

func (c *ClaudeCLI) Close() error {
	if c == nil || c.Image == nil {
		return nil
	}
	return c.Image.Close()
}

func (*ClaudeCLI) Version() string { return "" }
func SelectClaudeCLI(context.Context, func(context.Context, string) ([]byte, error)) (*ClaudeCLI, error) {
	return nil, errors.New("native Code selection is unavailable on this platform")
}
func SelectClaudeSavedCLI(context.Context, []string, func(context.Context, string) ([]byte, error)) (*ClaudeCLI, error) {
	return nil, errors.New("native Code selection is unavailable on this platform")
}
func HoldClaudeSavedCLI(context.Context, []string) (*ClaudeCLI, error) {
	return nil, errors.New("native Code selection is unavailable on this platform")
}
func OpenClaudeCode(context.Context) error {
	return errors.New("registered product activation is unavailable on this platform")
}

// ClaudeRunning cannot read the process inventory on this platform; callers
// treat the error as a possibly open conversation.
func ClaudeRunning(context.Context) (bool, error) {
	return false, errors.New("Claude process detection is unavailable on this platform")
}
