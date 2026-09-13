package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
	facade "github.com/openabstractions/abstraction-facade/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
)

var panelRuntimeSerial atomic.Uint64

func panelConfigRuntime(t *testing.T) func() {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("config editing requires Program-bound IPC; native macOS proof remains unavailable")
	}
	prefix := fmt.Sprintf("panel-config-%d-%d", os.Getpid(), panelRuntimeSerial.Add(1))
	options := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "-log"), ConfigEndpoint: listen.Endpoint(prefix + "-config")}
	h, err := host.Listen(options)
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
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("panel config runtime did not stop")
		}
	})
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", options.Endpoint)
	trustPanelRuntime(t, options.Endpoint)
	return func() { cancel(); h.Close() }
}

func TestPanelEditRequiresService(t *testing.T) {
	own(t)
	if err := config.Save(config.UserPath(), config.Config{NASStore: "preserve-existing-setting"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.UserPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", listen.Endpoint(fmt.Sprintf("panel-absent-%d-%d", os.Getpid(), panelRuntimeSerial.Add(1))))
	w := &window{panel: newPanel()}
	if err := w.forget(); err == nil {
		t.Fatal("absent config service allowed a local file write")
	}
	after, err := os.ReadFile(config.UserPath())
	if err != nil || string(before) != string(after) {
		t.Fatalf("failed edit changed stored settings: %v", err)
	}
}

func TestPanelEditsOnlyUserSettings(t *testing.T) {
	own(t)
	if err := config.Save(config.UserPath(), config.Config{NASStore: "old-nas", Store: "retained-user-store", Off: map[string]string{"other": "keep"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABSTRACTION_STORE", "inherited-run-setting")
	panelConfigRuntime(t)
	w := &window{panel: newPanel()}
	if err := w.forget(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	editor, err := panelMachine().ResolveConfigEditor(ctx, facade.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := editor.ReadUserContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Values.NasStore != "" || current.Values.Store != "retained-user-store" || current.Values.Off["other"] != "keep" {
		t.Fatalf("panel copied effective overrides or lost unrelated settings: %+v", current.Values)
	}
}
