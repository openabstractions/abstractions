package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenCodeConnectionPanelProjectsNativeOutcome(t *testing.T) {
	for _, outcome := range []string{"page", "connect_pending", "conflict"} {
		t.Run(outcome, func(t *testing.T) {
			mux := http.NewServeMux()
			calls := 0
			(&servicePanel{}).installedAgentConnectionRoutesWith(mux, "fixture", func(context.Context) (installedAgentConnectionView, error) {
				return installedAgentConnectionView{Outcome: "page", Revision: "held"}, nil
			}, func(ctx context.Context, revision string) (installedAgentConnectionView, error) {
				calls++
				if revision != "held" {
					t.Fatal(revision)
				}
				v := installedAgentConnectionView{Outcome: outcome, Registration: "absent"}
				if outcome == "page" {
					v.Registration, v.Owned = "saved", true
				}
				if outcome == "conflict" {
					return v, errors.New("changed")
				}
				return v, nil
			})
			if outcome == "page" {
				page := httptest.NewRecorder()
				mux.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/connections/opencode?k=fixture", nil))
				if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "OpenCode connection") ||
					!strings.Contains(page.Body.String(), `agentHost="opencode"`) {
					t.Fatalf("installed connection page HTTP %d", page.Code)
				}
			}
			r := httptest.NewRequest(http.MethodPost, "/connections/opencode/connect", strings.NewReader(`{"revision":"held"}`))
			r.Header.Set("X-Panel-Key", "fixture")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			var v installedAgentConnectionView
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Outcome != outcome || v.Owned != (outcome == "page") || calls != 1 {
				t.Fatalf("%d %s calls=%d", w.Code, w.Body, calls)
			}
		})
	}
}
