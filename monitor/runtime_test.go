package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
)

// own points config at a directory of this test's own, so a test never rewrites
// the answers the machine it is running on lives by.
func own(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("ProgramData", filepath.Join(dir, "machine"))
	for _, v := range config.EnvVars {
		t.Setenv(v, "")
	}
	return dir
}

var panelRuntimeSerial atomic.Uint64

func panelConfigRuntime(t *testing.T) func() {
	t.Helper()
	return panelConfigRuntimeWith(t, nil)
}

func panelConfigRuntimeWith(t *testing.T, configure func(*host.Options)) func() {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("config editing requires Program-bound IPC; native macOS proof remains unavailable")
	}
	prefix := fmt.Sprintf("panel-config-%d-%d", os.Getpid(), panelRuntimeSerial.Add(1))
	options := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "-log"), ConfigEndpoint: listen.Endpoint(prefix + "-config")}
	if configure != nil {
		configure(&options)
	}
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
