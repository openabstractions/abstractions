//go:build windows

package win

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Opt-in: OA_PERMISSION_SCREENSHOT=<directory> shows one fixture permission
// prompt on the desktop for a moment, saves it as permission-prompt.png and
// closes it without answering. The requester is a system program, so its own
// icon is drawn. The sound is replaced by a counter.
func TestPermissionPromptScreenshot(t *testing.T) {
	dir := os.Getenv("OA_PERMISSION_SCREENSHOT")
	if dir == "" {
		t.Skip("OA_PERMISSION_SCREENSHOT=<directory> captures a fixture permission prompt")
	}
	if !modernPermissionFor(QuestionPrompt{Permission: true}, windowsVersion()) || !InteractiveDesktop() {
		t.Skip("needs the Windows 11 renderer on an unlocked desktop")
	}
	count := countPermissionChimes(t)
	empty := ""
	prompt := QuestionPrompt{
		Title: "OpenAbstractions Panel — Permission request", Permission: true,
		Choices: []QuestionChoice{{"Allow", "allow"}, {"Refuse", "refuse"}}, Default: "refuse",
		Details: "Program: " + filepath.Join(os.Getenv("SystemRoot"), "explorer.exe"),
		PermissionView: &PermissionView{
			Question:         "Allow this app to apply the saved credential “demo”?",
			Requester:        "Windows Explorer",
			Publisher:        "Publisher: Microsoft Windows (verified at request).",
			Scope:            "Applies to this app, this action and this resource.",
			Effects:          "Allow saves ongoing access for this app. Refuse changes no permissions.",
			RequesterProgram: filepath.Join(os.Getenv("SystemRoot"), "explorer.exe"),
			Warning:          &empty,
		},
		Level: PromptLevelGray,
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_, err := ShowQuestion(prompt)
		done <- err
	}()
	var hwnd uintptr
	for deadline := time.Now().Add(5 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		hwnd = activePermissionDialog.Load()
	}
	if hwnd == 0 {
		t.Fatal("fixture prompt did not appear")
	}
	time.Sleep(700 * time.Millisecond) // armed and painted
	img := capturePermissionWindow(t, hwnd)
	sendMessage.Call(hwnd, permissionWMClose, 0, 0)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if *count != 1 {
		t.Fatalf("fixture prompt rang %d times", *count)
	}
	path := filepath.Join(dir, "permission-prompt.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	t.Logf("saved %s (%dx%d)", path, img.Bounds().Dx(), img.Bounds().Dy())
}

// Opt-in: OA_PERMISSION_LEVEL_CAPTURE=<directory> shows one fixture
// permission prompt per level in the light and the dark app theme, each in
// its own titled window of this test process, and captures it with
// scripts/capture_window.py as <level>-<theme>.png. Each prompt closes
// without answering. The theme setting is never changed: the fixture
// replaces the renderer's theme reading.
func TestPermissionLevelCaptures(t *testing.T) {
	dir := os.Getenv("OA_PERMISSION_LEVEL_CAPTURE")
	if dir == "" {
		t.Skip("OA_PERMISSION_LEVEL_CAPTURE=<directory> captures one prompt per level and theme")
	}
	if !modernPermissionFor(QuestionPrompt{Permission: true}, windowsVersion()) || !InteractiveDesktop() {
		t.Skip("needs the Windows 11 renderer on an unlocked desktop")
	}
	countPermissionChimes(t)
	script, err := filepath.Abs(privateInput(t, "..", "..", "scripts", "capture_window.py"))
	if err != nil {
		t.Fatal(err)
	}
	restore := permissionDarkMode
	t.Cleanup(func() { permissionDarkMode = restore })
	explorer := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	empty := ""
	fixtures := []struct {
		level                                 PromptLevel
		question, requester, publisher, scope string
		program                               string
	}{
		{PromptLevelBlue, "Allow the Panel to look up models in “hf”?", "OpenAbstractions Panel",
			"Publisher: OpenAbstractions", "Applies to OpenAbstractions' own Panel.", ""},
		{PromptLevelGray, "Allow this app to apply the saved credential “demo”?", "Windows Explorer",
			"Publisher: Microsoft Windows", "Applies to this app, this action and this resource.", explorer},
		{PromptLevelYellow, "Allow this app to look up models in “hf”?", "tool (not registered)",
			"Publisher: not verified.", "Applies to this app, this action and this resource.", ""},
		{PromptLevelOrange, "Allow this app to read every saved secret?", "Windows Explorer",
			"Publisher: Microsoft Windows", "All: saved app secrets\n\nIncludes every current and future saved app secret.", explorer},
	}
	// The Windows 11 renderer in each app theme, then the TaskDialog
	// fallback, which Windows draws in its light style only.
	for _, pass := range []string{"light", "dark", "taskdialog"} {
		dark := pass == "dark"
		permissionDarkMode = func() bool { return dark }
		for _, f := range fixtures {
			title := "OA level fixture " + string(f.level) + " " + pass
			prompt := QuestionPrompt{
				Title: title, Permission: true, Level: f.level,
				Choices: []QuestionChoice{{"Allow", "allow"}, {"Refuse", "refuse"}}, Default: "refuse",
				Details: "Level: " + string(f.level),
				PermissionView: &PermissionView{Question: f.question, Requester: f.requester, Publisher: f.publisher,
					Scope: f.scope, Effects: "Allow saves ongoing access for this app. Refuse changes no permissions.",
					RequesterProgram: f.program, Warning: &empty},
			}
			show, active, closeWindow := ShowQuestion, &activePermissionDialog, func(hwnd uintptr) { sendMessage.Call(hwnd, permissionWMClose, 0, 0) }
			if pass == "taskdialog" {
				prompt.Instruction = f.question
				prompt.Content = f.requester + "\n" + f.publisher + "\n\n" + f.scope
				prompt.Footer = "Cancel or Escape closes this window without answering."
				show, active, closeWindow = showTaskDialogQuestion, &activeTaskDialogHwnd, func(hwnd uintptr) { sendMessage.Call(hwnd, tdmClickButton, idcCancel, 0) }
			}
			done := make(chan error, 1)
			go func() {
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				defer withPanelManifest(t)()
				// The shipped Panel is per-monitor aware; a test binary is not.
				if permissionThreadDPI.Find() == nil {
					if previous, _, _ := permissionThreadDPI.Call(^uintptr(3)); previous != 0 {
						defer permissionThreadDPI.Call(previous)
					}
				}
				_, err := show(prompt)
				done <- err
			}()
			var hwnd uintptr
			for deadline := time.Now().Add(5 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				hwnd = active.Load()
			}
			if hwnd == 0 {
				t.Fatalf("%s did not appear", title)
			}
			time.Sleep(700 * time.Millisecond) // armed and painted
			out := filepath.Join(dir, string(f.level)+"-"+pass+".png")
			capture := exec.Command("py", "-3.12", script, "--title", title, "--pid", strconv.Itoa(os.Getpid()), "--out", out)
			output, captureErr := capture.CombinedOutput()
			closeWindow(hwnd)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if captureErr != nil {
				t.Fatalf("capture %s: %v\n%s", title, captureErr, output)
			}
			t.Logf("captured %s", out)
		}
	}
}

// withPanelManifest activates the Panel's own manifest (monitor/panel.manifest)
// on the calling, locked thread, so the fixture's controls are the Common
// Controls 6 controls the shipped Panel draws; a test binary carries no
// manifest. The returned function deactivates it.
func withPanelManifest(t *testing.T) func() {
	manifest, err := filepath.Abs(filepath.Join("..", "panel.manifest"))
	if err != nil {
		t.Error(err)
		return func() {}
	}
	source, _ := syscall.UTF16PtrFromString(manifest)
	context := struct {
		Size, Flags              uint32
		Source                   *uint16
		Architecture, Language   uint16
		Directory, Resource, App *uint16
		Module                   uintptr
	}{Source: source}
	context.Size = uint32(unsafe.Sizeof(context))
	kernel := syscall.NewLazyDLL("kernel32.dll")
	handle, _, callErr := kernel.NewProc("CreateActCtxW").Call(uintptr(unsafe.Pointer(&context)))
	if handle == ^uintptr(0) {
		t.Errorf("CreateActCtxW %s: %v", manifest, callErr)
		return func() {}
	}
	var cookie uintptr
	if ok, _, callErr := kernel.NewProc("ActivateActCtx").Call(handle, uintptr(unsafe.Pointer(&cookie))); ok == 0 {
		t.Errorf("ActivateActCtx: %v", callErr)
		kernel.NewProc("ReleaseActCtx").Call(handle)
		return func() {}
	}
	return func() {
		kernel.NewProc("DeactivateActCtx").Call(0, cookie)
		kernel.NewProc("ReleaseActCtx").Call(handle)
	}
}

func capturePermissionWindow(t *testing.T, hwnd uintptr) image.Image {
	t.Helper()
	// Measure in physical pixels, as the per-monitor-aware dialog draws.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if permissionThreadDPI.Find() == nil {
		if previous, _, _ := permissionThreadDPI.Call(^uintptr(3)); previous != 0 {
			defer permissionThreadDPI.Call(previous)
		}
	}
	var r rect
	getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
	screen, _, _ := permissionGetDC.Call(0)
	defer permissionReleaseDC.Call(0, screen)
	memory, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(screen)
	defer gdi32.NewProc("DeleteDC").Call(memory)
	header := struct {
		Size                  uint32
		Width, Height         int32
		Planes, BitCount      uint16
		Compression, SizeImg  uint32
		XPels, YPels          int32
		ClrUsed, ClrImportant uint32
	}{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	header.Size = uint32(unsafe.Sizeof(header))
	var bits unsafe.Pointer
	bitmap, _, _ := gdi32.NewProc("CreateDIBSection").Call(screen, uintptr(unsafe.Pointer(&header)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 {
		t.Fatal("CreateDIBSection failed")
	}
	defer permissionDeleteObject.Call(bitmap)
	old, _, _ := permissionSelectObject.Call(memory, bitmap)
	defer permissionSelectObject.Call(memory, old)
	if ok, _, err := user32.NewProc("PrintWindow").Call(hwnd, memory, 2); ok == 0 { // PW_RENDERFULLCONTENT
		t.Fatalf("PrintWindow: %v", err)
	}
	pixels := unsafe.Slice((*byte)(bits), w*h*4)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			img.Set(x, y, color.RGBA{pixels[i+2], pixels[i+1], pixels[i], 255})
		}
	}
	return img
}
