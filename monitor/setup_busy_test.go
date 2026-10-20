package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// Journey suite, 2026-11-10: a second step-starting Setup request sent while
// a removal runs is refused at once with the running step; it never waits
// for the step and never starts another one.
func TestSetupRefusesASecondStepWhileOneRuns(t *testing.T) {
	old := nativeUninstallStep
	t.Cleanup(func() { nativeUninstallStep = old })
	release, ended := make(chan struct{}), make(chan struct{})
	calls := 0
	nativeUninstallStep = func(_ *servicePanel, ctx context.Context, _ setup.Runner, _, _ string) error {
		calls++
		<-release
		close(ended)
		return nil
	}
	p := &servicePanel{}
	p.setupActionMu.Lock()
	if _, err := p.startNativeUninstall(setup.Runner{}, setup.Journal{}, "comfyui-app"); err != nil {
		p.setupActionMu.Unlock()
		t.Fatal(err)
	}
	body := `{"component_id":"lemonade","plan_id":"plan-1","confirm":true}`
	for name, handler := range map[string]http.HandlerFunc{
		"uninstall":      p.setupUninstall,
		"settle-removal": p.setupSettleRemoval,
		"install":        p.setupInstall,
		"advance-next":   p.setupAdvanceNext,
		"advance":        p.setupAdvance,
	} {
		w := httptest.NewRecorder()
		began := time.Now()
		handler(w, httptest.NewRequest(http.MethodPost, "/setup/"+name, strings.NewReader(body)))
		if took := time.Since(began); took > 100*time.Millisecond {
			t.Fatalf("%s waited %v", name, took)
		}
		var reply setupBusyReply
		if w.Code != http.StatusConflict || json.Unmarshal(w.Body.Bytes(), &reply) != nil || !reply.Busy || reply.Error != "Another Catalog step is running." {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if a := reply.Actions["comfyui-app"]; a.Kind != "remove" || len(reply.Actions) != 1 {
			t.Fatalf("%s: running steps %+v", name, reply.Actions)
		}
	}
	close(release)
	<-ended
	deadline := time.Now().Add(5 * time.Second)
	for !p.setupActionMu.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("the step kept the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.setupActionMu.Unlock()
	if calls != 1 {
		t.Fatalf("%d removal steps ran", calls)
	}
	if o := p.setupOutcomesNow()["comfyui-app"]; o.Failure != "" {
		t.Fatalf("the first step's outcome changed: %+v", o)
	}
	if _, ok := p.setupOutcomesNow()["lemonade"]; ok {
		t.Fatal("a refused request recorded an outcome")
	}
}

// Reads never wait on the Setup action lock: GET /setup/modelhost/coding
// answers while a step holds it.
func TestSetupReadsDoNotWaitOnTheStepLock(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	panel := &servicePanel{setupJournal: &journal}
	call := installHandlerFor(panel.handler("setup-test-key"))
	panel.setupActionMu.Lock()
	defer panel.setupActionMu.Unlock()
	for _, path := range []string{"/setup/modelhost/coding", "/setup/maintenance"} {
		answered := make(chan int, 1)
		go func() { answered <- call("GET", path, "").Code }()
		select {
		case <-answered:
		case <-time.After(time.Second):
			t.Fatalf("GET %s waited behind the step lock", path)
		}
	}
}
