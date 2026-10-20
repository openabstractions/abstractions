package main

import (
	byteutil "bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

func panelServiceRuntime(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	prefix := fixtureName("panel-service")
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"), JobEndpoint: listen.Endpoint(prefix + "j"), JobRoot: filepath.Join(dir, "private-provider"), JobOwner: "panel-service-owner", JobExecutor: downloadserve.HTTPExecution{}}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime failed to stop")
		}
	})
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", o.Endpoint)
	trustPanelRuntime(t, o.Endpoint)
}
func panelRequest(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b byteutil.Buffer
	method := "GET"
	if body != nil {
		method = "POST"
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, &b)
	r.Host = "127.0.0.1:8734"
	r.Header.Set("X-Panel-Key", "test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestServicePanelAbsenceHasNoLocalFallback(t *testing.T) {
	own(t)
	dir := t.TempDir()
	t.Setenv("ABSTRACTION_STORE", filepath.Join(dir, "must-not-create"))
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", listen.Endpoint(fmt.Sprintf("absent-panel-%d", time.Now().UnixNano())))
	p := &servicePanel{}
	h := p.handler("test-key")
	r := panelRequest(t, h, "/inventory", nil)
	if r.Code != 503 {
		t.Fatalf("absence: %d %s", r.Code, r.Body.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("default client created provider files", err)
	}
	r = panelRequest(t, h, "/action", serviceAction{Action: "pause"})
	if r.Code != 400 || !strings.Contains(r.Body.String(), "unavailable") {
		t.Fatal("legacy action silently available")
	}
}
func TestServicePanelRealSubmitInventoryReconcileAndResult(t *testing.T) {
	own(t)
	panelServiceRuntime(t)
	body := []byte("real service result\x00\n")
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body)
	}))
	defer source.Close()
	p := &servicePanel{}
	h := p.handler("test-key")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	jobs, _, history, err := p.binding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	action := serviceAction{Action: "submit", Endpoint: jobs.Endpoint(), Owner: history.LogicalOwner, Identity: api.RequestIdentity{Key: "persisted-panel-key", HistoryEpoch: history.HistoryEpoch}, URL: source.URL, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Size: int64(len(body))}
	wrong := action
	wrong.Endpoint = "untrusted-other-endpoint"
	if r := panelRequest(t, h, "/action", wrong); r.Code != 409 {
		t.Fatal("browser retargeted provider")
	}
	r := panelRequest(t, h, "/action", action)
	var accepted api.AcceptanceResult
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &accepted) != nil || accepted.Receipt == nil {
		t.Fatalf("submit: %d %s", r.Code, r.Body.String())
	}
	action.Action = "reconcile"
	r = panelRequest(t, h, "/action", action)
	var recovered api.AcceptanceResult
	_ = json.Unmarshal(r.Body.Bytes(), &recovered)
	if recovered.Receipt == nil || recovered.Receipt.OperationID != accepted.Receipt.OperationID {
		t.Fatal("reconcile changed operation")
	}
	for {
		observed, err := jobs.ObserveWork(ctx, action.Identity)
		if err != nil {
			t.Fatal(err)
		}
		if observed.Snapshot != nil && observed.Snapshot.State == api.WorkStateComplete {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	r = panelRequest(t, h, "/inventory", nil)
	var page api.InventoryPage
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &page) != nil || len(page.Snapshots) != 1 {
		t.Fatalf("inventory: %d %s", r.Code, r.Body.String())
	}
	// The job list names what the operation fetches: the runtime derived the
	// label from the source URL, which has a host and no path (JOB-A12).
	if want := "127.0.0.1"; page.Snapshots[0].Label != want || !page.Snapshots[0].LabelDerived {
		t.Fatalf("inventory label: %q derived=%v, want %q derived", page.Snapshots[0].Label, page.Snapshots[0].LabelDerived, want)
	}
	q := url.Values{"key": {action.Identity.Key}, "epoch": {action.Identity.HistoryEpoch}, "owner": {action.Owner}, "endpoint": {action.Endpoint}}
	r = panelRequest(t, h, "/result?"+q.Encode(), nil)
	if r.Code != 200 || !byteutil.Equal(r.Body.Bytes(), body) {
		t.Fatalf("result: %d %q", r.Code, r.Body.Bytes())
	}
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", listen.Endpoint("now-missing-panel-runtime"))
	action.Action = "observe"
	r = panelRequest(t, h, "/action", action)
	if r.Code != 200 {
		t.Fatal("binding rediscovered after selection")
	}
}

