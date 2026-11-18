package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

func retireUpgradeAdmission(ctx context.Context, path, scope string) error {
	return retireUpgradeAdmissionWithOwner(ctx, path, scope, trustedExclusionOwner)
}

// MSI RemoveFiles precedes the commit finalizer, which can recreate this CAS
// sidecar while releasing its still-live removal record. Never unlink a CAS
// inode with a holder or waiter: a share-zero handle excludes every already-open
// descriptor, and blocks new opens until held deletion and close complete.
func retireUpgradeAdmissionWithOwner(ctx context.Context, path, scope string, trust func(*windows.SID, string) error) (failure error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if scope != "machine" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "exclusion.json" || filepath.Base(dir) != "upgrade-v1" || filepath.Base(filepath.Dir(dir)) != "abstraction" {
		return errors.New("upgrade admission cleanup requires its exact machine scratch scope")
	}
	_, release, err := holdUpgradeDirectory(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	lock := filepath.Join(dir, "admission.lock")
	key, err := fileid.Resolve(ctx, fileid.Self, lock, fileid.Options{Links: fileid.RefuseLinks})
	if errors.Is(err, os.ErrNotExist) {
		if err := emptyUpgradeScope(path); err != nil {
			return err
		}
		release()
		release = nil
		return pruneUpgradeScratch(dir)
	}
	if err != nil {
		return err
	}
	file, err := openUpgradeFile(lock, windows.DELETE|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, 0)
	if err != nil {
		return fmt.Errorf("upgrade admission cleanup could not exclude lock holders: %w", err)
	}
	defer func() {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}()
	held, err := fileid.FromFile(ctx, file)
	if err != nil {
		return err
	}
	same := key.Equal(held.FileID())
	if err := held.Close(); err != nil {
		return err
	}
	if !same {
		return errors.New("upgrade admission cleanup file identity changed")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return err
	}
	if info.FileSizeHigh != 0 || info.FileSizeLow != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_READONLY != 0 {
		return errors.New("upgrade admission cleanup requires an empty writable sidecar")
	}
	security, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := security.Owner()
	if err != nil {
		return err
	}
	if err := trust(owner, scope); err != nil {
		return err
	}
	// A competing installer can finish between CAS release and our exclusive
	// open. Its record/sidecars must survive; absence is rechecked while no
	// claimant can hold or acquire this lock object.
	if err := emptyUpgradeScope(path); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := deleteUpgradeFile(file); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		file = nil
		return err
	}
	file = nil
	release()
	release = nil
	return pruneUpgradeScratch(dir)
}

func emptyUpgradeScope(path string) error {
	for _, target := range []string{path, chainPath(path), joinedUpgradePath(path)} {
		if _, err := os.Lstat(target); err == nil {
			return errors.New("upgrade admission cleanup found successor scope state")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func pruneUpgradeScratch(dir string) error {
	// These are the two fixed machine scratch folders, never an install or
	// caller-data folder. Native removal is nonrecursive and clears no attrs.
	for _, folder := range []string{dir, filepath.Dir(dir)} {
		name, err := windows.UTF16PtrFromString(folder)
		if err != nil {
			return err
		}
		if err := windows.RemoveDirectory(name); errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) {
			return nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
