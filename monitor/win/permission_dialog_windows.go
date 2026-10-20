//go:build windows

package win

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errPermissionViewport = errors.New("permission actions need the legacy scrolling native layout")

var (
	permissionDialogBox    = user32.NewProc("DialogBoxIndirectParamW")
	permissionEndDialog    = user32.NewProc("EndDialog")
	permissionSetTimer     = user32.NewProc("SetTimer")
	permissionKillTimer    = user32.NewProc("KillTimer")
	permissionEnableWindow = user32.NewProc("EnableWindow")
	permissionIsEnabled    = user32.NewProc("IsWindowEnabled")
	permissionGetFocus     = user32.NewProc("GetFocus")
	permissionGetDC        = user32.NewProc("GetDC")
	permissionReleaseDC    = user32.NewProc("ReleaseDC")
	permissionDrawText     = user32.NewProc("DrawTextW")
	permissionFillRect     = user32.NewProc("FillRect")
	permissionDrawFocus    = user32.NewProc("DrawFocusRect")
	permissionInvalidate   = user32.NewProc("InvalidateRect")
	permissionBeginPaint   = user32.NewProc("BeginPaint")
	permissionEndPaint     = user32.NewProc("EndPaint")
	permissionMonitor      = user32.NewProc("MonitorFromWindow")
	permissionMonitorRect  = user32.NewProc("MonitorFromRect")
	permissionMonitorInfo  = user32.NewProc("GetMonitorInfoW")
	permissionGetSysColor  = user32.NewProc("GetSysColor")
	permissionThreadDPI    = user32.NewProc("SetThreadDpiAwarenessContext")
	permissionDPIBehavior  = user32.NewProc("SetDialogDpiChangeBehavior")
	permissionSetLong      = user32.NewProc("SetWindowLongPtrW")
	permissionSelectObject = gdi32.NewProc("SelectObject")
	permissionDeleteObject = gdi32.NewProc("DeleteObject")
	permissionCreatePen    = gdi32.NewProc("CreatePen")
	permissionBkMode       = gdi32.NewProc("SetBkMode")
	permissionDialogProc   = syscall.NewCallback(permissionDialogDispatch)
	activePermissionDialog atomic.Uintptr
	permissionOccurrence   atomic.Uint64
	permissionDialogs      = struct {
		sync.Mutex
		pending map[uintptr]*permissionDialog
		windows map[uintptr]*permissionDialog
	}{pending: map[uintptr]*permissionDialog{}, windows: map[uintptr]*permissionDialog{}}
)

const (
	permissionWMClose        = 0x0010
	permissionWMInit         = 0x0110
	permissionWMTimer        = 0x0113
	permissionWMNotify       = 0x004e
	permissionWMPaint        = 0x000f
	permissionWMErase        = 0x0014
	permissionWMNCDestroy    = 0x0082
	permissionWMDPI          = 0x02e0
	permissionDetailsID      = 20
	permissionTextID         = 21
	permissionTimerID        = 1
	permissionDMSetDefID     = 0x0401
	permissionDMGetDefID     = 0x0400
	permissionDIP            = 96
	permissionDrawWrap       = 0x0010 | 0x0800 // DT_WORDBREAK | DT_NOPREFIX
	permissionDrawCalc       = 0x0400
	permissionButtonGetState = 0x00f2
	permissionCDPrePaint     = 1
	permissionCDSkipDefault  = 4
	permissionNMCustomDraw   = uint32(0xfffffff4) // NM_FIRST - 12
)

func windowsVersion() *windows.OsVersionInfoEx { return windows.RtlGetVersion() }

// RtlGetVersion ignores ProductName and manifest version aliases. Server and
// missing/unknown version evidence retain the existing TaskDialog renderer.
func modernPermissionFor(prompt QuestionPrompt, version *windows.OsVersionInfoEx) bool {
	return prompt.Permission && !prompt.AppChoice && version != nil && version.ProductType == 1 &&
		version.MajorVersion == 10 && version.MinorVersion == 0 && version.BuildNumber >= 22000
}

type permissionMonitorRecord struct {
	Size          uint32
	Monitor, Work rect
	Flags         uint32
}

type permissionPaintRecord struct {
	DC                 uintptr
	Erase              int32
	Paint              rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}

type permissionNotifyHeader struct {
	From, ID uintptr
	Code     uint32
}

type permissionCustomDraw struct {
	Header permissionNotifyHeader
	Stage  uint32
	DC     uintptr
	Bounds rect
	Item   uintptr
	State  uint32
	Param  uintptr
}

type permissionBlock struct {
	hwnd     uintptr
	text     string
	font     uintptr
	fontKind int
	card     bool
	header   bool
	bounds   rect
}

