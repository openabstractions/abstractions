//go:build windows

package win

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	identity "github.com/openabstractions/abstraction-identity"
)

func permissionContrast(a, b uint32) float64 {
	luminance := func(c uint32) float64 {
		linear := func(v uint32) float64 {
			s := float64(v) / 255
			if s <= 0.04045 {
				return s / 12.92
			}
			return math.Pow((s+0.055)/1.055, 2.4)
		}
		return .2126*linear(c&255) + .7152*linear((c>>8)&255) + .0722*linear((c>>16)&255)
	}
	x, y := luminance(a), luminance(b)
	return (max(x, y) + .05) / (min(x, y) + .05)
}

func TestPermissionSystemThemePalettesAndContrast(t *testing.T) {
	t.Logf("Observed existing AppsUseLightTheme preference: dark=%t; settings unchanged", appDarkMode())
	levels := []PromptLevel{PromptLevelBlue, PromptLevelGray, PromptLevelYellow, PromptLevelOrange}
	for _, dark := range []bool{false, true} {
		gray := permissionThemeColors(dark, PromptLevelGray)
		bands := map[uint32]PromptLevel{}
		for _, level := range levels {
			c := permissionThemeColors(dark, level)
			if c.background != gray.background || c.accent != gray.accent || c.text != gray.text || c.button != gray.button {
				t.Fatalf("dark=%t level %s changed more than the header", dark, level)
			}
			if other, seen := bands[c.header]; seen {
				t.Fatalf("dark=%t levels %s and %s share a header band", dark, level, other)
			}
			bands[c.header] = level
			if permissionContrast(c.header, c.background) < 1.1 && level != PromptLevelGray {
				t.Fatalf("dark=%t level %s band is invisible against the window", dark, level)
			}
			for _, pair := range [][2]uint32{{c.text, c.background}, {c.headerText, c.header}, {c.accentText, c.accent}, {c.text, c.button}, {c.link, c.background}} {
				if ratio := permissionContrast(pair[0], pair[1]); ratio < 4.5 {
					t.Fatalf("dark=%t level %s colors=%x contrast=%f", dark, level, pair, ratio)
				}
			}
			if permissionContrast(c.border, c.button) < 3 {
				t.Fatal("rectangular action boundary lacks contrast")
			}
		}
	}
}

// The renderer draws each level's own band in the light and dark app themes,
// and a prompt without a known level as yellow.
func TestPermissionLevelHeaderColorsLightAndDark(t *testing.T) {
	for _, tc := range []struct {
		level             PromptLevel
		light, dark       uint32
		lightInk, darkInk uint32
	}{
		{PromptLevelBlue, 0x00b85f00, 0x00923e00, 0x00ffffff, 0x00ffffff},
		{PromptLevelGray, 0x00e8e8e8, 0x00383838, 0x00222222, 0x00ffffff},
		{PromptLevelYellow, 0x0000b9ff, 0x00005c7a, 0x00222222, 0x00ffffff},
		{PromptLevelOrange, 0x000c63f7, 0x00003a9a, 0x00222222, 0x00ffffff},
		{"", 0x0000b9ff, 0x00005c7a, 0x00222222, 0x00ffffff},
		{"red", 0x0000b9ff, 0x00005c7a, 0x00222222, 0x00ffffff},
	} {
		light, dark := permissionThemeColors(false, tc.level), permissionThemeColors(true, tc.level)
		if light.header != tc.light || dark.header != tc.dark || light.headerText != tc.lightInk || dark.headerText != tc.darkInk {
			t.Errorf("level %q: light %06x/%06x dark %06x/%06x", tc.level, light.header, light.headerText, dark.header, dark.headerText)
		}
	}
}

// A dialog takes its header from the prompt's level alone: the publisher text
// and the result flag choose nothing.
func TestPermissionDialogPaletteFollowsPromptLevel(t *testing.T) {
	restore := permissionDarkMode
	t.Cleanup(func() { permissionDarkMode = restore })
	for _, dark := range []bool{false, true} {
		permissionDarkMode = func() bool { return dark }
		for _, level := range []PromptLevel{PromptLevelBlue, PromptLevelGray, PromptLevelYellow, PromptLevelOrange} {
			for _, result := range []bool{false, true} {
				d := &permissionDialog{result: result, prompt: QuestionPrompt{Level: level, Permission: true,
					PermissionView: &PermissionView{Publisher: "Publisher: Microsoft Windows (verified at request)."}}}
				d.palette()
				want := permissionThemeColors(dark, level)
				if !d.contrast && (d.dark != dark || d.cardColor != want.header || d.colors.headerText != want.headerText) {
					t.Fatalf("dark=%t level %s result=%t: band %06x, want %06x", dark, level, result, d.cardColor, want.header)
				}
				for _, h := range []uintptr{d.background, d.cardBrush} {
					permissionDeleteObject.Call(h)
				}
			}
		}
	}
}

