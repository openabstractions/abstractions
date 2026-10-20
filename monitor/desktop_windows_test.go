//go:build windows

package main

import (
	"errors"
	"net"
	"testing"
)

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

func TestStandalonePersonalReviewNeverOpensWebView(t *testing.T) {
	previous, available, context := personalPermissionWindow, resourceWindowAvailable, startPermissionReview
	defer func() {
		personalPermissionWindow, resourceWindowAvailable, startPermissionReview = previous, available, context
	}()
	_, _, _, startPermissionReview = reviewFixture(t)
	called := false
	personalPermissionWindow = func(p *servicePanel) error { called = true; return nil }
	resourceWindowAvailable = func() bool { t.Fatal("standalone review consulted WebView"); return false }
	if err := runServicePanel("127.0.0.1:0", true, true, "permission-review", true, applicationStart{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("standalone native surface not used")
	}
}
