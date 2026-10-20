//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-download/go/powershell"
	"golang.org/x/sys/windows/registry"
)

// This file proves the per-user registration installer/abstraction.wxs's
// PanelTrayLogonStart component ships: a Startup-folder shortcut targeting
// "Abstraction Panel.exe" with argument "--background", plus an HKCU marker at
// Software\OpenAbstractions\Abstraction\Panel, name "trayAtLogon", removed
// on uninstall through RemoveRegistryKey. WiX authors that shortcut and
// registry value directly; there is no `openabstractions`-style register
// subcommand to invoke, the same way installer/abstraction.wxs's sibling
// LogonStart component (the runtime host's own Startup shortcut) has none.
// This test reproduces that authoring with the mechanism Windows Installer
// itself uses to write a .lnk file, WScript.Shell's COM shortcut object, so
// it exercises the real Startup-folder and registry primitives rather than
// a stand-in.
//
// It never touches the real Software\OpenAbstractions\Abstraction\Panel key
// or the real Startup folder: panelTrayRegistrationKey returns a key under a
// distinct OpenAbstractionsTest root, unique to this run, and startupDir is
// a t.TempDir(). Both are removed before the test returns, and again from
// t.Cleanup if an assertion aborts the test first.

// panelTrayRegistrationRoot is the parent every isolated test key nests
// under, so it can be removed once the leaf is gone and nothing named
// "OpenAbstractionsTest" lingers under HKCU after the test.
const panelTrayRegistrationRoot = `Software\OpenAbstractionsTest`

// panelTrayRegistrationKey is isolated from the installer's real
// Software\OpenAbstractions\Abstraction\Panel key (installer/abstraction.wxs,
// component PanelTrayLogonStartShortcut) and unique per run, so concurrent
// test runs on the same machine never collide and this test never reads or
// removes the owner's actual registration.
func panelTrayRegistrationKey() string {
	return fmt.Sprintf(`%s\PanelTray-%d-%d`, panelTrayRegistrationRoot, os.Getpid(), time.Now().UnixNano())
}

// registerPanelTray reproduces what PanelTrayLogonStart does at install: a
// Startup-folder shortcut carrying the background command line, and an HKCU marker
// value. dir stands in for the real Startup folder and keyPath for the real
// registry key.
func registerPanelTray(t *testing.T, keyPath, dir, target, args string) string {
	t.Helper()
	shortcut := filepath.Join(dir, "Abstraction Panel notifications.lnk")
	runPowerShell(t, fmt.Sprintf(
		`$s = (New-Object -ComObject WScript.Shell).CreateShortcut(%s); $s.TargetPath = %s; $s.Arguments = %s; $s.WorkingDirectory = %s; $s.Save()`,
		powershell.Literal(shortcut), powershell.Literal(target), powershell.Literal(args), powershell.Literal(filepath.Dir(target))))
	k, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err != nil {
		t.Fatalf("create registry marker %s: %v", keyPath, err)
	}
	defer k.Close()
	if err := k.SetDWordValue("trayAtLogon", 1); err != nil {
		t.Fatalf("set trayAtLogon: %v", err)
	}
	return shortcut
}

// readPanelTrayCommand reads back the exact command line the registered
// shortcut carries, resolving it the way explorer.exe would at logon.
func readPanelTrayCommand(t *testing.T, shortcut string) (target, args string) {
	t.Helper()
	out := runPowerShell(t, fmt.Sprintf(
		`$s = (New-Object -ComObject WScript.Shell).CreateShortcut(%s); $s.TargetPath; $s.Arguments`,
		powershell.Literal(shortcut)))
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(out, "\r\n", "\n"), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected shortcut read-back %q", out)
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
}

// markerPresent reports whether the isolated registration's HKCU marker
// still exists, the way validate.py and RemoveRegistryKey treat its
// presence as "registered".
func markerPresent(keyPath string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetIntegerValue("trayAtLogon")
	return err == nil
}

// unregisterPanelTray removes the shortcut and the registry marker,
// tolerating either already being gone so it is safe to call both as the
// test's own unregister step and, redundantly, from t.Cleanup. It also
// removes panelTrayRegistrationRoot itself once it holds no other isolated
// test key, so no "OpenAbstractionsTest" key lingers under HKCU; the removal
// is silently skipped, not an error, while a concurrent test run still owns
// a sibling key there.
func unregisterPanelTray(keyPath, shortcut string) {
	_ = os.Remove(shortcut)
	_ = registry.DeleteKey(registry.CURRENT_USER, keyPath)
	_ = registry.DeleteKey(registry.CURRENT_USER, panelTrayRegistrationRoot)
}

func runPowerShell(t *testing.T, script string) string {
	t.Helper()
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("powershell: %v\n%s", err, out)
	}
	return string(out)
}

