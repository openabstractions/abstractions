//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/openabstractions/abstractions/monitor/win"
)

func windowed() bool { return win.Windowed() }

func nativeWebView2Available() bool { return win.WebView2Available() }

// fail says what went wrong somewhere a person will see it. A binary built for
// the desktop has no stderr at all, so the log line this program would print
// goes nowhere and the window simply never appears.
func fail(native bool, err error) {
	if !native {
		log.Fatal(err)
	}
	win.Say("Abstraction — control panel", err.Error())
	os.Exit(1)
}

// The native window starts at the same size every time and remembers nothing
// between runs (research/panel-native/DECISION.md item 2). The minimum keeps
// its WebView content usable while a person resizes it.
const (
	panelWindowTitle     = "Abstraction Panel"
	panelWindowWidth     = 1100
	panelWindowHeight    = 760
	panelWindowMinWidth  = 960
	panelWindowMinHeight = 640
)

// desktop opens the Panel's own loopback URL — the same page and per-run key
// a browser tab would get — in a WebView2 window, and returns once that
// window closes, ending the process the way the Win32 list window it
// replaced did. When this machine has no WebView2 runtime, it opens the
// browser instead and blocks so the server (already serving in its own
// goroutine) stays up for that tab.
func (p *servicePanel) desktop(url string) error {
	if openNativeWindow(url) {
		return nil
	}
	select {}
}

// openNativeWindow shows url in a WebView2 window (win.OpenWebView2) and
// blocks until it is closed, reporting true. It reports false, having
// opened the browser instead and said so on stderr, when the WebView2
// runtime is absent or the window fails to attach at any step —
// win.WebView2Available resolves the installed runtime's environment entry point.
func openNativeWindow(url string) bool {
	if !win.WebView2Available() {
		fmt.Fprintln(os.Stderr, "WebView2 runtime not found; opening the Panel in the browser instead.") //unchecked: a notice on stderr; the browser fallback follows either way
		launch(url)
		return false
	}
	if win.OpenWebView2(panelWindowTitle, panelWindowWidth, panelWindowHeight,
		panelWindowMinWidth, panelWindowMinHeight, url, openExternal) {
		return true
	}
	fmt.Fprintln(os.Stderr, "WebView2 window failed to open; opening the Panel in the browser instead.") //unchecked: a notice on stderr; the browser fallback follows either way
	launch(url)
	return false
}

// openExternal is win.OpenWebView2's external callback: it is called with
// the target URI of a link or window.open() that would leave the Panel's
// own loopback origin — a help link (target="_blank", per help.go) is the
// only kind this app ever shows. It only ever launches http(s) URLs through
// the same rundll32 path the browser fallback above already uses, and
// leaves the Panel window exactly where it was (commit 4608a4da).
func openExternal(rawURL string) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return
	}
	launch(rawURL)
}
