package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runtimeLogonKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runtimeLogonName = "OpenAbstractions runtime"
const presenterLogonName = "OpenAbstractions presenter"

// presenterImage is the Asks presenter installed beside the runtime: the one
// program that draws native questions and notifications. Before it, the
// presenter entry started the Panel's background mode (legacyPresenterImage).
const (
	presenterImage       = "OpenAbstractions Presenter.exe"
	legacyPresenterImage = "Abstraction Panel.exe"
	presenterArguments   = "--background"
)

type logonValue struct {
	kind uint32
	data []byte
}

func (v logonValue) equal(other logonValue) bool {
	return v.kind == other.kind && bytes.Equal(v.data, other.data)
}

type windowsLogonOps struct {
	selection  func(context.Context) (bootstrap.Selection, error)
	account    func() (string, error)
	guard      func() error
	executable func() (string, error)
	plain      func(string) error
	stat       func(string) (os.FileInfo, error)
	read       func(string) (logonValue, bool, error)
	write      func(string, logonValue) error
	remove     func(string) error
}

func reconcileRuntimeLogon(ctx context.Context, enabled bool) error {
	return reconcileWindowsLogon(ctx, enabled, installedWindowsLogonOps())
}

// installedWindowsLogonOps binds this account's installed runtime and its
// HKCU Run values.
func installedWindowsLogonOps() windowsLogonOps {
	return windowsLogonOps{
		selection: bootstrap.SelectInstalled,
		account: func() (string, error) {
			u, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				return "", err
			}
			return u.User.Sid.String(), nil
		},
		guard: func() error {
			if err := startGuard(); err != nil {
				return err
			}
			return refuseVirtualizedProfile("runtime login", bootstrap.CurrentProfileView)
		},
		executable: os.Executable, plain: programfiles.CheckPlainPath, stat: os.Lstat,
		read: readRuntimeLogonValue, write: writeRuntimeLogonValue, remove: removeRuntimeLogonValue,
	}
}

// readRuntimeLogon reads this account's two owned Run entries with the
// operations reconcileRuntimeLogon uses.
func readRuntimeLogon(ctx context.Context) (runtime, presenter bool, err error) {
	return readWindowsLogon(ctx, installedWindowsLogonOps())
}

func runtimeLogonCommand(program, arguments string) (logonValue, error) {
	command := windows.EscapeArg(program) + " " + arguments
	// Run values have a documented 260-character command limit, including NUL.
	units, err := syscall.UTF16FromString(command)
	if err != nil || len(units) > 260 {
		return logonValue{}, errors.New("runtime login command exceeds the Windows Run limit")
	}
	raw := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], unit)
	}
	return logonValue{registry.SZ, raw}, nil
}

// windowsLogonEntries validates this account's installation and returns the
// two owned Run entries' names and exact values. presenter is false when the
// installation has no presenter. It reads and writes no Run value.
func windowsLogonEntries(ctx context.Context, ops windowsLogonOps) (names []string, values []logonValue, presenter bool, err error) {
	names, values, _, presenter, err = windowsLogonEntriesWithLegacy(ctx, ops)
	return names, values, presenter, err
}

// windowsLogonEntriesWithLegacy also returns, per entry, the value an earlier
// version wrote that this installation replaces: the presenter entry once
// started the Panel's background mode beside this runtime.
func windowsLogonEntriesWithLegacy(ctx context.Context, ops windowsLogonOps) (names []string, values []logonValue, legacy []*logonValue, presenter bool, err error) {
	names, values, presenter, dir, err := windowsLogonCurrentEntries(ctx, ops)
	if err != nil {
		return nil, nil, nil, false, err
	}
	previous, err := runtimeLogonCommand(filepath.Join(dir, legacyPresenterImage), presenterArguments)
	if err != nil {
		return nil, nil, nil, false, err
	}
	return names, values, []*logonValue{nil, &previous}, presenter, nil
}

