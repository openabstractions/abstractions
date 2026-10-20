//go:build windows

package setup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"golang.org/x/sys/windows"
)

var installProduct = msi.NewProc("MsiInstallProductW")
var configureProduct = msi.NewProc("MsiConfigureProductW")

const (
	installLevelDefault = 0
	installStateAbsent  = 2
)

var enableLog = msi.NewProc("MsiEnableLogW")

// MsiEnableLog modes and attributes: errors, warnings, user messages and a
// fatal exit, one line flushed at a time, the file replaced.
const (
	installLogModes           = 0x1 | 0x2 | 0x4 | 0x8
	installLogFlushEach       = 0x2
	extensionInstallLogLimit  = 256 << 10
	extensionInstallLogLength = 300
)

var errFreshExtensionProductPresent = errors.New("Windows Installer already has a related integration. Resolve the existing installation before connecting.")

func checkFreshExtensionPackage(ctx context.Context, upgradeCode string) error {
	return withExtensionPackageOperation(ctx, upgradeCode, extensionFreshPackage, relatedProducts, func([]string) error { return nil })
}

// withExtensionPackageOperation owns enumeration and dispatch in one native
// installer settings scope. A fresh operation cannot configure a related
// product or create the connection config.
func withExtensionPackageOperation(ctx context.Context, upgradeCode string, mode extensionPackageMode, enumerate func(string) ([]string, error), operation func([]string) error) error {
	installerSettingsMu.Lock()
	defer installerSettingsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	products, err := enumerate(upgradeCode)
	if err != nil {
		return err
	}
	if mode == extensionFreshPackage && len(products) != 0 {
		return errFreshExtensionProductPresent
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation(products)
}

func connectExtensionPackageWithEffect(ctx context.Context, msiPath, base string, spec product.PackageSpec, mode extensionPackageMode, markEffect func() error) error {
	return connectExtensionPackageWithOperations(ctx, msiPath, base, spec, mode, markEffect, relatedProducts, dispatchExtensionPackage)
}

func connectExtensionPackageWithOperations(ctx context.Context, msiPath, base string, spec product.PackageSpec, mode extensionPackageMode, markEffect func() error,
	enumerate func(string) ([]string, error), dispatch func(context.Context, []string, string, string, string, product.PackageSpec, extensionPackageMode, func() error) error) error {
	return withExtensionPackageOperation(ctx, spec.UpgradeCode, mode, enumerate, func(products []string) error {
		if mode == extensionFreshPackage {
			if err := verifyFreshExtensionMSI(msiPath, spec.UpgradeCode); err != nil {
				return err
			}
		}
		config, err := extensionHostConfig(ctx, spec, base)
		if err != nil {
			return err
		}
		return dispatch(ctx, products, msiPath, config, filepath.Dir(packageConnector(spec, base)), spec, mode, markEffect)
	})
}

// dispatchExtensionPackage installs the extension silently. Replacement removes
// related products and their empty folders before calling the MSI.
func dispatchExtensionPackage(ctx context.Context, products []string, msiPath, config, folder string, spec product.PackageSpec, mode extensionPackageMode, markEffect func() error) error {
	previous, _, _ := setInternalUI.Call(installUILevelNone, 0)
	defer setInternalUI.Call(previous, 0)
	started := false
	for _, product := range products {
		if err := ctx.Err(); err != nil {
			return err
		}
		code, _ := windows.UTF16PtrFromString(product)
		if !started {
			if err := markEffect(); err != nil {
				return err
			}
			started = true
		}
		result, _, _ := configureProduct.Call(uintptr(unsafe.Pointer(code)), installLevelDefault, installStateAbsent)
		if err := msiResult(result, fmt.Sprintf("remove the %s integration from the other installation", spec.Title)); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.ContainsRune(config, '"') {
		return fmt.Errorf("the %s folder's settings path %s holds a quote", spec.Title, config)
	}
	if !started {
		if err := markEffect(); err != nil {
			return err
		}
	}
	if err := removeEmptyFolderTree(folder); err != nil {
		log.Printf("setup: %s integration folder: %v", spec.Title, err)
	}
	logFile := filepath.Join(filepath.Dir(config), spec.InstallFolder+"-install.log")
	logName, _ := windows.UTF16PtrFromString(logFile)
	if result, _, _ := enableLog.Call(installLogModes, uintptr(unsafe.Pointer(logName)), installLogFlushEach); result != msiSuccess {
		logFile = ""
	}
	path, _ := windows.UTF16PtrFromString(msiPath)
	command, _ := windows.UTF16PtrFromString(extensionInstallProperties(config, mode))
	result, _, _ := installProduct.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(command)))
	enableLog.Call(0, 0, 0)
	err := msiResult(result, "install the "+spec.Title+" integration")
	if err != nil && logFile != "" {
		if text := msiFailureText(logFile); text != "" {
			err = fmt.Errorf("%w: %s", err, text)
		}
	}
	return err
}

func extensionInstallProperties(config string, mode extensionPackageMode) string {
	properties := `OAHOSTCONFIG="` + config + `"`
	if mode == extensionFreshPackage {
		properties += " OAFRESHONLY=1"
	}
	return properties
}

// msiFailureText is the last message line of an MSI log written with
// installLogModes: the installer's own words for why it stopped.
func msiFailureText(logFile string) string {
	return lastMSIMessage(readMSIText(logFile), extensionInstallLogLength)
}

func readMSIText(logFile string) string {
	raw, err := readBounded(logFile, extensionInstallLogLimit)
	if err != nil {
		return ""
	}
	if len(raw) >= 2 && raw[0] == 0xff && raw[1] == 0xfe {
		return decodeMSIText(raw[2:], true)
	}
	return string(raw)
}

func decodeMSIText(raw []byte, unicode bool) string {
	if unicode {
		units := make([]uint16, 0, len(raw)/2)
		for i := 0; i+1 < len(raw); i += 2 {
			units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
		}
		return windows.UTF16ToString(units)
	}
	return string(raw)
}

func msiResult(code uintptr, doing string) error {
	switch code {
	case msiSuccess, msiSuccessRebootRequired:
		return nil
	case msiInstallAlreadyRunning:
		return fmt.Errorf("%w (Windows Installer code %d)", ErrFixupInstallerBusy, code)
	default:
		return fmt.Errorf("Windows Installer could not %s (Windows Installer code %d)", doing, code)
	}
}

// runExtensionRegistration runs connector register-self with no window
// and reads its answer.
func runExtensionRegistration(ctx context.Context, connector string) (outcome, reason string, err error) {
	if !isRegularFile(connector) {
		return "", "", fmt.Errorf("%s is missing", connector)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := childproc.Hidden(exec.CommandContext(ctx, connector, "register-self"))
	cmd.Dir = filepath.Dir(connector)
	output, runErr := cmd.Output()
	outcome, reason = extensionRegistrationAnswer(output)
	if outcome == "" && runErr != nil {
		return "", "", runErr
	}
	return outcome, reason, nil
}

// startDesktopProgram starts a discovered program as the person would from the Start menu:
// a program directly, a portable folder's start file through cmd.exe in its
// own console window.
func startDesktopProgram(program string) error {
	var cmd *exec.Cmd
	if strings.EqualFold(filepath.Ext(program), ".bat") {
		cmd = exec.Command("cmd.exe", "/c", filepath.Base(program))
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
	} else {
		cmd = exec.Command(program)
	}
	cmd.Dir = filepath.Dir(program)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
