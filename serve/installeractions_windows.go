package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows Installer discards a custom action's standard error. Every command
// the MSI runs appends one line on failure to installer-actions.txt in its
// scope's upgrade directory: %LOCALAPPDATA%\openabstractions\upgrade-v1 for a
// per-user action, %ProgramData%\abstraction\upgrade-v1 for a machine action.
// The append is best-effort and never changes the exit status.

// Administrators and SYSTEM control; Users read and traverse.
const machineUpgradeSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)"

// upgradeDirectory is the scope's upgrade-v1 directory, beside its exclusion
// record.
func upgradeDirectory(scope string) (string, error) {
	switch scope {
	case "user":
		base, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "openabstractions", "upgrade-v1"), nil
	case "machine":
		base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "abstraction", "upgrade-v1"), nil
	}
	return "", fmt.Errorf("upgrade scope %q is not user or machine", scope)
}

func installerActionsPath(scope string) (string, error) {
	dir, err := upgradeDirectory(scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "installer-actions.txt"), nil
}

// recordInstallerFailure appends "<UTC time> <command>: <error>" for a failed
// installer-invoked command, with the error folded onto one line.
func recordInstallerFailure(path func(scope string) (string, error), scope, command string, err error, now time.Time) {
	appendInstallerAction(path, scope, fmt.Sprintf("%s %s: %s\n",
		now.UTC().Format(time.RFC3339), command, strings.Join(strings.Fields(err.Error()), " ")))
}

// appendInstallerAction creates the machine directory with its protected ACL,
// as the exclusion record does, and ignores every failure.
func appendInstallerAction(path func(scope string) (string, error), scope, line string) {
	file, err := path(scope)
	if err != nil {
		return
	}
	dir := filepath.Dir(file)
	if scope == "machine" {
		err = secureMachineDirectories(dir)
	} else {
		err = os.MkdirAll(dir, 0o700)
	}
	if err != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// secureMachineDirectories creates %ProgramData%\abstraction and its upgrade
// directory with an administrator-controlled ACL, and refuses either one when
// something other than SYSTEM or Administrators owns it.
func secureMachineDirectories(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(machineUpgradeSDDL)
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	for _, d := range []string{filepath.Dir(dir), dir} {
		p, err := windows.UTF16PtrFromString(d)
		if err != nil {
			return err
		}
		if err := windows.CreateDirectory(p, attributes); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return err
		}
		if err := requireAdministrativeOwner(d); err != nil {
			return err
		}
	}
	return nil
}

func pathOwner(path string) (*windows.SID, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err := sd.Owner()
	return owner, err
}

func administrativeSID(sid *windows.SID) bool {
	return sid != nil && (sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
}

func requireAdministrativeOwner(path string) error {
	owner, err := pathOwner(path)
	if err != nil {
		return err
	}
	if !administrativeSID(owner) {
		return fmt.Errorf("%s is owned by %s, not by SYSTEM or Administrators", path, owner)
	}
	return nil
}