func windowsLogonCurrentEntries(ctx context.Context, ops windowsLogonOps) (names []string, values []logonValue, presenter bool, dir string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, false, "", err
	}
	if err := ops.guard(); err != nil {
		return nil, nil, false, "", err
	}
	selected, err := ops.selection(ctx)
	if err != nil {
		return nil, nil, false, "", err
	}
	account, err := ops.account()
	if err != nil {
		return nil, nil, false, "", err
	}
	if selected.EndpointFromEnvironment || selected.Server.Principal.Kind != "windows" || account == "" || selected.Server.Principal.SID != account {
		return nil, nil, false, "", errors.New("runtime login refuses endpoint override or another account's installation")
	}
	self, err := ops.executable()
	if err != nil {
		return nil, nil, false, "", err
	}
	server := filepath.Join(filepath.Dir(self), "openabstractions.exe")
	if !samePrograms(selected.Server.Program, server) {
		return nil, nil, false, "", errors.New("runtime login executable differs from selected installation")
	}
	// Selection retains the MSI InstallLocation spelling used by uninstall.
	// Native file identity admits this process's alias, not its sibling folder.
	dir = filepath.Dir(selected.Server.Program)
	programs := []string{filepath.Join(dir, windowlessName), filepath.Join(dir, presenterImage)}
	arguments := []string{"serve supervisor", presenterArguments}
	names = []string{runtimeLogonName, presenterLogonName}
	values = make([]logonValue, 2)
	presenter = true
	for i, program := range programs {
		if !filepath.IsAbs(program) || identity.LocalPath(program) != nil {
			return nil, nil, false, "", errors.New("runtime login requires an absolute local program")
		}
		info, statErr := ops.stat(program)
		if i == 1 && errors.Is(statErr, os.ErrNotExist) {
			presenter = false
		} else {
			if statErr != nil {
				return nil, nil, false, "", statErr
			}
			if !info.Mode().IsRegular() {
				return nil, nil, false, "", errors.New("runtime login program is not a plain file")
			}
			if err := ops.plain(program); err != nil {
				return nil, nil, false, "", err
			}
		}
		values[i], err = runtimeLogonCommand(program, arguments[i])
		if err != nil {
			return nil, nil, false, "", err
		}
	}
	return names, values, presenter, dir, nil
}

// readWindowsLogon reports whether each owned Run entry holds exactly this
// installation's value. It changes nothing.
func readWindowsLogon(ctx context.Context, ops windowsLogonOps) (runtime, presenter bool, err error) {
	names, values, _, err := windowsLogonEntries(ctx, ops)
	if err != nil {
		return false, false, err
	}
	in := make([]bool, 2)
	for i, name := range names {
		current, present, err := ops.read(name)
		if err != nil {
			return false, false, err
		}
		in[i] = present && current.equal(values[i])
	}
	return in[0], in[1], nil
}

// reconcileWindowsLogon writes or removes both owned Run entries. A presenter
// entry an earlier version wrote for the Panel's background mode beside this
// runtime is this installation's own: it is replaced by the presenter's
// value, or removed, as the presenter's own would be.
func reconcileWindowsLogon(ctx context.Context, enabled bool, ops windowsLogonOps) error {
	names, values, legacy, panel, err := windowsLogonEntriesWithLegacy(ctx, ops)
	if err != nil {
		return err
	}
	// Validate both existing values before any mutation.
	previous := make([]logonValue, 2)
	exists := make([]bool, 2)
	wanted := []bool{enabled, enabled && panel}
	for i := range names {
		previous[i], exists[i], err = ops.read(names[i])
		if err != nil {
			return err
		}
		if exists[i] && !previous[i].equal(values[i]) && (legacy[i] == nil || !previous[i].equal(*legacy[i])) {
			return fmt.Errorf("runtime login refuses foreign value %q", names[i])
		}
	}
	for i, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, present, err := ops.read(name)
		if err != nil {
			return err
		}
		if present != exists[i] || (present && !current.equal(previous[i])) {
			return fmt.Errorf("runtime login value %q changed before update", name)
		}
		if wanted[i] && (!present || !current.equal(values[i])) {
			err = ops.write(name, values[i])
		} else if !wanted[i] && present {
			err = ops.remove(name)
		}
		if err != nil {
			return fmt.Errorf("runtime login update %q: %w", name, err)
		}
	}
	return nil
}

