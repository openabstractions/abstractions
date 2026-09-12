package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
)

func TestRuntimeStatusUsesLiveResolver(t *testing.T) {
	options, _ := isolatedRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	}
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var out bytes.Buffer
	if err := runtimeStatus([]string{"--json", "--endpoint", options.endpoint}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var report runtimeReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Error != "" || len(report.Capabilities) != 2 {
		t.Fatalf("%s", out.Bytes())
	}
	for _, item := range report.Capabilities {
		if item.Status != "resolved" {
			t.Fatalf("%+v", item)
		}
	}
}

func TestRuntimeStatusPreservesRefusal(t *testing.T) {
	options, _ := isolatedRuntime(t)
	catalog, err := resolution.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	host, err := resolution.Listen(options.endpoint, catalog, func(*identity.Peer, wire.ServiceReference) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	defer func() { cancel(); host.Close(); <-done }()
	var out bytes.Buffer
	if err := runtimeStatus([]string{"--json", "--endpoint", options.endpoint}, &out, io.Discard); err == nil {
		t.Fatal("empty catalogue reported ready")
	}
	var report runtimeReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Capabilities) != 2 || report.Error != "" {
		t.Fatalf("%s", out.Bytes())
	}
	for _, item := range report.Capabilities {
		if item.Status != "unavailable" {
			t.Fatalf("%+v", item)
		}
	}
}

func TestRuntimeStatusMissingAndInvalid(t *testing.T) {
	options, _ := isolatedRuntime(t)
	var out bytes.Buffer
	if err := runtimeStatus([]string{"--json", "--endpoint", options.endpoint, "--timeout", "100ms"}, &out, io.Discard); err == nil {
		t.Fatal("missing host reported ready")
	}
	var report runtimeReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Error == "" || len(report.Capabilities) != 0 {
		t.Fatalf("%s", out.Bytes())
	}
	for _, args := range [][]string{{"--timeout", "0"}, {"unexpected"}} {
		out.Reset()
		if err := runtimeStatus(args, &out, io.Discard); err == nil || out.Len() != 0 {
			t.Fatalf("args %v produced %q: %v", args, out.String(), err)
		}
	}
}