type permissionDialog struct {
	prompt                          QuestionPrompt
	result                          bool
	footer                          string
	hwnd                            uintptr
	start                           time.Time
	dpi                             int
	err                             error
	blocks                          []permissionBlock
	buttons                         []uintptr
	details, detailToggle           uintptr
	expanded, overflow              bool
	card, action                    rect
	iconBounds                      rect
	requesterIcon                   uintptr
	iconOwned, dark, consentPair    bool
	iconFailed                      bool
	colors                          permissionColors
	fonts                           []uintptr
	background, cardBrush           uintptr
	contrast                        bool
	bgColor, cardColor, ink, border uint32
}

// The modal dialog uses EndDialog. It never posts WM_QUIT or borrows the
// resident Window class whose destruction terminates the tray message loop.
// The lParam is a bounded occurrence key, keeping Go pointers out of Win32.
func showPermissionDialog(prompt QuestionPrompt, result bool) (string, error) {
	if len(prompt.Choices) == 0 || len(prompt.Choices) > 128 {
		return "", fmt.Errorf("question requires between 1 and 128 answer options")
	}
	texts := []string{prompt.Title, prompt.Instruction, prompt.Content, prompt.Details, prompt.Footer}
	if v := prompt.PermissionView; v != nil {
		texts = append(texts, v.Question, v.Requester, v.Publisher, v.Scope, v.Effects)
		if v.Warning != nil {
			texts = append(texts, *v.Warning)
		}
	}
	for _, c := range prompt.Choices {
		if c.Label == "" || c.Value == "" {
			return "", fmt.Errorf("question options require display labels and values")
		}
		texts = append(texts, c.Label, c.Value)
	}
	for _, text := range texts {
		if _, err := syscall.UTF16FromString(text); err != nil {
			return "", fmt.Errorf("encode permission text: %w", err)
		}
	}
	var previous uintptr
	if permissionThreadDPI.Find() == nil {
		previous, _, _ = permissionThreadDPI.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
		if previous != 0 {
			defer permissionThreadDPI.Call(previous)
		}
	}
	d := &permissionDialog{prompt: prompt, result: result, footer: permissionVisibleFooter(prompt, result), dpi: 96}
	key := uintptr(permissionOccurrence.Add(1))
	permissionDialogs.Lock()
	permissionDialogs.pending[key] = d
	permissionDialogs.Unlock()
	defer func() {
		permissionDialogs.Lock()
		delete(permissionDialogs.pending, key)
		permissionDialogs.Unlock()
	}()
	template := permissionTemplate()
	instance, _, _ := getModuleHandle.Call(0)
	selected, _, callErr := permissionDialogBox.Call(instance, uintptr(unsafe.Pointer(&template[0])), prompt.parent, permissionDialogProc, key)
	runtime.KeepAlive(template)
	runtime.KeepAlive(d)
	if d.err != nil {
		return "", d.err
	}
	if selected == ^uintptr(0) {
		return "", fmt.Errorf("show Windows 11 permission dialog: %w", callErr)
	}
	if selected == 0 {
		return "", fmt.Errorf("show Windows 11 permission dialog: invalid native owner")
	}
	index := int(selected) - firstQuestionButton
	if index >= 0 && index < len(prompt.Choices) {
		return prompt.Choices[index].Value, nil
	}
	return "", nil
}

// DLGTEMPLATE is WORD-packed, with a DWORD-aligned start. There are no
// template controls or DS_SETFONT: all control geometry uses one pixel scaler.
func permissionTemplate() [6]uint32 {
	var words [6]uint32
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), 24)
	binary.LittleEndian.PutUint32(bytes, 0x80000000|0x00c00000|0x00080000|0x00000080|0x02000000)
	binary.LittleEndian.PutUint16(bytes[14:], 300)
	binary.LittleEndian.PutUint16(bytes[16:], 200)
	return words
}

