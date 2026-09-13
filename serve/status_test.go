package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
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
	if report.Error != "" || len(report.Capabilities) != 5 {
		t.Fatalf("%s", out.Bytes())
	}
	contracts := map[string]bool{}
	for _, item := range report.Capabilities {
		if item.Contract == "" || contracts[item.Contract] {
			t.Fatalf("missing/duplicate contract: %+v", item)
		}
		contracts[item.Contract] = true
		if item.Status != "resolved" {
			t.Fatalf("%+v", item)
		}
	}

	out.Reset()
	if err := runtimeStatus([]string{"--endpoint", options.endpoint}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	for contract := range contracts {
		if !strings.Contains(out.String(), "("+contract+"): resolved") {
			t.Fatalf("text omitted contract %s: %s", contract, out.String())
		}
	}
	// Exercise both default job services through the ordinary typed binding.
	// This read-only identity has never been submitted.
	call, finish := context.WithTimeout(ctx, 5*time.Second)
	defer finish()
	machine := client.New(options.endpoint)
	jobs, err := machine.ResolveJobs(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	history, err := jobs.GetHistoryWindow(call)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := machine.ResolveJobOperations(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := operations.ObserveWork(call, api.RequestIdentity{Key: "status-unsubmitted", HistoryEpoch: history.HistoryEpoch})
	if err != nil || observation.Outcome != "unknown" || observation.Snapshot != nil {
		t.Fatalf("default operation service: %+v %v", observation, err)
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
	if len(report.Capabilities) != 5 || report.Error != "" {
		t.Fatalf("%s", out.Bytes())
	}
	for _, item := range report.Capabilities {
		if item.Status != "unavailable" {
			t.Fatalf("%+v", item)
		}
	}
}

func TestRuntimeStatusRequiresJobAndEditorContracts(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	for _, missing := range []string{"abstraction.job/acceptance@1", "abstraction.job/operations@1", "abstraction.config/editor@1"} {
		t.Run(missing, func(t *testing.T) {
			options, _ := isolatedRuntime(t)
			var candidates []resolution.Candidate
			for _, entry := range [][2]string{{"abstraction.logging", "abstraction.logging/sink@1"}, {"abstraction.config", "abstraction.config/reader@1"}, {"abstraction.job", "abstraction.job/acceptance@1"}, {"abstraction.job", "abstraction.job/operations@1"}, {"abstraction.config", "abstraction.config/editor@1"}} {
				if entry[1] == missing {
					continue
				}
				candidates = append(candidates, resolution.Candidate{Ready: true, Reference: wire.ServiceReference{Provider: "fixture", Capability: entry[0], Contract: entry[1], Scope: wire.ScopeLocal, Transport: resolution.LocalTransport, Endpoint: "unused"}})
			}
			catalog, err := resolution.New(candidates)
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
				t.Fatal("missing required contract reported ready")
			}
			var report runtimeReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Capabilities) != 5 {
				t.Fatalf("missing query results: %s", out.Bytes())
			}
			found := false
			for _, item := range report.Capabilities {
				if item.Contract == missing {
					found = true
					if item.Status == "resolved" {
						t.Fatalf("missing contract resolved: %+v", item)
					}
				} else if item.Status != "resolved" {
					t.Fatalf("unrelated readiness lost: %+v", item)
				}
			}
			if !found {
				t.Fatalf("missing contract not identified: %s", out.Bytes())
			}
		})
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
	if report.Error == "" || len(report.Capabilities) != 5 || report.Bootstrap.State != "unknown" {
		t.Fatalf("%s", out.Bytes())
	}
	for _, item := range report.Capabilities {
		if item.Status != "" || len(item.Result) != 0 {
			t.Fatal("missing transport fabricated observation", item)
		}
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
	if report.Error == "" || len(report.Capabilities) != 5 {
		t.Fatalf("proof refusal reported capabilities: %s", output)
	}
	for _, item := range report.Capabilities {
		if item.Status != "" || len(item.Result) != 0 {
			t.Fatal("proof refusal fabricated observation", item)
		}
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
