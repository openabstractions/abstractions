//go:build windows

package win

import (
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// A body with one long unbroken path switches to the scrolling Edit. Since
// 36e55ea53 that Edit took the maximum body height and opened a tall, mostly
// empty popup; 1c8f9afef sizes it to its text. The popup with the path stays
// within two text lines of the one without.
func TestPermissionDialogLongPathKeepsTextHeight(t *testing.T) {
	if !modernPermissionFor(QuestionPrompt{}, windowsVersion()) || !InteractiveDesktop() {
		t.Skip("needs the Windows 11 renderer on an unlocked desktop")
	}
	sentence := "This program isn't signed, so Windows can't confirm who made it."
	path := `C:\Users\person\AppData\Local\oa\a\cu\0.8.0-r17\very-long-folder-name-without-any-break-points\launch-helper.exe`
	short, dpi := permissionDialogHeight(t, sentence)
	long, _ := permissionDialogHeight(t, path+" "+sentence)
	line := 20 * dpi / 96
	if long-short > 2*line {
		t.Fatalf("a long path grew the popup from %d to %d pixels (line %d)", short, long, line)
	}
	if long > short+short/2 {
		t.Fatalf("popup with a path is %d pixels, near the maximum; short body is %d", long, short)
	}
}

func permissionDialogHeight(t *testing.T, content string) (height, dpi int) {
	t.Helper()
	prompt := QuestionPrompt{Title: "OpenAbstractions Panel — Height", Instruction: "Keep launch-helper in OpenAbstractions?", Content: content,
		Choices: []QuestionChoice{{"Keep", "keep"}, {"Remove", "remove"}}, Dismiss: "not_now", Level: PromptLevelYellow}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_, err := ShowQuestion(Occurrence{ID: "fixture-height", Prompt: prompt})
		done <- err
	}()
	var hwnd uintptr
	for deadline := time.Now().Add(5 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		hwnd = activePermissionDialog.Load()
	}
	if hwnd == 0 {
		t.Fatal("popup did not appear")
	}
	time.Sleep(300 * time.Millisecond)
	var r rect
	getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	d, _, _ := dpiForWindow.Call(hwnd)
	sendMessage.Call(hwnd, permissionWMClose, 0, 0)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for activePermissionDialog.Load() == hwnd {
		time.Sleep(20 * time.Millisecond)
	}
	return int(r.Bottom - r.Top), max(96, int(d))
}