func TestPermissionConsentPairSafeLabelsAndDefault(t *testing.T) {
	for _, choices := range [][]QuestionChoice{
		{{"Allow", "allow"}, {"Refuse", "refuse"}}, {{"Refuse", "refuse"}, {"Allow", "allow"}},
	} {
		if !permissionConsentPair(choices) {
			t.Fatal("closed pair not recognized")
		}
		for i, c := range choices {
			if c.Value == "refuse" {
				if permissionChoiceLabel(c, true) != "Don't allow" || permissionDefaultButton(QuestionPrompt{Choices: choices, Default: "allow"}, false) != int32(firstQuestionButton+i) {
					t.Fatal("visible Don't allow lost exact Refuse semantics or safe Enter default")
				}
			}
		}
	}
	for _, choices := range [][]QuestionChoice{
		{{"Allow", "allow"}}, {{"Allow", "allow"}, {"Never", "never"}, {"Refuse", "refuse"}},
		{{"Allow", "allow"}, {"Application's choice", "other"}},
	} {
		if permissionConsentPair(choices) {
			t.Fatal("unknown/extra options must retain all offered controls and Cancel")
		}
		for _, c := range choices {
			if permissionChoiceLabel(c, false) != c.Label {
				t.Fatal("unrecognized option relabeled")
			}
		}
	}
}

func TestPermissionCompactHiddenNativeGeometryAndDefault(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Hidden native HWNDs exercise actual fonts/layout and Dialog Manager
	// defaults. No WS_VISIBLE on the parent, no modal/show call or activation.
	for _, dpi := range []int{96, 144, 192} {
		for _, expanded := range []bool{false, true} {
			for _, long := range []bool{false, true} {
				instance, _, _ := getModuleHandle.Call(0)
				hwnd, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(utf16("#32770"))), uintptr(unsafe.Pointer(utf16("Hidden consent geometry test"))),
					0x80000000|0x00c00000|0x00080000, 0, 0, 560, 380, 0, 0, instance, 0)
				if hwnd == 0 {
					t.Fatalf("hidden dialog: %v", err)
				}
				func() {
					defer user32.NewProc("DestroyWindow").Call(hwnd)
					d := &permissionDialog{hwnd: hwnd, dpi: dpi, consentPair: true, expanded: expanded,
						prompt: QuestionPrompt{Choices: []QuestionChoice{{"Allow", "allow"}, {"Refuse", "refuse"}}, Default: "refuse", Details: "Exact account, program, action/profile, resource and lifetime."}}
					d.replaceFonts()
					d.palette()
					defer d.freeResources()
					body := "One: Saved app secret “alpha”\n\nThis app can view, copy or use the secret value anywhere.\n\nOngoing access until removed."
					if long {
						body = strings.Repeat("All present and future saved app secrets; runtime keys excluded. Use; Read; Create; Rotate; Revoke; Metadata. Raw values can be copied and used anywhere.\n", 100) + "LAST FINITE MEMBER"
					}
					for i, b := range []permissionBlock{
						{text: "Allow this app to read the secret value?", fontKind: 1, header: true},
						{text: "Saved secrets app", fontKind: 2, card: true}, {text: "Publisher: not verified.", card: true},
						{text: body},
					} {
						h, e := d.control("STATIC", b.text, 0x0080, 30+i)
						if e != nil {
							t.Fatal(e)
						}
						b.hwnd, b.font = h, d.fonts[b.fontKind]
						d.blocks = append(d.blocks, b)
					}
					var e error
					d.details, e = d.control("EDIT", d.prompt.Details, wsTabStop|0x0004|0x0040|0x0800|0x00200000, permissionTextID)
					if e != nil {
						t.Fatal(e)
					}
					d.detailToggle, e = d.control("BUTTON", "Show details", wsTabStop|0x8000|0x0100, permissionDetailsID)
					if e != nil {
						t.Fatal(e)
					}
					for i, c := range d.prompt.Choices {
						style := uint32(wsTabStop)
						if c.Value == "refuse" {
							style |= 1
						}
						h, e := d.control("BUTTON", permissionChoiceLabel(c, true), style, firstQuestionButton+i)
						if e != nil {
							t.Fatal(e)
						}
						d.buttons = append(d.buttons, h)
					}
					d.layout(nil)
					if d.err != nil {
						t.Fatalf("dpi=%d expanded=%t: %v", dpi, expanded, d.err)
					}
					var client, first, second rect
					getClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
					getWindowRect.Call(d.buttons[0], uintptr(unsafe.Pointer(&first)))
					getWindowRect.Call(d.buttons[1], uintptr(unsafe.Pointer(&second)))
					if first.Right-first.Left != second.Right-second.Left || first.Top != second.Top || d.cancelButton() != 0 {
						t.Fatal("two equal actions or absent visible Cancel changed")
					}
					if d.action.Bottom > client.Bottom || d.action.Top < 0 {
						t.Fatal("action area outside client")
					}
					if !long && !expanded && dpi == 96 && client.Bottom > 370 {
						t.Fatalf("specific raw Read remains tall: %d px", client.Bottom)
					}
					if d.card.Left != 0 || d.card.Right != client.Right || d.card.Top != 0 {
						t.Fatal("header is not the full-width compact band")
					}
					sendMessage.Call(hwnd, permissionDMSetDefID, uintptr(permissionDefaultButton(d.prompt, false)), 0)
					got, _, _ := sendMessage.Call(hwnd, permissionDMGetDefID, 0, 0)
					if got&0xffff != firstQuestionButton+1 {
						t.Fatal("native Enter default is not Refuse")
					}
					if long {
						h := d.overflowControl()
						length, _, _ := getWindowTextLength.Call(h)
						full := make([]uint16, length+1)
						getWindowText.Call(h, uintptr(unsafe.Pointer(&full[0])), uintptr(len(full)))
						if !d.overflow || !strings.Contains(strings.ReplaceAll(syscall.UTF16ToString(full), "\r\n", "\n"), body) {
							t.Fatal("default scrolling body lost full finite scope/consequences")
						}
					}
					visible, _, _ := user32.NewProc("IsWindowVisible").Call(hwnd)
					if visible != 0 {
						t.Fatal("geometry test unexpectedly showed its native window")
					}
					t.Logf("hidden dpi=%d expanded=%t long=%t client=%dx%d equal-action-width=%d overflow=%t", dpi, expanded, long, client.Right, client.Bottom, first.Right-first.Left, d.overflow)
				}()
			}
		}
	}
}

