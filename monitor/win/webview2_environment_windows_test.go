//go:build windows

package win

import (
	"os"
	"slices"
	"testing"
)

// Security review 2026-11-22: no WEBVIEW2_ variable the Panel was started
// with reaches its browser process.
func TestWebView2EnvironmentIsCleared(t *testing.T) {
	names := webView2Variables([]string{"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9222", "webview2_browser_executable_folder=x", "PATH=x", "WEBVIEW2_USER_DATA_FOLDER=y"})
	if !slices.Equal(names, []string{"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "webview2_browser_executable_folder", "WEBVIEW2_USER_DATA_FOLDER"}) {
		t.Fatal(names)
	}
	t.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--remote-debugging-port=9222")
	t.Setenv("WEBVIEW2_BROWSER_EXECUTABLE_FOLDER", `C:\elsewhere`)
	clearWebView2Environment()
	for _, name := range []string{"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "WEBVIEW2_BROWSER_EXECUTABLE_FOLDER"} {
		if _, ok := os.LookupEnv(name); ok {
			t.Fatal(name, "survived")
		}
	}
}
