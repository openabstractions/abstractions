//go:build windows

package win

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Opt-in: OA_POPUP_CAPTURE=<directory> shows each kind of OpenAbstractions
// popup once with fixture text, in its own titled window of this test
// process, captures it with scripts/capture_window.py as <name>.png and
// closes it without answering: keeping an app that added itself, asked after
// the person acts on it in Apps, and a permission question. Nothing reaches a runtime, an installed product or a setting.
func TestPopupStyleCaptures(t *testing.T) {
	dir := os.Getenv("OA_POPUP_CAPTURE")
	if dir == "" {
		t.Skip("OA_POPUP_CAPTURE=<directory> captures one fixture of each OpenAbstractions popup")
	}
	if !InteractiveDesktop() {
		t.Skip("needs an unlocked desktop")
	}
	countPermissionChimes(t)
	script, err := filepath.Abs(privateInput(t, "..", "..", "scripts", "capture_window.py"))
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	explorer := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	question := func(prompt QuestionPrompt) func() error {
		return func() error { _, err := ShowQuestion(Occurrence{ID: "fixture-style", Prompt: prompt}); return err }
	}
	fixtures := []struct {
		name string
		show func() error
	}{
		{"keep-an-app", question(QuestionPrompt{Title: "OpenAbstractions", Level: PromptLevelYellow,
			Instruction: "Let ComfyUI work with OpenAbstractions?",
			Content:     "",
			Details:     `Program: C:\Users\person\OAComfy\launch-helper.exe`,
			Choices:     []QuestionChoice{{"Allow", "keep"}, {"Don't allow", "remove"}}, Dismiss: "not_now"})},
		{"permission-question", question(QuestionPrompt{Title: "OpenAbstractions Panel — Permission request", Permission: true,
			Level: PromptLevelGray, Choices: []QuestionChoice{{"Allow", "allow"}, {"Refuse", "refuse"}}, Default: "refuse",
			PermissionView: &PermissionView{Question: "Allow this app to apply the saved credential “demo”?",
				Requester: "Windows Explorer", Publisher: "Publisher: Microsoft Windows (verified at request).",
				Scope:   "Applies to this app, this action and this resource.",
				Effects: "Allow saves ongoing access for this app. Refuse changes no permissions.", RequesterProgram: explorer, Warning: &empty}})},
	}
	for _, f := range fixtures {
		done := make(chan error, 1)
		activePermissionDialog.Store(0)
		activeTaskDialogHwnd.Store(0)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			defer withPanelManifest(t)()
			if permissionThreadDPI.Find() == nil {
				if previous, _, _ := permissionThreadDPI.Call(^uintptr(3)); previous != 0 {
					defer permissionThreadDPI.Call(previous)
				}
			}
			done <- f.show()
		}()
		var hwnd uintptr
		for deadline := time.Now().Add(5 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if hwnd = activePermissionDialog.Load(); hwnd == 0 {
				hwnd = activeTaskDialogHwnd.Load()
			}
		}
		if hwnd == 0 {
			t.Fatalf("%s did not appear", f.name)
		}
		time.Sleep(700 * time.Millisecond) // armed and painted
		title := windowTitle(hwnd)
		out := filepath.Join(dir, f.name+".png")
		output, captureErr := exec.Command("py", "-3.12", script, "--title", title, "--pid", strconv.Itoa(os.Getpid()), "--out", out).CombinedOutput()
		sendMessage.Call(hwnd, permissionWMClose, 0, 0)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if captureErr != nil {
			t.Fatalf("capture %s: %v\n%s", f.name, captureErr, output)
		}
		t.Logf("captured %s", out)
	}
}

// windowTitle reads a top-level window's caption.
func windowTitle(hwnd uintptr) string {
	return (&Control{h: syscall.Handle(hwnd)}).Text()
}