func permissionDialogDispatch(hwnd, msg, wparam, lparam uintptr) uintptr {
	permissionDialogs.Lock()
	d := permissionDialogs.windows[hwnd]
	if msg == permissionWMInit {
		d = permissionDialogs.pending[lparam]
		if d != nil {
			permissionDialogs.windows[hwnd] = d
		}
	}
	permissionDialogs.Unlock()
	if d == nil {
		return 0
	}
	switch msg {
	case permissionWMInit:
		d.hwnd = hwnd
		activePermissionDialog.Store(hwnd)
		if err := d.initialize(); err != nil {
			d.err = err
			permissionEndDialog.Call(hwnd, ^uintptr(0))
			return 1
		}
		return 0 // Focus was explicitly set while answers are unarmed.
	case wmCommand:
		id := int32(wparam & 0xffff)
		if (wparam >> 16) != 0 {
			return 0
		}
		switch id {
		case idcCancel:
			permissionEndDialog.Call(hwnd, idcCancel)
		case permissionDetailsID:
			d.expanded = !d.expanded
			label := "Show details"
			if d.expanded {
				label = "Hide details"
			}
			setWindowText.Call(d.detailToggle, uintptr(unsafe.Pointer(utf16(label))))
			d.layout(nil)
		default:
			index := int(id) - firstQuestionButton
			if permissionAnswerAllowed(d.result, time.Since(d.start), index, len(d.prompt.Choices)) {
				permissionEndDialog.Call(hwnd, uintptr(id))
			}
		}
		return 1
	case permissionWMClose:
		permissionEndDialog.Call(hwnd, idcCancel)
		return 1
	case permissionWMTimer:
		if wparam == permissionTimerID && time.Since(d.start) >= questionArmDelay*time.Millisecond {
			for _, button := range d.buttons {
				permissionEnableWindow.Call(button, 1)
			}
			permissionKillTimer.Call(hwnd, permissionTimerID)
			def := permissionDefaultButton(d.prompt, d.result)
			sendMessage.Call(hwnd, permissionDMSetDefID, uintptr(def), 0)
			if focus, _, _ := permissionGetFocus.Call(); focus == d.cancelButton() || focus == hwnd || focus == 0 {
				setFocus.Call(d.item(int(def)))
			}
		}
		return 1
	case permissionWMDPI:
		if lparam != 0 && int(wparam&0xffff) >= 96 {
			d.dpi = int(wparam & 0xffff)
			d.replaceFonts()
			d.loadRequesterIcon()
			d.layout((*rect)(unsafe.Pointer(lparam)))
		}
		return 1
	case wmThemeChanged, wmSettingChange:
		d.palette()
		d.frameTheme()
		permissionRedraw.Call(hwnd, 0, 0, 0x0001|0x0004|0x0080) // INVALIDATE | ERASE | ALLCHILDREN
		return 1
	case permissionWMPaint:
		var paint permissionPaintRecord
		dc, _, _ := permissionBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		d.paint(dc)
		permissionEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&paint)))
		return 1
	case permissionWMErase:
		d.paint(wparam)
		return 1
	case wmCtlColorStatic, wmCtlColorEdit:
		setTextColor.Call(wparam, uintptr(d.ink))
		color, brush := d.bgColor, d.background
		for _, b := range d.blocks {
			if b.hwnd == lparam && b.header {
				color, brush = d.cardColor, d.cardBrush
				setTextColor.Call(wparam, uintptr(d.colors.headerText))
				break
			}
		}
		setBkColor.Call(wparam, uintptr(color))
		return brush
	case permissionWMNotify:
		if lparam != 0 && !d.contrast {
			draw := (*permissionCustomDraw)(unsafe.Pointer(lparam))
			if draw.Header.Code == permissionNMCustomDraw && draw.Stage == permissionCDPrePaint && d.isButton(draw.Header.From) {
				d.paintButton(draw)
				// Dialog procedures return TRUE and place notification results
				// in DWLP_MSGRESULT, rather than returning CDRF_SKIPDEFAULT.
				if permissionSetLong.Find() == nil {
					permissionSetLong.Call(hwnd, 0, permissionCDSkipDefault)
				}
				return 1
			}
		}
	case permissionWMNCDestroy:
		permissionKillTimer.Call(hwnd, permissionTimerID)
		d.freeResources()
		activePermissionDialog.CompareAndSwap(hwnd, 0)
		permissionDialogs.Lock()
		delete(permissionDialogs.windows, hwnd)
		permissionDialogs.Unlock()
	}
	return 0
}

