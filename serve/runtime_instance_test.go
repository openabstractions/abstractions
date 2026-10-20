package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// shortClaimWaits keeps losers from waiting the installed two seconds.
func shortClaimWaits(t *testing.T) {
	t.Helper()
	runtime, host := runtimeClaimWait, hostClaimWait
	runtimeClaimWait, hostClaimWait = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { runtimeClaimWait, hostClaimWait = runtime, host })
}

// coreEndpoints are the listeners whose loss left the owner's runtime serving
// without applications, credentials and logging on 2026-10-31.
func coreEndpoints(t *testing.T, o runtimeFlags) map[string]string {
	t.Helper()
	applications, err := runtimeApplicationsEndpoint(o)
	if err != nil {
		t.Fatal(err)
	}
	credentials, _, _, err := credentialsEndpoints(o)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"runtime": o.endpoint, "logging": o.logEndpoint, "applications": applications, "credentials": credentials}
}

// assertServesEveryCoreService checks from outside that one runtime holds each
// core listener and the retained log.
func assertServesEveryCoreService(t *testing.T, o runtimeFlags) {
	t.Helper()
	for name, endpoint := range coreEndpoints(t, o) {
		l, err := listen.ListenFramed(endpoint, listen.Program)
		if err == nil {
			l.Close()
			t.Errorf("%s listener %s is not served", name, endpoint)
		} else if !errors.Is(err, listen.ErrTaken) {
			t.Errorf("%s listener %s: %v", name, endpoint, err)
		}
	}
	if sink, err := logging.OpenDefaultRetainedFileSink(o.out); err == nil {
		sink.Close()
		t.Error("the retained log has no writer")
	} else if !errors.Is(err, logging.ErrRetentionOwner) {
		t.Errorf("retained log: %v", err)
	}
}

// assertBindsNothing checks that every core listener is free and the retained
// log was never opened.
func assertBindsNothing(t *testing.T, o runtimeFlags) {
	t.Helper()
	for name, endpoint := range coreEndpoints(t, o) {
		l, err := listen.ListenFramed(endpoint, listen.Program)
		if err != nil {
			t.Errorf("%s listener %s held after a lost claim: %v", name, endpoint, err)
			continue
		}
		l.Close()
	}
	if _, err := os.Lstat(o.out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a runtime that lost its claim opened the log: %v", err)
	}
}

type runtimeRun struct {
	ready chan struct{}
	done  chan error
}

func startInProcessRuntime(ctx context.Context, o runtimeFlags) runtimeRun {
	r := runtimeRun{ready: make(chan struct{}), done: make(chan error, 1)}
	go func() { r.done <- runRuntimeReady(ctx, o, func() error { close(r.ready); return nil }) }()
	return r
}

// Three runtimes started at once on one account's state, as three supervisors
// started them on 2026-10-31: exactly one becomes ready and serves every core
// service; the other two fail on the claim.
func TestConcurrentRuntimesLeaveOneServingEveryCoreService(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("the Unix fixture cannot prove Program; startup refuses before readiness")
	}
	shortClaimWaits(t)
	o, _ := isolatedRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runs := make([]runtimeRun, 3)
	for i := range runs {
		runs[i] = startInProcessRuntime(ctx, o)
	}
	type outcome struct {
		run   int
		ready bool
		err   error
	}
	outcomes := make(chan outcome, len(runs))
	for i := range runs {
		go func() {
			select {
			case <-runs[i].ready:
				outcomes <- outcome{run: i, ready: true}
			case err := <-runs[i].done:
				outcomes <- outcome{run: i, err: err}
			}
		}()
	}
	winner := -1
	for range runs {
		select {
		case got := <-outcomes:
			switch {
			case got.ready && winner >= 0:
				t.Fatal("two runtimes reported ready")
			case got.ready:
				winner = got.run
			case !errors.Is(got.err, errRuntimeClaimed):
				t.Fatalf("a runtime ended with %v, want the lost claim", got.err)
			}
		case <-time.After(runtimeWait):
			t.Fatalf("no outcome within %v", runtimeWait)
		}
	}
	if winner < 0 {
		t.Fatal("no runtime became ready")
	}
	assertServesEveryCoreService(t, o)
	cancel()
	if err := awaitStopped(t, runs[winner].done, "runtime"); err != nil {
		t.Error(err)
	}
}

