//go:build windows

package product

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// coreUpgradeCode is the OpenAbstractions core package's upgrade code
// (installer/abstraction.wxs), as bootstrap selects the installed runtime.
const coreUpgradeCode = "{CEEF6550-5E44-4E83-8E18-A855C9D941CA}"

// maxCoreProducts bounds the related products read. An account may register
// any number of per-user products under the upgrade code.
const maxCoreProducts = 64

// Windows Installer contexts: managed user, unmanaged user, machine.
var installContexts = [...]uint32{1, 2, 4}

var (
	msiDLL          = windows.NewLazySystemDLL("msi.dll")
	msiEnumRelated  = msiDLL.NewProc("MsiEnumRelatedProductsW")
	msiProductInfoX = msiDLL.NewProc("MsiGetProductInfoExW")
)

const (
	errorUnknownProduct  = syscall.Errno(1605)
	errorUnknownProperty = syscall.Errno(1608)
)

// RegisteredToolFolders are the tools folders of the OpenAbstractions core
// installations Windows Installer records for this account and this machine:
// each registered product's InstallLocation, which APPLICATIONFOLDER may put
// anywhere (installer/README.md). The environment moves none of them. A
// per-user record is the account's own to edit, so these folders only add to
// what a guard covers and never give a program product standing.
func RegisteredToolFolders() ([]string, error) {
	// Windows Installer wants successive related-product queries on one
	// thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, proc := range []*windows.LazyProc{msiEnumRelated, msiProductInfoX} {
		if err := proc.Find(); err != nil {
			return nil, err
		}
	}
	return recordedToolFolders(installRecords)
}

// installQueries are the Windows Installer reads RegisteredToolFolders makes:
// the related product at an index of an upgrade code, and a product's
// property in one install context.
type installQueries struct {
	related  func(upgrade string, index uint32) (string, error)
	property func(product string, context uint32, property string) (string, error)
}

// installRecords are this machine's Windows Installer records. Tests replace
// them.
var installRecords = installQueries{relatedProduct, registeredProperty}

func recordedToolFolders(q installQueries) ([]string, error) {
	var folders []string
	for index := uint32(0); ; index++ {
		if index >= maxCoreProducts {
			return folders, errors.New("more registered OpenAbstractions products than are read")
		}
		product, err := q.related(coreUpgradeCode, index)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			return folders, nil
		}
		if err != nil {
			return folders, fmt.Errorf("enumerate registered OpenAbstractions products: %w", err)
		}
		for _, context := range installContexts {
			location, err := q.property(product, context, "InstallLocation")
			if errors.Is(err, errorUnknownProduct) || errors.Is(err, errorUnknownProperty) {
				continue
			}
			if err != nil {
				return folders, fmt.Errorf("read the registered OpenAbstractions install location: %w", err)
			}
			if location != "" && filepath.IsAbs(location) {
				folders = append(folders, filepath.Join(filepath.Clean(location), "tools"))
			}
		}
	}
}

// relatedProduct is MsiEnumRelatedProductsW: the product code at index among
// upgrade's related products.
func relatedProduct(upgrade string, index uint32) (string, error) {
	code, err := windows.UTF16PtrFromString(upgrade)
	if err != nil {
		return "", err
	}
	var product [39]uint16
	//unchecked: MsiEnumRelatedProductsW returns its error status in result; GetLastError is not this API's status
	result, _, _ := msiEnumRelated.Call(uintptr(unsafe.Pointer(code)), 0, uintptr(index), uintptr(unsafe.Pointer(&product[0])))
	if result != 0 {
		return "", syscall.Errno(result)
	}
	return windows.UTF16ToString(product[:]), nil
}

// registeredProperty is MsiGetProductInfoExW for this account's product in
// one context.
func registeredProperty(product string, context uint32, property string) (string, error) {
	code, err := windows.UTF16PtrFromString(product)
	if err != nil {
		return "", err
	}
	name, err := windows.UTF16PtrFromString(property)
	if err != nil {
		return "", err
	}
	value := make([]uint16, 32768)
	size := uint32(len(value))
	//unchecked: MsiGetProductInfoExW returns its error status in result; GetLastError is not this API's status
	result, _, _ := msiProductInfoX.Call(uintptr(unsafe.Pointer(code)), 0, uintptr(context), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&value[0])), uintptr(unsafe.Pointer(&size)))
	if result != 0 {
		return "", syscall.Errno(result)
	}
	if size >= uint32(len(value)) {
		return "", errors.New("the registered property exceeds its bound")
	}
	return windows.UTF16ToString(value[:size]), nil
}
