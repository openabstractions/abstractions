package logobservation_test

import (
	"context"
	"fmt"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	logging "github.com/openabstractions/abstraction-logging/go"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type writeOnly struct{}

func (writeOnly) Write(logging.Record) error { return nil }
func TestInstalledLogObservation(t *testing.T) {
	probe := os.Getenv("OA_CPP_LOG_OBSERVER_PROBE")
	if probe == "" {
		t.Fatal("installed probe required")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "private-history")
	sink, err := logging.OpenFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-log-observe-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), Sink: sink}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := func() func() {
		h, e := host.Listen(o)
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
				t.Fatal("runtime drain timeout")
			}
		}
	}
	run := func(mode string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, probe, append([]string{o.Endpoint, mode}, args...)...)
		cmd.Dir = t.TempDir()
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%s %s: %v", mode, out, e)
		}
		return strings.TrimSpace(string(out))
	}
	var cursor string
	func() { stop := start(); defer stop(); cursor = run("observe") }()
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	sink, err = logging.OpenFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	o.Sink = sink
	func() { stop := start(); defer stop(); run("gap", cursor) }()
	o.Sink = writeOnly{}
	func() { stop := start(); defer stop(); run("unsupported") }()
	t.Log("PASS current-end notification; bounded slow pages; canceled wait preserves binding; restart gap; unsupported not advertised")
}
