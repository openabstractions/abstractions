//go:build windows

package win

import (
	"encoding/binary"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// callerMemory is the Go memory a native callback argument addresses. The
// stand-in TaskDialogIndirect below receives addresses of Go values that
// taskDialogConfig.call keeps alive for the call. A uintptr converted to
// unsafe.Pointer that lands in the Go heap fails the race build's checkptr
// arithmetic check, which cannot see the value's origin; reading the bits as
// a pointer carries the same address without that conversion.
func callerMemory(address uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&address))
}

// The offsets below come from Windows SDK 10.0.26100.0 commctrl.h's
// pshpack1 TaskDialog declarations. A C++ compiler probe independently
// asserts these offsets, sizeof and alignof against that installed header.
// x64 TASKDIALOGCONFIG is 160 bytes and each button is 12; x86 is 96 and 8.
func TestTaskDialogSDKPackedLayout(t *testing.T) {
	pointerSize := int(unsafe.Sizeof(uintptr(0)))
	wide := pointerSize == 8
	sdk := func(narrow, broad int) int {
		if wide {
			return broad
		}
		return narrow
	}
	configSize, buttonSize := sdk(96, 160), sdk(8, 12)
	if unsafe.Sizeof(packedTaskDialogConfig{}) != uintptr(configSize) || unsafe.Alignof(packedTaskDialogConfig{}) != 1 {
		t.Fatal("native config must have the Windows SDK's packed size and alignment")
	}
	if unsafe.Sizeof(packedTaskDialogButton{}) != uintptr(buttonSize) || unsafe.Alignof(packedTaskDialogButton{}) != 1 {
		t.Fatal("native buttons must have the Windows SDK's packed stride and alignment")
	}
	readPointer := func(bytes []byte) uintptr {
		if wide {
			return uintptr(binary.LittleEndian.Uint64(bytes))
		}
		return uintptr(binary.LittleEndian.Uint32(bytes))
	}
	for _, question := range []bool{true, false} {
		name := "result"
		if question {
			name = "question"
		}
		t.Run(name, func(t *testing.T) {
			buttons := []taskDialogButton{{ID: 100, Text: utf16("Allow")}, {ID: 101, Text: utf16("Not now")}}
			config := taskDialogConfig{
				Size: uint32(taskDialogConfigSize), Parent: 0x101, Instance: 0x202,
				Flags: 0x303, CommonButtons: 0x404, WindowTitle: utf16("permission title"),
				MainIcon: 0x505, MainInstruction: utf16("instruction"), Content: utf16("content"),
				DefaultButton: 2, RadioButtonCount: 0x606, RadioButtons: 0x707, DefaultRadioButton: -7,
				VerificationText: utf16("verification"), ExpandedInformation: utf16("details"),
				ExpandedControlText: utf16("show"), CollapsedControlText: utf16("hide"),
				FooterIcon: 0x808, Footer: utf16("footer"), Callback: 0x909, CallbackData: 1, Width: 0xa0a,
			}
			if question {
				config.ButtonCount, config.Buttons = uint32(len(buttons)), &buttons[0]
			}
			fields := []struct {
				name   string
				offset int
				width  int
				want   uintptr
			}{
				{"cbSize", 0, 4, uintptr(configSize)},
				{"hwndParent", 4, pointerSize, uintptr(config.Parent)},
				{"hInstance", sdk(8, 12), pointerSize, uintptr(config.Instance)},
				{"dwFlags", sdk(12, 20), 4, uintptr(config.Flags)},
				{"dwCommonButtons", sdk(16, 24), 4, uintptr(config.CommonButtons)},
				{"pszWindowTitle", sdk(20, 28), pointerSize, uintptr(unsafe.Pointer(config.WindowTitle))},
				{"hMainIcon", sdk(24, 36), pointerSize, config.MainIcon},
				{"pszMainInstruction", sdk(28, 44), pointerSize, uintptr(unsafe.Pointer(config.MainInstruction))},
				{"pszContent", sdk(32, 52), pointerSize, uintptr(unsafe.Pointer(config.Content))},
				{"cButtons", sdk(36, 60), 4, uintptr(config.ButtonCount)},
				{"nDefaultButton", sdk(44, 72), 4, uintptr(uint32(config.DefaultButton))},
				{"cRadioButtons", sdk(48, 76), 4, uintptr(config.RadioButtonCount)},
				{"pRadioButtons", sdk(52, 80), pointerSize, config.RadioButtons},
				{"nDefaultRadioButton", sdk(56, 88), 4, uintptr(uint32(config.DefaultRadioButton))},
				{"pszVerificationText", sdk(60, 92), pointerSize, uintptr(unsafe.Pointer(config.VerificationText))},
				{"pszExpandedInformation", sdk(64, 100), pointerSize, uintptr(unsafe.Pointer(config.ExpandedInformation))},
				{"pszExpandedControlText", sdk(68, 108), pointerSize, uintptr(unsafe.Pointer(config.ExpandedControlText))},
				{"pszCollapsedControlText", sdk(72, 116), pointerSize, uintptr(unsafe.Pointer(config.CollapsedControlText))},
				{"hFooterIcon", sdk(76, 124), pointerSize, config.FooterIcon},
				{"pszFooter", sdk(80, 132), pointerSize, uintptr(unsafe.Pointer(config.Footer))},
				{"pfCallback", sdk(84, 140), pointerSize, config.Callback},
				{"lpCallbackData", sdk(88, 148), pointerSize, config.CallbackData},
				{"cxWidth", sdk(92, 156), 4, uintptr(config.Width)},
			}
			called := false
			proc := syscall.NewCallback(func(native, selected, radio, verification uintptr) uintptr {
				called = true
				runtime.GC() // Packed uintptr bytes require the logical pointer roots to survive this native call.
				bytes := unsafe.Slice((*byte)(callerMemory(native)), configSize)
				for _, field := range fields {
					var got uintptr
					if field.width == 4 {
						got = uintptr(binary.LittleEndian.Uint32(bytes[field.offset:]))
					} else {
						got = readPointer(bytes[field.offset:])
					}
					if got != field.want {
						t.Errorf("SDK %s offset %d: got %#x, want %#x", field.name, field.offset, got, field.want)
					}
				}
				buttonPointer := readPointer(bytes[sdk(40, 64):])
				if question {
					if buttonPointer == 0 {
						t.Error("packed custom button array is absent")
						return 1
					}
					buttonBytes := unsafe.Slice((*byte)(callerMemory(buttonPointer)), buttonSize*len(buttons))
					for i, button := range buttons {
						entry := buttonBytes[i*buttonSize:]
						if int32(binary.LittleEndian.Uint32(entry)) != button.ID || readPointer(entry[4:]) != uintptr(unsafe.Pointer(button.Text)) {
							t.Errorf("SDK button %d has wrong id, pointer offset or stride", i)
						}
					}
				} else if buttonPointer != 0 {
					t.Error("result config must pass a null custom button array")
				}
				if radio != 0 || verification != 0 {
					t.Error("unused output pointers must be null")
				}
				*(*int32)(callerMemory(selected)) = 101
				return 0
			})
			var selected int32
			if hr := config.call(proc, &selected); hr != 0 || !called || selected != 101 {
				t.Fatalf("packed native call returned hr=%#x, called=%t, selection=%d", hr, called, selected)
			}
		})
	}
}

