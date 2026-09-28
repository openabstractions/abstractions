//go:build windows

package win

// An in-house WebView2 binding, replacing the vendored github.com/jchv/
// go-webview2 and its loader github.com/jchv/go-winloader (policy, owner,
// 2026-09-22: OpenAbstractions avoids third-party libraries). It reaches the
// same COM surface that library did, through syscall and this package's own
// window (win.Run), the way win_windows.go and tray_windows.go already reach
// every other Win32 API: no cgo, no vendored code.
//
// The COM surface used is:
//
//	EmbeddedBrowserWebView.dll CreateWebViewEnvironmentWithOptionsInternal
//	ICoreWebView2Environment  CreateCoreWebView2Controller
//	ICoreWebView2Controller   bounds, visibility, CoreWebView2
//	ICoreWebView2             Navigate, GetSettings, AddNavigationStarting,
//	                          AddNewWindowRequested
//	ICoreWebView2Settings     PutAreDefaultContextMenusEnabled,
//	                          PutAreDevToolsEnabled
//
// plus the two completed-handler callbacks
// (ICoreWebView2CreateCoreWebView2EnvironmentCompletedHandler,
// ICoreWebView2CreateCoreWebView2ControllerCompletedHandler) and the two
// event handlers (ICoreWebView2NavigationStartingEventHandler,
// ICoreWebView2NewWindowRequestedEventHandler) WebView2 calls back into.
//
// Every vtable below is transcribed field-for-field from two sources:
//
//   - ICoreWebView2Environment, ICoreWebView2Controller, ICoreWebView2 and
//     ICoreWebView2Settings are copied from the vtable structs the vendored
//     github.com/jchv/go-webview2@v0.0.0-20260205173254-56598839c808 module
//     (in the Go module cache, read-only, not vendored into this tree) used
//     for the same calls in pkg/edge/corewebview2.go,
//     pkg/edge/ICoreWebView2Controller.go and pkg/edge/ICoreWebView2Settings.go
//     — a binding that has made these exact calls in production, so its
//     field order is proven rather than transcribed from a header by hand.
//   - ICoreWebView2NavigationStartingEventArgs and
//     ICoreWebView2NewWindowRequestedEventArgs are not in that library (it
//     has no NewWindowRequested/NavigationStarting binding at all, which is
//     why desktop_windows.go used to intercept external links with injected
//     JavaScript instead of these two native events). Microsoft's own Win32
//     reference pages list interface members alphabetically, not in vtable
//     order, so their order here was cross-checked instead against the
//     webview2-sys Rust crate's bindgen output (docs.rs/webview2-sys, which
//     generates struct field order directly from the WebView2 SDK's own
//     WebView2.h), consistent with an independent web search of the same
//     two interfaces. Both interfaces are Applies-To WebView2 Win32 SDK
//     0.9.430 and have not been revised since (a revision adds a numbered
//     interface, e.g. ...EventArgs2, rather than changing this one).
//
// The completed-handler and event-handler objects this file implements
// follow the same Invoke(sender, args) uintptr shape the vendored library
// used for its own handlers — the one part of its design worth keeping,
// since it is how WebView2 itself calls back into any host language.
//
// Unlike the vendored library, which builds one package-level vtable per
// handler kind and routes every instance's callback through a stored impl
// interface (needed because it supports many simultaneous webviews sharing
// one function table), this file opens exactly one WebView2 window per
// process — the same one-window-per-process shape win.Run already assumes —
// so each handler is its own syscall.NewCallback trampoline, built fresh per
// OpenWebView2 call and closing directly over that call's state. There is
// only ever one instance to dispatch to, so there is nothing to look up.
import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// comProc is a COM vtable slot: a function pointer called with the stdcall
// (all-uintptr) convention every COM method uses. syscall.SyscallN, added in
// Go 1.18, is the stdlib's own way to call an arbitrary function pointer by
// address — the same job ComProc.Call did in the vendored library, without
// depending on it.
//
// Its second and third raw returns (a duplicate of the primary uintptr
// result, and the last Win32 error) are not COM's HRESULT and are not
// checked, the same way win_windows.go's own calls leave them unchecked:
// every method here already reports success or failure through its return
// value or an out pointer.
type comProc uintptr

