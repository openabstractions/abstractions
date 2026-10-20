package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-credentials/go"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func factoryResetRoots() (resetRoots, error) {
	if err := refuseVirtualizedProfile("factory-reset", bootstrap.CurrentProfileView); err != nil {
		return resetRoots{}, err
	}
	if windows.GetCurrentProcessToken().IsElevated() {
		return resetRoots{}, errors.New("factory-reset: run as your ordinary account")
	}
	return factoryResetAccountRoots()
}

func factoryResetAccountRoots() (resetRoots, error) {
	local, roaming, err := layout.WindowsAccountFolders(layout.Current())
	if err != nil {
		return resetRoots{}, err
	}
	return resetRoots{local, roaming}, nil
}

// Conservative admission for old installed versions, which have no reset
// guard. The user follows the existing uninstall flow; reset never kills or
// unregisters a foreign process. Ambiguous inspection fails closed.
func factoryResetOffline() error {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\Uninstall`, registry.READ|view)
			if errors.Is(err, registry.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			names, err := k.ReadSubKeyNames(-1)
			if err != nil {
				k.Close()
				return err
			}
			for _, name := range names {
				sub, err := registry.OpenKey(k, name, registry.READ|view)
				if err != nil {
					k.Close()
					return err
				}
				display, _, readErr := sub.GetStringValue("DisplayName")
				publisher, _, _ := sub.GetStringValue("Publisher")
				sub.Close()
				if readErr != nil && !errors.Is(readErr, registry.ErrNotExist) {
					k.Close()
					return readErr
				}
				if strings.Contains(strings.ToLower(display), "openabstractions") || (display == "Abstraction" && publisher == "Open Abstractions") {
					k.Close()
					return errors.New("factory-reset: uninstall OA before resetting saved settings")
				}
			}
			k.Close()
		}
	}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(hostServiceName)
	service, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err == nil {
		windows.CloseServiceHandle(service)
		return errors.New("factory-reset: OA supervisor is registered; uninstall OA first")
	}
	if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == uint32(os.Getpid()) {
			continue
		}
		image := windows.UTF16ToString(entry.ExeFile[:])
		if resetProcessName(image) {
			return fmt.Errorf("factory-reset: close OA process %d before resetting", entry.ProcessID)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	return nil
}

func resetProcessName(image string) bool {
	for _, name := range oaProgramFiles(oaProgramStems) {
		if identity.SamePathName(image, name) {
			return true
		}
	}
	return false
}

func runtimeSettingsGuard(ctx context.Context, options runtimeFlags) (func(), error) {
	return runtimeStateGuard(ctx, options, factoryResetAccountRoots, resetGuard)
}

func settingsAccountRoots() (resetRoots, error) { return factoryResetAccountRoots() }

// Hold exact OA-profile files without read/write sharing while the directory
// entry moves. This neither inspects nor blocks any unrelated WebView profile.
// New Panel instances are also excluded by the shared account lifecycle guard.
func factoryResetProfileGuard(ctx context.Context, r resetRoots) (func(), error) {
	profile := filepath.Join(r.Roaming, "Abstraction Panel")
	if err := resetSafePath(ctx, profile); err != nil {
		return nil, err
	}
	var held []windows.Handle
	var paths []string
	release := func() {
		for _, h := range held {
			windows.CloseHandle(h)
		}
	}
	err := filepath.WalkDir(profile, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && path == profile {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := resetSafePath(ctx, path); err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			release()
			return nil, err
		}
		h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			release()
			return nil, fmt.Errorf("factory-reset: OA Panel profile is busy at %s; close the OA Panel and its profile processes before retrying: %w", path, err)
		}
		held = append(held, h)
	}
	return release, nil
}

func factoryResetSecretPlan() ([]string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return credentials.WindowsAccountSecretTargets(u.User.Sid.String())
}
func factoryResetSecrets(targets []string, out io.Writer) error {
	report, err := credentials.DeleteWindowsAccountSecretTargets(targets)
	if _, writeErr := fmt.Fprintf(out, "OS credentials irreversibly deleted: %d; remaining: %d. File restore does not recover deleted OS secrets.\n", len(report.Deleted), len(report.Remaining)); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}
