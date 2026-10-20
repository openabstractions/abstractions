package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A running step names its kind and progress in the Panel state, and Cancel
// stops it; after it ends the state names nothing and Cancel refuses.
func TestSetupStepReportsProgressAndCancels(t *testing.T) {
	p := &servicePanel{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress, end := p.beginSetupStep("comfyui-app", "remove", ctx, cancel)
	progress(200, 39200)
	a := p.setupActionsNow()["comfyui-app"]
	if a.Kind != "remove" || a.Done != 200 || a.Total != 39200 || a.ProgressAt == "" {
		t.Fatalf("running step %+v", a)
	}
	cancelRequest := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.setupCancelStep(w, httptest.NewRequest(http.MethodPost, "/setup/cancel-step", strings.NewReader(`{"component_id":"comfyui-app"}`)))
		return w
	}
	if w := cancelRequest(); w.Code != http.StatusOK || ctx.Err() == nil {
		t.Fatalf("Cancel: %d %q, step context %v", w.Code, w.Body.String(), ctx.Err())
	}
	end()
	if len(p.setupActionsNow()) != 0 {
		t.Fatal("an ended step stayed listed")
	}
	if w := cancelRequest(); w.Code != http.StatusConflict {
		t.Fatalf("Cancel with nothing running: %d", w.Code)
	}
	// A late report from an ended step changes nothing.
	progress(300, 39200)
	if len(p.setupActionsNow()) != 0 {
		t.Fatal("a late report listed an ended step")
	}
}