//go:uintptrescapes
func (p comProc) call(a ...uintptr) uintptr {
	r1, _, _ := syscall.SyscallN(uintptr(p), a...) //unchecked: a COM method answers in r1; the errno SyscallN reports is not the call's result and every caller reads the HRESULT
	return r1
}

// iUnknownVtbl is IUnknown: QueryInterface, AddRef, Release, the same three
// slots that begin every COM interface's vtable.
type iUnknownVtbl struct {
	QueryInterface comProc
	AddRef         comProc
	Release        comProc
}

// coreWebView2EnvironmentVtbl is ICoreWebView2Environment.
type coreWebView2EnvironmentVtbl struct {
	iUnknownVtbl
	CreateCoreWebView2Controller     comProc
	CreateWebResourceResponse        comProc
	GetBrowserVersionString          comProc
	AddNewBrowserVersionAvailable    comProc
	RemoveNewBrowserVersionAvailable comProc
}

type coreWebView2Environment struct {
	vtbl *coreWebView2EnvironmentVtbl
}

// coreWebView2ControllerVtbl is ICoreWebView2Controller.
type coreWebView2ControllerVtbl struct {
	iUnknownVtbl
	GetIsVisible                      comProc
	PutIsVisible                      comProc
	GetBounds                         comProc
	PutBounds                         comProc
	GetZoomFactor                     comProc
	PutZoomFactor                     comProc
	AddZoomFactorChanged              comProc
	RemoveZoomFactorChanged           comProc
	SetBoundsAndZoomFactor            comProc
	MoveFocus                         comProc
	AddMoveFocusRequested             comProc
	RemoveMoveFocusRequested          comProc
	AddGotFocus                       comProc
	RemoveGotFocus                    comProc
	AddLostFocus                      comProc
	RemoveLostFocus                   comProc
	AddAcceleratorKeyPressed          comProc
	RemoveAcceleratorKeyPressed       comProc
	GetParentWindow                   comProc
	PutParentWindow                   comProc
	NotifyParentWindowPositionChanged comProc
	Close                             comProc
	GetCoreWebView2                   comProc
}

type coreWebView2Controller struct {
	vtbl *coreWebView2ControllerVtbl
}

// coreWebView2Vtbl is ICoreWebView2. Only a handful of its 58 methods are
// ever called; the rest are named placeholders so the ones after them land
// at the right offset — this is a vtable, called by position, not by name.
type coreWebView2Vtbl struct {
	iUnknownVtbl
	GetSettings                            comProc
	GetSource                              comProc
	Navigate                               comProc
	NavigateToString                       comProc
	AddNavigationStarting                  comProc
	RemoveNavigationStarting               comProc
	AddContentLoading                      comProc
	RemoveContentLoading                   comProc
	AddSourceChanged                       comProc
	RemoveSourceChanged                    comProc
	AddHistoryChanged                      comProc
	RemoveHistoryChanged                   comProc
	AddNavigationCompleted                 comProc
	RemoveNavigationCompleted              comProc
	AddFrameNavigationStarting             comProc
	RemoveFrameNavigationStarting          comProc
	AddFrameNavigationCompleted            comProc
	RemoveFrameNavigationCompleted         comProc
	AddScriptDialogOpening                 comProc
	RemoveScriptDialogOpening              comProc
	AddPermissionRequested                 comProc
	RemovePermissionRequested              comProc
	AddProcessFailed                       comProc
	RemoveProcessFailed                    comProc
	AddScriptToExecuteOnDocumentCreated    comProc
	RemoveScriptToExecuteOnDocumentCreated comProc
	ExecuteScript                          comProc
	CapturePreview                         comProc
	Reload                                 comProc
	PostWebMessageAsJSON                   comProc
	PostWebMessageAsString                 comProc
	AddWebMessageReceived                  comProc
	RemoveWebMessageReceived               comProc
	CallDevToolsProtocolMethod             comProc
	GetBrowserProcessID                    comProc
	GetCanGoBack                           comProc
	GetCanGoForward                        comProc
	GoBack                                 comProc
	GoForward                              comProc
	GetDevToolsProtocolEventReceiver       comProc
	Stop                                   comProc
	AddNewWindowRequested                  comProc
	RemoveNewWindowRequested               comProc
	AddDocumentTitleChanged                comProc
	RemoveDocumentTitleChanged             comProc
	GetDocumentTitle                       comProc
	AddHostObjectToScript                  comProc
	RemoveHostObjectFromScript             comProc
	OpenDevToolsWindow                     comProc
	AddContainsFullScreenElementChanged    comProc
	RemoveContainsFullScreenElementChanged comProc
	GetContainsFullScreenElement           comProc
	AddWebResourceRequested                comProc
	RemoveWebResourceRequested             comProc
	AddWebResourceRequestedFilter          comProc
	RemoveWebResourceRequestedFilter       comProc
	AddWindowCloseRequested                comProc
	RemoveWindowCloseRequested             comProc
}

