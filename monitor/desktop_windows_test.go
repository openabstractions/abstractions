//go:build windows

package main

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/win"
)

func TestNativeSettingsInstanceExcludesInstallerDialogs(t *testing.T) {
	available, open, explicit := resourceWindowAvailable, resourceWindowOpen, panelExplicit
	defer func() { resourceWindowAvailable, resourceWindowOpen, panelExplicit = available, open, explicit }()
	resourceWindowAvailable = func() bool { return true }
	panelExplicit = nil
	for _, test := range []struct{ page, instance string }{
		{"overview", "installed"}, {"setup", "installed"},
		// An installer's request opens no window; a page named for it is
		// the Panel on Apps.
		{"register-application", "installed"}, {"remove-application", "installed"},
	} {
		t.Run(test.page, func(t *testing.T) {
			resourceWindowOpen = func(_ string, _, _, _, _ int, _ string, resources *win.Resources, _ func(string), _ func() string) bool {
				if resources.SettingsInstance != test.instance {
					t.Errorf("instance = %q, want %q", resources.SettingsInstance, test.instance)
				}
				return true
			}
			run, err := newPanelRun(test.page)
			if err != nil {
				t.Fatal(err)
			}
			if err := runResourceWindow(run, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Without the WebView2 runtime the native Panel refuses with a message and
// opens neither a listener nor a browser (owner decision, 2026-10-07).
func TestNativePanelWithoutWebView2BindsNothing(t *testing.T) {
	listened := false
	previousListen, previousAvailable := panelListen, resourceWindowAvailable
	panelListen = func(network, address string) (net.Listener, error) {
		listened = true
		return previousListen(network, address)
	}
	resourceWindowAvailable = func() bool { return false }
	defer func() { panelListen, resourceWindowAvailable = previousListen, previousAvailable }()

	err := runServicePanel("127.0.0.1:0", true, true, "overview", true, applicationStart{})
	if !errors.Is(err, errNoWebView2) {
		t.Fatalf("err = %v, want the WebView2 message", err)
	}
	if listened {
		t.Fatal("the native Panel bound a listener")
	}
}

func TestNativePanelClosedStdoutStillOpensWindow(t *testing.T) {
	previousStdout, available, open := os.Stdout, resourceWindowAvailable, resourceWindowOpen
	defer func() { os.Stdout, resourceWindowAvailable, resourceWindowOpen = previousStdout, available, open }()
	closed, err := os.CreateTemp(t.TempDir(), "closed-stdout")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = closed
	resourceWindowAvailable = func() bool { return true }
	for _, windowOpened := range []bool{true, false} {
		called := false
		resourceWindowOpen = func(title string, width, height, minWidth, minHeight int, start string, resources *win.Resources, external func(string), accent func() string) bool {
			called = true
			if !strings.Contains(start, "/setup") || resources == nil {
				t.Errorf("wrong native setup launch: %q", start)
			}
			return windowOpened
		}
		err := runServicePanel("127.0.0.1:0", false, true, "setup", false, applicationStart{})
		if !called {
			t.Fatal("closed stdout prevented native window creation")
		}
		if windowOpened && err != nil {
			t.Fatalf("native GUI failed for stdout: %v", err)
		}
		if !windowOpened && (err == nil || err.Error() != "The Panel window could not open.") {
			t.Fatalf("window failure was lost: %v", err)
		}
	}
}

func TestNativePanelWorkingStdoutKeepsAnnouncement(t *testing.T) {
	previousStdout, available, open := os.Stdout, resourceWindowAvailable, resourceWindowOpen
	defer func() { os.Stdout, resourceWindowAvailable, resourceWindowOpen = previousStdout, available, open }()
	output, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	os.Stdout = output
	resourceWindowAvailable = func() bool { return true }
	resourceWindowOpen = func(string, int, int, int, int, string, *win.Resources, func(string), func() string) bool {
		return true
	}
	if err := runServicePanel("127.0.0.1:0", false, true, "setup", false, applicationStart{}); err != nil {
		t.Fatal(err)
	}
	if err := output.Sync(); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(output.Name())
	if err != nil || string(text) != "service control panel: window\n" {
		t.Fatalf("announcement %q: %v", text, err)
	}
}

func TestDeveloperPanelClosedStdoutKeepsWriteFailure(t *testing.T) {
	previousStdout, listen := os.Stdout, panelListen
	defer func() { os.Stdout, panelListen = previousStdout, listen }()
	closed, err := os.CreateTemp(t.TempDir(), "closed-stdout")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = closed
	var bound net.Listener
	panelListen = func(network, address string) (net.Listener, error) {
		var err error
		bound, err = listen(network, address)
		return bound, err
	}
	defer func() {
		if bound != nil {
			bound.Close()
		}
	}()
	if err := runServicePanel("127.0.0.1:0", false, false, "setup", false, applicationStart{}); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("developer HTTP swallowed stdout failure: %v", err)
	}
}

// The HTTP mode is the explicit developer mode and still listens on loopback.
func TestBrowserPanelStillListensOnLoopback(t *testing.T) {
	p, server, listener, url, err := bindPanelPage("127.0.0.1:0", "overview")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer listener.Close()
	if p == nil || url == "" || !loopback(listener.Addr().(*net.TCPAddr).IP.String()) {
		t.Fatalf("listener %v url %q", listener.Addr(), url)
	}
}

// A standalone permission review is a Panel page: its Allow button is the one
// confirmation, and the Panel draws no native question for it.
func TestStandalonePersonalReviewOpensThePanelPage(t *testing.T) {
	available, open, context := resourceWindowAvailable, resourceWindowOpen, startPermissionReview
	defer func() {
		resourceWindowAvailable, resourceWindowOpen, startPermissionReview = available, open, context
	}()
	_, _, _, startPermissionReview = reviewFixture(t)
	resourceWindowAvailable = func() bool { return true }
	opened := ""
	resourceWindowOpen = func(_ string, _, _, _, _ int, start string, resources *win.Resources, _ func(string), _ func() string) bool {
		if resources == nil {
			t.Error("the review window has no resources")
		}
		opened = start
		return true
	}
	if err := runServicePanel("127.0.0.1:0", true, true, "permission-review", true, applicationStart{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(opened, "/permission-review") {
		t.Fatalf("the review opened %q, want the Panel's permission review page", opened)
	}
}
