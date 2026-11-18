//go:build windows

package win

import (
	"context"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	a11yOleAut       = windows.NewLazySystemDLL("oleaut32.dll")
	a11yFreeString   = a11yOleAut.NewProc("SysFreeString")
	a11yGetWindow    = user32.NewProc("GetWindow")
	a11yFindWindowEx = user32.NewProc("FindWindowExW")
)

// Popup accessibility (asks README "keyboard/UIA inspection"): on the Windows
// 11 permission popup, Tab and screen readers meet the controls in the order
// the popup shows them, top to bottom, every control a person can reach has a
// UI Automation name, and the credential form takes the new font after a DPI
// change. A body too wide for the card moves into a scrolling Edit named by
// the question. The popup opens through ShowQuestion on a private desktop
// nobody sees; no answer is sent.
func TestPermissionDialogReadingOrderAndNames(t *testing.T) {
	if !modernPermissionFor(QuestionPrompt{Permission: true}, windowsVersion()) {
		t.Skip("needs the Windows 11 renderer")
	}
	countPermissionChimes(t)
	desktop := newRenderDesktop(t)
	prompt := QuestionPrompt{
		Title:       "OpenAbstractions — Fixture",
		Instruction: "Let Fixture use your OpenRouter key?",
		Content:     "Fixture asks to send requests to openrouter.ai with your key.",
		Details:     "Program C:\\Program Files\\Fixture\\fixture.exe\nAction abstraction.credentials/apply",
		Footer:      "Fixture is not signed, so Windows can't confirm who made it.",
		Choices:     []QuestionChoice{{"Allow", "allow"}, {"Don't allow", "refuse"}},
		Dismiss:     "not_now",
		Level:       PromptLevelYellow,
		CredentialForm: &CredentialForm{Label: "OpenRouter", BudgetUnit: "tokens", NeedsKey: true,
			Allow: func(context.Context, []byte, int64) error { return nil }},
	}
	t.Run("card", func(t *testing.T) { checkReadingOrderAndNames(t, desktop, prompt, 6, false) })
	prompt.Content = `C:\Users\person\AppData\Local\fixture\0.8.1\a-very-long-folder-name-without-any-break-points-at-all\fixture-helper.exe asks to send requests with your key.`
	t.Run("scrolling body", func(t *testing.T) { checkReadingOrderAndNames(t, desktop, prompt, 7, true) })
	restore := permissionHighContrast
	t.Cleanup(func() { permissionHighContrast = restore })
	permissionHighContrast = func() bool { return true }
	t.Run("high contrast", func(t *testing.T) { checkReadingOrderAndNames(t, desktop, prompt, 7, true) })
}