type coreWebView2 struct {
	vtbl *coreWebView2Vtbl
}

// coreWebView2SettingsVtbl is ICoreWebView2Settings.
type coreWebView2SettingsVtbl struct {
	iUnknownVtbl
	GetIsScriptEnabled                comProc
	PutIsScriptEnabled                comProc
	GetIsWebMessageEnabled            comProc
	PutIsWebMessageEnabled            comProc
	GetAreDefaultScriptDialogsEnabled comProc
	PutAreDefaultScriptDialogsEnabled comProc
	GetIsStatusBarEnabled             comProc
	PutIsStatusBarEnabled             comProc
	GetAreDevToolsEnabled             comProc
	PutAreDevToolsEnabled             comProc
	GetAreDefaultContextMenusEnabled  comProc
	PutAreDefaultContextMenusEnabled  comProc
	GetAreHostObjectsAllowed          comProc
	PutAreHostObjectsAllowed          comProc
	GetIsZoomControlEnabled           comProc
	PutIsZoomControlEnabled           comProc
	GetIsBuiltInErrorPageEnabled      comProc
	PutIsBuiltInErrorPageEnabled      comProc
}

type coreWebView2Settings struct {
	vtbl *coreWebView2SettingsVtbl
}

// coreWebView2NavigationStartingEventArgsVtbl is
// ICoreWebView2NavigationStartingEventArgs — see the package doc comment for
// how this order was checked.
type coreWebView2NavigationStartingEventArgsVtbl struct {
	iUnknownVtbl
	GetUri             comProc
	GetIsUserInitiated comProc
	GetIsRedirected    comProc
	GetRequestHeaders  comProc
	GetCancel          comProc
	PutCancel          comProc
	GetNavigationId    comProc
}

type coreWebView2NavigationStartingEventArgs struct {
	vtbl *coreWebView2NavigationStartingEventArgsVtbl
}

// coreWebView2NewWindowRequestedEventArgsVtbl is
// ICoreWebView2NewWindowRequestedEventArgs — see the package doc comment for
// how this order was checked.
type coreWebView2NewWindowRequestedEventArgsVtbl struct {
	iUnknownVtbl
	GetUri             comProc
	PutNewWindow       comProc
	GetNewWindow       comProc
	PutHandled         comProc
	GetHandled         comProc
	GetIsUserInitiated comProc
	GetDeferral        comProc
	GetWindowFeatures  comProc
}

type coreWebView2NewWindowRequestedEventArgs struct {
	vtbl *coreWebView2NewWindowRequestedEventArgsVtbl
}

// eventRegistrationToken is EventRegistrationToken (WebView2.h): every
// add_Xxx call writes one through an out pointer. Nothing here ever calls
// the matching remove_Xxx, so the value is never read back, but a live
// pointer must be given — the callee's ABI does not know it is unwanted.
type eventRegistrationToken struct{ Value int64 }

// The two async completion handlers WebView2 calls back into, and the two
// event handlers. Each is IUnknown plus one Invoke slot; the four Go types
// exist only so a value's own type says which handler it is.
type handlerVtbl struct {
	iUnknownVtbl
	Invoke comProc
}
type environmentCompletedHandler struct{ vtbl *handlerVtbl }
type controllerCompletedHandler struct{ vtbl *handlerVtbl }
type navigationStartingHandler struct{ vtbl *handlerVtbl }
type newWindowRequestedHandler struct{ vtbl *handlerVtbl }

