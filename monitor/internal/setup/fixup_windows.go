//go:build windows

package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"unsafe"

	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

// MSI UI and log settings apply to the entire process.
var installerSettingsMu sync.Mutex

var setInternalUI = msi.NewProc("MsiSetInternalUI")
var reinstallProduct = msi.NewProc("MsiReinstallProductW")

const (
	installUILevelNone = 2
	// reinstallOMUS is msiexec /fomus, its default repair: files missing or
	// older, the package's machine and user registry entries and shortcuts.
	// Custom actions run as on install.
	reinstallOMUS            = 0x004 | 0x080 | 0x100 | 0x200
	msiSuccessRebootRequired = 3010
	msiInstallAlreadyRunning = 1618
)

// installedPackage reports whether a product of upgradeCode is installed.
func installedPackage(upgradeCode string) (bool, error) {
	products, err := relatedProducts(upgradeCode)
	return len(products) > 0, err
}

// repairPackage runs the Windows Installer repair of upgradeCode's one
// installed product with no installer window: msiexec /fomus <product> /qn.
func repairPackage(ctx context.Context, upgradeCode string) error {
	products, err := relatedProducts(upgradeCode)
	if err != nil {
		return err
	}
	if len(products) != 1 {
		return fmt.Errorf("expected one installed package of %s, found %d", upgradeCode, len(products))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	product, _ := windows.UTF16PtrFromString(products[0])
	installerSettingsMu.Lock()
	defer installerSettingsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	previous, _, _ := setInternalUI.Call(installUILevelNone, 0)
	code, _, _ := reinstallProduct.Call(uintptr(unsafe.Pointer(product)), reinstallOMUS)
	setInternalUI.Call(previous, 0)
	switch code {
	case msiSuccess, msiSuccessRebootRequired:
		return nil
	case msiInstallAlreadyRunning:
		return fmt.Errorf("%w (Windows Installer code %d)", ErrFixupInstallerBusy, code)
	default:
		return fmt.Errorf("Windows Installer did not repair %s (Windows Installer code %d)", products[0], code)
	}
}

// folderProgramRunning reports whether a process runs a program inside
// folder. A process this account cannot query is not one of its apps.
func folderProgramRunning(folder string) (bool, error) {
	pids, err := folderProcesses([]string{folder})
	return len(pids) > 0, err
}

type runningFolder struct {
	path string
	held fileid.Handle
}

// Spelling identifies an uncertain observation worth reporting. It never
// establishes that a process runs in this folder.
func (root runningFolder) mayContain(image string) bool {
	return fileid.PathNameWithin(image, root.path) || fileid.PathNameWithin(image, root.held.FileID().Path())
}

// folderProcesses lists the processes running a program inside one of
// folders. A process this account cannot query is not one of its apps.
func folderProcesses(folders []string) ([]uint32, error) {
	var roots []runningFolder
	defer func() {
		for _, root := range roots {
			root.held.Close()
		}
	}()
	ctx := context.Background()
	localFolder := fileid.Options{Links: fileid.FollowLocalLinks}
	for _, folder := range folders {
		// Callers include optional sibling folders. A local alias to an
		// installed folder is valid, and Open checks its target before I/O.
		root, err := fileid.Open(ctx, fileid.Self, folder, localFolder)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("could not identify the application folder: %w", err)
		}
		if !root.FileID().IsDir() {
			root.Close()
			return nil, errors.New("the application folder is not a directory")
		}
		roots = append(roots, runningFolder{path: folder, held: root})
	}
	if len(roots) == 0 {
		return nil, nil
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	var found []uint32
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if err != nil {
			continue
		}
		inside, imageErr := observedImageInFolders(handle, roots)
		windows.CloseHandle(handle)
		if imageErr != nil {
			return found, imageErr
		}
		if inside {
			found = append(found, entry.ProcessID)
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		for _, root := range roots {
			current, err := fileid.Resolve(ctx, fileid.Self, root.path, localFolder)
			if err != nil || !current.Equal(root.held.FileID()) {
				return found, errors.New("the application folder changed during the running-app check")
			}
		}
		return found, nil
	}
	return found, err
}

func observedImageInFolders(process windows.Handle, roots []runningFolder) (bool, error) {
	var buffer [windows.MAX_LONG_PATH]uint16
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return false, nil
	}
	image := windows.UTF16ToString(buffer[:size])
	held, err := fileid.Open(context.Background(), fileid.Self, image, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		for _, root := range roots {
			if root.mayContain(image) {
				return false, fmt.Errorf("could not identify a possible running application: %w", err)
			}
		}
		return false, nil
	}
	defer held.Close()
	inside, err := relateRunningImage(held, image, roots)
	if err != nil || !inside {
		return false, err
	}
	size = uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return false, nil
	}
	if image != windows.UTF16ToString(buffer[:size]) || held.FileID().IsDir() {
		return false, errors.New("a possible running application changed during the check")
	}
	return true, nil
}

func relateRunningImage(held fileid.Handle, image string, roots []runningFolder) (bool, error) {
	canonical := held.FileID().Path()
	relevant := false
	for _, root := range roots {
		relation, err := held.Relate(root.held)
		if err != nil {
			return false, fmt.Errorf("could not confirm a possible running application: %w", err)
		}
		if relation.Kind == fileid.RelationInside {
			return true, nil
		}
		relevant = relevant || root.mayContain(image) || root.mayContain(canonical)
	}
	// Relate can return Outside after a failed ancestry read or a move.
	if relevant {
		return false, errors.New("could not confirm the folder of a possible running application")
	}
	return false, nil
}