// checkReadingOrderAndNames opens prompt, presses Show details, and checks
// the Tab order, the UI Automation names and the DPI font of the popup.
func checkReadingOrderAndNames(t *testing.T, desktop uintptr, prompt QuestionPrompt, tabs int, overflow bool) {
	activePermissionDialog.Store(0)
	shown := make(chan error, 1)
	onRenderDesktop(t, desktop, func() {
		defer withPanelManifest(t)()
		if permissionThreadDPI.Find() == nil {
			permissionThreadDPI.Call(^uintptr(3))
		}
		_, err := ShowQuestion(Occurrence{ID: "fixture-accessibility", Prompt: prompt})
		shown <- err
	})
	var hwnd uintptr
	for deadline := time.Now().Add(5 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		hwnd = activePermissionDialog.Load()
	}
	if hwnd == 0 {
		select {
		case err := <-shown:
			t.Fatalf("popup did not appear: %v", err)
		default:
		}
		t.Fatal("popup did not appear")
	}
	<-onRenderDesktop(t, desktop, func() {
		defer sendMessage.Call(hwnd, permissionWMClose, 0, 0)
		time.Sleep(800 * time.Millisecond) // armed and laid out
		// High contrast draws with the system's window colours and leaves the
		// buttons to the system renderer.
		permissionDialogs.Lock()
		d := permissionDialogs.windows[hwnd]
		permissionDialogs.Unlock()
		sys := func(index uintptr) uint32 { c, _, _ := permissionGetSysColor.Call(index); return uint32(c) }
		if d == nil || d.contrast != permissionHighContrast() {
			t.Errorf("dialog contrast does not follow the system setting")
		} else if d.contrast && (d.ink != sys(8) || d.bgColor != sys(5) || d.cardColor != sys(5) || d.colors.headerText != sys(8)) {
			t.Errorf("high contrast colours ink %06x background %06x header %06x/%06x", d.ink, d.bgColor, d.cardColor, d.colors.headerText)
		}
		label, _ := syscall.UTF16PtrFromString("Show details")
		if toggle, _, _ := a11yFindWindowEx.Call(hwnd, 0, 0, uintptr(unsafe.Pointer(label))); toggle != 0 {
			sendMessage.Call(toggle, 0x00F5, 0, 0) // BM_CLICK
			time.Sleep(300 * time.Millisecond)
		} else {
			t.Error("no Show details toggle")
		}
		var order []uintptr
		for h, _, _ := a11yGetWindow.Call(hwnd, 5); h != 0; h, _, _ = a11yGetWindow.Call(h, 2) { // GW_CHILD, GW_HWNDNEXT
			order = append(order, h)
		}
		// Tab order is the z-order of visible, enabled tab stops; it must run
		// top to bottom, and left to right within a row.
		lastY, lastX := int32(-1), int32(-1)
		var tabbed []string
		for _, h := range order {
			style, _, _ := renderGetStyle.Call(h, ^uintptr(15))
			visible, _, _ := renderIsVisible.Call(h)
			if visible == 0 || style&0x00010000 == 0 { // WS_TABSTOP
				continue
			}
			var r rect
			getWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
			renderMapPoints.Call(0, hwnd, uintptr(unsafe.Pointer(&r)), 2)
			tabbed = append(tabbed, renderText(h))
			if r.Top < lastY || r.Top == lastY && r.Left < lastX {
				t.Errorf("Tab reaches %q at (%d,%d) after a control at (%d,%d): %q", renderText(h), r.Left, r.Top, lastX, lastY, tabbed)
			}
			lastY, lastX = r.Top, r.Left
		}
		if len(tabbed) != tabs { // [scrolling body], key, budget, Hide details, details, two answers
			t.Errorf("tab stops %q", tabbed)
		}
		// Every visible control a person can reach or read has a UI
		// Automation name. An empty status line carries nothing to read.
		names, err := uiaNames(order)
		if err != nil {
			t.Fatalf("UI Automation: %v", err)
		}
		for i, h := range order {
			if visible, _, _ := renderIsVisible.Call(h); visible == 0 {
				continue
			}
			class := make([]uint16, 64)
			renderGetClassName.Call(h, uintptr(unsafe.Pointer(&class[0])), 64)
			if syscall.UTF16ToString(class) == "Static" && renderText(h) == "" {
				continue
			}
			if names[i] == "" {
				t.Errorf("visible %s %q has no UI Automation name", syscall.UTF16ToString(class), renderText(h))
			}
		}
		if got := names[indexOfID(order, permissionTextID)]; got != "Details" {
			t.Errorf("details Edit is named %q", got)
		}
		if i := indexOfID(order, permissionOverflowID); overflow != (i >= 0) {
			t.Errorf("scrolling body present %t, want %t", i >= 0, overflow)
		} else if overflow && names[i] != prompt.Instruction {
			t.Errorf("scrolling body is named %q", names[i])
		}
		// After a DPI change the credential form uses the dialog's new body font.
		var r rect
		getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		sendMessage.Call(hwnd, permissionWMDPI, uintptr(144|144<<16), uintptr(unsafe.Pointer(&r)))
		time.Sleep(300 * time.Millisecond)
		key, _, _ := permissionGetItem.Call(hwnd, credentialKeyID)
		button := order[indexOfID(order, firstQuestionButton)]
		keyFont, _, _ := sendMessage.Call(key, 0x0031, 0, 0) // WM_GETFONT
		bodyFont, _, _ := sendMessage.Call(button, 0x0031, 0, 0)
		if key == 0 || keyFont == 0 || keyFont != bodyFont {
			t.Errorf("credential key font %x after a DPI change, body font %x", keyFont, bodyFont)
		}
	})
	if err := <-shown; err != nil {
		t.Fatal(err)
	}
}

func indexOfID(order []uintptr, id int) int {
	getID := user32.NewProc("GetDlgCtrlID")
	for i, h := range order {
		if v, _, _ := getID.Call(h); int(v) == id {
			return i
		}
	}
	return -1
}

// uiaNames reads IUIAutomationElement.CurrentName for each window, as a
// screen reader does.
func uiaNames(handles []uintptr) ([]string, error) {
	if hr, _, _ := uiaCoInitialize.Call(0, 0); int32(hr) < 0 {
		return nil, syscall.Errno(hr)
	}
	defer uiaCoUninit.Call()
	var automation uintptr
	if hr, _, _ := uiaCoCreate.Call(uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, 1, uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&automation))); int32(hr) < 0 {
		return nil, syscall.Errno(hr)
	}
	defer comMethod(automation, 2).call(automation)
	names := make([]string, len(handles))
	for i, h := range handles {
		var element uintptr
		if hr := comMethod(automation, 6).call(automation, h, uintptr(unsafe.Pointer(&element))); int32(hr) < 0 || element == 0 { // ElementFromHandle
			return nil, syscall.Errno(hr)
		}
		var name *uint16
		hr := comMethod(element, 23).call(element, uintptr(unsafe.Pointer(&name))) // get_CurrentName
		comMethod(element, 2).call(element)
		if int32(hr) < 0 {
			return nil, syscall.Errno(hr)
		}
		if name != nil {
			names[i] = windows16(name)
			a11yFreeString.Call(uintptr(unsafe.Pointer(name)))
		}
	}
	return names, nil
}

func windows16(p *uint16) string { return windows.UTF16PtrToString(p) }
