package win

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestWebView2HandlerQueryInterface(t *testing.T) {
	w := &webView2Window{}
	cases := []struct {
		name string
		ptr  uintptr
		vtbl *handlerVtbl
		iid  syscall.GUID
	}{
		{"environment completed", uintptr(unsafe.Pointer(w.newEnvironmentCompletedHandler(""))), w.envHandler.vtbl,
			syscall.GUID{0x4e8a3389, 0xc9d8, 0x4bd2, [8]byte{0xb6, 0xb5, 0x12, 0x4f, 0xee, 0x6c, 0xc1, 0x4d}}},
		{"controller completed", uintptr(unsafe.Pointer(w.newControllerCompletedHandler(""))), w.ctrlHandler.vtbl,
			syscall.GUID{0x6c4819f3, 0xc9b7, 0x4260, [8]byte{0x81, 0x27, 0xc9, 0xf5, 0xbd, 0xe7, 0xf6, 0x8c}}},
		{"navigation starting", uintptr(unsafe.Pointer(w.newNavigationStartingHandler())), w.navHandler.vtbl,
			syscall.GUID{0x9adbe429, 0xf36d, 0x432b, [8]byte{0x9d, 0xdc, 0xf8, 0x88, 0x1f, 0xbd, 0x76, 0xe3}}},
		{"new window requested", uintptr(unsafe.Pointer(w.newNewWindowRequestedHandler())), w.newWinHandler.vtbl,
			syscall.GUID{0xd4c185fe, 0xc81c, 0x4989, [8]byte{0x97, 0xaf, 0x2d, 0x3f, 0xa7, 0xab, 0x56, 0x51}}},
		{"add script completed", uintptr(unsafe.Pointer(w.newAddScriptCompletedHandler())), w.scriptHandler.vtbl,
			syscall.GUID{0xb99369f3, 0x9b11, 0x47b5, [8]byte{0xbc, 0x6f, 0x8e, 0x78, 0x95, 0xfc, 0xea, 0x17}}},
		{"process failed", uintptr(unsafe.Pointer(w.newProcessFailedHandler())), w.failHandler.vtbl,
			syscall.GUID{0x79e0aea4, 0x990b, 0x42d9, [8]byte{0xaa, 0x1d, 0x0f, 0xcc, 0x2e, 0x5b, 0xc7, 0xf1}}},
	}
	unknown := syscall.GUID{0, 0, 0, [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	foreign := syscall.GUID{0x12345678, 0, 0, [8]byte{}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, iid := range []syscall.GUID{unknown, tc.iid} {
				var out uintptr
				hr := tc.vtbl.QueryInterface.call(tc.ptr, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&out)))
				if hr != 0 || out != tc.ptr {
					t.Errorf("QueryInterface(%v) = hr %#x, pointer %#x; want S_OK and %#x", iid, hr, out, tc.ptr)
				}
			}
			out := uintptr(0xbeef)
			hr := tc.vtbl.QueryInterface.call(tc.ptr, uintptr(unsafe.Pointer(&foreign)), uintptr(unsafe.Pointer(&out)))
			if uint32(hr) != 0x80004002 || out != 0 {
				t.Errorf("foreign QueryInterface = hr %#x, pointer %#x; want E_NOINTERFACE and nil", hr, out)
			}
			hr = tc.vtbl.QueryInterface.call(tc.ptr, uintptr(unsafe.Pointer(&tc.iid)), 0)
			if uint32(hr) != 0x80004003 {
				t.Errorf("nil output QueryInterface = %#x; want E_POINTER", hr)
			}
		})
	}
}

func TestWebView2ControllerStartFailureEndsAttach(t *testing.T) {
	w := &webView2Window{win: &Window{}}
	env := &coreWebView2Environment{vtbl: &coreWebView2EnvironmentVtbl{
		iUnknownVtbl: iUnknownVtbl{AddRef: comProc(syscall.NewCallback(func(uintptr) uintptr { return 1 }))},
		CreateCoreWebView2Controller: comProc(syscall.NewCallback(func(uintptr, uintptr, uintptr) uintptr {
			return 0x80004005 // E_FAIL, synchronously before any completion callback
		})),
	}}
	handler := w.newEnvironmentCompletedHandler("http://127.0.0.1/")
	handler.vtbl.Invoke.call(uintptr(unsafe.Pointer(handler)), 0, uintptr(unsafe.Pointer(env)))
	if w.failed == nil || !strings.Contains(w.failed.Error(), "CreateCoreWebView2Controller: 0x80004005") {
		t.Fatalf("synchronous controller failure was not reported: %v", w.failed)
	}
	if w.ready {
		t.Fatal("window marked ready after controller creation failed")
	}
}

