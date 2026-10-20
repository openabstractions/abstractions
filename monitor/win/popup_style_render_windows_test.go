//go:build windows

package win

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Opt-in: OA_POPUP_STYLE_DIR=<folder> renders every fixture prompt in
// <folder>/fixtures-<set>/*.json (written by the monitor package's
// TestPopupStyleFixtures) through the production ShowQuestion path on a
// private, invisible desktop of this test process, in the light and dark
// app themes at 100% and 150% scale, collapsed and with Show details
// pressed. Each render is captured with PrintWindow to
// <folder>/<set>/<name>-<theme>-<dpi>[-details].png, with its measured
// window size, controls, tab order and default button in
// <folder>/<set>/metrics.json. Nothing appears on the person's desktop, the
// alert sound is muted, no setting changes and no answer is sent.
func TestPopupStyleRender(t *testing.T) {
	dir := os.Getenv("OA_POPUP_STYLE_DIR")
	if dir == "" {
		t.Skip("OA_POPUP_STYLE_DIR=<folder> renders the popup style fixtures off screen")
	}
	if !modernPermissionFor(QuestionPrompt{Permission: true}, windowsVersion()) {
		t.Skip("needs the Windows 11 renderer")
	}
	countPermissionChimes(t)
	restore := permissionDarkMode
	t.Cleanup(func() { permissionDarkMode = restore })
	desktop := newRenderDesktop(t)
	sets, _ := filepath.Glob(filepath.Join(dir, "fixtures-*"))
	if len(sets) == 0 {
		t.Fatalf("no fixtures-* folders in %s", dir)
	}
	for _, set := range sets {
		name := strings.TrimPrefix(filepath.Base(set), "fixtures-")
		out := filepath.Join(dir, name)
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		files, _ := filepath.Glob(filepath.Join(set, "*.json"))
		sort.Strings(files)
		metrics := map[string]renderMetrics{}
		for _, file := range files {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var prompt QuestionPrompt
			if err := json.Unmarshal(raw, &prompt); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			kind := strings.TrimSuffix(filepath.Base(file), ".json")
			for _, theme := range []string{"light", "dark"} {
				dark := theme == "dark"
				permissionDarkMode = func() bool { return dark }
				for _, dpi := range []int{96, 144} {
					for _, expand := range []bool{false, true} {
						if expand && (dpi != 96 || prompt.Details == "") {
							continue
						}
						suffix := fmt.Sprintf("%s-%s-%d", kind, theme, dpi*100/96)
						if expand {
							suffix += "-details"
						}
						m, img := renderPrompt(t, desktop, prompt, dpi, expand)
						metrics[suffix] = m
						path := filepath.Join(out, suffix+".png")
						f, err := os.Create(path)
						if err != nil {
							t.Fatal(err)
						}
						if err := png.Encode(f, img); err != nil {
							t.Fatal(err)
						}
						f.Close()
						t.Logf("%s %dx%d renderer=%s", path, m.Width, m.Height, m.Renderer)
					}
				}
			}
		}
		raw, _ := json.MarshalIndent(metrics, "", "  ")
		if err := os.WriteFile(filepath.Join(out, "metrics.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type renderControl struct {
	Class, Text            string
	Visible, Tab, Disabled bool
	X, Y, W, H             int32
}

type renderMetrics struct {
	Renderer      string
	Width, Height int32
	Client        [2]int32
	Caption       string
	DefaultID     uint32
	DefaultLabel  string
	Focus         string
	Controls      []renderControl
}

var (
	renderCreateDesktop    = user32.NewProc("CreateDesktopW")
	renderCloseDesktop     = user32.NewProc("CloseDesktop")
	renderSetThreadDesktop = user32.NewProc("SetThreadDesktop")
	renderEnumChildren     = user32.NewProc("EnumChildWindows")
	renderGetClassName     = user32.NewProc("GetClassNameW")
	renderGetStyle         = user32.NewProc("GetWindowLongW")
	renderIsVisible        = user32.NewProc("IsWindowVisible")
	renderGetParent        = user32.NewProc("GetParent")
	renderMapPoints        = user32.NewProc("MapWindowPoints")
	renderGetFocusOf       = user32.NewProc("GetGUIThreadInfo")
)

// newRenderDesktop creates a desktop of this process's window station that no
// one switches to: windows created on it are never shown to the person.
func newRenderDesktop(t *testing.T) uintptr {
	t.Helper()
	name, _ := syscall.UTF16PtrFromString(fmt.Sprintf("oa-popup-render-%d", os.Getpid()))
	const genericAll = 0x10000000
	h, _, err := renderCreateDesktop.Call(uintptr(unsafe.Pointer(name)), 0, 0, 0, genericAll, 0)
	if h == 0 {
		t.Fatalf("CreateDesktopW: %v", err)
	}
	t.Cleanup(func() { renderCloseDesktop.Call(h) })
	return h
}

// onRenderDesktop runs f on a fresh OS thread bound to the private desktop.
// The goroutine exits locked, so the thread ends with it and never returns
// to the scheduler's pool attached to that desktop.
func onRenderDesktop(t *testing.T, desktop uintptr, f func()) chan struct{} {
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer close(done)
		if ok, _, err := renderSetThreadDesktop.Call(desktop); ok == 0 {
			t.Errorf("SetThreadDesktop: %v", err)
			return
		}
		f()
	}()
	return done
}

func renderPrompt(t *testing.T, desktop uintptr, prompt QuestionPrompt, dpi int, expand bool) (renderMetrics, image.Image) {
	t.Helper()
	activePermissionDialog.Store(0)
	activeTaskDialogHwnd.Store(0)
	shown := make(chan error, 1)
	onRenderDesktop(t, desktop, func() {
		defer withPanelManifest(t)()
		if permissionThreadDPI.Find() == nil {
			permissionThreadDPI.Call(^uintptr(3)) // per-monitor aware v2, as the shipped presenter
		}
		_, err := ShowQuestion(Occurrence{ID: "fixture-popup-style", Prompt: prompt})
		shown <- err
	})
	var hwnd uintptr
	renderer := "windows11"
	for deadline := time.Now().Add(5 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if hwnd = activePermissionDialog.Load(); hwnd == 0 {
			if hwnd = activeTaskDialogHwnd.Load(); hwnd != 0 {
				renderer = "taskdialog"
			}
		}
	}
	if hwnd == 0 {
		select {
		case err := <-shown:
			t.Fatalf("prompt did not appear: %v", err)
		default:
		}
		t.Fatal("prompt did not appear")
	}
	var m renderMetrics
	var img image.Image
	<-onRenderDesktop(t, desktop, func() {
		if permissionThreadDPI.Find() == nil {
			permissionThreadDPI.Call(^uintptr(3))
		}
		time.Sleep(800 * time.Millisecond) // armed and painted
		if renderer == "windows11" {
			current := 96
			if dpiForWindow.Find() == nil {
				if v, _, _ := dpiForWindow.Call(hwnd); v >= 96 {
					current = int(v)
				}
			}
			if current != dpi {
				var r rect
				getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
				sendMessage.Call(hwnd, permissionWMDPI, uintptr(dpi|dpi<<16), uintptr(unsafe.Pointer(&r)))
				time.Sleep(300 * time.Millisecond)
			}
			if expand {
				label, _ := syscall.UTF16PtrFromString("Show details")
				button, _, _ := user32.NewProc("FindWindowExW").Call(hwnd, 0, 0, uintptr(unsafe.Pointer(label)))
				if button != 0 {
					sendMessage.Call(button, 0x00F5, 0, 0) // BM_CLICK
					time.Sleep(300 * time.Millisecond)
				}
			}
		}
		m = measureRender(hwnd)
		m.Renderer = renderer
		img = printRender(t, hwnd)
		if renderer == "windows11" {
			sendMessage.Call(hwnd, permissionWMClose, 0, 0)
		} else {
			sendMessage.Call(hwnd, tdmClickButton, idcCancel, 0)
		}
	})
	if err := <-shown; err != nil {
		t.Fatal(err)
	}
	return m, img
}

func measureRender(hwnd uintptr) renderMetrics {
	var m renderMetrics
	var outer, client rect
	getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&outer)))
	getClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	m.Width, m.Height = outer.Right-outer.Left, outer.Bottom-outer.Top
	m.Client = [2]int32{client.Right, client.Bottom}
	m.Caption = renderText(hwnd)
	def, _, _ := sendMessage.Call(hwnd, permissionDMGetDefID, 0, 0)
	if def>>16 == 0x534b { // DC_HASDEFID
		m.DefaultID = uint32(def & 0xffff)
		if h, _, _ := permissionGetItem.Call(hwnd, def&0xffff); h != 0 {
			m.DefaultLabel = renderText(h)
		}
	}
	info := struct {
		Size, Flags                               uint32
		Active, Focus, Capture, Menu, Move, Caret uintptr
		CaretRect                                 rect
	}{}
	info.Size = uint32(unsafe.Sizeof(info))
	thread, _, _ := user32.NewProc("GetWindowThreadProcessId").Call(hwnd, 0)
	if ok, _, _ := renderGetFocusOf.Call(thread, uintptr(unsafe.Pointer(&info))); ok != 0 && info.Focus != 0 {
		m.Focus = renderText(info.Focus)
	}
	var children []uintptr
	callback := syscall.NewCallback(func(h, _ uintptr) uintptr { children = append(children, h); return 1 })
	renderEnumChildren.Call(hwnd, callback, 0)
	for _, h := range children {
		if p, _, _ := renderGetParent.Call(h); p != hwnd {
			continue
		}
		class := make([]uint16, 64)
		renderGetClassName.Call(h, uintptr(unsafe.Pointer(&class[0])), 64)
		style, _, _ := renderGetStyle.Call(h, ^uintptr(15)) // GWL_STYLE (-16)
		visible, _, _ := renderIsVisible.Call(h)
		var r rect
		getWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
		renderMapPoints.Call(0, hwnd, uintptr(unsafe.Pointer(&r)), 2)
		m.Controls = append(m.Controls, renderControl{Class: syscall.UTF16ToString(class), Text: renderText(h),
			Visible: visible != 0, Tab: style&0x00010000 != 0, Disabled: style&0x08000000 != 0,
			X: r.Left, Y: r.Top, W: r.Right - r.Left, H: r.Bottom - r.Top})
	}
	return m
}

