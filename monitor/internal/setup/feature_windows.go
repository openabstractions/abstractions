//go:build windows

package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
)

const oaUpgradeCode = "{CEEF6550-5E44-4E83-8E18-A855C9D941CA}"
const lemonadeFeatureID = "LemonadeImage"
const modelHostFeatureID = "ModelHost"

const (
	msiSuccess     = 0
	msiMoreData    = 234
	msiNoMoreItems = 259
	msiLocal       = 3
	msiAbsent      = 2
	msiUnknown     = -1
)

var msi = windows.NewLazySystemDLL("msi.dll")
var enumRelated = msi.NewProc("MsiEnumRelatedProductsW")
var productInfo = msi.NewProc("MsiGetProductInfoW")
var queryFeature = msi.NewProc("MsiQueryFeatureStateW")
var configureFeature = msi.NewProc("MsiConfigureFeatureW")

func queryLemonadeFeature() (FeatureStatus, string, error) {
	return queryPictureFeature(lemonadeFeatureID, "lemonaded.exe", "Lemonade Image")
}

func queryEngineFeature(engine AppID) (FeatureStatus, string, error) {
	switch engine {
	case Lemonade:
		return queryLemonadeFeature()
	case TheNoise:
		return queryPictureFeature("TheNoiseImage", "thenoise-provider.exe", "TheNoise Image")
	case PyTorch:
		return queryPictureFeature("PyTorchImage", "pytorchd.exe", "PyTorch Image")
	default:
		return FeatureStatus{}, "", errors.New("unknown installed engine feature")
	}
}

func queryPictureFeature(featureID, providerBinary, title string) (FeatureStatus, string, error) {
	if runtime.GOARCH != "amd64" {
		return FeatureStatus{Reason: "The OpenAbstractions " + title + " feature requires Windows x64"}, "", nil
	}
	program, err := os.Executable()
	if err != nil {
		return FeatureStatus{}, "", err
	}
	if !identity.SamePathName(filepath.Base(program), "Abstraction Panel.exe") ||
		!identity.SamePathName(filepath.Base(filepath.Dir(program)), "tools") {
		return FeatureStatus{Reason: "Open the installed Abstraction Panel to add the " + title + " feature"}, "", nil
	}
	root := filepath.Dir(filepath.Dir(program))
	products, err := relatedProducts()
	if err != nil {
		return FeatureStatus{}, "", err
	}
	var selected string
	for _, product := range products {
		location, err := getProductInfo(product, "InstallLocation")
		if err != nil || !sameInstallPath(location, root) {
			continue
		}
		if selected != "" {
			return FeatureStatus{}, "", fmt.Errorf("multiple OA products own Panel install %q", root)
		}
		selected = product
	}
	if selected == "" {
		return FeatureStatus{Reason: "Install the current OpenAbstractions core package before adding " + title}, "", nil
	}
	feature, _ := windows.UTF16PtrFromString(featureID)
	product, _ := windows.UTF16PtrFromString(selected)
	state, _, _ := queryFeature.Call(uintptr(unsafe.Pointer(product)), uintptr(unsafe.Pointer(feature)))
	switch int32(state) {
	case msiAbsent:
		return FeatureStatus{Available: true, Reason: "Add the optional " + title + " feature to this OpenAbstractions installation"}, selected, nil
	case msiLocal:
		binary := filepath.Join(root, "tools", providerBinary)
		info, err := os.Lstat(binary)
		if err != nil || !info.Mode().IsRegular() {
			return FeatureStatus{Available: true, Reason: "Repair the " + title + " feature; its provider program is missing"}, selected, nil
		}
		return FeatureStatus{Available: true, Installed: true}, selected, nil
	case msiUnknown:
		return FeatureStatus{Reason: "Update the OpenAbstractions core package to one with " + title}, selected, nil
	default:
		return FeatureStatus{Available: true, Reason: "The " + title + " feature must be installed locally"}, selected, nil
	}
}

func addLemonadeFeature(ctx context.Context) (FeatureStatus, error) {
	return addEngineFeature(ctx, Lemonade)
}