// ICoreWebView2Controller2 adds its two slots after ICoreWebView2Controller's
// 26, and the two ICoreWebView2 calls the skin needs sit at their WebView2.h
// positions: a slot off by one calls a different method.
func TestWebView2SkinVtableSlots(t *testing.T) {
	slot := unsafe.Sizeof(comProc(0))
	var c2 coreWebView2Controller2Vtbl
	if got := unsafe.Offsetof(c2.PutDefaultBackgroundColor) / slot; got != 27 {
		t.Errorf("PutDefaultBackgroundColor at slot %d, want 27", got)
	}
	var wv coreWebView2Vtbl
	if got := unsafe.Offsetof(wv.AddScriptToExecuteOnDocumentCreated) / slot; got != 27 {
		t.Errorf("AddScriptToExecuteOnDocumentCreated at slot %d, want 27", got)
	}
	if got := unsafe.Offsetof(wv.RemoveScriptToExecuteOnDocumentCreated) / slot; got != 28 {
		t.Errorf("RemoveScriptToExecuteOnDocumentCreated at slot %d, want 28", got)
	}
	if got := unsafe.Offsetof(wv.ExecuteScript) / slot; got != 29 {
		t.Errorf("ExecuteScript at slot %d, want 29", got)
	}
	if got := unsafe.Offsetof(wv.AddProcessFailed) / slot; got != 25 {
		t.Errorf("AddProcessFailed at slot %d, want 25", got)
	}
	var failed coreWebView2ProcessFailedEventArgsVtbl
	if got := unsafe.Offsetof(failed.GetProcessFailedKind) / slot; got != 3 {
		t.Errorf("GetProcessFailedKind at slot %d, want 3", got)
	}
	if unsafe.Sizeof(margins{}) != 16 {
		t.Errorf("MARGINS is %d bytes, want 16", unsafe.Sizeof(margins{}))
	}
}

// With Mica the default background is fully transparent; without it, the
// opaque Windows base for the theme, both as COREWEBVIEW2_COLOR {A,R,G,B}
// and as the WEBVIEW2_DEFAULT_BACKGROUND_COLOR string.
func TestWebViewBackground(t *testing.T) {
	cases := []struct {
		mica, dark bool
		color      uint32
		variable   string
	}{
		{true, false, 0, "00000000"},
		{true, true, 0, "00000000"},
		{false, false, 0xf3f3f3ff, "FFF3F3F3"},
		{false, true, 0x202020ff, "FF202020"},
	}
	for _, c := range cases {
		color, variable := webViewBackground(c.mica, c.dark)
		if color != c.color || variable != c.variable {
			t.Errorf("webViewBackground(mica=%t, dark=%t) = %#08x, %q; want %#08x, %q", c.mica, c.dark, color, variable, c.color, c.variable)
		}
	}
}

// The host marks the root for static/skin-windows.css; data-backdrop only
// when Mica is behind the page.
func TestHostScript(t *testing.T) {
	with, without := hostScript(true), hostScript(false)
	for _, s := range []string{with, without} {
		if !strings.Contains(s, "r.setAttribute('data-host','webview2');") {
			t.Errorf("host script does not mark data-host:\n%s", s)
		}
	}
	if !strings.Contains(with, "r.setAttribute('data-backdrop','mica');") || strings.Contains(with, "removeAttribute") {
		t.Errorf("Mica script does not set data-backdrop only:\n%s", with)
	}
	if !strings.Contains(without, "r.removeAttribute('data-backdrop');") || strings.Contains(without, "'mica'") {
		t.Errorf("solid script does not clear data-backdrop:\n%s", without)
	}
}

// The background variable reaches the WebView2 browser process only; the
// restore puts back the earlier value, or removes a variable that was not
// there, so programs the Panel starts later never inherit it.
func TestSetEnvironmentRestores(t *testing.T) {
	const name = "OA_TEST_WEBVIEW2_DEFAULT_BACKGROUND"
	t.Setenv(name, "earlier")
	restore, err := setEnvironment(name, "00000000")
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(name); got != "00000000" {
		t.Fatalf("while set: %q", got)
	}
	restore()
	if got, had := os.LookupEnv(name); !had || got != "earlier" {
		t.Fatalf("after restore: %q, present %t; want the earlier value", got, had)
	}

	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	restore, err = setEnvironment(name, "FFF3F3F3")
	if err != nil {
		t.Fatal(err)
	}
	restore()
	if got, had := os.LookupEnv(name); had {
		t.Fatalf("after restore: %q present; want no variable", got)
	}
}