func renderText(h uintptr) string {
	n, _, _ := getWindowTextLength.Call(h)
	buf := make([]uint16, n+1)
	getWindowText.Call(h, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

// printRender asks the window to draw itself, frame included, into a bitmap.
func printRender(t *testing.T, hwnd uintptr) image.Image {
	t.Helper()
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
	flags := uintptr(2) // PW_RENDERFULLCONTENT
	if os.Getenv("OA_POPUP_STYLE_PRINT") == "legacy" {
		flags = 0
	}
	if ok, _, err := user32.NewProc("PrintWindow").Call(hwnd, memory, flags); ok == 0 {
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

// Opt-in with OA_POPUP_STYLE_DIR: what Enter and Escape do on each fixture
// once its answers are armed, delivered as keystrokes to the control that
// holds the focus, as a person's keyboard would. Written to
// <folder>/<set>/keys.json.
func TestPopupStyleKeys(t *testing.T) {
	dir := os.Getenv("OA_POPUP_STYLE_DIR")
	if dir == "" {
		t.Skip("OA_POPUP_STYLE_DIR=<folder> records the popup fixtures' Enter and Escape")
	}
	if !modernPermissionFor(QuestionPrompt{Permission: true}, windowsVersion()) {
		t.Skip("needs the Windows 11 renderer")
	}
	countPermissionChimes(t)
	desktop := newRenderDesktop(t)
	files, _ := filepath.Glob(filepath.Join(dir, "fixtures-before", "*.json"))
	sort.Strings(files)
	results := map[string]map[string]string{}
	post := user32.NewProc("PostMessageW")
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var prompt QuestionPrompt
		if err := json.Unmarshal(raw, &prompt); err != nil {
			t.Fatal(err)
		}
		if prompt.AppChoice {
			continue
		}
		kind := strings.TrimSuffix(filepath.Base(file), ".json")
		results[kind] = map[string]string{}
		for _, key := range []struct {
			name string
			vk   uintptr
		}{{"Enter", 0x0D}, {"Escape", 0x1B}} {
			activePermissionDialog.Store(0)
			answer := make(chan string, 1)
			onRenderDesktop(t, desktop, func() {
				defer withPanelManifest(t)()
				option, err := ShowQuestion(Occurrence{ID: "fixture-popup-keys", Prompt: prompt})
				if err != nil {
					option = "error: " + err.Error()
				}
				answer <- option
			})
			var hwnd uintptr
			for deadline := time.Now().Add(5 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				hwnd = activePermissionDialog.Load()
			}
			if hwnd == 0 {
				t.Fatalf("%s did not appear", kind)
			}
			var focus string
			<-onRenderDesktop(t, desktop, func() {
				time.Sleep(800 * time.Millisecond)
				focus = measureRender(hwnd).Focus
				info := struct {
					Size, Flags                               uint32
					Active, Focus, Capture, Menu, Move, Caret uintptr
					CaretRect                                 rect
				}{}
				info.Size = uint32(unsafe.Sizeof(info))
				thread, _, _ := user32.NewProc("GetWindowThreadProcessId").Call(hwnd, 0)
				renderGetFocusOf.Call(thread, uintptr(unsafe.Pointer(&info)))
				target := info.Focus
				if target == 0 {
					target = hwnd
				}
				post.Call(target, 0x0100, key.vk, 0) // WM_KEYDOWN
				post.Call(target, 0x0101, key.vk, 0) // WM_KEYUP
			})
			target := hwnd
			outcome := ""
			select {
			case option := <-answer:
				outcome = fmt.Sprintf("closed, answer %q", option)
			case <-time.After(1500 * time.Millisecond):
				outcome = "stayed open"
				_ = target
				for _, c := range measureRender(hwnd).Controls {
					if c.Class == "Button" && strings.HasSuffix(c.Text, "details") {
						outcome += fmt.Sprintf("; the toggle now reads %q", c.Text)
					}
				}
				sendMessage.Call(hwnd, permissionWMClose, 0, 0)
				<-answer
			}
			results[kind][key.name] = fmt.Sprintf("focus %q: %s", focus, outcome)
		}
	}
	raw, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "before", "keys.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", raw)
}
