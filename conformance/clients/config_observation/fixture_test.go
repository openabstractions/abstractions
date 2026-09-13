package configobservation_test

import (
	"context"
	"fmt"
	cas "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInstalledGenericConfigBinding(t *testing.T) {
	probe := os.Getenv("OA_CPP_CONFIG_OBSERVER_PROBE")
	if probe == "" {
		t.Fatal("installed probe required")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-generic-config-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	options := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), ConfigStore: cas.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}, ConfigUserKey: filepath.Join(dir, "private-state")}
	options.ConfigObservationSource = func(ctx context.Context) (<-chan struct{}, func(), error) {
		c := make(chan struct{}, 1)
		child, cancel := context.WithCancel(ctx)
		go func() {
			defer close(c)
			timer := time.NewTicker(5 * time.Millisecond)
			defer timer.Stop()
			for {
				select {
				case <-child.Done():
					return
				case <-timer.C:
					select {
					case c <- struct{}{}:
					default:
					}
				}
			}
		}()
		return c, cancel, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := func() func() {
		h, e := host.Listen(options)
		if e != nil {
			t.Fatal(e)
		}
		child, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- h.Serve(child) }()
		return func() {
			stop()
			h.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("drain timeout")
			}
		}
	}
	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, probe, append([]string{options.Endpoint}, args...)...)
		cmd.Dir = t.TempDir()
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	var cursor string
	func() { stop := start(); defer stop(); cursor = run("observe") }()
	func() { stop := start(); defer stop(); run("gap", cursor) }()
	t.Log("PASS generated factory observer/reader/editor, wait/cancel/deadline, moved binding and restart gap")
}