func readRuntimeLogonValue(name string) (logonValue, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runtimeLogonKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return logonValue{}, false, nil
	}
	if err != nil {
		return logonValue{}, false, err
	}
	defer k.Close()
	n, kind, err := k.GetValue(name, nil)
	if errors.Is(err, registry.ErrNotExist) {
		return logonValue{}, false, nil
	}
	if err != nil {
		return logonValue{}, false, err
	}
	if n > 4096 {
		return logonValue{}, false, errors.New("runtime login value is oversized")
	}
	raw := make([]byte, n)
	n, kind, err = k.GetValue(name, raw)
	if err != nil {
		return logonValue{}, false, err
	}
	return logonValue{kind, raw[:n]}, true, nil
}

func writeRuntimeLogonValue(name string, value logonValue) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runtimeLogonKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	units := make([]uint16, len(value.data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(value.data[i*2:])
	}
	return k.SetStringValue(name, string(utf16.Decode(units[:len(units)-1])))
}

func removeRuntimeLogonValue(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runtimeLogonKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.DeleteValue(name)
}

// The installer commit runs from its Binary table after installed files may
// have gone. Its prior ToolsFolder is data, not a program to execute or probe.
func removeInstalledRuntimeLogon(ctx context.Context, folder string) error {
	if err := startGuard(); err != nil {
		return err
	}
	if err := refuseVirtualizedProfile("remove login", bootstrap.CurrentProfileView); err != nil {
		return err
	}
	return removeWindowsLogon(ctx, folder, readRuntimeLogonValue, removeRuntimeLogonValue)
}

func removeWindowsLogon(ctx context.Context, folder string, read func(string) (logonValue, bool, error), remove func(string) error) error {
	// MSI's quoted-directory idiom ends in a dot so a final slash cannot escape
	// the quote. Accept exactly that suffix plus ordinary canonical drive paths.
	folder = strings.TrimSuffix(folder, `\.`)
	volume := filepath.VolumeName(folder)
	if !filepath.IsAbs(folder) || len(volume) != 2 || volume[1] != ':' || !((volume[0] >= 'A' && volume[0] <= 'Z') || (volume[0] >= 'a' && volume[0] <= 'z')) || filepath.Clean(folder) != folder || strings.Contains(folder, "/") || identity.LocalPath(folder) != nil {
		return errors.New("remove login requires the prior canonical local-drive ToolsFolder")
	}
	var failures []error
	for i, name := range []string{runtimeLogonName, presenterLogonName} {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		program, arguments := filepath.Join(folder, windowlessName), "serve supervisor"
		if i == 1 {
			program, arguments = filepath.Join(folder, presenterImage), presenterArguments
		}
		expected, err := runtimeLogonCommand(program, arguments)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		// A presenter entry not reconciled since its upgrade still starts
		// the Panel's background mode beside this runtime.
		previous := expected
		if i == 1 {
			if previous, err = runtimeLogonCommand(filepath.Join(folder, legacyPresenterImage), presenterArguments); err != nil {
				return errors.Join(append(failures, err)...)
			}
		}
		value, present, err := read(name)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !present {
			continue
		}
		if !value.equal(expected) && !value.equal(previous) {
			failures = append(failures, fmt.Errorf("remove login retained foreign value %q", name))
			continue
		}
		current, present, err := read(name)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !present || !current.equal(value) {
			failures = append(failures, fmt.Errorf("remove login retained changed value %q", name))
			continue
		}
		if err := remove(name); err != nil {
			failures = append(failures, fmt.Errorf("remove login %q: %w", name, err))
		}
	}
	return errors.Join(failures...)
}
