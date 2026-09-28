package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if !programProven {
		err := runRuntimeReady(context.Background(), options, func() error { t.Error("unproven runtime acknowledged readiness"); return nil })
		if !errors.Is(err, identity.ErrNotProven) {
			t.Fatalf("Unix runtime startup: %v", err)
		}
		assertListenersReleased(t, options)
		t.Skip("live-resolver fixture requires Program proof; Unix startup refusal is checked separately")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	defer func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
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
	if report.Error != "" || len(report.Capabilities) != 5 || report.Profile == "" {
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
	if err != nil || observation.Outcome.String() != "unknown" || observation.Snapshot != nil {
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
	if !programProven && errors.Is(err, identity.ErrNotProven) {
		select {
		case <-policyCalled:
			t.Fatal("unproven peer reached policy")
		default:
		}
		assertListenersReleased(t, options)
		t.Skip("resolver-refusal fixture requires Program proof; Unix startup refusal is checked separately")
	}
	if err != nil {
		t.Fatal(err)
	}
	serverErrors := make(chan error, 2)
	host.OnError = func(err error) { serverErrors <- err }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	defer func() { cancel(); host.Close(); awaitStopped(t, done, "resolver fixture") }()
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
			defer func() { cancel(); host.Close(); awaitStopped(t, done, "resolver fixture") }()
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

// rights and status follow the same runtime-endpoint rule (rightsMachine in
// rights.go, runtimeStatus above): with only ABSTRACTION_RUNTIME_ENDPOINT set
// and no --endpoint, both connect to the named (here, isolated) runtime
// unverified and say so once, instead of one silently verifying against a
// different (installed) runtime's registration while the other stays honest
// (research/packaged-activation/GATEWAY-REFUSAL-2026-09-22.md).
func TestRightsAndStatusFollowTheSameEnvironmentEndpointRule(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on this transport")
	}
	options, _ := credentialsRuntime(t)
	t.Setenv(runtimeEndpointVar, options.endpoint)

	var statusOut, statusDiag bytes.Buffer
	if err := runtimeStatus([]string{"--json"}, &statusOut, &statusDiag); err != nil {
		t.Fatalf("status: %v %s", err, statusOut.String())
	}
	var rightsOut, rightsDiag bytes.Buffer
	if err := rightsCommand([]string{"list", "--json"}, &rightsOut, &rightsDiag); err != nil {
		t.Fatalf("rights list: %v %s", err, rightsOut.String())
	}

	for _, notice := range []string{statusDiag.String(), rightsDiag.String()} {
		for _, want := range []string{runtimeEndpointVar, options.endpoint, "unverified"} {
			if !strings.Contains(notice, want) {
				t.Fatalf("notice %q does not mention %q", notice, want)
			}
		}
	}

	var report runtimeReport
	if err := json.Unmarshal(statusOut.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Error != "" || len(report.Capabilities) != 5 {
		t.Fatalf("status %s", statusOut.String())
	}
	var list rightsReply
	if err := json.Unmarshal(rightsOut.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Outcome != "page" {
		t.Fatalf("rights list %+v", list)
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

// Requested help (--help) prints to stdout and exits 0; a bad flag is a
// mistake, prints its prose to diagnostics instead, and exits usage (2)
// rather than the 1 a plain unwrapped error used to carry.
func TestRuntimeStatusHelpAndBadFlag(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if err := runtimeStatus([]string{"--help"}, &out, &diagnostics); err != nil {
		t.Fatalf("--help: %v", err)
	}
	if !strings.Contains(out.String(), "Usage: openabstractions status") {
		t.Fatalf("--help: output %q does not look like statusUsage", out.String())
	}
	if diagnostics.String() != "" {
		t.Fatalf("--help: diagnostics %q, want none", diagnostics.String())
	}
	out.Reset()
	diagnostics.Reset()
	err := runtimeStatus([]string{"--not-a-real-flag"}, &out, &diagnostics)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("bad flag: err = %v, want *exitError{exitUsage}", err)
	}
	if !strings.Contains(diagnostics.String(), "Usage: openabstractions status") {
		t.Fatalf("bad flag: diagnostics %q does not look like statusUsage", diagnostics.String())
	}
}

// TestStatusSplitsUnexpectedArgumentFromBadTimeout is item 3, round 5: an
// unexpected argument and a non-positive --timeout used to print the same
// "status: supply valid flags and a positive timeout", with no usage line
// and no --help pointer. Each now gets its own standard mistake shape: the
// argument names itself in the flagMistake three-line shape, and a
// non-positive timeout names the flag, the value as typed, and the accepted
// form, the same way badFlag already does for a value the flag package's own
// Parse rejects outright.
func TestStatusSplitsUnexpectedArgumentFromBadTimeout(t *testing.T) {
	var out, diagnostics bytes.Buffer
	err := runtimeStatus([]string{"extra-unexpected-arg"}, &out, &diagnostics)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || out.Len() != 0 {
		t.Fatalf("unexpected argument: err = %v, out = %q", err, out.String())
	}
	if !strings.Contains(diagnostics.String(), `unexpected argument "extra-unexpected-arg"`) {
		t.Fatalf("unexpected argument: diagnostics %q does not name it", diagnostics.String())
	}
	if !strings.Contains(diagnostics.String(), "Usage: openabstractions status") || !strings.Contains(diagnostics.String(), "--help") {
		t.Fatalf("unexpected argument: diagnostics %q missing the usage line or --help pointer", diagnostics.String())
	}

	for _, args := range [][]string{{"--timeout", "0"}, {"--timeout", "-5s"}} {
		out.Reset()
		diagnostics.Reset()
		err := runtimeStatus(args, &out, &diagnostics)
		if !errors.As(err, &exit) || exit.code != exitUsage || out.Len() != 0 {
			t.Fatalf("%v: err = %v, out = %q", args, err, out.String())
		}
		typed := args[1]
		if !strings.Contains(diagnostics.String(), fmt.Sprintf("--timeout %q", typed)) || !strings.Contains(diagnostics.String(), "positive duration") {
			t.Fatalf("%v: diagnostics %q does not name --timeout, %q and a positive duration", args, diagnostics.String(), typed)
		}
		if !strings.Contains(diagnostics.String(), "Usage: openabstractions status") || !strings.Contains(diagnostics.String(), "--help") {
			t.Fatalf("%v: diagnostics %q missing the usage line or --help pointer", args, diagnostics.String())
		}
		if strings.Contains(diagnostics.String(), "supply valid flags") {
			t.Fatalf("%v: diagnostics %q still uses the old combined message", args, diagnostics.String())
		}
	}
}

// TestStatusHelpNamesExitCodes is item 1's own test: status --help, like
// every other command's --help, states its exit codes.
func TestStatusHelpNamesExitCodes(t *testing.T) {
	var out bytes.Buffer
	if err := runtimeStatus([]string{"--help"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Exit codes:") {
		t.Fatalf("status --help names no exit codes: %q", out.String())
	}
}

// TestStatusPrintsThisProgramsOwnIdentity is item 2's own test: status
// prints, in both the plain and --json forms, the same canonicalized
// identity path a rights rule's --program names to cover this program
// (selfProgramPath, the path the runtime derives for the same peer
// connection) — the path the rights grant warning already tells an operator
// to read from status.
func TestStatusPrintsThisProgramsOwnIdentity(t *testing.T) {
	want := probeSelf().Program
	if want == "" {
		t.Skip("this process's own executable path is unavailable")
	}
	var out bytes.Buffer
	// An unreachable endpoint still lets status report this program's own
	// identity: that line does not depend on the runtime answering.
	if err := runtimeStatus([]string{"--endpoint", unreachableEndpoint(t), "--timeout", "2s"}, &out, io.Discard); err == nil {
		t.Fatal("status against an unreachable endpoint: err = nil")
	}
	if !strings.Contains(out.String(), "this program: "+want) {
		t.Fatalf("plain status omits \"this program: %s\": %q", want, out.String())
	}
	out.Reset()
	if err := runtimeStatus([]string{"--json", "--endpoint", unreachableEndpoint(t), "--timeout", "2s"}, &out, io.Discard); err == nil {
		t.Fatal("status --json against an unreachable endpoint: err = nil")
	}
	var report runtimeReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("status --json: %v\n%s", err, out.String())
	}
	if report.Program != want {
		t.Fatalf("status --json: program = %q, want %q", report.Program, want)
	}
}

// TestStatusDescribeRefusesANonEndpointArgument is item 5's own test: an
// argument that is not shaped like a platform endpoint path, such as a
// contract id copied from status's own capability list, refuses with the
// endpoint form and an example instead of the raw transport error opening
// it as a file would produce.
func TestStatusDescribeRefusesANonEndpointArgument(t *testing.T) {
	var out, diagnostics bytes.Buffer
	err := statusDescribe([]string{"abstraction.config/reader@1"}, &out, &diagnostics)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage {
		t.Fatalf("err = %v, want *exitError{exitUsage}", err)
	}
	// item 6, round 6: this bare refusal now goes through flagMistake, the
	// same three-line shape every other exit-2 mistake uses.
	for _, want := range []string{"status describe: takes an endpoint", `\\.\pipe\`, ".sock", "openabstractions status"} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("diagnostics %q does not mention %q", diagnostics.String(), want)
		}
	}
	if !strings.Contains(diagnostics.String(), "Usage: openabstractions status describe") || !strings.Contains(diagnostics.String(), "--help") {
		t.Fatalf("diagnostics %q missing the usage line or --help pointer", diagnostics.String())
	}
	if strings.Contains(diagnostics.String(), "expected local named pipe") {
		t.Fatalf("diagnostics %q still carries the raw transport error", diagnostics.String())
	}
}

// TestStatusDescribeNamesNoRuntimeListening is item 3's own test: an
// endpoint that looks like one, but that nothing answers, refuses with a
// sentence naming the endpoint and the command that lists this runtime's
// own, not the raw connect failure ("open \\.\pipe\...: The system cannot
// find the file specified.").
func TestStatusDescribeNamesNoRuntimeListening(t *testing.T) {
	endpoint := unreachableEndpoint(t)
	var out, diagnostics bytes.Buffer
	err := statusDescribe([]string{endpoint, "--timeout", "2s"}, &out, &diagnostics)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitNotResolved {
		t.Fatalf("err = %v, want *exitError{exitNotResolved}", err)
	}
	for _, want := range []string{"no runtime listens at " + endpoint, `run "openabstractions status"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err %q does not mention %q", err.Error(), want)
		}
	}
	for _, notWant := range []string{"cannot find the file", "The system cannot", "no such file"} {
		if strings.Contains(err.Error(), notWant) {
			t.Fatalf("err %q still carries the raw connect failure", err.Error())
		}
	}
}

// A platform endpoint path shape (a Windows named pipe here) is accepted as
// an endpoint and reaches the transport, rather than being refused as not
// looking like one.
func TestLooksLikeEndpointPath(t *testing.T) {
	for _, c := range []struct {
		s    string
		want bool
	}{
		{`\\.\pipe\openabstractions-user-S-1-runtime-v1`, true},
		{`\\.\PIPE\upper-case`, true},
		{"abstraction.config/reader@1", false},
		{"short-name", false},
	} {
		if got := looksLikeEndpointPath(c.s); got != c.want {
			t.Fatalf("looksLikeEndpointPath(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
