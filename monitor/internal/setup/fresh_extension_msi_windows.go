//go:build windows

package setup

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	msiOpenDatabase = msi.NewProc("MsiOpenDatabaseW")
	msiOpenView     = msi.NewProc("MsiDatabaseOpenViewW")
	msiExecuteView  = msi.NewProc("MsiViewExecute")
	msiFetchView    = msi.NewProc("MsiViewFetch")
	msiGetInteger   = msi.NewProc("MsiRecordGetInteger")
	msiGetString    = msi.NewProc("MsiRecordGetStringW")
	msiRecordIsNull = msi.NewProc("MsiRecordIsNull")
	msiCloseHandle  = msi.NewProc("MsiCloseHandle")
	errNoFreshMSI   = errors.New("the integration MSI has no verified fresh-install guard")
)

const (
	freshGuardAction    = "RefuseFreshExtensionReplacement"
	freshGuardCondition = "OAFRESHONLY AND (Installed OR OAFRESHRELATED)"
	// OnlyDetect (0x2) and VersionMinInclusive (0x100).
	freshDetectionAttributes = 0x102
)

// verifyFreshExtensionMSI reads the exact held MSI without opening an install
// session. The release pin predates this guard for some supported packages.
// Only a proved guard allows the fresh-only command; legacy replacement
// and regular MSI invocation keep their prior behavior.
func verifyFreshExtensionMSI(path, upgradeCode string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return errNoFreshMSI
	}
	var db uint32
	result, _, _ := msiOpenDatabase.Call(uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&db)))
	if result != msiSuccess {
		return fmt.Errorf("%w (Windows Installer code %d)", errNoFreshMSI, result)
	}
	defer msiCloseHandle.Call(uintptr(db))

	guard := "`Action` = '" + freshGuardAction + "'"
	if err := readFreshMSIRow(db, "SELECT `Type` FROM `CustomAction` WHERE "+guard, func(row uintptr) bool {
		return msiInteger(row, 1) == 19
	}); err != nil {
		return err
	}
	if err := readFreshMSIRow(db, "SELECT `Value` FROM `Property` WHERE `Property` = 'SecureCustomProperties'", func(row uintptr) bool {
		secure := strings.Split(msiString(row, 1), ";")
		return slices.Contains(secure, "OAFRESHONLY") && slices.Contains(secure, "OAFRESHRELATED")
	}); err != nil {
		return err
	}
	if err := readFreshMSIRow(db, "SELECT `UpgradeCode`, `VersionMin`, `VersionMax`, `Language`, `Attributes`, `Remove` FROM `Upgrade` WHERE `ActionProperty` = 'OAFRESHRELATED'", func(row uintptr) bool {
		return strings.EqualFold(msiString(row, 1), upgradeCode) && msiString(row, 2) == "0.0.0" &&
			msiNull(row, 3) && msiNull(row, 4) &&
			msiInteger(row, 5) == freshDetectionAttributes && msiNull(row, 6)
	}); err != nil {
		return err
	}
	sequence := func(action, condition string) (int32, error) {
		var index int32
		err := readFreshMSIRow(db, "SELECT `Sequence`, `Condition` FROM `InstallExecuteSequence` WHERE `Action` = '"+action+"'", func(row uintptr) bool {
			index = msiInteger(row, 1)
			if action == "FindRelatedProducts" {
				return index > 0 && msiNull(row, 2)
			}
			return index > 0 && (condition == "" || msiString(row, 2) == condition)
		})
		return index, err
	}
	find, err := sequence("FindRelatedProducts", "")
	if err != nil {
		return err
	}
	refuse, err := sequence(freshGuardAction, freshGuardCondition)
	if err != nil {
		return err
	}
	remove, err := sequence("RemoveExistingProducts", "")
	if err != nil {
		return err
	}
	if find >= refuse || refuse >= remove {
		return errNoFreshMSI
	}
	return nil
}

func readFreshMSIRow(db uint32, query string, accept func(uintptr) bool) error {
	sql, _ := windows.UTF16PtrFromString(query)
	var view, row uint32
	if result, _, _ := msiOpenView.Call(uintptr(db), uintptr(unsafe.Pointer(sql)), uintptr(unsafe.Pointer(&view))); result != msiSuccess {
		return fmt.Errorf("%w (Windows Installer code %d)", errNoFreshMSI, result)
	}
	defer msiCloseHandle.Call(uintptr(view))
	if result, _, _ := msiExecuteView.Call(uintptr(view), 0); result != msiSuccess {
		return fmt.Errorf("%w (Windows Installer code %d)", errNoFreshMSI, result)
	}
	if result, _, _ := msiFetchView.Call(uintptr(view), uintptr(unsafe.Pointer(&row))); result != msiSuccess {
		return fmt.Errorf("%w (Windows Installer code %d)", errNoFreshMSI, result)
	}
	defer msiCloseHandle.Call(uintptr(row))
	if !accept(uintptr(row)) {
		return errNoFreshMSI
	}
	return nil
}

func msiInteger(row uintptr, field uint32) int32 {
	result, _, _ := msiGetInteger.Call(row, uintptr(field))
	return int32(result)
}

func msiNull(row uintptr, field uint32) bool {
	result, _, _ := msiRecordIsNull.Call(row, uintptr(field))
	return result != 0
}

func msiString(row uintptr, field uint32) string {
	var value [512]uint16
	size := uint32(len(value))
	result, _, _ := msiGetString.Call(row, uintptr(field), uintptr(unsafe.Pointer(&value[0])), uintptr(unsafe.Pointer(&size)))
	if result != msiSuccess {
		return ""
	}
	return windows.UTF16ToString(value[:])
}