func (d *permissionDialog) initialize() error {
	if unsafe.Sizeof(uintptr(0)) == 4 {
		permissionSetLong = user32.NewProc("SetWindowLongW")
	}
	setWindowText.Call(d.hwnd, uintptr(unsafe.Pointer(utf16(d.prompt.Title))))
	if dpiForWindow.Find() == nil {
		if dpi, _, _ := dpiForWindow.Call(d.hwnd); dpi >= 96 {
			d.dpi = int(dpi)
		}
	}
	// Opt out of Dialog Manager's automatic control/font scaling. Our measured
	// layout owns WM_DPICHANGED and rebuilds every control exactly once.
	if permissionDPIBehavior.Find() == nil {
		permissionDPIBehavior.Call(d.hwnd, 1, 1)
	}
	d.palette()
	d.replaceFonts()
	d.consentPair = !d.result && permissionConsentPair(d.prompt.Choices)
	instance, _, _ := getModuleHandle.Call(0)
	sendMessage.Call(d.hwnd, 0x0080, 0, loadSharedTaskDialogIcon(instance)) // WM_SETICON / ICON_SMALL
	d.frameTheme()
	add := func(text string, font int, card bool) error {
		if text == "" {
			return nil
		}
		h, err := d.control("STATIC", text, 0x0080, 30+len(d.blocks)) // SS_NOPREFIX
		if err != nil {
			return err
		}
		d.blocks = append(d.blocks, permissionBlock{hwnd: h, text: text, font: d.fonts[font], fontKind: font, card: card})
		return nil
	}
	if d.result {
		if err := add("OpenAbstractions", 0, false); err != nil {
			return err
		}
	}
	heading := d.prompt.Instruction
	if v := d.prompt.PermissionView; v != nil && v.Question != "" {
		heading = v.Question
	}
	if err := add(heading, 1, false); err != nil {
		return err
	}
	if heading != "" {
		d.blocks[len(d.blocks)-1].header = true
	}
	if v := d.prompt.PermissionView; v != nil {
		d.loadRequesterIcon()
		if err := add(v.Requester, 2, true); err != nil {
			return err
		}
		if err := add(v.Publisher, 0, true); err != nil {
			return err
		}
		if err := add(strings.TrimSpace(v.Scope+"\n\n"+v.Effects), 0, false); err != nil {
			return err
		}
	} else {
		if err := add(d.prompt.Content, 0, false); err != nil {
			return err
		}
	}
	// A read-only multiline Edit supplies native scrolling and keyboard text
	// access for exact details and for arbitrarily long visible permission text.
	var err error
	d.details, err = d.control("EDIT", d.prompt.Details, wsTabStop|0x0004|0x0040|0x0800|0x00200000, permissionTextID)
	if err != nil {
		return err
	}
	showWindow.Call(d.details, 0)
	if d.prompt.Details != "" {
		d.detailToggle, err = d.control("BUTTON", "Show details", wsTabStop|0x8000|0x0100, permissionDetailsID) // BS_FLAT | BS_LEFT
		if err != nil {
			return err
		}
	}
	for i, choice := range d.prompt.Choices {
		style := uint32(wsTabStop | 0x2000) // BS_MULTILINE
		if permissionDefaultButton(d.prompt, d.result) == int32(firstQuestionButton+i) {
			style |= 1
		}
		h, err := d.control("BUTTON", permissionChoiceLabel(choice, d.consentPair), style, firstQuestionButton+i)
		if err != nil {
			return err
		}
		d.buttons = append(d.buttons, h)
		if !d.result {
			permissionEnableWindow.Call(h, 0)
		}
	}
	if !d.consentPair {
		if _, err = d.control("BUTTON", "Cancel", wsTabStop|0x2000, idcCancel); err != nil {
			return err
		}
	}
	d.layout(nil)
	if d.err != nil {
		return d.err
	}
	d.start = time.Now() // monotonic; never take arming time from window messages
	if !d.result {
		if timer, _, callErr := permissionSetTimer.Call(d.hwnd, permissionTimerID, 50, 0); timer == 0 {
			return fmt.Errorf("arm permission controls: %w", callErr)
		}
	}
	setWindowPos.Call(d.hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
	initial := d.cancelButton()
	if initial == 0 {
		initial = d.hwnd
	}
	setFocus.Call(initial)
	// A disabled safe default ignores Enter before the monotonic arm delay.
	sendMessage.Call(d.hwnd, permissionDMSetDefID, uintptr(permissionDefaultButton(d.prompt, d.result)), 0)
	if !d.result {
		d.prompt.chime.ring() // the question is now shown; a result dialog stays silent
	}
	return nil
}

// loadRequesterIcon (re)extracts the requester's icon at the layout's 32 DIP
// size for the current DPI, freeing the previous one. A program whose icon
// failed or timed out once is not read again for this dialog, so a DPI change
// cannot start another stuck extraction.
func (d *permissionDialog) loadRequesterIcon() {
	v := d.prompt.PermissionView
	if v == nil {
		return
	}
	d.freeRequesterIcon()
	program := v.RequesterProgram
	if d.iconFailed {
		program = ""
	}
	var fromProgram bool
	d.requesterIcon, d.iconOwned, fromProgram = permissionRequesterIcon(program, d.px(32))
	if program != "" && !fromProgram {
		d.iconFailed = true
	}
}

func (d *permissionDialog) freeRequesterIcon() {
	if d.iconOwned && d.requesterIcon != 0 {
		permissionDestroyIcon.Call(d.requesterIcon)
	}
	d.requesterIcon, d.iconOwned = 0, false
}

func permissionAnswerAllowed(result bool, elapsed time.Duration, index, count int) bool {
	return index >= 0 && index < count && (result || elapsed >= questionArmDelay*time.Millisecond)
}

func (d *permissionDialog) control(class, text string, style uint32, id int) (uintptr, error) {
	if class == "EDIT" {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	}
	instance, _, _ := getModuleHandle.Call(0)
	h, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(text))),
		uintptr(wsChild|wsVisible|style), 0, 0, 1, 1, d.hwnd, uintptr(id), instance, 0)
	if h == 0 {
		return 0, fmt.Errorf("create permission %s control: %w", class, err)
	}
	sendMessage.Call(h, wmSetFont, d.fonts[0], 1)
	return h, nil
}

