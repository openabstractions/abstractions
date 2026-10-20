package setup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

type agentReplacementResult struct {
	Backup    string
	Published bool
}

// The boundary is diagnostic custody, not permission to retry or weaken the
// open. In particular, an empty backup alone cannot distinguish these opens.
type agentReplacementError struct {
	Operation string
	Err       error
}

func (e *agentReplacementError) Error() string { return fmt.Sprintf("%s: %v", e.Operation, e.Err) }
func (e *agentReplacementError) Unwrap() error { return e.Err }

// This private helper update has an explicit namespace gap. Backup names the
// exact retained old object after its own handle moves it without replacement.
// A later failure preserves that backup and the stage for owning recovery;
// another occupant of target is never overwritten. Published records the
// publication boundary even if subsequent handle closure reports an error.
func replaceOwnedAgentImage(ctx context.Context, target string, oldPin agentReplacementPin, staged string, stagePin agentReplacementPin) (agentReplacementResult, error) {
	return replaceAgentImage(ctx, target, oldPin, staged, stagePin, nil)
}

func replaceAgentImage(ctx context.Context, target string, oldPin agentReplacementPin, staged string, stagePin agentReplacementPin, boundary func(string)) (result agentReplacementResult, err error) {
	return replaceAgentImageAtBackup(ctx, target, oldPin, staged, stagePin, "", boundary)
}

func replaceAgentImageAtBackup(ctx context.Context, target string, oldPin agentReplacementPin, staged string, stagePin agentReplacementPin, backup string, boundary func(string)) (result agentReplacementResult, err error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !filepath.IsAbs(target) || !filepath.IsAbs(staged) {
		return result, errors.New("helper replacement requires absolute files")
	}
	if err := fileid.SafeName(filepath.Base(target)); err != nil {
		return result, err
	}
	if err := fileid.SafeName(filepath.Base(staged)); err != nil {
		return result, err
	}
	unlock, err := cas.LockContext(ctx, target, false)
	if err != nil {
		return result, err
	}
	defer unlock()
	directory, err := programfiles.HoldDirectory(filepath.Dir(target))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, directory.Release()) }()
	parent, err := fileid.Open(ctx, fileid.Self, filepath.Dir(target), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	stageParent, err := fileid.Open(ctx, fileid.Self, filepath.Dir(staged), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return result, err
	}
	sameParent := parent.FileID().Equal(stageParent.FileID())
	if err := stageParent.Close(); err != nil {
		return result, err
	}
	if !sameParent || oldPin.ID.Equal(stagePin.ID) {
		return result, errors.New("helper stage must be a separate object beside target")
	}
	if backup != "" && (!filepath.IsAbs(backup) || !identity.SamePathName(filepath.Dir(backup), parent.FileID().Path()) || fileid.SafeName(filepath.Base(backup)) != nil || identity.SamePathName(backup, target) || identity.SamePathName(backup, staged)) {
		return result, errors.New("helper backup must be a separate name beside target")
	}
	// A helper an agent app is running keeps its image under the backup name
	// until that app restarts; the next launch runs the published image. Undo
	// publishes such a running backup as the stage. Only another program's
	// open handle without delete sharing refuses admission.
	old, err := openReplacementFile(ctx, parent, filepath.Base(target), oldPin)
	if err != nil {
		return result, &agentReplacementError{"current image admission", err}
	}
	defer func() { err = errors.Join(err, old.Close()) }()
	stage, err := openReplacementFile(ctx, parent, filepath.Base(staged), stagePin)
	if err != nil {
		return result, &agentReplacementError{"staged image admission", err}
	}
	defer func() { err = errors.Join(err, stage.Close()) }()
	if boundary != nil {
		boundary("admitted")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// The temporary name is only a nomination. Native no-replace rename refuses
	// any occupant that races its removal; no name is treated as ownership proof.
	if backup == "" {
		reservation, err := os.CreateTemp(parent.FileID().Path(), ".oa-helper-old-*")
		if err != nil {
			return result, err
		}
		backup = reservation.Name()
		// A concurrent set-aside sweep may already have removed the empty
		// reservation; the no-replace rename below refuses any new occupant.
		closeErr := reservation.Close()
		removeErr := os.Remove(backup)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		if err := errors.Join(closeErr, removeErr); err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := renameReplacementNoReplace(old, parent, filepath.Base(backup)); err != nil {
		return result, &agentReplacementError{"retain previous image", err}
	}
	result.Backup = backup
	if boundary != nil {
		boundary("backup")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := renameReplacementNoReplace(stage, parent, filepath.Base(target)); err != nil {
		return result, &agentReplacementError{"publish staged image", err}
	}
	result.Published = true
	return result, nil
}

// replacementImageAccess admits an image an agent app runs: Windows renames a
// mapped image and refuses write access to one. Delete access without delete
// sharing still refuses admission while another program holds the file open.
const replacementImageAccess = windows.GENERIC_READ | windows.DELETE | windows.SYNCHRONIZE

func openReplacementFile(ctx context.Context, parent fileid.Handle, name string, pin agentReplacementPin) (*os.File, error) {
	if pin.ID.IsZero() || pin.ID.IsDir() || pin.ID.Links() != 1 || pin.Bytes < 0 || pin.Bytes > 1<<30 {
		return nil, errors.New("invalid helper replacement pin")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wide, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.File().Fd()), ObjectName: wide}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, replacementImageAccess, &oa, &status, nil, 0, windows.FILE_SHARE_READ, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(handle), name)
	fail := func(err error) (*os.File, error) { return nil, errors.Join(err, f.Close()) }
	facts, err := fileid.FromFile(ctx, f)
	if err != nil {
		return fail(err)
	}
	id := facts.FileID()
	if err := facts.Close(); err != nil {
		return fail(err)
	}
	if !id.Equal(pin.ID) || id.IsDir() || id.Links() != 1 {
		return fail(errors.New("helper replacement object changed"))
	}
	stat, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if !stat.Mode().IsRegular() || stat.Size() != pin.Bytes {
		return fail(errors.New("helper replacement size changed"))
	}
	hash := sha256.New()
	reader := &replacementReader{ctx: ctx, file: f}
	n, err := io.Copy(hash, io.LimitReader(reader, pin.Bytes+1))
	if err != nil {
		return fail(err)
	}
	if n != pin.Bytes || string(hash.Sum(nil)) != string(pin.Hash[:]) {
		return fail(errors.New("helper replacement content changed"))
	}
	return f, nil
}

type replacementReader struct {
	ctx  context.Context
	file *os.File
}

func (r *replacementReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Read(p)
}

func renameReplacementNoReplace(file *os.File, parent fileid.Handle, name string) error {
	if err := fileid.SafeName(name); err != nil {
		return err
	}
	wide, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	info := struct {
		Flags  uint32
		Root   windows.Handle
		Length uint32
		Name   [windows.MAX_LONG_PATH]uint16
	}{Root: windows.Handle(parent.File().Fd()), Length: uint32((len(wide) - 1) * 2)}
	if len(wide) > len(info.Name) {
		return errors.New("helper replacement name exceeds bound")
	}
	copy(info.Name[:], wide)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(windows.Handle(file.Fd()), &status, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), windows.FileRenameInformation)
}
