//go:build windows

package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// removeEmptyFolderTree checks the complete tree before removing anything.
// Hold each directory against substitution during inspection and pruning.
func removeEmptyFolderTree(folder string) error {
	parent, err := fileid.Open(context.Background(), fileid.Self, filepath.Dir(folder), fileid.Options{Links: fileid.RefuseLinks})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer parent.Close()
	top, err := openCleanupDirectory(parent.File(), filepath.Base(folder))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	held := []*os.File{top}
	defer func() {
		for _, directory := range held {
			if directory != nil {
				_ = directory.Close()
			}
		}
	}()
	var order []int
	var walk func(int, string) error
	walk = func(index int, path string) error {
		directory := held[index]
		entries, err := directory.ReadDir(-1)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			childPath := filepath.Join(path, entry.Name())
			if entry.Type() != fs.ModeDir {
				return fmt.Errorf("%s holds %s", folder, childPath)
			}
			child, err := openCleanupDirectory(directory, entry.Name())
			if err != nil {
				return fmt.Errorf("inspect the folder %s: %w", childPath, err)
			}
			childIndex := len(held)
			held = append(held, child)
			if err := walk(childIndex, childPath); err != nil {
				return err
			}
		}
		order = append(order, index)
		return nil
	}
	if err := walk(0, folder); err != nil {
		return err
	}
	for _, index := range order {
		directory := held[index]
		remove := byte(1)
		var status windows.IO_STATUS_BLOCK
		err := windows.NtSetInformationFile(windows.Handle(directory.Fd()), &status, &remove, 1, windows.FileDispositionInformation)
		held[index] = nil
		if err := errors.Join(err, directory.Close()); err != nil {
			return fmt.Errorf("remove the empty folder %s: %w", directory.Name(), err)
		}
	}
	return nil
}

// Open a single name without traversing a reparse point, then check its
// type through that handle. DELETE access without delete sharing excludes
// rename while inspecting and removing the admitted directory.
func openCleanupDirectory(parent *os.File, name string) (*os.File, error) {
	if err := fileid.SafeName(name); err != nil {
		return nil, err
	}
	wide, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: wide}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	const access = windows.FILE_LIST_DIRECTORY | windows.FILE_READ_ATTRIBUTES | windows.DELETE | windows.SYNCHRONIZE
	if err := windows.NtCreateFile(&handle, access, &attributes, &status, nil, 0, windows.FILE_SHARE_READ,
		windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0); err != nil {
		var native windows.NTStatus
		if errors.As(err, &native) {
			return nil, native.Errno()
		}
		return nil, err
	}
	directory := os.NewFile(uintptr(handle), name)
	info, err := directory.Stat()
	if err == nil && (!info.IsDir() || info.Mode()&fs.ModeType != fs.ModeDir || reparsePoint(info)) {
		err = fmt.Errorf("%s is not a plain folder", name)
	}
	if err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	return directory, nil
}

// lastMSIMessage is the last message line of an MSI log, without the log's
// own start and stop lines and its closing "Installation failed", cut to
// limit characters.
func lastMSIMessage(text string, limit int) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "===") || strings.HasPrefix(line, "MSI (") || strings.HasSuffix(line, "-- Installation failed.") {
			continue
		}
		if runes := []rune(line); len(runes) > limit {
			line = string(runes[:limit])
		}
		return line
	}
	return ""
}