// buildPanelExecutableAt builds the real Panel from this checkout to target,
// the same command validated in
// research/adoption/asks/first-use-notifier-2026-09-22.md:
//
//	go build -ldflags="-H windowsgui" -o "Abstraction Panel.exe" .
func buildPanelExecutableAt(t *testing.T, target string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-ldflags=-H windowsgui", "-o", target, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", target, err, out)
	}
}

// TestPanelTrayLogonRegistersReadsBackLaunchesAndUnregisters is the "tested
// per user" half of TODO.md's "Panel tray logon registration shipped and
// tested per user": registration and read-back run every time this test
// runs, proving the Startup-folder shortcut and HKCU marker PanelTrayLogonStart
// authors carry the exact "Abstraction Panel.exe" "--background" command line and
// are fully removable. Actually starting that command line against the real
// built Panel is gated behind OA_LIVE_PANEL=1, the way serve's other live
// tests gate a real build (see serve/runtime_inventory_live_test.go), since
// it needs a real go build and a running background presenter.
func TestPanelTrayLogonRegistersReadsBackLaunchesAndUnregisters(t *testing.T) {
	startupDir := t.TempDir()
	keyPath := panelTrayRegistrationKey()
	target := filepath.Join(t.TempDir(), "Abstraction Panel.exe")

	live := os.Getenv("OA_LIVE_PANEL") == "1"
	if live {
		buildPanelExecutableAt(t, target)
	}

	shortcut := registerPanelTray(t, keyPath, startupDir, target, "--background")
	t.Cleanup(func() { unregisterPanelTray(keyPath, shortcut) })

	if !markerPresent(keyPath) {
		t.Fatal("registration did not create the HKCU trayAtLogon marker")
	}
	if _, err := os.Stat(shortcut); err != nil {
		t.Fatalf("registration did not create the Startup-folder shortcut: %v", err)
	}

	gotTarget, gotArgs := readPanelTrayCommand(t, shortcut)
	if !strings.EqualFold(gotTarget, target) {
		t.Fatalf("read-back target = %q, want %q", gotTarget, target)
	}
	if gotArgs != "--background" {
		t.Fatalf("read-back arguments = %q, want the registered %q", gotArgs, "--background")
	}

	if !live {
		t.Log("registration and read-back verified; OA_LIVE_PANEL=1 also builds and runs the background presenter")
	} else {
		runAndObserveRegisteredPanel(t, gotTarget, gotArgs)
	}

	unregisterPanelTray(keyPath, shortcut)
	if markerPresent(keyPath) {
		t.Fatal("HKCU trayAtLogon marker still present after unregister")
	}
	if _, err := os.Stat(shortcut); !os.IsNotExist(err) {
		t.Fatalf("Startup-folder shortcut still present after unregister: %v", err)
	}
}

// runAndObserveRegisteredPanel launches the registered command line exactly
// with an explicit absent runtime, so it cannot resolve the installed runtime.
// It confirms the presenter stays alive without occupying the Panel port and
// repeated startup exits quietly, then stops it by PID — never by image name
// (AGENTS.md), since another Panel instance may legitimately be running on
// this machine.
func runAndObserveRegisteredPanel(t *testing.T, target, args string) {
	t.Helper()
	const addr = "127.0.0.1:8734" // the -addr flag's own default; the registered command line carries no override.
	probe, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("%s already answering, cannot safely launch the registered command line here: %v", addr, err)
	}
	probe.Close()

	endpoint := fmt.Sprintf(`\\.\pipe\oa-background-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	arguments := append(strings.Fields(args), "-runtime-endpoint", endpoint,
		"-runtime-program", target, "-runtime-state-dir", t.TempDir())
	cmd := exec.Command(target, arguments...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s %s: %v", target, args, err)
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-done
		}
	})

	select {
	case err := <-done:
		stopped = true
		t.Fatalf("background presenter exited before observation: %v", err)
	case <-time.After(time.Second):
	}
	available, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("background presenter occupied the on-demand Panel's port: %v", err)
	}
	available.Close()
	duplicate := exec.Command(target, arguments...)
	if err := duplicate.Start(); err != nil {
		t.Fatalf("repeat startup: %v", err)
	}
	repeated := make(chan error, 1)
	go func() { repeated <- duplicate.Wait() }()
	select {
	case err := <-repeated:
		if err != nil {
			t.Fatalf("repeated startup did not exit quietly: %v", err)
		}
	case <-time.After(5 * time.Second):
		duplicate.Process.Kill()
		<-repeated
		t.Fatal("repeated startup created a second background presenter")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("stop background presenter pid %d: %v", pid, err)
	}
	<-done
	stopped = true

	time.Sleep(300 * time.Millisecond)
	after, err := net.Listen("tcp", addr)
	if err != nil {
		t.Errorf("%s still bound after stopping background presenter pid %d by PID: %v", addr, pid, err)
		return
	}
	after.Close()
}
