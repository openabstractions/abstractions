//go:build windows

package main

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// DWM stores AccentColor as 0xAABBGGRR; Windows' default blue is #0078d7.
func TestAccentHex(t *testing.T) {
	for abgr, want := range map[uint32]string{
		0xffd77800: "#0078d7",
		0xff000000: "#000000",
		0x00ffffff: "#ffffff",
		0xff1c2bc4: "#c42b1c",
	} {
		if got := accentHex(abgr); got != want {
			t.Errorf("accentHex(%#08x) = %q, want %q", abgr, got, want)
		}
	}
}

// AccentPalette is eight RGBA quadruples, Light 3 first; Light 2 is the
// second and Dark 1 the fifth. The bytes are this machine's default-blue
// palette as Windows stores it.
func TestAccentPaletteShades(t *testing.T) {
	raw := []byte{
		0x9a, 0xeb, 0xff, 0x00, 0x61, 0xcc, 0xff, 0x00, 0x00, 0x93, 0xfc, 0x00, 0x00, 0x78, 0xd7, 0x00,
		0x00, 0x5f, 0xba, 0x00, 0x00, 0x3f, 0x95, 0x00, 0x00, 0x1a, 0x6a, 0x00, 0x88, 0x17, 0x98, 0x00,
	}
	light2, dark1 := accentPaletteShades(raw)
	if light2 != "#61ccff" || dark1 != "#005fba" {
		t.Fatalf("shades = %q, %q; want #61ccff, #005fba", light2, dark1)
	}
	for _, short := range [][]byte{nil, raw[:31], append(append([]byte{}, raw...), 0)} {
		if l, d := accentPaletteShades(short); l != "" || d != "" {
			t.Errorf("%d-byte palette gave %q, %q; want none", len(short), l, d)
		}
	}
}

// The live-change script calls the shell's oaSetAccent with well-formed
// values only, and is empty without a base colour.
func TestAccentScript(t *testing.T) {
	if got, want := accentScript(accentColors{Accent: "#0078d7", Light2: "#61ccff", Dark1: "#005fba"}),
		"if(typeof oaSetAccent==='function')oaSetAccent('#0078d7','#61ccff','#005fba');"; got != want {
		t.Errorf("script %q, want %q", got, want)
	}
	if got, want := accentScript(accentColors{Accent: "#0078d7", Light2: "#61CCFF", Dark1: "');alert(1);('"}),
		"if(typeof oaSetAccent==='function')oaSetAccent('#0078d7','','');"; got != want {
		t.Errorf("malformed shades: script %q, want %q", got, want)
	}
	for _, bad := range []accentColors{{}, {Accent: "#0078d7');alert(1);('"}, {Accent: "red", Light2: "#61ccff"}} {
		if got := accentScript(bad); got != "" {
			t.Errorf("accent %+v produced %q", bad, got)
		}
	}
}

// Without a registry accent the shell's own default stays.
func TestReadWindowsAccentFallsBack(t *testing.T) {
	fallback := func() accentColors { return accentColors{"#0078D4", "#60CDFF", "#005FB8"} }
	got := readWindowsAccent(fallback)
	if _, ok := readWindowsAccentColors(); !ok && got != fallback() {
		t.Fatalf("no registry accent gave %+v, want the fallback", got)
	}
}

// The init sets the shell's hook, so a Windows page's root carries the
// person's accent the registry holds.
func TestWindowsAccentReachesTheRoot(t *testing.T) {
	accent, ok := readWindowsAccentColors()
	if !ok {
		t.Skip("no DWM AccentColor for this account")
	}
	hex := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	if !hex.MatchString(accent.Accent) {
		t.Fatalf("accent %q", accent.Accent)
	}
	for _, shade := range []string{accent.Light2, accent.Dark1} {
		if shade != "" && !hex.MatchString(shade) {
			t.Fatalf("shade %q", shade)
		}
	}
	doc := panelPage(httptest.NewRequest("GET", "/?k=k&skin=windows", nil), "k", "status", "", servicePage)
	root := doc[:strings.Index(doc, "<head>")]
	if !strings.Contains(root, "--oa-accent:"+accent.Accent) {
		t.Fatalf("root %s lacks the registry accent %s", root, accent.Accent)
	}
	if panelAccentScript() == "" {
		t.Fatal("accent read but no live-change script")
	}
}
