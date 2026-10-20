package nativecli

import (
	"context"
	"debug/pe"
	"errors"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/runtime/packaged"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// SelectCodexSavedCLI reopens the explicitly saved installation without requiring
// a live desktop process. The saved FileID is a locator; the current native image
// hold and bounded version probe supply the same admission as the C1 selector.
func SelectCodexSavedCLI(ctx context.Context, stored []string, probe func(context.Context, string) ([]byte, error)) (*CodexCLI, error) {
	return selectCodexSavedCLI(ctx, stored, probe, followingImage(FollowCodexCLI, holdSavedCodexImage))
}

func selectCodexSavedCLI(ctx context.Context, stored []string, probe func(context.Context, string) ([]byte, error), hold func(context.Context, string) (nativeProductImage, error)) (*CodexCLI, error) {
	if probe == nil {
		return nil, errors.New("saved Codex command unavailable")
	}
	selected, err := selectSavedImage(ctx, stored, hold)
	if err != nil {
		return nil, err
	}
	result := &CodexCLI{image: selected}
	if err := codexProbe(ctx, result, probe); err != nil {
		return nil, errors.Join(err, result.Close())
	}
	return result, nil
}

func holdSavedCodexImage(ctx context.Context, program string) (nativeProductImage, error) {
	if !fileid.SamePathName(filepath.Base(program), "codex.exe") {
		return nil, errors.New("saved Codex executable name unavailable")
	}
	image, matched, err := packaged.MatchImage(ctx, codexProductFamily, "app/resources/codex.exe", program)
	if err != nil {
		return nil, err
	}
	if matched {
		if err := productSubsystem(image.File(), pe.IMAGE_SUBSYSTEM_WINDOWS_CUI); err != nil {
			return nil, errors.Join(err, image.Close())
		}
		return image, nil
	}
	return holdCodexPath(ctx, program)
}

// HoldCodexSavedCLI authenticates and holds the current saved image. It
// performs no process discovery or invocation; a start recipe's approval uses
// it to recheck the saved executable without running it.
func HoldCodexSavedCLI(ctx context.Context, stored []string) (*CodexCLI, error) {
	selected, err := selectSavedImage(ctx, stored, followingImage(FollowCodexCLI, holdSavedCodexImage))
	if err != nil {
		return nil, err
	}
	return &CodexCLI{image: selected}, nil
}
