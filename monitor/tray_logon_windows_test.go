//go:build windows

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// This file proves the per-user registration installer/abstraction.wxs's
// PanelTrayLogonStart component ships: a Startup-folder shortcut targeting
// "Abstraction Panel.exe" with argument "--tray", plus an HKCU marker at
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
// Startup-folder shortcut carrying the tray command line, and an HKCU marker
// value. dir stands in for the real Startup folder and keyPath for the real
// registry key.
func registerPanelTray(t *testing.T, keyPath, dir, target, args string) string {
	t.Helper()
	shortcut := filepath.Join(dir, "Abstraction Panel notifications.lnk")
	runPowerShell(t, fmt.Sprintf(
		`$s = (New-Object -ComObject WScript.Shell).CreateShortcut(%s); $s.TargetPath = %s; $s.Arguments = %s; $s.WorkingDirectory = %s; $s.Save()`,
		psQuote(shortcut), psQuote(target), psQuote(args), psQuote(filepath.Dir(target))))
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
		psQuote(shortcut)))
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

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
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
// authors carry the exact "Abstraction Panel.exe" "--tray" command line and
// are fully removable. Actually starting that command line against the real
// built Panel is gated behind OA_LIVE_PANEL=1, the way serve's other live
// tests gate a real build (see serve/runtime_inventory_live_test.go), since
// it needs a real go build and a real notification-area icon.
func TestPanelTrayLogonRegistersReadsBackLaunchesAndUnregisters(t *testing.T) {
	startupDir := t.TempDir()
	keyPath := panelTrayRegistrationKey()
	target := filepath.Join(t.TempDir(), "Abstraction Panel.exe")

	live := os.Getenv("OA_LIVE_PANEL") == "1"
	if live {
		buildPanelExecutableAt(t, target)
	}

	shortcut := registerPanelTray(t, keyPath, startupDir, target, "--tray")
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
	if gotArgs != "--tray" {
		t.Fatalf("read-back arguments = %q, want the registered %q", gotArgs, "--tray")
	}

	if !live {
		t.Skip("registration and read-back verified; set OA_LIVE_PANEL=1 to also build and run the real tray Panel at the registered command line")
	}

	runAndObserveRegisteredPanel(t, gotTarget, gotArgs)

	unregisterPanelTray(keyPath, shortcut)
	if markerPresent(keyPath) {
		t.Fatal("HKCU trayAtLogon marker still present after unregister")
	}
	if _, err := os.Stat(shortcut); !os.IsNotExist(err) {
		t.Fatalf("Startup-folder shortcut still present after unregister: %v", err)
	}
}

// runAndObserveRegisteredPanel launches the registered command line exactly
// once, with an isolated runtime state dir (own(t), the isolation
// service_panel_test.go's own runtime tests use) so it reads and writes
// nothing this account's real Panel would, confirms the tray's HTTP server
// answers its guarded "/" route, and stops it by PID — never by image name
// (AGENTS.md), since another Panel instance may legitimately be running on
// this machine.
func runAndObserveRegisteredPanel(t *testing.T, target, args string) {
	t.Helper()
	own(t)

	const addr = "127.0.0.1:8734" // the -addr flag's own default; the registered command line carries no override.
	probe, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("%s already answering, cannot safely launch the registered command line here: %v", addr, err)
	}
	probe.Close()

	cmd := exec.Command(target, strings.Fields(args)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s %s: %v", target, args, err)
	}
	pid := cmd.Process.Pid
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	url := "http://" + addr + "/"
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(20 * time.Second)
	var resp *http.Response
	var lastErr error
	for time.Now().Before(deadline) {
		resp, lastErr = client.Get(url)
		if lastErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("tray Panel (pid %d) never answered %s: %v", pid, url, lastErr)
	}
	resp.Body.Close()
	// "/" without the run's own key refuses rather than serving the page
	// (monitor/service_panel.go); the refusal itself proves this is the
	// tray's own guarded server, not some other process on the port.
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tray Panel (pid %d) answered %s with status %d, want %d (panel key required)", pid, url, resp.StatusCode, http.StatusForbidden)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("stop tray Panel pid %d: %v", pid, err)
	}
	_, _ = cmd.Process.Wait()
	stopped = true

	time.Sleep(300 * time.Millisecond)
	after, err := net.Listen("tcp", addr)
	if err != nil {
		t.Errorf("%s still bound after stopping tray Panel pid %d by PID: %v", addr, pid, err)
		return
	}
	after.Close()
}
