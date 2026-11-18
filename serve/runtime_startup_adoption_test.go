package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
)

// TestRuntimeReadyFollowsRegistrationAdoption: the startup adoption gives a
// declaration file its canonical registration, and with it a new binding ID.
// The runtime reports ready only after that adoption is stored, so a request
// sent right after readiness never carries the replaced binding (the
// registration:changed refusal TestRuntimeEmbedsThroughAHostedHost hit).
func TestRuntimeReadyFollowsRegistrationAdoption(t *testing.T) {
	options, _ := isolatedRuntime(t)
	config := `{"local":[],"hosted":[{"name":"openrouter","base":"http://127.0.0.1:9/api/v1","wire":"openai-compatible","credential":"openrouter"}]}`
	writeStateFile(t, filepath.Join(options.stateDir, "inference", inferenceHostsFile), config)
	ctx, cancel := context.WithCancel(context.Background())
	adoptedAtReady := make(chan bool, 1)
	done := make(chan error, 1)
	go func() {
		done <- runRuntimeReady(ctx, options, func() error {
			adoptedAtReady <- registrationIndexNames(t, options.stateDir, "openrouter")
			return nil
		})
	}()
	defer cancel()
	select {
	case adopted := <-adoptedAtReady:
		t.Cleanup(func() {
			cancel()
			if err := awaitStopped(t, done, "runtime"); err != nil {
				t.Error(err)
			}
		})
		if runtime.GOOS == "darwin" {
			t.Fatal("Darwin Unix unexpectedly started the Program-only runtime")
		}
		if !adopted {
			t.Fatal("the runtime reported ready before the openrouter declaration was adopted")
		}
	case err := <-done:
		if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
			return
		}
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		cancel()
		awaitStopped(t, done, "runtime")
		dumpGoroutines()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
}

// registrationIndexNames reports whether a stored registration record under
// state has the key name.
func registrationIndexNames(t *testing.T, state, name string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(state, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".json") || filepath.Base(filepath.Dir(path)) != "registrations" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), `"key":"`+name+`"`) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
	return found
}
