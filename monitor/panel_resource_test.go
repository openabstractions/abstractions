package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

const testResourceOrigin = "https://panel.invalid"

// checkedAt timestamps each readiness observation. The parity fixture holds
// its caller budget fixed; all other response fields must match.
var checkedAt = regexp.MustCompile(`"checkedAt":"[^"]*"`)

// resourceParityPaths is every route the Panel registers, a few with the
// queries their pages send, plus a path no route serves.
func resourceParityPaths() []string {
	paths := []string{"/", "/static/panel.css", "/static/skin-windows.css", "/static/skin-macos.css", "/static/none.css",
		"/binding", "/inventory", "/action", "/account-work", "/result", "/work", "/setup", "/setup/plan",
		"/setup/other-copies", "/setup/install", "/setup/install/grants", "/setup/state", "/setup/feature",
		"/setup/model", "/settings", "/settings/observe?cursor=", "/questions", "/choices", "/rights",
		"/rights/observe", "/rights/delegation", "/rights/assistant", "/rights/answered", "/activity/answers", "/activity/starts", "/credentials-page",
		"/credentials", "/runtime", "/logging", "/identity", "/explore", "/application/register",
		"/application/remove", "/inference", "/inference/hosts", "/registry", "/registry/view", "/card",
		"/apps/registrations?source=applications&wait_ms=0", "/app/example", "/panel/nav.json", "/start",
		"/no-such-page"}
	for alias := range navAliases {
		paths = append(paths, alias)
	}
	sort.Strings(paths)
	return paths
}

// The resource mode answers every route exactly as the HTTP mode does:
// same status, content type and body, without a key, with the key in the
// query, and with the key in the header the pages' fetch calls send.
// Redirects differ only in form (TestPanelResourceRedirectBecomesReplace).
func TestPanelResourceMatchesHTTPMode(t *testing.T) {
	p := &servicePanel{windowTitle: "OpenAbstractions Panel"}
	key := "k" + strings.Repeat("0", 47)
	httpMode := p.handler(key)
	resources := newPanelResources(testResourceOrigin, p.routes(key))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// /runtime observes supervision and capability readiness independently on
	// each call. An ended caller budget gives both real routes the same stable
	// observation through their existing cancellation boundaries.
	runtimeCtx, endRuntime := context.WithCancel(ctx)
	endRuntime()
	statuses := map[int]int{}
	defer func() {
		t.Logf("HTTP-mode statuses compared: %v", statuses)
		// Guard against a parity check that only ever compares refusals.
		if statuses[http.StatusOK] < 20 || statuses[http.StatusFound] < 5 || statuses[http.StatusForbidden] < 20 {
			t.Errorf("too few pages, redirects or refusals compared: %v", statuses)
		}
	}()
	for _, path := range resourceParityPaths() {
		requestCtx := ctx
		if path == "/runtime" {
			requestCtx = runtimeCtx
		}
		for _, variant := range []struct {
			name   string
			query  string
			header string
		}{{"no key", "", ""}, {"query key", "k=" + key, ""}, {"header key", "", key}} {
			target := path
			if variant.query != "" {
				if strings.Contains(target, "?") {
					target += "&" + variant.query
				} else {
					target += "?" + variant.query
				}
			}
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8734"+target, nil).WithContext(requestCtx)
			header := http.Header{}
			if variant.header != "" {
				req.Header.Set("X-Panel-Key", variant.header)
				header.Set("X-Panel-Key", variant.header)
			}
			rec := httptest.NewRecorder()
			httpMode.ServeHTTP(rec, req)
			statuses[rec.Code]++
			got := resources.serve(requestCtx, panelResourceRequest{Method: http.MethodGet, URL: testResourceOrigin + target, Header: header})
			if path == "/runtime" && variant.name != "no key" {
				var view readinessView
				if err := json.Unmarshal(got.Body, &view); err != nil {
					t.Fatal(err)
				}
				if view.Detail != context.Canceled.Error() || view.Error != context.Canceled.Error() {
					t.Errorf("%s %s: the runtime observation lost its canceled caller budget", variant.name, target)
				}
			}
			if rec.Code >= 300 && rec.Code < 400 {
				want := testResourceOrigin + resolveLocation(t, target, rec.Header().Get("Location"))
				if got.Status != http.StatusOK || !strings.Contains(string(got.Body), "location.replace(\""+want+"\")") {
					t.Errorf("%s %s: HTTP %d to %q; resource %d %q", variant.name, target, rec.Code, want, got.Status, got.Body)
				}
				continue
			}
			if got.Status != rec.Code || got.Header.Get("Content-Type") != rec.Header().Get("Content-Type") ||
				checkedAt.ReplaceAllString(string(got.Body), "") != checkedAt.ReplaceAllString(rec.Body.String(), "") {
				t.Errorf("%s %s: HTTP %d %q (%d bytes); resource %d %q (%d bytes)", variant.name, target,
					rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len(), got.Status, got.Header.Get("Content-Type"), len(got.Body))
			}
		}
	}
}

func resolveLocation(t *testing.T, from, location string) string {
	t.Helper()
	base := httptest.NewRequest(http.MethodGet, "http://x"+from, nil).URL
	u, err := base.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	return u.RequestURI()
}