// Interface IDs from Microsoft.Web.WebView2 1.0.2903.40's WebView2.h.
var (
	iidIUnknown             = syscall.GUID{0, 0, 0, [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidEnvironmentCompleted = syscall.GUID{0x4e8a3389, 0xc9d8, 0x4bd2, [8]byte{0xb6, 0xb5, 0x12, 0x4f, 0xee, 0x6c, 0xc1, 0x4d}}
	iidControllerCompleted  = syscall.GUID{0x6c4819f3, 0xc9b7, 0x4260, [8]byte{0x81, 0x27, 0xc9, 0xf5, 0xbd, 0xe7, 0xf6, 0x8c}}
	iidNavigationStarting   = syscall.GUID{0x9adbe429, 0xf36d, 0x432b, [8]byte{0x9d, 0xdc, 0xf8, 0x88, 0x1f, 0xbd, 0x76, 0xe3}}
	iidNewWindowRequested   = syscall.GUID{0xd4c185fe, 0xc81c, 0x4989, [8]byte{0x97, 0xaf, 0x2d, 0x3f, 0xa7, 0xab, 0x56, 0x51}}
)

var (
	ole32          = syscall.NewLazyDLL("ole32.dll")
	coInitializeEx = ole32.NewProc("CoInitializeEx")
	coTaskMemFree  = ole32.NewProc("CoTaskMemFree")
)

const coinitApartmentThreaded = 0x2

// webView2Window holds the state OpenWebView2 builds up as WebView2's async
// callbacks fire, and the pieces the event handlers need.
//
// It also holds every handler object this file hands WebView2 a raw pointer
// to (envHandler, ctrlHandler, navHandler, newWinHandler): WebView2 is
// native code, invisible to Go's garbage collector, so a handler reachable
// from nowhere else in Go is a candidate for collection the moment its
// constructor returns, even though WebView2 still holds — and will later
// call through — the pointer it was given. Storing each one here for the
// window's whole life is what keeps it alive; the vendored github.com/jchv/
// go-webview2 kept its own four handlers the same way, as fields on its
// long-lived Chromium struct.
type webView2Window struct {
	win           *Window
	external      func(href string)
	origin        string // scheme://host[:port] of the page this window opened
	controller    *coreWebView2Controller
	envHandler    *environmentCompletedHandler
	ctrlHandler   *controllerCompletedHandler
	navHandler    *navigationStartingHandler
	newWinHandler *newWindowRequestedHandler
	ready         bool
	failed        error
}

// OpenWebView2 shows pageURL in a WebView2-hosted window titled title, sized
// width by height with a minimum client area of minWidth by minHeight
// (96-dpi units), and blocks until that window is closed. external is
// called, on the window's own thread, with the target URI of any link or
// window.open() that would navigate away from pageURL's own origin — the
// caller opens that URI in the system browser; the Panel window and its
// current page are left exactly where they were (research/panel-native/
// DECISION.md item 2, commit 4608a4da).
//
// It reports false, having opened no window, when the WebView2 runtime is
// absent or environment/controller setup fails at any step; the caller
// falls back to opening pageURL in the browser itself, the same contract
// desktop_windows.go already had with the vendored library.
func OpenWebView2(title string, width, height, minWidth, minHeight int, pageURL string, external func(href string)) bool {
	if !WebView2Available() {
		return false
	}
	origin, err := url.Parse(pageURL)
	if err != nil || origin.Host == "" {
		return false
	}

	w := &webView2Window{external: external, origin: origin.Scheme + "://" + origin.Host}
	runErr := RunWithMinimum(title, width, height, minWidth, minHeight, func(win *Window) {
		w.win = win
		w.attach(pageURL)
		if w.failed != nil {
			fmt.Fprintln(os.Stderr, "WebView2:", w.failed) //unchecked: stderr is the final startup diagnostic; a write failure cannot make the native window ready
			win.Close()
		}
	})
	return runErr == nil && w.ready
}

// attach kicks off environment creation and pumps this thread's message
// queue itself — the same queue win.Run's own pump will take over once this
// returns — until the async chain finishes or fails. WebView2's completion
// handlers arrive as ordinary window messages on whichever thread called
// CreateCoreWebView2EnvironmentWithOptions, which is why this must run on
// the locked OS thread win.Run's build callback already runs on, before
// win.Run starts showing the window.
func (w *webView2Window) attach(pageURL string) {
	if r, _, _ := coInitializeEx.Call(0, coinitApartmentThreaded); int32(r) < 0 { //unchecked: the HRESULT in r1 is the answer; LazyProc.Call's error is GetLastError, which a COM entry point does not set
		// RPC_E_CHANGED_MODE means some other package already initialized
		// this thread's COM apartment differently; every other outcome,
		// including S_FALSE for "already initialized the same way", is
		// non-negative and never reaches here.
		w.failed = fmt.Errorf("CoInitializeEx: %#x", uint32(r))
		return
	}

	proc, err := webviewCreateProc()
	if err != nil {
		w.failed = err
		return
	}
	dataPathPtr, err := syscall.UTF16PtrFromString(filepath.Join(os.Getenv("AppData"), "Abstraction Panel"))
	if err != nil {
		w.failed = err
		return
	}
	handler := w.newEnvironmentCompletedHandler(pageURL)
	hr, _, _ := proc.Call(1, 0, uintptr(unsafe.Pointer(dataPathPtr)), 0, uintptr(unsafe.Pointer(handler))) //unchecked: HRESULT is the result; checkRunningInstance=1, runtimeType=0 (Evergreen)
	if int32(hr) < 0 {
		w.failed = fmt.Errorf("CreateWebViewEnvironmentWithOptionsInternal: %#x", uint32(hr))
		return
	}

	var m message
	for !w.ready && w.failed == nil {
		r, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0) //unchecked: GetMessage answers in r1 (-1 is the failure), read on the next line
		if int32(r) <= 0 {
			w.failed = fmt.Errorf("window closed before WebView2 finished attaching")
			return
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&m))) //unchecked: TranslateMessage reports whether a character was produced, never a failure
		dispatchMessage.Call(uintptr(unsafe.Pointer(&m)))  //unchecked: DispatchMessage returns the window procedure's value, never a failure
	}
}