func TestServicePanelBrowserBoundary(t *testing.T) {
	h := (&servicePanel{}).handler("test-key")
	for _, v := range []struct{ host, origin, key string }{{"evil.example", "", "test-key"}, {"127.0.0.1:8734", "http://evil.example", "test-key"}, {"127.0.0.1:8734", "http://127.0.0.1:8734", "wrong"}} {
		r := httptest.NewRequest("GET", "/inventory", nil)
		r.Host = v.host
		r.Header.Set("Origin", v.origin)
		r.Header.Set("X-Panel-Key", v.key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("browser boundary accepted %+v", v)
		}
	}
}

// TestSetupMutatingRoutesRefuseAnUnkeyedOrForeignCaller walks every mutating
// /setup route through the real mux. Each is refused with 403, and the
// Applications client sees no call, for no key, a wrong key, a foreign Origin
// and a non-loopback Host. The keyed same-origin Remove succeeds.
func TestSetupMutatingRoutesRefuseAnUnkeyedOrForeignCaller(t *testing.T) {
	const key = "test-key"
	routes := []string{
		"/setup/plan", "/setup/intent", "/setup/advance", "/setup/retry", "/setup/recover",
		"/setup/feature/add",
		"/setup/modelhost/feature/add", "/setup/modelhost/engine", "/setup/modelhost/root/preview", "/setup/modelhost/root", "/setup/modelhost/host",
		"/setup/model/start", "/setup/model/advance", "/setup/model/retry", "/setup/model/configure", "/setup/model/reconcile",
		"/setup/comfy/register", "/setup/comfy/activate", "/setup/comfy/remove",
	}
	entry := fwire.ApplicationDescriptor{Name: "comfyui", Program: `C:\OA\python.exe`, Title: "ComfyUI"}
	newFake := func() *setupApplicationsFake {
		return &setupApplicationsFake{
			page:        fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: entry}}},
			removeReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied},
		}
	}
	callers := []struct {
		name                string
		host, origin, token string
	}{
		{"no key", "127.0.0.1:8734", "http://127.0.0.1:8734", ""},
		{"wrong key", "127.0.0.1:8734", "http://127.0.0.1:8734", "wrong"},
		{"foreign origin", "127.0.0.1:8734", "http://evil.example", key},
		{"non-loopback host", "evil.example", "", key},
	}
	for _, route := range routes {
		for _, caller := range callers {
			fake := newFake()
			h := (&servicePanel{comfyApps: fake}).handler(key)
			r := httptest.NewRequest("POST", route, strings.NewReader(`{"confirm":true,"setup_plan_id":"x"}`))
			r.Host = caller.host
			if caller.origin != "" {
				r.Header.Set("Origin", caller.origin)
			}
			if caller.token != "" {
				r.Header.Set("X-Panel-Key", caller.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("%s from %s: status %d, want 403", route, caller.name, w.Code)
			}
			if len(fake.removed) != 0 || fake.registerCalls != 0 || fake.activateCalls != 0 {
				t.Fatalf("%s from %s reached the Applications client: removed=%v registered=%d activated=%d",
					route, caller.name, fake.removed, fake.registerCalls, fake.activateCalls)
			}
		}
	}
	fake := newFake()
	h := (&servicePanel{comfyApps: fake}).handler(key)
	r := httptest.NewRequest("POST", "/setup/comfy/remove", strings.NewReader(`{"confirm":true}`))
	r.Host = "127.0.0.1:8734"
	r.Header.Set("Origin", "http://127.0.0.1:8734")
	r.Header.Set("X-Panel-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || len(fake.removed) != 1 || fake.removed[0] != "comfyui" {
		t.Fatalf("keyed same-origin Remove: status %d removed=%v body=%s", w.Code, fake.removed, w.Body.String())
	}
}
