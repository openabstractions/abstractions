package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestPanelStartPagesAreClosedAndKeyed(t *testing.T) {
	tests := []struct {
		name, path, page string
	}{
		{"overview", "/start", "/"},
		{"questions", "/rights", "/rights"},
		{"credentials", "/credentials-page", "/credentials-page"},
		{"connect-deepseek", "/credentials-page", "/credentials-page"},
		{"setup", "/setup", "/setup"},
	}
	saved := panelRegisteredApps
	panelRegisteredApps = func(context.Context) (int, bool) { return 1, true }
	t.Cleanup(func() { panelRegisteredApps = saved })
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, server, listener, rawURL, err := bindPanelPage("127.0.0.1:0", test.name)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			defer listener.Close()
			p.setupJournal = &setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}

			panelURL, err := url.Parse(rawURL)
			if err != nil {
				t.Fatal(err)
			}
			if panelURL.Path != test.path {
				t.Fatalf("initial path = %q, want %q", panelURL.Path, test.path)
			}
			key := panelURL.Query().Get("k")
			if key == "" || len(panelURL.Query()) != 1 {
				t.Fatalf("initial URL query = %v, want only this Panel run's key", panelURL.Query())
			}

			req := httptest.NewRequest(http.MethodGet, rawURL, nil)
			rec := httptest.NewRecorder()
			p.handler(key).ServeHTTP(rec, req)
			if test.path != test.page {
				// The landing address redirects, keeping the key.
				if rec.Code != http.StatusFound || rec.Header().Get("Location") != test.page+"?k="+url.QueryEscape(key) {
					t.Fatalf("landing: %d to %q, want %s", rec.Code, rec.Header().Get("Location"), test.page)
				}
				req = httptest.NewRequest(http.MethodGet, "http://"+panelURL.Host+rec.Header().Get("Location"), nil)
				rec = httptest.NewRecorder()
				p.handler(key).ServeHTTP(rec, req)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("initial page status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if test.name == "setup" {
				wantNav := `<a class="oa-nav-item active" href="/setup?k=` + url.QueryEscape(key) + `"`
				if !strings.Contains(rec.Body.String(), wantNav) || p.windowTitle != "OpenAbstractions Setup" {
					t.Fatalf("setup launch shell/title missing: title=%q", p.windowTitle)
				}
			} else {
				wantNav := `<a class="oa-nav-item active" href="` + test.page + `?k=` + url.QueryEscape(key) + `"`
				if !strings.Contains(rec.Body.String(), wantNav) {
					t.Fatalf("initial page did not render the selected %s link as active", test.name)
				}
			}

			apiReq := httptest.NewRequest(http.MethodPost, "/credentials", strings.NewReader(`{"action":"list"}`))
			apiRec := httptest.NewRecorder()
			p.handler(key).ServeHTTP(apiRec, apiReq)
			if apiRec.Code != http.StatusForbidden {
				t.Fatalf("credential API without the Panel key status = %d, want 403", apiRec.Code)
			}
		})
	}
}

// landingCase serves /start against one setup journal and one registry
// answer and returns the response.
func landingCase(t *testing.T, journal setup.Journal, apps int, known bool, query string, header string) *httptest.ResponseRecorder {
	t.Helper()
	saved := panelRegisteredApps
	panelRegisteredApps = func(context.Context) (int, bool) { return apps, known }
	t.Cleanup(func() { panelRegisteredApps = saved })
	req := httptest.NewRequest(http.MethodGet, "/start"+query, nil)
	req.Host = "127.0.0.1"
	if header != "" {
		req.Header.Set("X-Panel-Key", header)
	}
	got := httptest.NewRecorder()
	(&servicePanel{setupJournal: &journal}).handler("landing-test-key").ServeHTTP(got, req)
	return got
}

func emptyJournal(t *testing.T) setup.Journal {
	return setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
}