func (d *permissionDialog) px(n int) int { return (n*d.dpi + 48) / 96 }

func (d *permissionDialog) replaceFonts() {
	old := d.fonts
	d.fonts = nil
	for _, pair := range [][2]int{{14, 400}, {18, 600}, {16, 600}} {
		h, _, _ := createFont.Call(uintptr(-d.px(pair[0])), 0, 0, 0, uintptr(pair[1]), 0, 0, 0,
			defaultCharSet, 0, 0, clearTypeQuality, 0, uintptr(unsafe.Pointer(utf16("Segoe UI"))))
		d.fonts = append(d.fonts, h)
	}
	for i := range d.blocks {
		font := d.blocks[i].fontKind
		d.blocks[i].font = d.fonts[font]
		sendMessage.Call(d.blocks[i].hwnd, wmSetFont, d.fonts[font], 1)
	}
	for _, h := range append(append([]uintptr{d.details, d.detailToggle, d.cancelButton(), d.item(23)}, d.buttons...), d.overflowControl()) {
		if h != 0 {
			sendMessage.Call(h, wmSetFont, d.fonts[0], 1)
		}
	}
	for _, h := range old {
		if h != 0 {
			permissionDeleteObject.Call(h)
		}
	}
}

func (d *permissionDialog) measure(text string, font uintptr, width int) int {
	if text == "" {
		return 0
	}
	r := d.measureBounds(text, font, width)
	return max(d.px(20), int(r.Bottom-r.Top))
}

func (d *permissionDialog) measureBounds(text string, font uintptr, width int) rect {
	if text == "" {
		return rect{}
	}
	dc, _, _ := permissionGetDC.Call(d.hwnd)
	defer permissionReleaseDC.Call(d.hwnd, dc)
	previous, _, _ := permissionSelectObject.Call(dc, font)
	defer permissionSelectObject.Call(dc, previous)
	r := rect{Right: int32(width)}
	u, _ := syscall.UTF16FromString(text)
	permissionDrawText.Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), permissionDrawWrap|permissionDrawCalc)
	return r
}

func (d *permissionDialog) workArea(suggested *rect) rect {
	monitor, _, _ := permissionMonitor.Call(d.hwnd, 2)
	if suggested != nil {
		monitor, _, _ = permissionMonitorRect.Call(uintptr(unsafe.Pointer(suggested)), 2)
	}
	info := permissionMonitorRecord{Size: uint32(unsafe.Sizeof(permissionMonitorRecord{}))}
	if ok, _, _ := permissionMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&info))); ok != 0 {
		return info.Work
	}
	return rect{Right: 1024, Bottom: 768}
}