func addEngineFeature(ctx context.Context, engine AppID) (FeatureStatus, error) {
	featureID := lemonadeFeatureID
	title := "Lemonade Image"
	if engine == TheNoise {
		featureID = "TheNoiseImage"
		title = "TheNoise Image"
	} else if engine == PyTorch {
		featureID = "PyTorchImage"
		title = "PyTorch Image"
	}
	status, product, err := queryEngineFeature(engine)
	if err != nil || status.Installed {
		return status, err
	}
	if !status.Available || product == "" {
		return status, errors.New(status.Reason)
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	productText, _ := windows.UTF16PtrFromString(product)
	featureText, _ := windows.UTF16PtrFromString(featureID)
	code, _, _ := configureFeature.Call(uintptr(unsafe.Pointer(productText)), uintptr(unsafe.Pointer(featureText)), msiLocal)
	// Windows Installer owns the operation once it starts. A caller disconnect or
	// result error is reconciled against the installed feature and program.
	after, _, inspectErr := queryEngineFeature(engine)
	if inspectErr != nil {
		return FeatureStatus{}, inspectErr
	}
	if after.Installed {
		return after, nil
	}
	if code != msiSuccess {
		return after, fmt.Errorf("Windows Installer did not add %s (code %d): %s", title, code, after.Reason)
	}
	return after, fmt.Errorf("Windows Installer returned success but %s is not installed: %s", title, after.Reason)
}

func queryModelHostFeature() (FeatureStatus, string, error) {
	if runtime.GOARCH != "amd64" {
		return FeatureStatus{Reason: "The OpenAbstractions ModelHost feature requires Windows x64"}, "", nil
	}
	program, err := os.Executable()
	if err != nil {
		return FeatureStatus{}, "", err
	}
	if !identity.SamePathName(filepath.Base(program), "Abstraction Panel.exe") ||
		!identity.SamePathName(filepath.Base(filepath.Dir(program)), "tools") {
		return FeatureStatus{Reason: "Open the installed Abstraction Panel to add the ModelHost feature"}, "", nil
	}
	root := filepath.Dir(filepath.Dir(program))
	products, err := relatedProducts()
	if err != nil {
		return FeatureStatus{}, "", err
	}
	var selected string
	for _, product := range products {
		location, err := getProductInfo(product, "InstallLocation")
		if err != nil || !sameInstallPath(location, root) {
			continue
		}
		if selected != "" {
			return FeatureStatus{}, "", fmt.Errorf("multiple OA products own Panel install %q", root)
		}
		selected = product
	}
	if selected == "" {
		return FeatureStatus{Reason: "Install the current OpenAbstractions core package before adding ModelHost"}, "", nil
	}
	feature, _ := windows.UTF16PtrFromString(modelHostFeatureID)
	product, _ := windows.UTF16PtrFromString(selected)
	state, _, _ := queryFeature.Call(uintptr(unsafe.Pointer(product)), uintptr(unsafe.Pointer(feature)))
	switch int32(state) {
	case msiAbsent:
		return FeatureStatus{Available: true, Reason: "Add the optional ModelHost feature to this OpenAbstractions installation"}, selected, nil
	case msiLocal:
		binary := filepath.Join(root, "tools", "modelhostd.exe")
		info, err := os.Lstat(binary)
		if err != nil || !info.Mode().IsRegular() {
			return FeatureStatus{Available: true, Reason: "Repair the ModelHost feature; its provider program is missing"}, selected, nil
		}
		return FeatureStatus{Available: true, Installed: true}, selected, nil
	case msiUnknown:
		return FeatureStatus{Reason: "Update the OpenAbstractions core package to one with ModelHost"}, selected, nil
	default:
		return FeatureStatus{Available: true, Reason: "The ModelHost feature must be installed locally"}, selected, nil
	}
}

func addModelHostFeature(ctx context.Context) (FeatureStatus, error) {
	status, product, err := queryModelHostFeature()
	if err != nil || status.Installed {
		return status, err
	}
	if !status.Available || product == "" {
		return status, errors.New(status.Reason)
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	productText, _ := windows.UTF16PtrFromString(product)
	featureText, _ := windows.UTF16PtrFromString(modelHostFeatureID)
	code, _, _ := configureFeature.Call(uintptr(unsafe.Pointer(productText)), uintptr(unsafe.Pointer(featureText)), msiLocal)
	after, _, inspectErr := queryModelHostFeature()
	if inspectErr != nil {
		return FeatureStatus{}, inspectErr
	}
	if after.Installed {
		return after, nil
	}
	if code != msiSuccess {
		return after, fmt.Errorf("Windows Installer did not add ModelHost (code %d): %s", code, after.Reason)
	}
	return after, fmt.Errorf("Windows Installer returned success but ModelHost is not installed: %s", after.Reason)
}

func relatedProducts() ([]string, error) {
	upgrade, _ := windows.UTF16PtrFromString(oaUpgradeCode)
	var products []string
	for index := uint32(0); ; index++ {
		var code [39]uint16
		result, _, _ := enumRelated.Call(uintptr(unsafe.Pointer(upgrade)), 0, uintptr(index), uintptr(unsafe.Pointer(&code[0])))
		switch result {
		case msiSuccess:
			products = append(products, windows.UTF16ToString(code[:]))
		case msiNoMoreItems:
			return products, nil
		default:
			return nil, fmt.Errorf("Windows Installer product enumeration failed (%d)", result)
		}
	}
}

func getProductInfo(product, property string) (string, error) {
	code, _ := windows.UTF16PtrFromString(product)
	name, _ := windows.UTF16PtrFromString(property)
	buffer := make([]uint16, 1024)
	size := uint32(len(buffer))
	result, _, _ := productInfo.Call(uintptr(unsafe.Pointer(code)), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if result == msiMoreData {
		buffer = make([]uint16, size+1)
		size = uint32(len(buffer))
		result, _, _ = productInfo.Call(uintptr(unsafe.Pointer(code)), uintptr(unsafe.Pointer(name)),
			uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	}
	if result != msiSuccess {
		return "", fmt.Errorf("Windows Installer product information failed (%d)", result)
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func sameInstallPath(first, second string) bool {
	if first == "" || second == "" {
		return false
	}
	same, err := identity.SameFile(first, second)
	return err == nil && same
}