// TestQuestionDefaultButtonIsTheRefusingChoiceOrCancel keeps Enter and Space
// from answering allow: the first button is never the default by position.
func TestQuestionDefaultButtonIsTheRefusingChoiceOrCancel(t *testing.T) {
	choices := []QuestionChoice{{Label: "Allow", Value: "allow"}, {Label: "Refuse", Value: "refuse"}, {Label: "Never allow", Value: "never"}}
	if got := questionDefaultButton(choices, "refuse"); got != firstQuestionButton+1 {
		t.Fatalf("default = %d, want the Refuse button %d", got, firstQuestionButton+1)
	}
	for _, def := range []string{"", "missing"} {
		if got := questionDefaultButton(choices, def); got != idcCancel {
			t.Fatalf("default for %q = %d, want Cancel", def, got)
		}
	}
}

// TestQuestionButtonsArmAfterDelay: for the first 500 ms an answer click is
// ignored and Cancel still closes.
func TestQuestionButtonsArmAfterDelay(t *testing.T) {
	if questionArmedAfter(0) || questionArmedAfter(499) || !questionArmedAfter(500) || !questionArmedAfter(2000) {
		t.Fatal("answer buttons must arm at 500 ms")
	}
	if questionClickAllowed(false, firstQuestionButton) {
		t.Fatal("an answer click before arming must be ignored")
	}
	if !questionClickAllowed(false, idcCancel) || !questionClickAllowed(true, firstQuestionButton) {
		t.Fatal("Cancel always closes; an armed answer is accepted")
	}
}