func (d *permissionDialog) layout(suggested *rect) {
	work := d.workArea(suggested)
	maxWidth := int(work.Right-work.Left) - d.px(32)
	width := min(d.px(560), maxWidth)
	pad, gap := d.px(20), d.px(12)
	contentWidth := max(1, width-2*pad)
	var outer, client rect
	getWindowRect.Call(d.hwnd, uintptr(unsafe.Pointer(&outer)))
	getClientRect.Call(d.hwnd, uintptr(unsafe.Pointer(&client)))
	frameW := int(outer.Right - outer.Left - client.Right)
	frameH := int(outer.Bottom - outer.Top - client.Bottom)
	maxHeight := int(work.Bottom-work.Top) - d.px(32) - frameH
	// Every answer remains present. Long/unknown options which cannot fit the
	// work area use the retained TaskDialog instead of clipping or hiding them.
	buttonWidths := make([]int, 0, len(d.buttons)+1)
	for _, choice := range d.prompt.Choices {
		label := permissionChoiceLabel(choice, d.consentPair)
		labelWidth := d.measureWidth(label)
		w := max(d.px(100), min(contentWidth, labelWidth+d.px(28)))
		if d.consentPair {
			w = (contentWidth - d.px(8)) / 2
		}
		if labelWidth+d.px(16) > w || d.measure(label, d.fonts[0], w-d.px(16)) > d.px(28) {
			d.err = errPermissionViewport
			permissionEndDialog.Call(d.hwnd, ^uintptr(0))
			return
		}
		buttonWidths = append(buttonWidths, w)
	}
	if !d.consentPair {
		buttonWidths = append(buttonWidths, d.px(100))
	}
	positions, actionHeight := permissionButtonLayout(buttonWidths, contentWidth, d.px(8), d.px(40))
	actionHeight += pad * 2
	toggleHeight := 0
	if d.detailToggle != 0 {
		toggleHeight = d.px(28) + gap
	}
	footerHeight := d.measure(d.footer, d.fonts[0], contentWidth)
	reserved := permissionReservedHeight(actionHeight, footerHeight, toggleHeight, gap)
	detailsHeight := 0
	if d.expanded {
		detailsHeight = min(d.px(180), max(0, maxHeight/3))
	}
	bodyLimit, fits := permissionTextBudget(maxHeight, reserved, pad, gap, detailsHeight, d.px(80))
	if !fits {
		d.err = errPermissionViewport
		permissionEndDialog.Call(d.hwnd, ^uintptr(0))
		return
	}
	y := 0
	wideBody := false
	d.card = rect{}
	d.iconBounds = rect{}
	for i := range d.blocks {
		b := &d.blocks[i]
		w, x := contentWidth, pad
		if b.card {
			w -= d.px(48)
			x += d.px(48)
		}
		if b.header {
			d.card = rect{Top: int32(y), Right: int32(width)}
			y += pad
		}
		if b.card && (i == 0 || !d.blocks[i-1].card) {
			d.iconBounds = rect{Left: int32(pad), Top: int32(y), Right: int32(pad + d.px(32)), Bottom: int32(y + d.px(32))}
		}
		measured := d.measureBounds(b.text, b.font, w)
		if int(measured.Right-measured.Left) > w {
			wideBody = true
		}
		h := max(d.px(20), int(measured.Bottom-measured.Top))
		b.bounds = rect{Left: int32(x), Top: int32(y), Right: int32(x + w), Bottom: int32(y + h)}
		y += h
		if b.header {
			y += pad
			d.card.Bottom = int32(y)
		}
		if b.card && (i+1 == len(d.blocks) || !d.blocks[i+1].card) {
			y = max(y, int(d.iconBounds.Bottom))
		}
		if b.card && i+1 < len(d.blocks) && d.blocks[i+1].card {
			y += d.px(4)
		} else {
			y += gap
		}
	}
	d.overflow = wideBody || y > bodyLimit+pad
	if d.overflow {
		// The exact full visible content is retained in a selectable, scrollable
		// Edit; no requester, qualifier, publisher or effect is truncated away.
		all := make([]string, 0, len(d.blocks))
		for _, b := range d.blocks {
			if !b.header {
				all = append(all, b.text)
			}
		}
		h := d.ensureOverflow(strings.Join(all, "\r\n\r\n"))
		setWindowPos.Call(h, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|0x0010)
		for _, b := range d.blocks {
			if !b.header {
				showWindow.Call(b.hwnd, 0)
			} else {
				r := b.bounds
				moveWindow.Call(b.hwnd, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 1)
				showWindow.Call(b.hwnd, 1)
			}
		}
		bodyTop := max(pad, int(d.card.Bottom)+gap)
		y = pad + bodyLimit
		if y-bodyTop < d.px(40) {
			d.err = errPermissionViewport
			permissionEndDialog.Call(d.hwnd, ^uintptr(0))
			return
		}
		moveWindow.Call(h, uintptr(pad), uintptr(bodyTop), uintptr(contentWidth), uintptr(y-bodyTop), 1)
		showWindow.Call(h, 1)
	} else {
		if h := d.overflowControl(); h != 0 {
			showWindow.Call(h, 0)
		}
		for _, b := range d.blocks {
			r := b.bounds
			sendMessage.Call(b.hwnd, wmSetFont, b.font, 0)
			moveWindow.Call(b.hwnd, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 1)
			showWindow.Call(b.hwnd, 1)
		}
	}
	if d.detailToggle != 0 {
		width := max(d.measureWidth("Show details"), d.measureWidth("Hide details")) + d.px(16)
		moveWindow.Call(d.detailToggle, uintptr(pad), uintptr(y), uintptr(width), uintptr(d.px(28)), 1)
		y += toggleHeight
	}
	if d.expanded {
		h := detailsHeight
		moveWindow.Call(d.details, uintptr(pad), uintptr(y), uintptr(contentWidth), uintptr(h), 1)
		showWindow.Call(d.details, 1)
		y += h + gap
	} else {
		showWindow.Call(d.details, 0)
	}
	if d.footer != "" {
		h := d.footerControl()
		moveWindow.Call(h, uintptr(pad), uintptr(y), uintptr(contentWidth), uintptr(footerHeight), 1)
		y += footerHeight + gap
	}
	d.action = rect{Top: int32(y), Right: int32(width), Bottom: int32(y + actionHeight)}
	buttons := append([]uintptr{}, d.buttons...)
	if !d.consentPair {
		buttons = append(buttons, d.cancelButton())
	}
	for i, h := range buttons {
		r := positions[i]
		moveWindow.Call(h, uintptr(pad+int(r.Left)), uintptr(y+pad+int(r.Top)), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 1)
	}
	height := y + actionHeight
	x, top := int(work.Left)+(int(work.Right-work.Left)-width-frameW)/2, int(work.Top)+(int(work.Bottom-work.Top)-height-frameH)/2
	if suggested != nil {
		x, top = int(suggested.Left), int(suggested.Top)
	} else if d.start != (time.Time{}) {
		x, top = int(outer.Left), int(outer.Top)
	}
	x = max(int(work.Left), min(x, int(work.Right)-width-frameW))
	top = max(int(work.Top), min(top, int(work.Bottom)-height-frameH))
	setWindowPos.Call(d.hwnd, 0, uintptr(x), uintptr(top), uintptr(width+frameW), uintptr(height+frameH), 0x0004|0x0010)
	permissionInvalidate.Call(d.hwnd, 0, 1)
}