// The landing address needs the Panel key, through guard: ?k= or the
// X-Panel-Key header, and no other key.
func TestLandingNeedsThePanelKey(t *testing.T) {
	for _, c := range []struct{ query, header string }{{"", ""}, {"?k=wrong", ""}, {"", "wrong"}} {
		if got := landingCase(t, emptyJournal(t), 1, true, c.query, c.header); got.Code != http.StatusForbidden {
			t.Errorf("landing with query %q header %q: %d", c.query, c.header, got.Code)
		}
	}
	if got := landingCase(t, emptyJournal(t), 1, true, "", "landing-test-key"); got.Code != http.StatusFound || got.Header().Get("Location") != "/" {
		t.Errorf("landing with the header key: %d to %q", got.Code, got.Header().Get("Location"))
	}
}

// A fresh install, with no setup chosen and no application registered,
// opens on Get started (zero-knowledge first run); the query, and with it
// the key and the native shell, is kept.
func TestLandingOnAFreshInstallIsGetStarted(t *testing.T) {
	got := landingCase(t, emptyJournal(t), 0, true, "?k=landing-test-key&shell=native", "")
	if got.Code != http.StatusFound || got.Header().Get("Location") != "/setup?k=landing-test-key&shell=native" {
		t.Fatalf("fresh install: %d to %q, want Get started", got.Code, got.Header().Get("Location"))
	}
}

// With no setup chosen, Apps once any application is registered, and Apps
// when the runtime cannot say (its health banner names the failure).
func TestLandingWithARegisteredAppIsApps(t *testing.T) {
	for _, c := range []struct {
		apps  int
		known bool
	}{{1, true}, {0, false}} {
		if got := landingCase(t, emptyJournal(t), c.apps, c.known, "?k=landing-test-key", ""); got.Code != http.StatusFound || got.Header().Get("Location") != "/?k=landing-test-key" {
			t.Errorf("%d apps known %v: %d to %q, want Apps", c.apps, c.known, got.Code, got.Header().Get("Location"))
		}
	}
}

// Get started while a component the person chose is unfinished, even with
// an application registered (SPEC §4).
func TestLandingWithUnfinishedSetupIsGetStarted(t *testing.T) {
	journal := emptyJournal(t)
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.OpenCode})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.SavePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if got := landingCase(t, journal, 1, true, "?k=landing-test-key", ""); got.Code != http.StatusFound || got.Header().Get("Location") != "/setup?k=landing-test-key" {
		t.Fatalf("chosen and unfinished: %d to %q, want Get started", got.Code, got.Header().Get("Location"))
	}
}

// Apps once the chosen setup is finished, even before the registry lists
// an app.
func TestLandingWithFinishedSetupIsApps(t *testing.T) {
	journal := emptyJournal(t)
	plan := setup.Plan{ID: "finished-plan", Preparable: true, Components: []setup.PlannedComponent{{Component: setup.Component{ID: "component"}}}}
	state := setup.State{Plan: plan, ApprovedPlanID: plan.ID, Effects: map[string]setup.Effect{"component": {Phase: setup.Installed}}, Configuration: map[string]bool{}}
	if !state.Ready() {
		t.Fatal("the fixture is not a finished setup")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(journal.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := landingCase(t, journal, 0, true, "?k=landing-test-key", ""); got.Code != http.StatusFound || got.Header().Get("Location") != "/?k=landing-test-key" {
		t.Fatalf("finished setup: %d to %q, want Apps", got.Code, got.Header().Get("Location"))
	}
}

func TestInvalidPanelStartPageDoesNotBind(t *testing.T) {
	_, _, listener, panelURL, err := bindPanelPage("127.0.0.1:0", "../../credentials?k=attacker")
	if err == nil {
		t.Fatal("arbitrary page path was accepted")
	}
	if listener != nil || panelURL != "" {
		t.Fatalf("invalid page partially started Panel: listener=%v url=%q", listener, panelURL)
	}
	if _, err := parsePanelStartPage("unknown"); err == nil {
		t.Fatal("unknown page was accepted")
	}
}

func TestQuietPanelStartupDoesNotWriteKeyedURL(t *testing.T) {
	var out bytes.Buffer
	if err := writePanelURL(&out, "http://127.0.0.1:8734/credentials-page?k=private", true); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("quiet startup wrote %q", out.String())
	}
	if err := writePanelURL(&out, "http://127.0.0.1:8734/?k=private", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "?k=private") {
		t.Fatalf("default startup URL output = %q", out.String())
	}
}
