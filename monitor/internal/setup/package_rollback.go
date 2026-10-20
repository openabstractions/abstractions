package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
)

// HoldSavedPackage verifies the complete digest of the package retained for
// rollback. Keep the returned file and directory holds through replacement,
// verification and any restore (research/vocabulary/PACKAGE-TRANSACTION.md).
func HoldSavedPackage(ctx context.Context, path, digest string) (func() error, error) {
	return holdSavedPackage(ctx, path, digest, io.Discard)
}

func holdSavedPackage(ctx context.Context, path, digest string, capture io.Writer) (release func() error, err error) {
	if !assetSHA.MatchString(digest) {
		return nil, errors.New("the saved package has no valid digest")
	}
	path = filepath.Clean(path)
	directory, err := programfiles.HoldDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, directory.Release())
		}
	}()
	file, err := openWriteExcluded(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, file.Close())
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64<<20 {
		return nil, errors.New("the saved package has an invalid size or file type")
	}
	hash := sha256.New()
	var bytes int64
	if err := copyBounded(ctx, io.MultiWriter(hash, capture), file, make([]byte, archiveChunk), &bytes, info.Size()); err != nil {
		return nil, err
	}
	if bytes != info.Size() || hex.EncodeToString(hash.Sum(nil)) != digest {
		return nil, errors.New("the saved package differs from its recorded digest")
	}
	return func() error { return errors.Join(file.Close(), directory.Release()) }, nil
}
