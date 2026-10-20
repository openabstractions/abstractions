package setup

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"

	"github.com/openabstractions/abstraction-identity/fileid"
)

func nativeArchivePeak(a Artifact, cacheRoot string) (int64, error) {
	root, err := installPath(a)
	if err != nil {
		return 0, err
	}
	var extra int64
	for _, member := range a.NativeResources.RecordRoots {
		pending, err := fileid.Pending(context.Background(), fileid.Self, filepath.Join(root, member))
		if err != nil {
			return 0, err
		}
		actual := pending.Path()
		// The selected Windows record writer can serialize four additional
		// prefix bytes. Reserve them conservatively without interpreting the
		// identity owner's spelling or resolving a namespace here.
		bytes := int64(len(actual)) + 4
		extra += 2 * bytes
	}
	if cacheRoot == "" {
		home, err := CurrentHome()
		if err != nil {
			return 0, err
		}
		cacheRoot = filepath.Join(home.Config, "downloads")
	}
	cache, err := fileid.Pending(context.Background(), fileid.Self, filepath.Join(cacheRoot, a.SHA256+".zip"))
	if err != nil {
		return 0, err
	}
	property := nativeArchivePathBytes(cache.Path())
	if a.NativeResources.MarkerBytes > 16<<20-property {
		return 0, errors.New("native install receipt exceeds writer bound")
	}
	extra += property
	if a.PeakBytes > math.MaxInt64-extra {
		return 0, errors.New("native archive peak overflows byte accounting")
	}
	return a.PeakBytes + extra, nil
}

func nativeArchiveSpace(a Artifact, root, stage string, freeBytes func(string) (uint64, error)) error {
	if freeBytes == nil {
		freeBytes = modelDiskFree
	}
	peak, err := nativeArchivePeak(a, filepath.Join(stage, "downloads"))
	if err != nil {
		return err
	}
	for _, folder := range []string{root, stage} {
		free, err := freeBytes(folder)
		if err != nil {
			return err
		}
		if free < uint64(peak) {
			return errors.New("insufficient space for retained archives, complete native trees and metadata")
		}
	}
	return nil
}

func validateNativeRecordBudget(a Artifact, files []archiveFileRecord, plan nativeInstallPlan) error {
	var base int64
	for _, root := range a.NativeResources.RecordRoots {
		size := int64(121)
		count := 0
		if root == plan.Directory {
			for _, f := range plan.Files {
				size += int64(len(f.Path)) + 96
				count++
			}
		} else {
			for _, f := range files {
				if strings.HasPrefix(f.Path, root+"/") {
					size += int64(len(strings.TrimPrefix(f.Path, root+"/"))) + 96
					count++
				}
			}
		}
		if count == 0 {
			return errors.New("native record root is absent from authenticated inventories")
		}
		base += 2 * size
	}
	if base != a.NativeResources.RecordBytes {
		return errors.New("native record byte budget differs from authenticated inventories")
	}
	remaining := a.PeakBytes
	charge := func(size int64) bool {
		if size < 0 || size > remaining {
			return false
		}
		remaining -= size
		return true
	}
	for _, size := range []int64{a.Bytes, a.NativeResources.Backend.Archive.Bytes, base, a.NativeResources.MarkerBytes, 65536} {
		if !charge(size) {
			return errors.New("native peak omits retained input or metadata bytes")
		}
	}
	for _, f := range files {
		if !charge(f.Size) {
			return errors.New("native peak omits host tree bytes")
		}
	}
	for _, f := range plan.Files {
		if !charge(f.Size) {
			return errors.New("native peak omits engine tree bytes")
		}
	}
	if remaining != 0 {
		return errors.New("native peak differs from source-bound complete install terms")
	}
	return nil
}

func nativeArchivePathBytes(name string) int64 {
	raw, _ := json.Marshal(name)
	return int64(len(`,"archive_path":`) + len(raw))
}

func nativeMarkerBase(a Artifact, files []archiveFileRecord, dirs []string) (int64, error) {
	raw, err := json.Marshal(archiveReceipt{Format: 2, ArtifactSHA256: a.SHA256, Version: a.Version, InstallIdentity: a.InstallIdentity, Files: files, Dirs: dirs})
	return int64(len(raw) + 1), err
}

func validateNativeMarkerBudget(a Artifact, files []archiveFileRecord, dirs []string) error {
	actual, err := nativeMarkerBase(a, files, dirs)
	if err != nil {
		return err
	}
	if a.NativeResources == nil || actual > a.NativeResources.MarkerBytes {
		return errors.New("native receipt structural bytes exceed source budget")
	}
	return nil
}
