package nativecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
)

var codexVersionLine = regexp.MustCompile(`^codex-cli ([0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}(?:-[0-9A-Za-z]{1,32}(?:[.-][0-9A-Za-z]{1,32}){0,8})?)\r?\n?$`)

// CodexCLI holds the installed executable selected from explicit PATH entries.
// The caller supplies its existing bounded --version process owner.
type CodexCLI struct {
	image        nativeProductImage
	version      string
	mu           sync.Mutex
	desktopCheck func(context.Context) error
	desktopClose func() error
}

func (c *CodexCLI) Version() string { return c.version }
func (c *CodexCLI) Program() string { return c.image.Program() }
func (c *CodexCLI) FileKey() string { return c.image.FileKey() }

func (c *CodexCLI) Check(ctx context.Context) error {
	if c == nil {
		return errors.New("Codex selection unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.image.Check(ctx); err != nil {
		return err
	}
	if c.desktopCheck != nil {
		return c.desktopCheck(ctx)
	}
	return ctx.Err()
}

func (c *CodexCLI) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	if c.image != nil {
		err = c.image.Close()
	}
	if c.desktopClose != nil {
		err = errors.Join(err, c.desktopClose())
		c.desktopClose = nil
	}
	return err
}

func SelectCodexCLI(ctx context.Context, versionProbe func(context.Context, string) ([]byte, error)) (*CodexCLI, error) {
	return selectCodexCLI(ctx, os.Getenv("PATH"), versionProbe, currentCodexDesktop)
}

func selectCodexCLI(ctx context.Context, searchPath string, probe func(context.Context, string) ([]byte, error), desktop func(context.Context) (*CodexCLI, error)) (*CodexCLI, error) {
	selected, err := desktop(ctx)
	if err != nil {
		return nil, err
	}
	if selected != nil {
		if err := codexProbe(ctx, selected, probe); err != nil {
			return nil, errors.Join(err, selected.Close())
		}
		return selected, nil
	}
	selected, err = selectCodexPaths(ctx, searchPath, probe)
	if err != nil {
		return nil, errors.New("Codex command is unavailable; check the installed Codex app and retry")
	}
	return selected, nil
}

func codexProbe(ctx context.Context, selected *CodexCLI, probe func(context.Context, string) ([]byte, error)) error {
	if probe == nil {
		return errors.New("Codex CLI version check is unavailable")
	}
	if err := selected.Check(ctx); err != nil {
		return err
	}
	output, err := probe(ctx, selected.Program())
	if err != nil {
		return err
	}
	if len(output) > 256 || strings.IndexByte(string(output), 0) >= 0 {
		return errors.New("Codex CLI version response is invalid")
	}
	match := codexVersionLine.FindSubmatch(output)
	if len(match) != 2 {
		return errors.New("The selected program did not report a valid Codex CLI version")
	}
	if err := selected.Check(ctx); err != nil {
		return err
	}
	selected.version = string(match[1])
	return nil
}

func selectCodexPaths(ctx context.Context, searchPath string, probe func(context.Context, string) ([]byte, error)) (*CodexCLI, error) {
	entries := filepath.SplitList(searchPath)
	if len(entries) > 256 || probe == nil {
		return nil, errors.New("Codex CLI selection is unavailable")
	}
	var selected *Image
	fail := func(err error) (*CodexCLI, error) {
		if selected != nil {
			err = errors.Join(err, selected.Close())
		}
		return nil, err
	}
	for _, dir := range entries {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			return fail(errors.New("Codex CLI PATH entry is relative"))
		}
		program := filepath.Join(dir, "codex.exe")
		if _, err := os.Lstat(program); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fail(err)
		}
		image, err := holdCodexPath(ctx, program)
		if err != nil {
			return fail(err)
		}

		if selected != nil {
			same := selected.FileKey() == image.FileKey()
			if err := image.Close(); err != nil {
				return fail(err)
			}
			if !same {
				return fail(errors.New("Several Codex CLI images are available; keep one PATH installation and retry"))
			}
			continue
		}
		selected = image
	}
	if selected == nil {
		return fail(errors.New("Codex CLI is unavailable on PATH; install it and retry"))
	}
	result := &CodexCLI{image: selected}
	if err := codexProbe(ctx, result, probe); err != nil {
		return fail(err)
	}
	return result, nil
}

func holdCodexPath(ctx context.Context, program string) (*Image, error) {
	directory, err := programfiles.HoldDirectory(filepath.Dir(program))
	if err != nil {
		return nil, err
	}
	input, err := openArchiveSource(ctx, program)
	if err != nil {
		return nil, errors.Join(err, directory.Release())
	}
	key, err := fileid.FileKeyOf(input)
	if err != nil {
		return nil, errors.Join(err, input.Close(), directory.Release())
	}
	image := &Image{file: input, directory: directory, program: program, key: key}
	if err := image.Check(ctx); err != nil {
		return nil, errors.Join(err, image.Close())
	}
	return image, nil
}
