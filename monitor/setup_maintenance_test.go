package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestSetupMaintenanceReviewConfirmationGuardAndTruthfulHandoff(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	opened := 0
	p := &servicePanel{setupJournal: &journal,
		maintenanceReview: func(context.Context) setup.MaintenanceReview {
			return setup.MaintenanceReview{Available: true, PlanID: "reviewed", Version: "0.8.0"}
		}, maintenanceOpen: func(_ context.Context, id string) error {
			if id != "reviewed" {
				return errors.New("installed Setup changed; review it again")
			}
			opened++
			return nil
		}}
	handler := p.handler("setup-test-key")
	call := installHandlerFor(handler)
	if got := call("GET", "/setup/maintenance", ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"plan_id":"reviewed"`) || opened != 0 {
		t.Fatalf("read-only review: %d %s", got.Code, got.Body.String())
	}
	for _, body := range []string{`{"plan_id":"reviewed"}`, `{"confirm":true}`, `{"plan_id":"reviewed","confirm":true,"selected":["opencode"]}`, `{"plan_id":"reviewed","confirm":true,"program":"arbitrary.exe"}`} {
		if got := call("POST", "/setup/maintenance", body); got.Code != http.StatusBadRequest || opened != 0 {
			t.Fatalf("invalid approval: %d %s", got.Code, got.Body.String())
		}
	}
	if got := call("POST", "/setup/maintenance", `{"plan_id":"stale","confirm":true}`); got.Code != http.StatusConflict || opened != 0 {
		t.Fatalf("stale review: %d %s", got.Code, got.Body.String())
	}
	for _, headers := range []map[string]string{{}, {"X-Panel-Key": "wrong"}, {"X-Panel-Key": "setup-test-key", "Origin": "https://foreign.invalid"}} {
		req := httptest.NewRequest("POST", "/setup/maintenance", strings.NewReader(`{"plan_id":"reviewed","confirm":true}`))
		req.Host = "127.0.0.1"
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		if got.Code != http.StatusForbidden || opened != 0 {
			t.Fatalf("guard bypass: %d %s", got.Code, got.Body.String())
		}
	}
	got := call("POST", "/setup/maintenance", `{"plan_id":"reviewed","confirm":true}`)
	if got.Code != 200 || opened != 1 || !strings.Contains(got.Body.String(), `"opened":true`) || !strings.Contains(got.Body.String(), "Installation has not been confirmed here") {
		t.Fatalf("handoff: %d %s", got.Code, got.Body.String())
	}
	if _, err := os.Stat(journal.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance handoff wrote archive-install journal: %v", err)
	}
}
