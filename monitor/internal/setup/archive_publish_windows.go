package setup

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

func publishArchiveDirectory(ctx context.Context, stage, destination, original string) error {
	return publishWindowsArchiveDirectory(ctx, stage, destination, original, renameReplacementNoReplace)
}

// A reader of any staged member can briefly block a directory rename.
// Keep the exact directory open without delete sharing and publish it relative
// to the held parent, using the same no-replace rename as a helper image.
func publishWindowsArchiveDirectory(ctx context.Context, stage, destination, original string, move func(*os.File, fileid.Handle, string) error) (err error) {
	if filepath.Dir(stage) != filepath.Dir(destination) {
		return errors.New("setup archive: stage and destination must share a parent")
	}
	committed := false
	noteClose := func(closeErr error) {
		if closeErr == nil {
			return
		}
		if committed {
			log.Printf("setup archive: release published stage custody: %v", closeErr)
			return
		}
		err = errors.Join(err, closeErr)
	}
	directory, err := programfiles.HoldDirectory(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer func() { noteClose(directory.Release()) }()
	parent, err := fileid.Open(ctx, fileid.Self, filepath.Dir(destination), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return err
	}
	defer func() { noteClose(parent.Close()) }()
	owned, err := openArchiveStage(parent, filepath.Base(stage), original)
	if err != nil {
		return err
	}
	defer func() { noteClose(owned.Close()) }()
	const retries = 4
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := absent(destination); err != nil {
			return err
		}
		err := move(owned, parent, filepath.Base(destination))
		if err == nil {
			committed = true
			return nil
		}
		var status windows.NTStatus
		if errors.As(err, &status) {
			err = status.Errno()
		}
		failure := &os.LinkError{Op: "rename", Old: stage, New: destination, Err: err}
		if attempt == retries || !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return failure
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func openArchiveStage(parent fileid.Handle, name, original string) (*os.File, error) {
	wide, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.File().Fd()), ObjectName: wide}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.GENERIC_READ|windows.DELETE|windows.SYNCHRONIZE, &attributes, &status,
		nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), name)
	current, err := fileid.FileKeyOf(file)
	if err == nil && current != original {
		err = errors.New("setup archive: staging directory changed before publication")
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// Clean only the original stage. Its held directory blocks replacement;
// os.Root confines removal to that directory, including nested entries.
func cleanArchiveStage(stage, original string) {
	if err := removeOwnedArchiveStage(stage, original); err != nil {
		log.Printf("setup archive: retained stage after cleanup could not be confirmed: %v", err)
	}
}

func removeOwnedArchiveStage(stage, original string) error {
	directory, err := programfiles.HoldDirectory(filepath.Dir(stage))
	if err != nil {
		return err
	}
	defer directory.Release()
	parent, err := fileid.Open(context.Background(), fileid.Self, filepath.Dir(stage), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return err
	}
	defer parent.Close()
	owned, err := openArchiveStage(parent, filepath.Base(stage), original)
	if err != nil {
		return err
	}
	defer owned.Close()
	entries, err := owned.ReadDir(-1)
	if err != nil {
		return err
	}
	rootParent, err := os.OpenRoot(filepath.Dir(stage))
	if err != nil {
		return err
	}
	defer rootParent.Close()
	root, err := rootParent.OpenRoot(filepath.Base(stage))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, entry := range entries {
		if err := root.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	remove := byte(1)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(windows.Handle(owned.Fd()), &status, &remove, 1, windows.FileDispositionInformation)
}