// newEnvironmentCompletedHandler builds the handler and stores it on w
// (w.envHandler) before returning it, so it stays reachable for the rest of
// the window's life — see webView2Window's doc comment.
func (w *webView2Window) newEnvironmentCompletedHandler(pageURL string) *environmentCompletedHandler {
	invoke := func(this, hr uintptr, env *coreWebView2Environment) uintptr {
		if int32(hr) < 0 || env == nil {
			w.failed = fmt.Errorf("environment creation: %#x", uint32(hr))
			return 0
		}
		// A completion handler's interface-pointer parameter is only valid
		// for the duration of this Invoke call unless it is AddRef'd — the
		// vendored library did this for the same three objects (env,
		// controller, webview), and omitting it here first showed up as a
		// window that painted once and then crashed on the next WM_SIZE,
		// once WebView2 had released its own temporary reference.
		env.vtbl.AddRef.call(uintptr(unsafe.Pointer(env)))
		controllerHandler := w.newControllerCompletedHandler(pageURL)
		if hr := env.vtbl.CreateCoreWebView2Controller.call(
			uintptr(unsafe.Pointer(env)), uintptr(w.win.h), uintptr(unsafe.Pointer(controllerHandler))); int32(hr) < 0 {
			w.failed = fmt.Errorf("CreateCoreWebView2Controller: %#x", uint32(hr))
		}
		return 0
	}
	w.envHandler = &environmentCompletedHandler{vtbl: &handlerVtbl{
		iUnknownVtbl: constUnknown(iidEnvironmentCompleted),
		Invoke:       comProc(syscall.NewCallback(invoke)),
	}}
	return w.envHandler
}

