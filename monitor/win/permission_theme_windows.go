//go:build windows

package win

import (
	"runtime"
	"syscall"
	"time"
	"unsafe"

	identity "github.com/openabstractions/abstraction-identity"
)

// COLORREF values (0x00BBGGRR). The palette is presentation only: the
// prompt's level selects its header band; it grants no operating-system right.
type permissionColors struct {
	background, header, text, headerText, border   uint32
	button, hover, pressed, disabled, disabledText uint32
	accent, accentText, link                       uint32
}

// permissionHeaders are the header band and its text for each level, in the
// light and dark app themes. They follow the UAC banners: blue for Windows
// itself, gray for a verified publisher, yellow for an unknown one. Light
// blue is Windows 11's default accent (#005FB8) and dark blue its darker
// shade (#003E92); light yellow is the Windows caution yellow (#FFB900) and
// light orange the Windows orange (#F7630C). The dark yellow (#7A5C00) and
// orange (#9A3A00) keep their hues apart under white text.
var permissionHeaders = map[PromptLevel][2][2]uint32{
	//                  light {band, text}        dark {band, text}
	PromptLevelBlue:   {{0x00b85f00, 0x00ffffff}, {0x00923e00, 0x00ffffff}},
	PromptLevelGray:   {{0x00e8e8e8, 0x00222222}, {0x00383838, 0x00ffffff}},
	PromptLevelYellow: {{0x0000b9ff, 0x00222222}, {0x00005c7a, 0x00ffffff}},
	PromptLevelOrange: {{0x000c63f7, 0x00222222}, {0x00003a9a, 0x00ffffff}},
}

// permissionDarkMode reads the app theme the person chose; a capture fixture
// replaces it to draw the other theme without changing the setting.
var permissionDarkMode = appDarkMode

func permissionThemeColors(dark bool, level PromptLevel) permissionColors {
	c := permissionColors{background: 0x00ffffff, text: 0x00222222,
		border: 0x00777777, button: 0x00f0f0f0, hover: 0x00e7e7e7, pressed: 0x00dedede,
		disabled: 0x00ededed, disabledText: 0x00717171, accent: 0x00c06700, accentText: 0x00ffffff, link: 0x00b45e00}
	theme := 0
	if dark {
		theme = 1
		c = permissionColors{background: 0x00202020, text: 0x00f5f5f5,
			border: 0x00888888, button: 0x003b3b3b, hover: 0x00474747, pressed: 0x00333333,
			disabled: 0x00333333, disabledText: 0x00ababab, accent: 0x00ffc24c, accentText: 0x00000000, link: 0x00ffc24c}
	}
	header := permissionHeaders[level.known()][theme]
	c.header, c.headerText = header[0], header[1]
	return c
}

func permissionConsentPair(choices []QuestionChoice) bool {
	return len(choices) == 2 && ((choices[0].Value == "allow" && choices[1].Value == "refuse") ||
		(choices[0].Value == "refuse" && choices[1].Value == "allow"))
}

func permissionChoiceLabel(choice QuestionChoice, pair bool) string {
	if pair {
		if choice.Value == "refuse" {
			return "Don't allow"
		}
		if choice.Value == "allow" {
			return "Allow"
		}
	}
	return choice.Label
}

func permissionDefaultButton(prompt QuestionPrompt, result bool) int32 {
	if !result && permissionConsentPair(prompt.Choices) {
		return questionDefaultButton(prompt.Choices, "refuse")
	}
	return questionDefaultButton(prompt.Choices, prompt.Default)
}

var (
	permissionExtractIcon = shell32.NewProc("SHDefExtractIconW")
	permissionScaledIcon  = comctl32.NewProc("LoadIconWithScaleDown")
	permissionDrawIcon    = user32.NewProc("DrawIconEx")
	permissionDestroyIcon = user32.NewProc("DestroyIcon")
	permissionRectangle   = gdi32.NewProc("Rectangle")
	permissionRedraw      = user32.NewProc("RedrawWindow")
)

// permissionIconDeadline bounds how long the dialog waits for the requester's
// icon before it shows the generic one.
const permissionIconDeadline = 750 * time.Millisecond

// permissionExtract reads one icon resource from a local file; tests count
// its calls.
var permissionExtract = extractPermissionIcon

// permissionRequesterIcon returns the requester's own icon at size pixels, or
// the generic application icon when program is empty or carries no icon.
// program is the verified program the Panel selected (an empty one is a script
// host or an unmatched asker); this never takes a path from display prose.
// Only a path identity.LocalPath accepts is read: a UNC path, a device or
// \\?\GLOBALROOT path and a network drive are never opened, so no share is
// contacted and no credential leaves the machine. Extraction runs off the UI
// thread for at most permissionIconDeadline. It reads the icon resource as
// data, never executes the requester, and cannot upgrade the retained
// verification. An owned icon is destroyed by the caller.
func permissionRequesterIcon(program string, size int) (icon uintptr, owned, fromProgram bool) {
	if program != "" && size > 0 && identity.LocalPath(program) == nil {
		if icon = permissionExtractWithin(program, size, permissionIconDeadline); icon != 0 {
			return icon, true, true
		}
	}
	if size > 0 && permissionScaledIcon.Find() == nil {
		if hr, _, _ := permissionScaledIcon.Call(0, idiApplication, uintptr(size), uintptr(size), uintptr(unsafe.Pointer(&icon))); hr == 0 && icon != 0 {
			return icon, true, false
		}
		icon = 0
	}
	icon, _, _ = loadIcon.Call(0, idiApplication) // shared generic application icon
	return icon, false, false
}

// permissionExtractWithin runs permissionExtract on its own thread and gives
// up after wait; an icon that arrives later is destroyed. The extractor is
// read before the thread starts, so a late thread never reads the variable.
func permissionExtractWithin(path string, size int, wait time.Duration) uintptr {
	done := make(chan uintptr, 1)
	extract := permissionExtract
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		done <- extract(path, size)
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case icon := <-done:
		return icon
	case <-timer.C:
		go func() {
			if late := <-done; late != 0 {
				permissionDestroyIcon.Call(late)
			}
		}()
		return 0
	}
}

func extractPermissionIcon(program string, size int) uintptr {
	path, err := syscall.UTF16FromString(program)
	if err != nil {
		return 0
	}
	var icon uintptr
	// SHDefExtractIconW(path, index 0, flags 0, large, no small, large size).
	hr, _, _ := permissionExtractIcon.Call(uintptr(unsafe.Pointer(&path[0])), 0, 0, uintptr(unsafe.Pointer(&icon)), 0, uintptr(size&0xffff))
	if hr != 0 && icon != 0 {
		permissionDestroyIcon.Call(icon)
		icon = 0
	}
	return icon
}
