package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstractions/monitor/win"
)

// Opt-in: OA_AGENT_UPDATE_CAPTURE=<file.png> shows the connection-update
// popup for a fixture record, captures it with scripts/capture_window.py and
// closes it without answering.
func TestAgentUpdatePopupCapture(t *testing.T) {
	out := os.Getenv("OA_AGENT_UPDATE_CAPTURE")
	if out == "" {
		t.Skip("OA_AGENT_UPDATE_CAPTURE=<file.png> captures the connection-update popup")
	}
	if !win.InteractiveDesktop() {
		t.Skip("needs an unlocked desktop")
	}
	record := askclient.RecordMetadata{ID: "cef113e9a7c41d55", Key: asks.AgentUpdateKey, Asked: time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano), Options: []string{"update", "not_now"}, Asker: "openabstractions.exe",
		Text: "Update the Claude integration, Codex integration and OpenCode connections? Keep these updated connections as your assistants. Each access permission is reviewed separately. Restart Claude integration, Codex integration and OpenCode to finish. Request cef113e9" + strings.Repeat("0", 40) + "."}
	view := trayQuestionViewForContent(record, trayPublisher{}, nil, "", nil)
	prompt, err := trayWindowsQuestionPrompt(view)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_, err := win.ShowQuestion(win.Occurrence{ID: "fixture-agent-update", Prompt: prompt})
		done <- err
	}()
	script, _ := filepath.Abs(privateInput(t, "..", "scripts", "capture_window.py"))
	var output []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		time.Sleep(700 * time.Millisecond)
		output, err = exec.Command("py", "-3.12", script, "--title", prompt.Title, "--pid", strconv.Itoa(os.Getpid()), "--out", out).CombinedOutput()
		if err == nil {
			break
		}
	}
	user32 := testUser32
	title, _ := syscall.UTF16PtrFromString(prompt.Title)
	if hwnd, _, _ := user32.NewProc("FindWindowW").Call(0, uintptr(unsafe.Pointer(title))); hwnd != 0 {
		user32.NewProc("PostMessageW").Call(hwnd, 0x0010, 0, 0) // WM_CLOSE
	}
	<-done
	if err != nil {
		t.Fatalf("capture: %v\n%s", err, output)
	}
	t.Logf("captured %s", out)
}

// Opt-in: OA_QUESTION_CAPTURE=<file.png> shows the generic question popup for
// the R21 "open ComfyUI" record from an unverified connection program.
func TestCommandQuestionPopupCapture(t *testing.T) {
	out := os.Getenv("OA_QUESTION_CAPTURE")
	if out == "" {
		t.Skip("OA_QUESTION_CAPTURE=<file.png> captures the generic question popup")
	}
	if !win.InteractiveDesktop() {
		t.Skip("needs an unlocked desktop")
	}
	root := t.TempDir()
	saved := agentConnectorRoot
	agentConnectorRoot = func() string { return root }
	t.Cleanup(func() { agentConnectorRoot = saved })
	program := filepath.Join(root, "oa-opencode-integration.exe")
	record := askclient.RecordMetadata{ID: "q-open", Key: asks.ApplicationCommandOnceKey, About: program, Asker: "oa-opencode-integration.exe", Options: []string{"once", "refuse"},
		Text: "Open ComfyUI — OA build? " + program + " requests this once. Command open. Request 95bfb139f3d46ef94b4b expires " + time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339) + "."}
	view := trayQuestionViewForContent(record, trayPublisher{}, nil, program, nil)
	t.Logf("instruction=%q content=%q footer=%q details=%q", view.Instruction, view.Content, view.Footer, view.Details)
	capturePopup(t, view, out)
}

func capturePopup(t *testing.T, view trayQuestionPresentation, out string) {
	capturePopupWith(t, view, out, false)
}

// capturePopupExpanded captures the popup with Show details pressed.
func capturePopupExpanded(t *testing.T, view trayQuestionPresentation, out string) {
	capturePopupWith(t, view, out, true)
}

// ownPopupWindow is this test process's top-level window with the title.
func ownPopupWindow(title string) uintptr {
	user32 := testUser32
	find, owner := user32.NewProc("FindWindowExW"), user32.NewProc("GetWindowThreadProcessId")
	name, _ := syscall.UTF16PtrFromString(title)
	for hwnd := uintptr(0); ; {
		hwnd, _, _ = find.Call(0, hwnd, 0, uintptr(unsafe.Pointer(name)))
		if hwnd == 0 {
			return 0
		}
		var pid uint32
		owner.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if int(pid) == os.Getpid() {
			return hwnd
		}
	}
}

func capturePopupWith(t *testing.T, view trayQuestionPresentation, out string, expand bool) {
	prompt, err := trayWindowsQuestionPrompt(view)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_, err := win.ShowQuestion(win.Occurrence{ID: "fixture-agent-update", Prompt: prompt})
		done <- err
	}()
	script, _ := filepath.Abs(privateInput(t, "..", "scripts", "capture_window.py"))
	var output []byte
	expanded := !expand
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		time.Sleep(700 * time.Millisecond)
		if !expanded {
			hwnd := ownPopupWindow(prompt.Title)
			label, _ := syscall.UTF16PtrFromString("Show details")
			button, _, _ := testUser32.NewProc("FindWindowExW").Call(hwnd, 0, 0, uintptr(unsafe.Pointer(label)))
			if hwnd == 0 || button == 0 {
				continue
			}
			testUser32.NewProc("SendMessageW").Call(button, 0x00F5, 0, 0) // BM_CLICK
			expanded = true
			time.Sleep(500 * time.Millisecond)
		}
		output, err = exec.Command("py", "-3.12", script, "--title", prompt.Title, "--pid", strconv.Itoa(os.Getpid()), "--out", out).CombinedOutput()
		if err == nil {
			break
		}
	}
	if hwnd := ownPopupWindow(prompt.Title); hwnd != 0 {
		testUser32.NewProc("PostMessageW").Call(hwnd, 0x0010, 0, 0) // WM_CLOSE
	}
	<-done
	if err != nil {
		t.Fatalf("capture: %v\n%s", err, output)
	}
	t.Logf("captured %s", out)
}

// Opt-in: OA_QUESTION_CAPTURE=<file.png> with -run TestKeepQuestionPopupCapture
// shows the R21 Keep question for an unsigned program whose title is its path.
func TestKeepQuestionPopupCapture(t *testing.T) {
	out := os.Getenv("OA_QUESTION_CAPTURE")
	if out == "" {
		t.Skip("OA_QUESTION_CAPTURE=<file.png> captures the Keep question popup")
	}
	if !win.InteractiveDesktop() {
		t.Skip("needs an unlocked desktop")
	}
	program := `C:\Users\person\AppData\Local\oa\a\cu\0.8.0-r17\launch-helper.exe`
	record := askclient.RecordMetadata{ID: "keep-q", Key: asks.SelfRegisteredKey, About: program,
		Text: program + " was added to OpenAbstractions. Keep it?", Options: []string{"keep", "remove"}}
	retained := &askclient.CatalogueContent{Slots: map[string]string{"program": program, "title": program}}
	view := trayQuestionViewForContent(record, trayPublisher{}, nil, "", retained)
	t.Logf("instruction=%q content=%q details=%q level=%q", view.Instruction, view.Content, view.Details, view.Level)
	capturePopup(t, view, out)
}