func TestAppChoiceUsesCommandLinksAndCloseDefault(t *testing.T) {
	choices := []QuestionChoice{{Label: "Allow System", Value: "allow"}, {Label: "Cancel", Value: "cancel"}}
	flags, common, def := questionPresentationFlags(QuestionPrompt{Choices: choices, Default: "allow", AppChoice: true})
	if flags&tdfUseCommandLinks == 0 || common != tdcbfCloseButton || def != idcClose {
		t.Fatalf("ordinary frame flags=%#x common=%#x default=%d", flags, common, def)
	}
	if !questionClickAllowed(false, idcClose) || questionClickAllowed(false, firstQuestionButton+1) {
		t.Fatal("Close must work before arming; offered Cancel remains an answer")
	}
	flags, common, def = questionPresentationFlags(QuestionPrompt{Choices: choices, Default: "cancel"})
	if flags&tdfUseCommandLinks != 0 || common != tdcbfCancelButton || def != firstQuestionButton+1 {
		t.Fatalf("permission form changed flags=%#x common=%#x default=%d", flags, common, def)
	}
}

func TestTypedPermissionUsesNativeLevelBarAndNeutralFallback(t *testing.T) {
	choices := []QuestionChoice{{Label: "Allow", Value: "allow"}, {Label: "Refuse", Value: "refuse"}, {Label: "Never allow", Value: "never"}}
	const appIcon = uintptr(0x1234)
	for _, tc := range []struct {
		name   string
		prompt QuestionPrompt
		bar    uintptr // 0 for the neutral HICON fallback
	}{
		// Microsoft's ShieldBlueBar, ShieldGrayBar and ShieldWarningYellowBar
		// resources; orange shares the yellow bar, since red means blocked.
		{"blue permission", QuestionPrompt{Permission: true, Level: PromptLevelBlue, Choices: choices, Default: "refuse"}, 65531},
		{"gray permission", QuestionPrompt{Permission: true, Level: PromptLevelGray, Choices: choices, Default: "refuse"}, 65527},
		{"yellow permission", QuestionPrompt{Permission: true, Level: PromptLevelYellow, Choices: choices, Default: "refuse"}, 65530},
		{"orange permission", QuestionPrompt{Permission: true, Level: PromptLevelOrange, Choices: choices, Default: "refuse"}, 65530},
		{"permission without a level", QuestionPrompt{Permission: true, Choices: choices, Default: "refuse"}, 65530},
		{"generic text", QuestionPrompt{Title: "Permission request", Instruction: "Allow System", Level: PromptLevelBlue, Choices: choices, Default: "refuse"}, 0},
		{"ordinary", QuestionPrompt{AppChoice: true, Level: PromptLevelGray, Choices: choices, Default: "allow"}, 0},
		{"conflicting private markers", QuestionPrompt{AppChoice: true, Permission: true, Level: PromptLevelBlue, Choices: choices, Default: "allow"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags, common, def := questionPresentationFlags(tc.prompt)
			icon, width := questionPresentationIcon(tc.prompt, appIcon), questionPresentationWidth(tc.prompt)
			if tc.bar != 0 {
				if flags&tdfUseHIconMain != 0 || icon != tc.bar || width != permissionQuestionWidth || common != tdcbfCancelButton || def != firstQuestionButton+1 {
					t.Fatalf("permission flags=%#x icon=%#x width=%d common=%#x default=%d", flags, icon, width, common, def)
				}
			} else if flags&tdfUseHIconMain == 0 || icon != appIcon || width != 0 {
				t.Fatalf("neutral fallback flags=%#x icon=%#x width=%d", flags, icon, width)
			}
			if flags&tdfCallbackTimer == 0 || flags&tdfAllowDialogCancellation == 0 {
				t.Fatal("visual styling must retain arming and dismissal")
			}
		})
	}
}