// A failed extraction (no file, no icon resource, an unencodable path) shows
// the generic application icon. The Go test binary carries no icon resource.
func TestPermissionRequesterIconFailureIsGeneric(t *testing.T) {
	for _, path := range []string{"", `C:\\nonexistent-oa-requester-20261005.exe`, "invalid\x00path", os.Args[0]} {
		icon, owned, fromProgram := permissionRequesterIcon(path, 48)
		if icon == 0 || fromProgram {
			t.Fatalf("path=%q did not fall back to the generic icon", path)
		}
		if owned {
			permissionDestroyIcon.Call(icon)
		}
	}
}

// The verified program's own icon is extracted at the layout's pixel size,
// owned by the dialog and destroyed by it.
func TestPermissionRequesterIconFromVerifiedProgramAtLayoutSize(t *testing.T) {
	program := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	if _, err := os.Stat(program); err != nil {
		t.Skipf("no icon-bearing system program: %v", err)
	}
	for _, size := range []int{32, 48, 64} {
		icon, owned, fromProgram := permissionRequesterIcon(program, size)
		if icon == 0 || !owned || !fromProgram {
			t.Fatalf("size=%d: program icon not extracted (icon=%#x owned=%t)", size, icon, owned)
		}
		if got := permissionIconWidth(t, icon); got != size {
			t.Errorf("size=%d: extracted icon is %d px wide", size, got)
		}
		if ok, _, _ := permissionDestroyIcon.Call(icon); ok == 0 {
			t.Fatalf("size=%d: owned icon handle was not destroyable", size)
		}
	}
	d := &permissionDialog{dpi: 144, prompt: QuestionPrompt{PermissionView: &PermissionView{RequesterProgram: program}}}
	d.loadRequesterIcon()
	first := d.requesterIcon
	if !d.iconOwned || permissionIconWidth(t, first) != 48 {
		t.Fatal("dialog did not load a 48 px icon at 144 DPI")
	}
	d.dpi = 192
	d.loadRequesterIcon()
	if !d.iconOwned || permissionIconWidth(t, d.requesterIcon) != 64 {
		t.Fatal("DPI change did not reload the icon at 64 px")
	}
	// Each DPI reload destroys the icon it replaces, and freeResources the last.
	// USER handles carry a reuse counter, so a destroyed handle stays invalid.
	iconAlive := func(icon uintptr) bool {
		var info struct {
			Icon        int32
			HotX, HotY  uint32
			Mask, Color uintptr
		}
		ok, _, _ := user32.NewProc("GetIconInfo").Call(icon, uintptr(unsafe.Pointer(&info)))
		if ok != 0 {
			permissionDeleteObject.Call(info.Mask)
			permissionDeleteObject.Call(info.Color)
		}
		return ok != 0
	}
	for i := 0; i < 6; i++ {
		previous := d.requesterIcon
		d.dpi = 96 + 48*(i%3)
		d.loadRequesterIcon()
		if previous != d.requesterIcon && iconAlive(previous) {
			t.Fatalf("reload %d left the replaced icon alive", i)
		}
	}
	last := d.requesterIcon
	d.freeResources()
	if d.requesterIcon != 0 || d.iconOwned {
		t.Fatal("freeResources kept the requester icon")
	}
	if iconAlive(last) {
		t.Fatal("freeResources did not destroy the requester icon")
	}
}

