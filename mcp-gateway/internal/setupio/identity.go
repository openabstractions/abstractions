package setupio

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/runtime/packaged"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"os"
	"path/filepath"
	"runtime"
)

type IdentityRef interface {
	FileID() fileid.FileID
	File() *os.File
	Close() error
}

func Open(ctx context.Context, path string, directory bool) (fileid.Handle, string, error) {
	h, err := fileid.Open(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return fileid.Handle{}, "", err
	}
	stored, err := Describe(h, directory)
	if err != nil {
		h.Close()
		return fileid.Handle{}, "", err
	}
	return h, stored, nil
}

func Describe(h IdentityRef, directory bool) (string, error) {
	if h.FileID().IsDir() != directory {
		return "", errors.New("wrong file kind")
	}
	info, err := h.File().Stat()
	if err != nil || (!directory && !info.Mode().IsRegular()) {
		return "", errors.New("not an ordinary executable file")
	}
	stored, err := h.FileID().Stored()
	if err != nil {
		return "", err
	}
	return stored.String(), nil
}

func Current(ctx context.Context, handles []IdentityRef) bool {
	for _, h := range handles {
		if registered, ok := h.(interface{ Check(context.Context) error }); ok {
			if registered.Check(ctx) != nil {
				return false
			}
			continue
		}
		current, err := fileid.Resolve(ctx, fileid.Self, h.FileID().Path(), fileid.Options{Links: fileid.RefuseLinks})
		if err != nil || !current.Equal(h.FileID()) {
			return false
		}
	}
	return true
}

func ClaudeHome() (string, error) {
	return ClaudeHomeFor(layout.Current(), os.Getenv("CLAUDE_CONFIG_DIR"), layout.ProcessHome)
}
func ClaudeHomeFor(platform layout.Platform, override string, home func() (string, error)) (string, error) {
	if _, err := layout.Resolve(platform); err != nil {
		return "", err
	}
	if root := override; root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("Claude settings directory must be absolute")
		}
		return root, nil
	}
	return home()
}

// ClaudeConfigDir is Claude Code's configuration folder: the absolute
// CLAUDE_CONFIG_DIR override, else .claude in the home folder.
func ClaudeConfigDir() (string, error) {
	return ClaudeConfigDirFor(layout.Current(), os.Getenv("CLAUDE_CONFIG_DIR"), layout.ProcessHome)
}
func ClaudeConfigDirFor(platform layout.Platform, override string, home func() (string, error)) (string, error) {
	root, err := ClaudeHomeFor(platform, override, home)
	if err != nil || override != "" {
		return root, err
	}
	return filepath.Join(root, ".claude"), nil
}
func CodexCLIIdentity(ctx context.Context, program string) (IdentityRef, string, error) {
	if runtime.GOOS != "windows" {
		return Open(ctx, program, false)
	}
	// A saved Codex Desktop path names a package version its update removed;
	// it follows to the registered version, which the match below verifies.
	program, err := nativecli.FollowCodexCLI(ctx, program)
	if err != nil {
		return nil, "", err
	}
	image, matched, err := packaged.MatchImage(ctx, "OpenAI.Codex_2p2nqsd0c76g0", "app/resources/codex.exe", program)
	if err != nil {
		return nil, "", err
	}
	if !matched {
		return Open(ctx, program, false)
	}
	stored, err := Describe(image, false)
	if err == nil {
		err = image.Check(ctx)
	}
	if err != nil {
		return nil, "", errors.Join(err, image.Close())
	}
	return image, stored, nil
}