// The completion handler copies the script id out of WebView2's string,
// which is valid only during Invoke, and ignores a failed add.
func TestAddScriptCompletedRecordsID(t *testing.T) {
	w := &webView2Window{}
	h := w.newAddScriptCompletedHandler()
	id, _ := syscall.UTF16PtrFromString("{script-7}")
	h.vtbl.Invoke.call(uintptr(unsafe.Pointer(h)), 0, uintptr(unsafe.Pointer(id)))
	if w.hostScriptID != "{script-7}" {
		t.Fatalf("recorded %q", w.hostScriptID)
	}
	other, _ := syscall.UTF16PtrFromString("{script-8}")
	h.vtbl.Invoke.call(uintptr(unsafe.Pointer(h)), 0x80004005, uintptr(unsafe.Pointer(other)))
	if w.hostScriptID != "{script-7}" {
		t.Fatalf("failed add replaced the id with %q", w.hostScriptID)
	}
}

func TestProcessFailedKind(t *testing.T) {
	for kind, want := range map[uint32]string{
		0: "browser process exited", 1: "render process exited", 6: "GPU process exited",
		9: "unknown process exited", 42: "unrecognised failure",
	} {
		if got := processFailedKind(kind); got != want {
			t.Errorf("processFailedKind(%d) = %q, want %q", kind, got, want)
		}
	}
}

// micaActive reads the backdrop back from DWM: a window that never asked for
// Mica reports none. After useModernFrame it reports Mica on Windows 11 22H2
// and later outside high contrast; the result is logged, since older builds
// and Server editions have no Mica.
func TestMicaActiveReadsBackdrop(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w, err := open("mica probe", 400, 300, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer destroyWindowProc.Call(uintptr(w.h))
	defer forget(w.h)
	if micaActive(w.h) {
		t.Fatal("a plain window reported Mica before asking for it")
	}
	w.useModernFrame()
	t.Logf("Mica after useModernFrame: %t", micaActive(w.h))
}

// TestIsExternal checks the same-origin test the NavigationStarting and
// NewWindowRequested handlers use to decide which links leave to the system
// browser (webview2_windows.go's isExternal) — the pure-Go replacement for
// the injected JavaScript externalLinkInterceptJS used to make this same
// decision in the page.
func TestIsExternal(t *testing.T) {
	const origin = "http://127.0.0.1:38121"
	cases := []struct {
		target string
		want   bool
	}{
		{"http://127.0.0.1:38121/", false},
		{"http://127.0.0.1:38121/panel?key=abc", false},
		{"http://127.0.0.1:38121", false},
		{"https://127.0.0.1:38121/", true}, // different scheme
		{"http://127.0.0.1:9999/", true},   // different port
		{"http://example.com/", true},      // different host
		{"https://openabstractions.example/help", true},
		{"", false}, // no scheme/host: not a navigation isExternal can judge
		{"/relative/path", false},
		{"javascript:alert(1)", false},
	}
	for _, c := range cases {
		if got := isExternal(origin, c.target); got != c.want {
			t.Errorf("isExternal(%q, %q) = %v, want %v", origin, c.target, got, c.want)
		}
	}
}

// Resolving the runtime export starts no browser or profile. An installed
// runtime must be usable by a freshly built binary without a sibling loader.
func TestWebView2AvailableInstalled(t *testing.T) {
	present := false
	for _, path := range installedWebViewPaths() {
		if _, err := os.Stat(path); err == nil {
			present = true
		}
	}
	if !present {
		t.Skip("WebView2 runtime not installed")
	}
	if !WebView2Available() {
		_, err := webviewCreateProc()
		t.Fatalf("installed runtime unavailable: %v", err)
	}
}

func TestWebViewRuntimePath(t *testing.T) {
	for arch, dir := range map[string]string{"amd64": "x64", "arm64": "arm64", "386": "x86"} {
		path, err := webviewRuntimePath(`C:\Program Files\Microsoft\EdgeWebView\Application`, "153.0.4234.48", arch)
		if err != nil || filepath.Base(filepath.Dir(path)) != dir {
			t.Fatalf("%s: %q, %v", arch, path, err)
		}
	}
	for _, version := range []string{"", "0", "1.2.3", `..\foreign`, "153.0.4234.48/other", "1..3.4"} {
		if _, err := webviewRuntimePath(`C:\runtime`, version, "amd64"); err == nil {
			t.Errorf("accepted version %q", version)
		}
	}
	if _, err := webviewRuntimePath(`relative`, "153.0.4234.48", "amd64"); err == nil {
		t.Error("accepted relative DLL directory")
	}
	if _, err := webviewRuntimePath(`C:\runtime`, "153.0.4234.48", "unknown"); err == nil {
		t.Error("accepted unknown architecture")
	}
}
