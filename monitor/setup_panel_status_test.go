package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestSetupStateRepliesWhileTheProviderOwnerRecovers(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	original := setupReconcileProviderRecords
	setupReconcileProviderRecords = func(context.Context) error {
		entered <- struct{}{}
		<-release
		return nil
	}
	t.Cleanup(func() { setupReconcileProviderRecords = original })
	defer close(release)

	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	panel := &servicePanel{setupJournal: &journal}
	handler := installHandlerFor(panel.handler("setup-test-key"))
	reply := make(chan *httptest.ResponseRecorder, 1)
	go func() { reply <- handler("GET", "/setup/state", "") }()
	select {
	case got := <-reply:
		if got.Code != http.StatusOK {
			t.Fatalf("Setup state: HTTP %d %s", got.Code, got.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Setup state waited for the provider repair")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Setup state did not ask the provider owner to recover")
	}
}
