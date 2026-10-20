package main

import (
	"context"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

type openAppFake struct {
	setupApplicationsFake
	result fwire.ApplicationActivationResult
}

func (f *openAppFake) Activate(context.Context, string) (fwire.ApplicationActivationResult, error) {
	f.activateCalls++
	return f.result, nil
}

// The Start-menu shortcut's -open-app sends one open through the runtime's
// owned launch; a refusal or a missing entry reports, and the Panel then
// opens the app's page.
func TestOpenAppSendsOneOwnedOpen(t *testing.T) {
	var shown []string
	withdrawn := 0
	prior := openingNotice
	openingNotice = func(_ context.Context, title string) func() {
		shown = append(shown, title)
		return func() { withdrawn++ }
	}
	t.Cleanup(func() { openingNotice = prior })
	defer func() {
		// The ready and refused opens each show one "Opening" notice and
		// withdraw it; an app missing from Apps shows none.
		if len(shown) != 2 || shown[0] != "ComfyUI — OA build" || withdrawn != 2 {
			t.Fatalf("opening notices %q withdrawn %d", shown, withdrawn)
		}
	}()
	entry := fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: fwire.ApplicationDescriptor{Name: "comfyui", Title: "ComfyUI — OA build"}}}}
	ready := &openAppFake{setupApplicationsFake: setupApplicationsFake{page: entry}, result: fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeReady, Started: true}}
	if err := openRegisteredApplication(context.Background(), ready, "comfyui"); err != nil || ready.activateCalls != 1 {
		t.Fatalf("ready open: calls=%d err=%v", ready.activateCalls, err)
	}
	refused := &openAppFake{setupApplicationsFake: setupApplicationsFake{page: entry}, result: fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeNotReady, Reason: "consent"}}
	if err := openRegisteredApplication(context.Background(), refused, "comfyui"); err == nil || !strings.Contains(err.Error(), "consent") {
		t.Fatalf("refused open: %v", err)
	}
	missing := &openAppFake{setupApplicationsFake: setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}}}
	if err := openRegisteredApplication(context.Background(), missing, "comfyui"); err == nil || missing.activateCalls != 0 {
		t.Fatalf("open of an app not in Apps: calls=%d err=%v", missing.activateCalls, err)
	}
	if page := openAppFromShortcut("Not A Name"); page != "overview" {
		t.Fatalf("an invalid name opened %q", page)
	}
}
