//go:build windows

package main

import (
	"context"
	"debug/pe"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	identity "github.com/openabstractions/abstraction-identity"
)

func TestHiddenNativeWindowControlsAndCloseCancellation(t *testing.T) {
	getText := appUser32.NewProc("GetWindowTextW")
	isVisible := appUser32.NewProc("IsWindowVisible")
	isEnabled := appUser32.NewProc("IsWindowEnabled")
	text := func(h syscall.Handle) string {
		var value [1024]uint16
		getText.Call(uintptr(h), uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)))
		return syscall.UTF16ToString(value[:])
	}
	started, cancelled := make(chan struct{}), make(chan struct{})
	err := runAppWindow(connection{}, "App A", "Playground A.exe", false, func(w *playgroundWindow) {
		if visible, _, _ := isVisible.Call(uintptr(w.h)); visible != 0 {
			t.Error("test window became visible")
		}
		if text(w.read) != "Read secret" || text(w.use) != "Use secret" || !strings.Contains(text(w.h), "App A") {
			t.Error("native app identity/action controls missing")
		}
		w.actions.execute = func(ctx context.Context, action windowAction) error {
			if action.Verb != "read" || action.Label != "alpha" {
				t.Error("native click selected the wrong operation")
			}
			close(started)
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		}
		appSendMessage.Call(uintptr(w.h), appWMCommand, appReadID, uintptr(w.read))
		for _, control := range []syscall.Handle{w.secret, w.read, w.use} {
			if enabled, _, _ := isEnabled.Call(uintptr(control)); enabled != 0 {
				t.Error("concurrent action control remained enabled")
			}
		}
		// The window thread continues pumping while the worker waits; close is
		// posted from another goroutine and cancels it without a UI-thread wait.
		go func() { <-started; appPostMessage.Call(uintptr(w.h), appWMClose, 0, 0) }()
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("window close did not cancel its operation")
	}
}

func TestPreparedWindowsGUIKeepsSeparateCallerFilesAndRedirectedCLI(t *testing.T) {
	root := t.TempDir()
	prebuilt := filepath.Join(root, "prebuilt")
	if err := os.Mkdir(prebuilt, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openabstractions.exe", "Abstraction Panel.exe"} {
		if err := os.WriteFile(filepath.Join(prebuilt, name), []byte("not launched"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pack := filepath.Join(root, "pack")
	script, _ := filepath.Abs("Build.ps1")
	output, err := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-Dir", pack, "-PrebuiltDir", prebuilt).CombinedOutput()
	if err != nil {
		t.Fatalf("packaging failed: %v\n%s", err, output)
	}
	appA, appB := filepath.Join(pack, "Playground A.exe"), filepath.Join(pack, "Playground B.exe")
	if same, err := identity.SameFile(appA, appB); err != nil || same {
		t.Fatal("A/B callers share one physical file")
	}
	for _, app := range []string{appA, appB} {
		file, err := pe.Open(app)
		if err != nil {
			t.Fatal(err)
		}
		if header, ok := file.OptionalHeader.(*pe.OptionalHeader64); !ok || header.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
			t.Error("app build retained console subsystem")
		}
		file.Close()
		output, err := exec.Command(app, "identity").CombinedOutput()
		var result struct {
			Program string `json:"program"`
		}
		if err != nil || json.Unmarshal(output, &result) != nil || !identity.SamePathName(result.Program, identity.CanonicalProgramPath(app)) {
			t.Fatalf("GUI subsystem lost redirected CLI/physical caller identity: %v\n%s", err, output)
		}
	}
	for _, label := range []string{"A", "B"} {
		wrapper, err := os.ReadFile(filepath.Join(pack, "App-"+label+".cmd"))
		if err != nil || !strings.Contains(string(wrapper), " window --connection ") || strings.Contains(string(wrapper), "pause") {
			t.Fatal("app launcher does not select the native window")
		}
	}
}