func permissionVisibleFooter(prompt QuestionPrompt, result bool) string {
	if !result && prompt.Permission && prompt.PermissionView != nil && prompt.PermissionView.Warning != nil {
		return *prompt.PermissionView.Warning
	}
	return prompt.Footer
}

func permissionReservedHeight(actions, footer, toggle, gap int) int {
	reserved := actions + toggle
	if footer > 0 {
		reserved += footer + gap
	}
	return reserved
}

// Include every expanded region before deciding whether the minimum scrolling
// body and all action rows fit. A viewport fallback preserves all choices.
func permissionTextBudget(maxHeight, reserved, pad, gap, detailsHeight, minimumBody int) (int, bool) {
	body := maxHeight - reserved - pad
	if detailsHeight > 0 {
		body -= detailsHeight + gap
	}
	return body, body >= minimumBody
}

// Native buttons keep their HWND, names, tab order, keyboard and default
// behavior; this layout only assigns rows and pixel bounds.
func permissionButtonLayout(widths []int, width, gap, height int) ([]rect, int) {
	positions := make([]rect, len(widths))
	x, y := 0, 0
	for i, desired := range widths {
		w := min(width, desired)
		if x > 0 && x+w > width {
			x = 0
			y += height + gap
		}
		positions[i] = rect{int32(x), int32(y), int32(x + w), int32(y + height)}
		x += w + gap
	}
	return positions, y + height
}

func (d *permissionDialog) measureWidth(text string) int {
	dc, _, _ := permissionGetDC.Call(d.hwnd)
	defer permissionReleaseDC.Call(d.hwnd, dc)
	old, _, _ := permissionSelectObject.Call(dc, d.fonts[0])
	defer permissionSelectObject.Call(dc, old)
	r := rect{}
	u, _ := syscall.UTF16FromString(text)
	permissionDrawText.Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), 0x0020|0x0800|permissionDrawCalc)
	return int(r.Right)
}

var permissionGetItem = user32.NewProc("GetDlgItem")

func (d *permissionDialog) item(id int) uintptr {
	h, _, _ := permissionGetItem.Call(d.hwnd, uintptr(id))
	return h
}
func (d *permissionDialog) cancelButton() uintptr    { return d.item(idcCancel) }
func (d *permissionDialog) overflowControl() uintptr { return d.item(22) }
func (d *permissionDialog) ensureOverflow(text string) uintptr {
	h := d.overflowControl()
	if h == 0 {
		var err error
		h, err = d.control("EDIT", text, wsTabStop|0x0004|0x0040|0x0800|0x00200000, 22)
		if err != nil {
			d.err = err
			permissionEndDialog.Call(d.hwnd, ^uintptr(0))
			return 0
		}
	}
	setWindowText.Call(h, uintptr(unsafe.Pointer(utf16(text))))
	return h
}
func (d *permissionDialog) footerControl() uintptr {
	h := d.item(23)
	if h == 0 {
		var err error
		h, err = d.control("STATIC", d.footer, 0x0080, 23)
		if err != nil {
			d.err = err
			permissionEndDialog.Call(d.hwnd, ^uintptr(0))
		}
	}
	return h
}

func (d *permissionDialog) palette() {
	for _, h := range []uintptr{d.background, d.cardBrush} {
		if h != 0 {
			permissionDeleteObject.Call(h)
		}
	}
	contrast := highContrast{Size: uint32(unsafe.Sizeof(highContrast{}))}
	ok, _, _ := systemParameters.Call(spiGetHighContrast, uintptr(contrast.Size), uintptr(unsafe.Pointer(&contrast)), 0)
	d.contrast = ok == 0 || contrast.Flags&hcfHighContrastOn != 0
	d.dark = permissionDarkMode()
	d.colors = permissionThemeColors(d.dark, d.prompt.Level)
	if d.contrast {
		color := func(index int) uint32 { c, _, _ := permissionGetSysColor.Call(uintptr(index)); return uint32(c) }
		d.colors.background, d.colors.header, d.colors.text, d.colors.headerText = color(5), color(5), color(8), color(8)
		d.colors.border = color(6)
	}
	d.bgColor, d.cardColor, d.ink, d.border = d.colors.background, d.colors.header, d.colors.text, d.colors.border
	d.background, _, _ = createSolidBrush.Call(uintptr(d.bgColor))
	d.cardBrush, _, _ = createSolidBrush.Call(uintptr(d.cardColor))
}