// A program identity.LocalPath refuses (UNC, \\?\GLOBALROOT, device, a
// reserved name) gets the generic icon and is never opened or extracted.
func TestPermissionRequesterIconNeverOpensNonLocalPaths(t *testing.T) {
	extracted := 0
	previous := permissionExtract
	permissionExtract = func(string, int) uintptr { extracted++; return 0 }
	t.Cleanup(func() { permissionExtract = previous })
	opens := 0
	stop, ok := identity.ObserveOpens(func(string) { opens++ })
	if !ok {
		t.Fatal("another open observer is set")
	}
	defer stop()
	for _, path := range []string{
		`\\fileserver\share\Editor\editor.exe`,
		`\\?\GLOBALROOT\Device\HarddiskVolume3\Windows\explorer.exe`,
		`\\.\C:\Windows\explorer.exe`,
		`\\?\UNC\fileserver\share\editor.exe`,
		`C:\Apps\CON\editor.exe`,
	} {
		icon, owned, fromProgram := permissionRequesterIcon(path, 48)
		if icon == 0 || fromProgram {
			t.Fatalf("%s: did not fall back to the generic icon", path)
		}
		if owned {
			permissionDestroyIcon.Call(icon)
		}
	}
	if extracted != 0 || opens != 0 {
		t.Fatalf("refused paths reached extraction %d times and identity opens %d times", extracted, opens)
	}
}

// A program whose icon failed or timed out is not read again when the dialog
// reloads its icon for a new DPI.
func TestPermissionRequesterIconFailureIsRemembered(t *testing.T) {
	calls := 0
	previous := permissionExtract
	permissionExtract = func(string, int) uintptr { calls++; return 0 }
	t.Cleanup(func() { permissionExtract = previous })
	d := &permissionDialog{dpi: 96, prompt: QuestionPrompt{PermissionView: &PermissionView{RequesterProgram: `C:\Windows\explorer.exe`}}}
	for _, dpi := range []int{96, 144, 192} {
		d.dpi = dpi
		d.loadRequesterIcon()
		if d.requesterIcon == 0 {
			t.Fatal("no generic icon after a failed extraction")
		}
	}
	d.freeResources()
	if calls != 1 {
		t.Fatalf("a failed requester icon was extracted %d times", calls)
	}
}

// A slow extraction cannot hold the prompt past its deadline.
func TestPermissionRequesterIconExtractionDeadline(t *testing.T) {
	release := make(chan struct{})
	previous := permissionExtract
	permissionExtract = func(string, int) uintptr { <-release; return 0 }
	t.Cleanup(func() { close(release); permissionExtract = previous })
	start := time.Now()
	if icon := permissionExtractWithin(`C:\Windows\explorer.exe`, 48, 50*time.Millisecond); icon != 0 {
		t.Fatal("a timed-out extraction returned an icon")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("extraction waited %v past its 50 ms deadline", elapsed)
	}
}

func permissionIconWidth(t *testing.T, icon uintptr) int {
	t.Helper()
	var info struct {
		Icon        int32
		HotX, HotY  uint32
		Mask, Color uintptr
	}
	if ok, _, err := user32.NewProc("GetIconInfo").Call(icon, uintptr(unsafe.Pointer(&info))); ok == 0 {
		t.Fatalf("GetIconInfo: %v", err)
	}
	defer permissionDeleteObject.Call(info.Mask)
	defer permissionDeleteObject.Call(info.Color)
	var bitmap struct {
		Type, Width, Height, WidthBytes int32
		Planes, BitsPixel               uint16
		Bits                            uintptr
	}
	bitmapHandle := info.Color
	if bitmapHandle == 0 {
		bitmapHandle = info.Mask
	}
	gdi32.NewProc("GetObjectW").Call(bitmapHandle, unsafe.Sizeof(bitmap), uintptr(unsafe.Pointer(&bitmap)))
	return int(bitmap.Width)
}
