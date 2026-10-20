//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// On Windows the shell's accent hook (panel_shell.go, panelAccent) reads the
// person's own accent colour, so every page the server writes carries it
// inline on the root (research/panel-redesign/SPEC.md §5 and §6). Without
// a registry value the shell's default blue stays.
func init() {
	fallback := panelAccent
	panelAccent = func() accentColors { return readWindowsAccent(fallback) }
}

// readWindowsAccent is the Windows accent as accentColors, or fallback's
// when Windows keeps no accent value.
func readWindowsAccent(fallback func() accentColors) accentColors {
	if accent, ok := readWindowsAccentColors(); ok {
		return accent
	}
	return fallback()
}

// readWindowsAccentColors reads HKCU\Software\Microsoft\Windows\DWM\
// AccentColor, the base colour DWM draws window chrome with, and, when
// present, the AccentPalette under Explorer\Accent for the two shades
// Windows 11 uses for accent fills: Dark 1 in light mode and Light 2 in
// dark mode. A shade is empty when Windows keeps no palette, and
// static/skin-windows.css derives it from the base. ok is false when
// Windows has no accent value.
func readWindowsAccentColors() (accentColors, bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\DWM`, registry.QUERY_VALUE)
	if err != nil {
		return accentColors{}, false
	}
	value, _, err := key.GetIntegerValue("AccentColor")
	closeErr := key.Close()
	if err != nil || closeErr != nil {
		return accentColors{}, false
	}
	accent := accentColors{Accent: accentHex(uint32(value))}
	if palette, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\Accent`, registry.QUERY_VALUE); err == nil {
		raw, _, err := palette.GetBinaryValue("AccentPalette")
		if palette.Close() == nil && err == nil {
			accent.Light2, accent.Dark1 = accentPaletteShades(raw)
		}
	}
	return accent, true
}

// accentHex converts DWM's AccentColor, a DWORD laid out 0xAABBGGRR, to
// "#rrggbb". The alpha byte is always opaque for an accent and is dropped.
func accentHex(abgr uint32) string {
	return fmt.Sprintf("#%02x%02x%02x", abgr&0xff, (abgr>>8)&0xff, (abgr>>16)&0xff)
}

// accentPaletteShades reads Light 2 and Dark 1 from AccentPalette: eight
// RGBA quadruples ordered Light 3, Light 2, Light 1, base, Dark 1, Dark 2,
// Dark 3 and a complement. A value of any other length gives two empty
// shades.
func accentPaletteShades(raw []byte) (light2, dark1 string) {
	if len(raw) != 32 {
		return "", ""
	}
	shade := func(i int) string { return fmt.Sprintf("#%02x%02x%02x", raw[4*i], raw[4*i+1], raw[4*i+2]) }
	return shade(1), shade(4)
}

// panelAccentScript is the JavaScript the WebView2 window runs on the open
// page when Windows reports a new accent colour (win.OpenWebView2's
// accentScript). It calls the shell's oaSetAccent, the live-change hook the
// shell contract names (SPEC §6); pages loaded afterwards carry the accent
// the server writes into them.
func panelAccentScript() string {
	accent, ok := readWindowsAccentColors()
	if !ok {
		return ""
	}
	return accentScript(accent)
}

// accentScript passes only "#rrggbb" values this file formatted itself, so
// nothing read from the registry reaches the page as code; any other value
// becomes an empty string, which oaSetAccent skips.
func accentScript(accent accentColors) string {
	if !isCSSHex(accent.Accent) {
		return ""
	}
	value := func(s string) string {
		if isCSSHex(s) {
			return s
		}
		return ""
	}
	return fmt.Sprintf("if(typeof oaSetAccent==='function')oaSetAccent('%s','%s','%s');", accent.Accent, value(accent.Light2), value(accent.Dark1))
}

// isCSSHex accepts exactly "#rrggbb" in lower-case hex, the only shape
// accentHex and accentPaletteShades produce.
func isCSSHex(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