// A POST without the key is refused the same way in both modes, and a keyed
// POST carries its body to the route.
func TestPanelResourcePostCarriesBody(t *testing.T) {
	var seen string
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", guard("key", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = r.Method + " " + r.Header.Get("Content-Type") + " " + string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	resources := newPanelResources(testResourceOrigin, mux)
	refused := resources.serve(context.Background(), panelResourceRequest{Method: "POST", URL: testResourceOrigin + "/echo", Body: []byte(`{}`)})
	if refused.Status != http.StatusForbidden || seen != "" {
		t.Fatalf("unkeyed POST: %d, route saw %q", refused.Status, seen)
	}
	h := http.Header{}
	h.Set("X-Panel-Key", "key")
	h.Set("Content-Type", "application/json")
	h.Set("Cookie", "ignored=1")
	got := resources.serve(context.Background(), panelResourceRequest{Method: "POST", URL: testResourceOrigin + "/echo", Header: h, Body: []byte(`{"a":"wörld"}`)})
	if got.Status != http.StatusNoContent || seen != `POST application/json {"a":"wörld"}` {
		t.Fatalf("keyed POST: %d, route saw %q", got.Status, seen)
	}
}

// Requests for another origin, with credentials in the URL, or carrying a
// foreign Origin never reach a route.
func TestPanelResourceRefusesOtherOrigins(t *testing.T) {
	reached := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { reached = true })
	resources := newPanelResources(testResourceOrigin, mux)
	for _, c := range []struct {
		url, origin string
	}{
		{"https://evil.example/", ""},
		{"http://panel.invalid/", ""},
		{"https://panel.invalid:444/", ""},
		{"https://user@panel.invalid/", ""},
		{"oa-panel://panel/", ""},
		{testResourceOrigin + "/", "https://evil.example"},
		{testResourceOrigin + "/", "null"},
	} {
		h := http.Header{}
		if c.origin != "" {
			h.Set("Origin", c.origin)
		}
		got := resources.serve(context.Background(), panelResourceRequest{URL: c.url, Header: h})
		if got.Status != http.StatusForbidden || reached {
			t.Fatalf("%s origin %q: %d, reached %v", c.url, c.origin, got.Status, reached)
		}
	}
	h := http.Header{}
	h.Set("Origin", testResourceOrigin)
	if got := resources.serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/", Header: h}); got.Status != http.StatusOK || !reached {
		t.Fatalf("own origin: %d, reached %v", got.Status, reached)
	}
	if got := resources.serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/"}); got.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("Referrer-Policy %q", got.Header.Get("Referrer-Policy"))
	}
}

// A same-origin redirect becomes a document that replaces itself with the
// target; one leaving the origin is refused.
func TestPanelResourceRedirectBecomesReplace(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/alias", redirectAlias("/rights"))
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/x", http.StatusFound)
	})
	mux.HandleFunc("/script", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/x?q=</script><script>alert(1)</script>", http.StatusFound)
	})
	resources := newPanelResources(testResourceOrigin, mux)
	got := resources.serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/alias?k=abc"})
	if got.Status != http.StatusOK || got.Header.Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.Contains(string(got.Body), `location.replace("https://panel.invalid/rights?k=abc")`) {
		t.Fatalf("alias: %d %q %s", got.Status, got.Header.Get("Content-Type"), got.Body)
	}
	if got := resources.serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/away"}); got.Status != http.StatusForbidden {
		t.Fatalf("cross-origin redirect: %d %s", got.Status, got.Body)
	}
	script := resources.serve(context.Background(), panelResourceRequest{URL: testResourceOrigin + "/script"})
	if strings.Count(string(script.Body), "</script>") != 1 {
		t.Fatalf("redirect target closed the script: %s", script.Body)
	}
}

// Ending the request's context ends a route waiting on it, as closing the
// window or abandoning a long poll does.
func TestPanelResourceContextEndsLongPoll(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/wait", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			w.WriteHeader(http.StatusServiceUnavailable)
		case <-time.After(time.Minute):
		}
	})
	resources := newPanelResources(testResourceOrigin, mux)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan panelResourceResponse, 1)
	go func() { done <- resources.serve(ctx, panelResourceRequest{URL: testResourceOrigin + "/wait"}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		if got.Status != http.StatusServiceUnavailable {
			t.Fatalf("status %d", got.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the route did not end with its context")
	}
}

// A run served as resources binds no listener, and its start URL is the
// keyed page under the window's origin.
func TestPanelRunResourcesBindNothing(t *testing.T) {
	listened := false
	previous := panelListen
	panelListen = func(network, address string) (net.Listener, error) {
		listened = true
		return previous(network, address)
	}
	defer func() { panelListen = previous }()
	run, err := newPanelRun("questions")
	if err != nil {
		t.Fatal(err)
	}
	resources, start := run.resources(testResourceOrigin)
	if start != testResourceOrigin+"/rights?k="+run.key {
		t.Fatalf("start %q", start)
	}
	page := resources.serve(context.Background(), panelResourceRequest{URL: start})
	if page.Status != http.StatusOK || !strings.Contains(page.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("start page: %d %q", page.Status, page.Header.Get("Content-Type"))
	}
	if listened {
		t.Fatal("resource mode called the listener")
	}
}
