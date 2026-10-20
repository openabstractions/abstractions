//go:build windows

package main

import (
	"context"
	"errors"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/openabstractions/abstractions/monitor/win"
)

func windowed() bool { return win.Windowed() }

func nativeWebView2Available() bool { return win.WebView2Available() }

// resourceWindowAvailable is win.WebView2Available; tests replace it.
var resourceWindowAvailable = win.WebView2Available
var resourceWindowOpen = win.OpenWebView2

// fail ends the Panel. A Panel that stops shows no popup (architect popup
// ruling 2026-11-10, row 33); the next open starts it again. A missing
// WebView2 Runtime, which only the person can install, is one notification:
// the Panel draws no native question (ARCHITECTURE.md).
func fail(native bool, err error) {
	if !native {
		log.Fatal(err)
	}
	log.Print(err)
	if errors.Is(err, errNoWebView2) {
		ctx, cancel := context.WithTimeout(context.Background(), trayAnswerBudget)
		if err := postPresenterNotice(ctx, webView2MissingNotice); err != nil {
			log.Printf("WebView2 notice: %v", err)
		}
		cancel()
	}
	os.Exit(1)
}

// webView2MissingNotice is the notification a Panel without the WebView2
// Runtime posts.
var webView2MissingNotice = presenterNotice{Title: "The Panel can't open", Body: "Install the Microsoft Edge WebView2 Runtime to open the Panel."}

// The native window starts at the same size every time and remembers nothing
// between runs (research/panel-native/DECISION.md item 2). The minimum keeps
// its WebView content usable while a person resizes it.
const (
	panelWindowTitle     = "OpenAbstractions Panel"
	panelWindowWidth     = 1100
	panelWindowHeight    = 760
	panelWindowMinWidth  = 960
	panelWindowMinHeight = 640
)

// errNoWebView2 is what a person sees when Windows lacks the WebView2
// runtime. The Panel opens no listener and no browser then; the browser is
// the developer mode, -native=false.
var errNoWebView2 = errors.New("The Panel window needs the Microsoft Edge WebView2 Runtime, which is not installed on this computer.")

// runResourceWindow shows this run in a WebView2 window whose every request
// the Panel answers in process (panel_resource.go); nothing listens. It
// returns once the window closes.
func runResourceWindow(run *panelRun, quiet bool) error {
	if !resourceWindowAvailable() {
		return errNoWebView2
	}
	resources, start := run.resources(panelResourceOriginWindows)
	// A Start-menu GUI launch has no console. Its optional announcement must
	// leave window creation independent of that missing output handle.
	//unchecked: the native window is the result; stdout is an optional announcement
	if run.owned == nil {
		writePanelURL(os.Stdout, panelWindowAnnouncement, quiet)
	}
	title := run.panel.windowTitle
	if title == "" {
		title = panelWindowTitle
	}
	serve := &win.Resources{Origin: panelResourceOriginWindows, Headers: panelResourceHeaders,
		BindDirectoryPicker: func(window context.Context, pick func(context.Context) (string, error)) {
			control := run.panel.claudeConnectionsForPanel()
			run.panel.mu.Lock()
			run.panel.directoryPicker = pick
			run.panel.mu.Unlock()
			control.mu.Lock()
			control.picker = pick
			control.window = window
			control.mu.Unlock()
			context.AfterFunc(window, control.close)
			context.AfterFunc(window, run.panel.codexConnectionsForPanel().close)
		},
		Serve: func(ctx context.Context, r win.ResourceRequest) win.ResourceResponse {
			out := resources.serve(ctx, panelResourceRequest{Method: r.Method, URL: r.URL, Header: r.Header, Body: r.Body})
			return win.ResourceResponse{Status: out.Status, Header: out.Header, Body: out.Body, Aborted: out.Aborted}
		}}
	if run.owned == nil {
		serve.SettingsInstance = "installed"
		if panelExplicit != nil {
			serve.SettingsInstance = panelExplicit.Endpoint
		}
	}
	if run.owned != nil {
		serve.DocumentURL = start
		serve.BindVisible = run.owned.bindVisible
		serve.BodyLimit = func(method, raw string) int {
			u, err := url.Parse(raw)
			if err == nil && strings.HasPrefix(u.Path, "/native-host/") {
				return panelOwnedFrameLimit
			}
			return 16 << 20
		}
	}
	// The window keeps DWM Mica behind a transparent WebView2 and pushes a
	// change of Windows accent colour onto the open page
	// (panelAccentScript) for static/skin-windows.css
	// (research/panel-redesign/SPEC.md §5).
	if !resourceWindowOpen(title, panelWindowWidth, panelWindowHeight,
		panelWindowMinWidth, panelWindowMinHeight, start, serve, openExternal, panelAccentScript) {
		return errors.New("The Panel window could not open.")
	}
	return nil
}

// openExternal is win.OpenWebView2's external callback: it is called with
// the target URI of a link or window.open() that would leave the Panel's
// own origin — a help link (target="_blank", per help.go) is the only kind
// this app ever shows. It only ever launches http(s) URLs through
// rundll32, and leaves the Panel window exactly where it was
// (commit 4608a4da). The one other target is Windows Settings at Apps,
// Setup uses that link for app removal.
func openExternal(rawURL string) {
	if !externalTarget(rawURL) {
		return
	}
	launch(rawURL)
}