// A runtime that loses the claim opens no log and binds no listener.
func TestRuntimeLosingTheClaimBindsNothing(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("the Unix fixture cannot prove Program; its listeners cannot be probed here")
	}
	shortClaimWaits(t)
	o, _ := isolatedRuntime(t)
	release, err := claimRuntime(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = runRuntimeReady(context.Background(), o, func() error {
		t.Error("a runtime without the claim reported ready")
		return errors.New("unexpected readiness")
	})
	if !errors.Is(err, errRuntimeClaimed) {
		t.Fatalf("err %v, want the lost claim", err)
	}
	assertBindsNothing(t, o)
}

// A runtime whose core listener or retained log another process holds never
// reports ready; serving without that service is how the Panel lost
// applications@2 on 2026-10-31.
func TestRuntimeWithATakenCoreServiceIsNotReady(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("the Unix fixture cannot prove Program; startup refuses before readiness")
	}
	for _, name := range []string{"applications", "credentials", "logging sink"} {
		t.Run(name, func(t *testing.T) {
			o, _ := isolatedRuntime(t)
			var holder interface{ Close() error }
			var err error
			if name == "logging sink" {
				if err := os.MkdirAll(filepath.Dir(o.out), 0o700); err != nil {
					t.Fatal(err)
				}
				holder, err = logging.OpenDefaultRetainedFileSink(o.out)
			} else {
				holder, err = listen.ListenFramed(coreEndpoints(t, o)[name], listen.Program)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
			defer cancel()
			err = runRuntimeReady(ctx, o, func() error {
				t.Errorf("a runtime without its %s reported ready", name)
				return errors.New("unexpected readiness")
			})
			if !errors.Is(err, listen.ErrTaken) && !errors.Is(err, logging.ErrRetentionOwner) {
				t.Fatalf("err %v, want the taken %s", err, name)
			}
		})
	}
}

// Three supervisors started at once (start, Restart Manager and a client's
// activation on 2026-10-31) start one runtime; the other two exit cleanly
// before launching anything.
func TestConcurrentSupervisorsStartOneRuntime(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("the Unix fixture cannot prove Program; startup refuses before readiness")
	}
	shortClaimWaits(t)
	o, _ := isolatedRuntime(t)
	claimPath := filepath.Join(t.TempDir(), "supervisor")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var starts atomic.Int32
	var mu sync.Mutex
	var logs []string
	children := make(chan runtimeRun, 3)
	plan := hostPlan{
		claim: func(ctx context.Context) (func(), error) { return claimExclusive(ctx, claimPath, hostClaimWait) },
		start: func(ctx context.Context) (runtimeChild, error) {
			starts.Add(1)
			run := startInProcessRuntime(ctx, o)
			children <- run
			select {
			case <-run.ready:
				return &inProcessChild{run: run}, nil
			case err := <-run.done:
				return nil, err
			}
		},
		logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, format)
		},
	}
	hosts := make(chan error, 3)
	for range 3 {
		go func() { hosts <- plan.run(ctx) }()
	}
	for range 2 {
		select {
		case err := <-hosts:
			if err != nil {
				t.Fatalf("a losing supervisor: %v", err)
			}
		case <-time.After(runtimeWait):
			t.Fatal("losing supervisors did not exit")
		}
	}
	var run runtimeRun
	select {
	case run = <-children:
	case <-time.After(runtimeWait):
		t.Fatal("no supervisor started a runtime")
	}
	select {
	case <-run.ready:
	case err := <-run.done:
		t.Fatalf("the one runtime: %v", err)
	case <-time.After(runtimeWait):
		t.Fatal("the one runtime is not ready")
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("%d runtime starts, want 1", n)
	}
	assertServesEveryCoreService(t, o)
	mu.Lock()
	yielded := 0
	for _, line := range logs {
		if strings.Contains(line, "another supervisor runs for this account") {
			yielded++
		}
	}
	mu.Unlock()
	if yielded != 2 {
		t.Fatalf("supervisor log %q", logs)
	}
	cancel()
	select {
	case err := <-hosts:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(runtimeWait):
		t.Fatal("the running supervisor did not stop")
	}
}

type inProcessChild struct {
	run runtimeRun
}

func (c *inProcessChild) PID() int     { return os.Getpid() }
func (c *inProcessChild) Wait() error  { return <-c.run.done }
func (c *inProcessChild) Close() error { return nil }
