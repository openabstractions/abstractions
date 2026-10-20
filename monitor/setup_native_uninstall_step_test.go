package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// Owner, 2026-11-10 (R20c): WebView2 dropped the Remove request after about
// 20 seconds. The removal step starts on its own and keeps running after
// the request that started it ends; it holds the Setup action lock until it
// ends and lists itself as a cancellable remove step meanwhile.
func TestNativeUninstallStepOutlivesItsRequest(t *testing.T) {
	old := nativeUninstallStep
	t.Cleanup(func() { nativeUninstallStep = old })
	release, stepCtx, ended := make(chan struct{}), make(chan context.Context, 1), make(chan struct{})
	nativeUninstallStep = func(_ *servicePanel, ctx context.Context, _ setup.Runner, _, _ string) error {
		stepCtx <- ctx
		<-release
		close(ended)
		return ctx.Err()
	}
	p := &servicePanel{}
	request, cancelRequest := context.WithCancel(context.Background())
	p.setupActionMu.Lock()
	if _, err := p.startNativeUninstall(setup.Runner{}, setup.Journal{}, "comfyui-app"); err != nil {
		p.setupActionMu.Unlock()
		t.Fatal(err)
	}
	cancelRequest()
	ctx := <-stepCtx
	if request.Err() == nil || ctx.Err() != nil {
		t.Fatalf("request %v, step %v", request.Err(), ctx.Err())
	}
	if a := p.setupActionsNow()["comfyui-app"]; a.Kind != "remove" || !a.Cancellable {
		t.Fatalf("running step %+v", a)
	}
	if p.setupActionMu.TryLock() {
		t.Fatal("the running step does not hold the Setup action lock")
	}
	close(release)
	<-ended
	deadline := time.Now().Add(5 * time.Second)
	for !p.setupActionMu.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("the ended step kept the Setup action lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.setupActionMu.Unlock()
	if len(p.setupActionsNow()) != 0 {
		t.Fatal("the ended step stayed listed")
	}
}

// Cancel is a keyed POST.
func TestSetupCancelStepRoute(t *testing.T) {
	p := &servicePanel{}
	routes := p.routes("test-key")
	request := func(method, key string) int {
		r := httptest.NewRequest(method, "/setup/cancel-step", strings.NewReader(`{"component_id":"comfyui-app"}`))
		if key != "" {
			r.Header.Set("X-Panel-Key", key)
		}
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w.Code
	}
	if code := request(http.MethodPost, ""); code != http.StatusForbidden {
		t.Fatalf("without the key: %d", code)
	}
	if code := request(http.MethodGet, "test-key"); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", code)
	}
}

// A removal step that fails after its request answered keeps its outcome in
// the Panel state and its failure on the install record for a reload after
// the Panel restarts. A Cancel by the person records no failure, a panic is
// a failure, and a later success clears the kept failure.
func TestNativeUninstallStepRecordsItsOutcome(t *testing.T) {
	old := nativeUninstallStep
	t.Cleanup(func() { nativeUninstallStep = old })
	ctx := context.Background()
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	if _, err := journal.ApproveInstall(ctx, "plan-a", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := &servicePanel{}
	run := func(mode string) setupOutcome {
		t.Helper()
		ended := make(chan struct{})
		nativeUninstallStep = func(p *servicePanel, ctx context.Context, _ setup.Runner, _, componentID string) error {
			defer close(ended)
			switch mode {
			case "cancel":
				p.cancelSetupStep(componentID)
				<-ctx.Done()
				return ctx.Err()
			case "panic":
				panic("boom")
			case "fail":
				return errors.New("the version is in use or locked; close it and retry.\nmore detail")
			}
			return nil
		}
		p.setupActionMu.Lock()
		id, err := p.startNativeUninstall(setup.Runner{}, journal, "comfyui-app")
		if err != nil {
			p.setupActionMu.Unlock()
			t.Fatal(err)
		}
		<-ended
		p.setupActionMu.Lock() // the step has ended and recorded
		p.setupActionMu.Unlock()
		if len(p.setupActionsNow()) != 0 {
			t.Fatal("the ended step stayed listed")
		}
		o := p.setupOutcomesNow()["comfyui-app"]
		if o.StepID != id || o.Kind != "remove" || o.EndedAt == "" {
			t.Fatalf("%s outcome %+v for step %s", mode, o, id)
		}
		return o
	}
	failure := func() *setup.InstallFailure {
		record, err := journal.LoadInstall(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return record.Failure
	}
	if o := run("cancel"); !o.Cancelled || o.Failure != "" || failure() != nil {
		t.Fatalf("a Cancel recorded %+v %+v", o, failure())
	}
	if o := run("fail"); o.Failure != "the version is in use or locked; close it and retry" {
		t.Fatalf("failed outcome %+v", o)
	}
	if f := failure(); f == nil || f.Component != "comfyui" || f.Sentence != "Removing ComfyUI stopped: the version is in use or locked; close it and retry." {
		t.Fatalf("recorded failure %+v", f)
	}
	if o := run("panic"); o.Failure == "" {
		t.Fatalf("a panic ended as %+v", o)
	}
	if o := run("ok"); o.Failure != "" || o.Cancelled || failure() != nil {
		t.Fatalf("a success ended as %+v and kept %+v", o, failure())
	}
}