// newControllerCompletedHandler stores itself on w.ctrlHandler; see
// newEnvironmentCompletedHandler.
func (w *webView2Window) newControllerCompletedHandler(pageURL string) *controllerCompletedHandler {
	invoke := func(this, hr uintptr, controller *coreWebView2Controller) uintptr {
		if int32(hr) < 0 || controller == nil {
			w.failed = fmt.Errorf("controller creation: %#x", uint32(hr))
			return 0
		}
		controller.vtbl.AddRef.call(uintptr(unsafe.Pointer(controller)))
		w.controller = controller
		if err := w.finish(pageURL); err != nil {
			w.failed = err
			return 0
		}
		w.ready = true
		return 0
	}
	w.ctrlHandler = &controllerCompletedHandler{vtbl: &handlerVtbl{
		iUnknownVtbl: constUnknown(iidControllerCompleted),
		Invoke:       comProc(syscall.NewCallback(invoke)),
	}}
	return w.ctrlHandler
}

// finish runs once the controller exists: it fetches the ICoreWebView2,
// disables the default context menu and dev tools (research/panel-native/
// DECISION.md's plain window, matching the debug=false the vendored library
// was always given), sizes the controller to the window's current client
// area, registers the two navigation-redirecting event handlers, wires
// resize, and navigates.
func (w *webView2Window) finish(pageURL string) error {
	var webview *coreWebView2
	if hr := w.controller.vtbl.GetCoreWebView2.call(
		uintptr(unsafe.Pointer(w.controller)), uintptr(unsafe.Pointer(&webview))); int32(hr) < 0 || webview == nil {
		return fmt.Errorf("GetCoreWebView2: %#x", uint32(hr))
	}
	// Matches the AddRef on env and controller above, and the vendored
	// library's own CreateCoreWebView2ControllerCompleted, which AddRefs
	// GetCoreWebView2's result the same way.
	webview.vtbl.AddRef.call(uintptr(unsafe.Pointer(webview)))

	var settings *coreWebView2Settings
	if hr := webview.vtbl.GetSettings.call(
		uintptr(unsafe.Pointer(webview)), uintptr(unsafe.Pointer(&settings))); int32(hr) == 0 && settings != nil {
		settings.vtbl.PutAreDefaultContextMenusEnabled.call(uintptr(unsafe.Pointer(settings)), 0)
		settings.vtbl.PutAreDevToolsEnabled.call(uintptr(unsafe.Pointer(settings)), 0)
	}

	w.resize()
	w.win.OnSize(func(int, int) { w.resize() })
	// A freshly created controller does not default to visible; without
	// this the window paints as a blank client area forever, even though
	// bounds, navigation and every event handler are otherwise correct.
	w.controller.vtbl.PutIsVisible.call(uintptr(unsafe.Pointer(w.controller)), 1)

	var token eventRegistrationToken
	navHandler := w.newNavigationStartingHandler()
	webview.vtbl.AddNavigationStarting.call(
		uintptr(unsafe.Pointer(webview)), uintptr(unsafe.Pointer(navHandler)), uintptr(unsafe.Pointer(&token)))
	newWinHandler := w.newNewWindowRequestedHandler()
	webview.vtbl.AddNewWindowRequested.call(
		uintptr(unsafe.Pointer(webview)), uintptr(unsafe.Pointer(newWinHandler)), uintptr(unsafe.Pointer(&token)))

	target, err := syscall.UTF16PtrFromString(pageURL)
	if err != nil {
		return err
	}
	if hr := webview.vtbl.Navigate.call(
		uintptr(unsafe.Pointer(webview)), uintptr(unsafe.Pointer(target))); int32(hr) < 0 {
		return fmt.Errorf("Navigate: %#x", uint32(hr))
	}
	return nil
}

// resize fills the window's exact current client area — the controller
// doesn't track the window it is hosted in on its own — the same call
// github.com/jchv/go-webview2's own Chromium.Resize made on every WM_SIZE.
func (w *webView2Window) resize() {
	if w.controller == nil {
		return
	}
	var bounds rect
	getClientRect.Call(uintptr(w.win.h), uintptr(unsafe.Pointer(&bounds))) //unchecked: on our own live window handle GetClientRect fails only once the window is gone, and the resize below is then moot
	w.controller.vtbl.PutBounds.call(uintptr(unsafe.Pointer(w.controller)), uintptr(unsafe.Pointer(&bounds)))
}