func (d *permissionDialog) frameTheme() {
	if dwmSetWindowAttribute.Find() == nil {
		dark := uint32(0)
		if d.dark && !d.contrast {
			dark = 1
		}
		for attribute, value := range map[uint32]uint32{dwmUseImmersiveDarkMode: dark, dwmWindowCornerPreference: dwmCornerRound} {
			dwmSetWindowAttribute.Call(d.hwnd, uintptr(attribute), uintptr(unsafe.Pointer(&value)), unsafe.Sizeof(value))
		}
	}
}

func (d *permissionDialog) paint(dc uintptr) {
	var r rect
	getClientRect.Call(d.hwnd, uintptr(unsafe.Pointer(&r)))
	permissionFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), d.background)
	if d.card.Bottom > d.card.Top {
		permissionFillRect.Call(dc, uintptr(unsafe.Pointer(&d.card)), d.cardBrush)
	}
	if !d.overflow && d.requesterIcon != 0 && d.iconBounds.Bottom > d.iconBounds.Top {
		r := d.iconBounds
		permissionDrawIcon.Call(dc, uintptr(r.Left), uintptr(r.Top), d.requesterIcon, uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0, 0, 3) // DI_NORMAL
	}
}

func (d *permissionDialog) rectangle(dc uintptr, r rect, fill, border uint32, thickness int) {
	brush, _, _ := createSolidBrush.Call(uintptr(fill))
	pen, _, _ := permissionCreatePen.Call(0, uintptr(max(1, d.px(thickness))), uintptr(border))
	oldBrush, _, _ := permissionSelectObject.Call(dc, brush)
	oldPen, _, _ := permissionSelectObject.Call(dc, pen)
	permissionRectangle.Call(dc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
	permissionSelectObject.Call(dc, oldBrush)
	permissionSelectObject.Call(dc, oldPen)
	permissionDeleteObject.Call(brush)
	permissionDeleteObject.Call(pen)
}

func (d *permissionDialog) isButton(h uintptr) bool {
	if h == d.cancelButton() || h == d.detailToggle {
		return true
	}
	for _, b := range d.buttons {
		if h == b {
			return true
		}
	}
	return false
}

func (d *permissionDialog) paintButton(draw *permissionCustomDraw) {
	fill, border, ink := d.colors.button, d.colors.border, d.ink
	link := draw.Header.From == d.detailToggle
	enabled, _, _ := permissionIsEnabled.Call(draw.Header.From)
	state, _, _ := sendMessage.Call(draw.Header.From, permissionButtonGetState, 0, 0)
	if draw.State&0x40 != 0 {
		fill = d.colors.hover
	}
	if state&4 != 0 {
		fill = d.colors.pressed
	}
	if enabled == 0 {
		fill, ink = d.colors.disabled, d.colors.disabledText
	}
	defaultID, _, _ := sendMessage.Call(d.hwnd, permissionDMGetDefID, 0, 0)
	thickness := 1
	if enabled != 0 && (defaultID&0xffff) == draw.Header.ID {
		fill, border, ink = d.colors.accent, d.colors.accent, d.colors.accentText
		thickness = 2
	}
	if link {
		permissionFillRect.Call(draw.DC, uintptr(unsafe.Pointer(&draw.Bounds)), d.background)
		ink = d.colors.link
	} else {
		d.rectangle(draw.DC, draw.Bounds, fill, border, thickness)
	}
	old, _, _ := permissionSelectObject.Call(draw.DC, d.fonts[0])
	defer permissionSelectObject.Call(draw.DC, old)
	setTextColor.Call(draw.DC, uintptr(ink))
	permissionBkMode.Call(draw.DC, 1)
	length, _, _ := getWindowTextLength.Call(draw.Header.From)
	text := make([]uint16, length+1)
	getWindowText.Call(draw.Header.From, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)))
	r := draw.Bounds
	r.Left += int32(d.px(8))
	r.Right -= int32(d.px(8))
	format := uintptr(0x0001 | 0x0004 | 0x0020 | 0x0800)
	if link {
		format &^= 0x0001 // Keep native Button identity; draw its compact label left-aligned.
	}
	permissionDrawText.Call(draw.DC, uintptr(unsafe.Pointer(&text[0])), length, uintptr(unsafe.Pointer(&r)), format)
	if focus, _, _ := permissionGetFocus.Call(); focus == draw.Header.From {
		r = draw.Bounds
		inset := int32(d.px(4))
		r.Left += inset
		r.Top += inset
		r.Right -= inset
		r.Bottom -= inset
		permissionDrawFocus.Call(draw.DC, uintptr(unsafe.Pointer(&r)))
	}
}

func (d *permissionDialog) freeResources() {
	d.freeRequesterIcon()
	for _, h := range append(d.fonts, d.background, d.cardBrush) {
		if h != 0 {
			permissionDeleteObject.Call(h)
		}
	}
	d.fonts = nil
	d.background = 0
	d.cardBrush = 0
}
