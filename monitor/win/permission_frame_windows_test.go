//go:build windows

package win

import "testing"

func TestPermissionFrameFollowsHeader(t *testing.T) {
	saved := permissionFrameAttribute
	defer func() { permissionFrameAttribute = saved }()
	for _, dark := range []bool{false, true} {
		for _, level := range []PromptLevel{PromptLevelBlue, PromptLevelGray, PromptLevelYellow, PromptLevelOrange} {
			colors := permissionThemeColors(dark, level)
			got := map[uint32]uint32{}
			permissionFrameAttribute = func(_ uintptr, attribute, value uint32) { got[attribute] = value }
			(&permissionDialog{colors: colors, dark: dark}).frameTheme()
			want := map[uint32]uint32{dwmCaptionColor: colors.header, dwmTextColor: colors.headerText,
				dwmBorderColor: colors.header, dwmWindowCornerPreference: dwmCornerDoNotRound}
			for a, v := range want {
				if got[a] != v {
					t.Errorf("dark=%v level=%v attribute %d = %#x, want %#x", dark, level, a, got[a], v)
				}
			}
		}
	}
	got := map[uint32]bool{}
	permissionFrameAttribute = func(_ uintptr, attribute, _ uint32) { got[attribute] = true }
	(&permissionDialog{contrast: true}).frameTheme()
	if got[dwmCaptionColor] || got[dwmTextColor] || !got[dwmWindowCornerPreference] {
		t.Errorf("high contrast attributes = %v", got)
	}
}

func TestTaskDialogFrameFollowsLevel(t *testing.T) {
	savedAttr, savedDark, savedContrast := permissionFrameAttribute, permissionDarkMode, permissionHighContrast
	defer func() {
		permissionFrameAttribute, permissionDarkMode, permissionHighContrast = savedAttr, savedDark, savedContrast
		pendingTaskDialogLevel.Store(nil)
		activeTaskDialogHwnd.Store(0)
	}()
	for _, contrast := range []bool{false, true} {
		for _, dark := range []bool{false, true} {
			for _, level := range []PromptLevel{PromptLevelBlue, PromptLevelGray, PromptLevelYellow, PromptLevelOrange} {
				permissionDarkMode = func() bool { return dark }
				permissionHighContrast = func() bool { return contrast }
				got := map[uint32]uint32{}
				calls := 0
				permissionFrameAttribute = func(_ uintptr, attribute, value uint32) { calls++; got[attribute] = value }
				pendingTaskDialogLevel.Store(&level)
				taskDialogNotify(0, tdnCreated, 0, 0, 0)
				colors := permissionThemeColors(dark, level)
				want := map[uint32]uint32{dwmWindowCornerPreference: dwmCornerDoNotRound}
				if !contrast {
					want[dwmCaptionColor], want[dwmTextColor], want[dwmBorderColor] = colors.header, colors.headerText, colors.header
				}
				for a, v := range want {
					if got[a] != v {
						t.Errorf("contrast=%v dark=%v level=%v attribute %d = %#x, want %#x", contrast, dark, level, a, got[a], v)
					}
				}
				if _, set := got[dwmCaptionColor]; contrast && set {
					t.Errorf("high contrast set the caption colour: %v", got)
				}
				if calls == 0 || pendingTaskDialogLevel.Load() != nil {
					t.Errorf("level=%v: frame not applied at TDN_CREATED", level)
				}
			}
		}
	}
	calls := 0
	permissionFrameAttribute = func(uintptr, uint32, uint32) { calls++ }
	taskDialogNotify(0, tdnCreated, 0, 0, 0)
	if calls != 0 {
		t.Errorf("a TaskDialog without a pending level set %d attributes", calls)
	}
}
