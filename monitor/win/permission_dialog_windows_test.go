//go:build windows

package win

import (
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPermissionAnswerGuardChecksHostTimeAndExactIndex(t *testing.T) {
	for _, elapsed := range []time.Duration{0, 499 * time.Millisecond} {
		if permissionAnswerAllowed(false, elapsed, 0, 3) {
			t.Fatal("early native command can submit an answer")
		}
	}
	if !permissionAnswerAllowed(false, 500*time.Millisecond, 1, 3) {
		t.Fatal("armed refusing choice must be available")
	}
	for _, index := range []int{-1, 3, 1000} {
		if permissionAnswerAllowed(false, time.Second, index, 3) || permissionAnswerAllowed(true, time.Second, index, 3) {
			t.Fatal("unknown IDs must never map to an answer")
		}
	}
	if !permissionAnswerAllowed(true, 0, 0, 1) {
		t.Fatal("a result acknowledgment does not need permission arming")
	}
}

func TestPermissionRendererUsesNativeClientVersion(t *testing.T) {
	host := windowsVersion()
	t.Logf("host RtlGetVersion major=%d minor=%d build=%d product=%d", host.MajorVersion, host.MinorVersion, host.BuildNumber, host.ProductType)
	for _, tc := range []struct {
		build   uint32
		product byte
		want    bool
	}{
		{0, 1, false}, {19045, 1, false}, {21999, 1, false}, {22000, 1, true}, {26200, 1, true},
		{26200, 0, false}, {26200, 2, false}, {26200, 3, false},
	} {
		v := &windows.OsVersionInfoEx{MajorVersion: 10, BuildNumber: tc.build, ProductType: tc.product}
		if got := modernPermissionFor(QuestionPrompt{Permission: true}, v); got != tc.want {
			t.Fatalf("native %d/%d: modern=%t", tc.build, tc.product, got)
		}
		for _, prompt := range []QuestionPrompt{{Title: "Permission", Instruction: "Administrator"}, {Permission: true, AppChoice: true}} {
			if modernPermissionFor(prompt, v) {
				t.Fatal("only the typed permission marker can select modern rendering")
			}
		}
	}
	if modernPermissionFor(QuestionPrompt{Permission: true}, nil) {
		t.Fatal("missing native version must retain legacy rendering")
	}
	v := &windows.OsVersionInfoEx{MajorVersion: 11, BuildNumber: 26200, ProductType: 1}
	if modernPermissionFor(QuestionPrompt{Permission: true}, v) {
		t.Fatal("unknown version family needs an explicit routing decision")
	}
}

func TestPermissionFontsMeasureLongTextAtEachDPI(t *testing.T) {
	// DT_CALCRECT on a screen DC measures text without drawing or creating a
	// window. Live DPI transitions and contrast modes still need native evidence.
	for _, dpi := range []int{96, 144, 192} {
		d := &permissionDialog{dpi: dpi}
		d.replaceFonts()
		width := d.px(512)
		short := d.measure("Publisher: signature verified at request.", d.fonts[0], width)
		long := d.measure(strings.Repeat("Exact permission target and ongoing answer effect. ", 100), d.fonts[0], width)
		if short < d.px(20) || long <= short*10 {
			t.Fatalf("%d dpi measured short=%d long=%d", dpi, short, long)
		}
		t.Logf("dpi=%d width=%d short-height=%d long-height=%d; full long body requires scrolling", dpi, width, short, long)
		d.freeResources()
	}
}

func TestPermissionNativeNotificationLayout(t *testing.T) {
	var v permissionCustomDraw
	// NMCUSTOMDRAW contains the padded NMHDR as a complete record. Flattening
	// it puts dwDrawStage at offset 20 rather than 24 on x64.
	match(t, "NMCUSTOMDRAW", []cField{
		{"hdr.hwndFrom", cPtr, 0}, {"hdr.idFrom", cPtr, 0}, {"hdr.code", cInt, 0},
		{"hdr tail padding", (cPtr - cInt) % cPtr, 1},
		{"dwDrawStage", cInt, 0}, {"hdc", cPtr, 0}, {"rc", 16, cInt},
		{"dwItemSpec", cPtr, 0}, {"uItemState", cInt, 0}, {"lItemlParam", cPtr, 0},
	}, unsafe.Sizeof(v),
		unsafe.Offsetof(v.Header)+unsafe.Offsetof(v.Header.From), unsafe.Offsetof(v.Header)+unsafe.Offsetof(v.Header.ID),
		unsafe.Offsetof(v.Header)+unsafe.Offsetof(v.Header.Code), unsafe.Offsetof(v.Header)+unsafe.Offsetof(v.Header.Code)+4,
		unsafe.Offsetof(v.Stage), unsafe.Offsetof(v.DC), unsafe.Offsetof(v.Bounds), unsafe.Offsetof(v.Item), unsafe.Offsetof(v.State), unsafe.Offsetof(v.Param))
}

func TestPermissionActionLayoutRetainsEveryButtonAtDPI(t *testing.T) {
	for _, dpi := range []int{96, 144, 192} {
		scale := func(n int) int { return (n*dpi + 48) / 96 }
		widths := []int{scale(100), scale(100), scale(116), scale(100)}
		positions, height := permissionButtonLayout(widths, scale(512), scale(8), scale(40))
		if len(positions) != 4 || height != scale(40) {
			t.Fatalf("%d dpi: positions=%v height=%d", dpi, positions, height)
		}
		for _, r := range positions {
			if r.Left < 0 || r.Right > int32(scale(512)) || r.Bottom-r.Top != int32(scale(40)) {
				t.Fatalf("out-of-bounds action at %d dpi: %+v", dpi, r)
			}
		}
		// Narrow work areas and a long translated label wrap without removing
		// the exact offered choice or Cancel.
		positions, height = permissionButtonLayout([]int{scale(900), scale(100), scale(140), scale(100)}, scale(300), scale(8), scale(40))
		if len(positions) != 4 || height <= scale(40) || positions[0].Right != int32(scale(300)) {
			t.Fatalf("long/narrow actions: %v height=%d", positions, height)
		}
	}
}

func TestPermissionExpandedDetailsKeepsActionsInsideSmallWorkArea(t *testing.T) {
	for _, dpi := range []int{96, 144, 192} {
		scale := func(n int) int { return (n*dpi + 48) / 96 }
		// Physical 768-pixel work area, including scaled margins/frame. These
		// are the same action/footer/toggle reservations used by layout().
		maxHeight := 768 - scale(32) - scale(28)
		reserved := permissionReservedHeight(scale(40)+2*scale(24), scale(20), scale(28)+scale(16), scale(16))
		pad, gap, minimum := scale(24), scale(16), scale(80)
		collapsed, fits := permissionTextBudget(maxHeight, reserved, pad, gap, 0, minimum)
		if !fits {
			t.Fatalf("collapsed viewport should fit at %d dpi: body=%d", dpi, collapsed)
		}
		details := min(scale(180), maxHeight/3)
		body, fits := permissionTextBudget(maxHeight, reserved, pad, gap, details, minimum)
		if fits && body+reserved+pad+details+gap > maxHeight {
			t.Fatalf("expanded action area escapes work area at %d dpi", dpi)
		}
		if dpi == 192 && fits {
			t.Fatal("small 200% viewport must take native fallback before expansion can push actions offscreen")
		}
		if dpi == 192 && collapsed >= minimum && body < scale(40) {
			// This is the original failure: collapsed admission passed, then
			// the old overflow clamp enlarged the body after detail subtraction.
			oldHeight := max(body, scale(40)) + reserved + pad + details + gap
			if oldHeight <= maxHeight {
				t.Fatal("regression no longer reproduces the old out-of-bounds geometry")
			}
		}
	}
}

func TestPermissionCompactChromeKeepsViewportBudget(t *testing.T) {
	for _, dpi := range []int{96, 144, 192} {
		scale := func(n int) int { return (n*dpi + 48) / 96 }
		maxHeight := 768 - scale(32) - scale(28)
		pad, gap := scale(24), scale(16)
		positions, actions := permissionButtonLayout([]int{scale(100), scale(100), scale(116), scale(100)}, scale(512), scale(8), scale(40))
		if len(positions) != 4 {
			t.Fatal("compact chrome lost an answer or Cancel")
		}
		actions += 2 * pad
		compact := permissionReservedHeight(actions, 0, scale(28)+gap, gap)
		withWarning := permissionReservedHeight(actions, scale(20), scale(28)+gap, gap)
		if withWarning-compact != scale(20)+gap {
			t.Fatal("omitted instruction still reserves a footer gap")
		}
		for _, reserved := range []int{compact, withWarning} {
			details := min(scale(180), maxHeight/3)
			body, fits := permissionTextBudget(maxHeight, reserved, pad, gap, details, scale(80))
			if fits && body+reserved+pad+details+gap > maxHeight {
				t.Fatalf("compact expanded controls leave the work area at %d dpi", dpi)
			}
			if dpi == 192 && fits {
				t.Fatal("small 200% viewport must preserve every action through fallback")
			}
		}
	}
}

func TestPermissionPrivateWarningKeepsUnknownAndExplicitFooterText(t *testing.T) {
	footer := "An older projection's complete footer."
	empty, warning := "", "Permission state could not be checked."
	for _, tc := range []struct {
		view *PermissionView
		want string
	}{
		{nil, footer}, {&PermissionView{}, footer},
		{&PermissionView{Warning: &empty}, ""}, {&PermissionView{Warning: &warning}, warning},
	} {
		prompt := QuestionPrompt{Permission: true, Footer: footer, PermissionView: tc.view}
		if got := permissionVisibleFooter(prompt, false); got != tc.want {
			t.Fatalf("private warning footer=%q want %q", got, tc.want)
		}
		if got := permissionVisibleFooter(prompt, true); got != footer {
			t.Fatal("result footer changed")
		}
		prompt.Permission = false
		if got := permissionVisibleFooter(prompt, false); got != footer {
			t.Fatal("untyped footer changed")
		}
	}
}
