package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

func TestRuntimeStatusUsesLiveResolver(t *testing.T) {
	programProven := statusTransportProvesProgram(t)
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
	statusErr := runtimeStatus([]string{"--json", "--endpoint", options.endpoint}, &out, io.Discard)
	if !programProven {
		assertUnprovenStatus(t, statusErr, out.Bytes())
		return
	}
	if statusErr != nil {
		t.Fatal(statusErr)
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
	programProven := statusTransportProvesProgram(t)
	options, _ := isolatedRuntime(t)
	catalog, err := resolution.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	policyCalled := make(chan struct{}, 2)
	host, err := resolution.Listen(options.endpoint, catalog, func(*identity.Peer, wire.ServiceReference) bool { policyCalled <- struct{}{}; return true })
	if err != nil {
		t.Fatal(err)
	}
	serverErrors := make(chan error, 2)
	host.OnError = func(err error) { serverErrors <- err }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	defer func() { cancel(); host.Close(); <-done }()
	var out bytes.Buffer
	statusErr := runtimeStatus([]string{"--json", "--endpoint", options.endpoint}, &out, io.Discard)
	if !programProven {
		assertUnprovenStatus(t, statusErr, out.Bytes())
		select {
		case err := <-serverErrors:
			if !errors.Is(err, identity.ErrNotProven) {
				t.Fatalf("expected identity proof refusal: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing server proof refusal")
		}
		select {
		case <-policyCalled:
			t.Fatal("unproven peer reached policy")
		default:
		}
		return
	}
	if statusErr == nil {
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

// Darwin's current peer transport cannot satisfy listen.Program. Exercise its
// real refusal without representing successful resolution as platform coverage.
func assertUnprovenStatus(t *testing.T, statusErr error, output []byte) {
	t.Helper()
	if statusErr == nil {
		t.Fatal("unproven transport reported successful status")
	}
	var report runtimeReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if report.Error == "" || len(report.Capabilities) != 0 {
		t.Fatalf("proof refusal reported capabilities: %s", output)
	}
	t.Log("UNPROVEN successful runtime resolution on Darwin; fail-closed CLI status verified")
}

// Measure the same transport and requirement before choosing the expected CLI
// result. A future Darwin transport that supplies Program proof takes the full
// successful-resolution assertions automatically.
func statusTransportProvesProgram(t *testing.T) bool {
	t.Helper()
	options, _ := isolatedRuntime(t)
	listener, err := listen.Listen(options.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1024)
		if call != nil {
			defer call.Close()
		}
		done <- err
	}()
	// One-way submission may report EOF after proof refusal. The server's typed
	// result, rather than that client transport result, determines this branch.
	_ = (listen.FrameClient{Endpoint: options.endpoint, Timeout: 5 * time.Second}).WriteFrameContext(ctx, []byte("proof probe"))
	select {
	case err := <-done:
		if err == nil {
			return true
		}
		if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
			t.Logf("measured Program proof refusal: %v", err)
			return false
		}
		t.Fatalf("unexpected transport proof failure: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return false
}