// readURI reads a NavigationStarting/NewWindowRequested args' Uri and frees
// it — CoTaskMemFree, per the WebView2 API's string convention for an
// [out] LPWSTR.
func readURI(getURI comProc, args unsafe.Pointer) string {
	var uri *uint16
	getURI.call(uintptr(args), uintptr(unsafe.Pointer(&uri)))
	if uri == nil {
		return ""
	}
	defer coTaskMemFree.Call(uintptr(unsafe.Pointer(uri)))
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(uri), uintptr(n)*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(uri, n))
}

// isExternal reports that target does not share pageOrigin's scheme and
// host[:port] — the same same-origin test desktop_windows.go's injected
// externalLinkInterceptJS used to make in JavaScript.
func isExternal(pageOrigin, target string) bool {
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	return u.Scheme+"://"+u.Host != pageOrigin
}

// newNavigationStartingHandler cancels any main-frame navigation leaving
// pageURL's origin and hands it to external instead — a link without
// target="_blank", or a script-driven location change.
func (w *webView2Window) newNavigationStartingHandler() *navigationStartingHandler {
	invoke := func(this, sender uintptr, args *coreWebView2NavigationStartingEventArgs) uintptr {
		uri := readURI(args.vtbl.GetUri, unsafe.Pointer(args))
		if isExternal(w.origin, uri) {
			args.vtbl.PutCancel.call(uintptr(unsafe.Pointer(args)), 1)
			if w.external != nil {
				w.external(uri)
			}
		}
		return 0
	}
	w.navHandler = &navigationStartingHandler{vtbl: &handlerVtbl{
		iUnknownVtbl: constUnknown(iidNavigationStarting),
		Invoke:       comProc(syscall.NewCallback(invoke)),
	}}
	return w.navHandler
}

// newNewWindowRequestedHandler cancels every window.open()/target="_blank"
// popup and hands its target to external — commit 4608a4da's "help links
// open in the system browser, the Panel window stays".
func (w *webView2Window) newNewWindowRequestedHandler() *newWindowRequestedHandler {
	invoke := func(this, sender uintptr, args *coreWebView2NewWindowRequestedEventArgs) uintptr {
		uri := readURI(args.vtbl.GetUri, unsafe.Pointer(args))
		args.vtbl.PutHandled.call(uintptr(unsafe.Pointer(args)), 1)
		if w.external != nil && (strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://")) {
			w.external(uri)
		}
		return 0
	}
	w.newWinHandler = &newWindowRequestedHandler{vtbl: &handlerVtbl{
		iUnknownVtbl: constUnknown(iidNewWindowRequested),
		Invoke:       comProc(syscall.NewCallback(invoke)),
	}}
	return w.newWinHandler
}

// constUnknown builds fresh QueryInterface/AddRef/Release trampolines for
// one handler instance. AddRef and Release report a constant 1 rather than
// really counting references — the same shortcut the vendored library's own
// handler objects took — which is safe because every handler this file
// creates is stored on the webView2Window that owns it (see its doc
// comment) for the whole life of the one WebView2 window, so nothing frees
// or moves it while WebView2 might still call it back. QueryInterface
// supports IUnknown and the handler's own interface, as COM requires.
func constUnknown(iid syscall.GUID) iUnknownVtbl {
	return iUnknownVtbl{
		QueryInterface: comProc(syscall.NewCallback(func(this uintptr, riid *syscall.GUID, ppv *uintptr) uintptr {
			if ppv == nil || riid == nil {
				return 0x80004003 // E_POINTER
			}
			*ppv = 0
			requested := *riid
			if requested != iidIUnknown && requested != iid {
				return 0x80004002 // E_NOINTERFACE
			}
			*ppv = this
			return 0 // S_OK
		})),
		AddRef:  comProc(syscall.NewCallback(func(this uintptr) uintptr { return 1 })),
		Release: comProc(syscall.NewCallback(func(this uintptr) uintptr { return 1 })),
	}
}
