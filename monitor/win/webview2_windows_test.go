package win

import (
	"os"
	"path/filepath"
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
