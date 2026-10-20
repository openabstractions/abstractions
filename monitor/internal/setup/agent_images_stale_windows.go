package setup

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"time"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
)

const agentImageCleanupTimeout = 10 * time.Second

// removeStaleAgentImageCopies deletes earlier staged and retained copies of
// one connector after its swap: "<image>.<id>.tmp" and any number of
// ".previous" suffixes on such a name. keep names the files that stay, such as
// the current image and the backup this update's Undo restores. Other
// connectors, lock files, directories and links stay. A file that cannot be
// deleted, for example one a process still holds, also stays; cleanup never
// fails an update.
func removeStaleAgentImageCopies(ctx context.Context, root, program string, keep ...string) []string {
	ctx, cancel := context.WithTimeout(ctx, agentImageCleanupTimeout)
	defer cancel()
	target, err := fileid.ResolveLeaf(ctx, fileid.Self, program)
	if err != nil {
		return nil
	}
	baseLen := len(target.Name())
	return removeAgentImageCopies(ctx, root, nil, func(name string) bool {
		if len(name) <= baseLen || name[baseLen] != '.' || !staleAgentImageSuffix(name[baseLen+1:]) {
			return false
		}
		candidate, err := fileid.ResolveLeaf(ctx, fileid.Self, filepath.Join(root, name[:baseLen]))
		return err == nil && candidate.Key() == target.Key()
	}, append([]string{program}, keep...)...)
}

func staleAgentImageSuffix(name string) bool {
	const previous = ".previous"
	const stage = ".tmp"
	for len(name) >= len(previous) && fileid.SamePathName(name[len(name)-len(previous):], previous) {
		name = name[:len(name)-len(previous)]
	}
	return len(name) > len(stage) && fileid.SamePathName(name[len(name)-len(stage):], stage)
}

// removeAgentImageCopies applies a Setup-owned naming rule within the held
// helper directory. The caller's directory check runs inside that hold. Keep
// applies to the named slots, even if replaced.
func removeAgentImageCopies(ctx context.Context, root string, checkDirectory func(string) error, stale func(string) bool, keep ...string) []string {
	if ctx.Err() != nil {
		return nil
	}
	directory, err := programfiles.HoldDirectory(root)
	if err != nil {
		return nil
	}
	defer directory.Release()
	if checkDirectory != nil && checkDirectory(root) != nil {
		return nil
	}
	parent, err := fileid.Open(ctx, fileid.Self, root, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return nil
	}
	defer parent.Close()
	kept := make(map[string]bool, len(keep))
	for _, path := range keep {
		if path == "" {
			continue
		}
		leaf, err := fileid.ResolveLeaf(ctx, fileid.Self, path)
		if err != nil {
			return nil
		}
		kept[leaf.Key()] = true
	}
	var removed []string
	for ctx.Err() == nil {
		entries, readErr := parent.File().ReadDir(64)
		if readErr != nil && readErr != io.EOF {
			break
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				break
			}
			name := entry.Name()
			if !entry.Type().IsRegular() || !stale(name) {
				continue
			}
			if removeAgentImageCopy(ctx, root, parent, name, kept) {
				removed = append(removed, name)
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	slices.Sort(removed)
	return removed
}

func removeAgentImageCopy(ctx context.Context, root string, parent fileid.Handle, name string, kept map[string]bool) bool {
	if len(kept) != 0 {
		leaf, err := fileid.ResolveLeaf(ctx, fileid.Self, filepath.Join(root, name))
		if err != nil || kept[leaf.Key()] {
			return false
		}
	}
	image, err := parent.Beneath(name, fileid.Options{})
	if err != nil {
		return false
	}
	id := image.FileID()
	return image.Close() == nil && deleteAgentImagePinned(ctx, id.Path(), id) == nil
}
