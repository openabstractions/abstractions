//go:build windows

package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"

	"github.com/openabstractions/abstractions/monitor/win"
)

func windowed() bool { return win.Windowed() }

func nativeWebView2Available() bool { return win.WebView2Available() }

// resourceWindowAvailable is win.WebView2Available; tests replace it.
var resourceWindowAvailable = win.WebView2Available
var personalPermissionWindow = runPersonalPermissionWindow

// fail says what went wrong somewhere a person will see it. A binary built for
// the desktop has no stderr at all, so the log line this program would print
// goes nowhere and the window simply never appears.
func fail(native bool, err error) {
	if !native {
		log.Fatal(err)
	}
	win.Say(panelWindowTitle, err.Error())
	os.Exit(1)
}

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
	if run.start.path == "/permission-review" && startPermissionReview != (permissionReviewContext{}) {
		return personalPermissionWindow(run.panel)
	}
	if !resourceWindowAvailable() {
		return errNoWebView2
	}
	resources, start := run.resources(panelResourceOriginWindows)
	if err := writePanelURL(os.Stdout, panelWindowAnnouncement, quiet); err != nil {
		return err
	}
	title := run.panel.windowTitle
	if title == "" {
		title = panelWindowTitle
	}
	serve := &win.Resources{Origin: panelResourceOriginWindows, Headers: panelResourceHeaders,
		Serve: func(ctx context.Context, r win.ResourceRequest) win.ResourceResponse {
			out := resources.serve(ctx, panelResourceRequest{Method: r.Method, URL: r.URL, Header: r.Header, Body: r.Body})
			return win.ResourceResponse{Status: out.Status, Header: out.Header, Body: out.Body, Aborted: out.Aborted}
		}}
	// The window keeps DWM Mica behind a transparent WebView2 and pushes a
	// change of Windows accent colour onto the open page
	// (panelAccentScript) for static/skin-windows.css
	// (research/panel-redesign/SPEC.md §5).
	if !win.OpenWebView2(title, panelWindowWidth, panelWindowHeight,
		panelWindowMinWidth, panelWindowMinHeight, start, serve, openExternal, panelAccentScript) {
		return errors.New("The Panel window could not open.")
	}
	return nil
}

// The standalone review uses the same session-aware native surface as Asks.
// Its hidden message-loop owner never creates a WebView or management page.
func runPersonalPermissionWindow(panel *servicePanel) error {
	done := make(chan error, 1)
	err := win.RunHidden(panelWindowTitle, func(w *win.Window) {
		session := &trayWindowsSession{prompting: true}
		if err := w.WatchSessionChanges(func(locked bool) {
			if locked {
				session.revision.Add(1)
				win.DismissActiveQuestion()
			}
		}); err != nil {
			done <- err
			w.Close()
			return
		}
		surface := &trayWindowsSurface{w: w, session: session}
		go func() {
			done <- panel.askPersonalPermissionReview(context.Background(), session, surface)
			w.Do(w.Close)
		}()
	})
	if err != nil {
		return err
	}
	return finishPersonalPermissionReview(<-done, nil)
}

// openExternal is win.OpenWebView2's external callback: it is called with
// the target URI of a link or window.open() that would leave the Panel's
// own origin — a help link (target="_blank", per help.go) is the only kind
// this app ever shows. It only ever launches http(s) URLs through
// rundll32, and leaves the Panel window exactly where it was
// (commit 4608a4da).
func openExternal(rawURL string) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return
	}
	launch(rawURL)
}
