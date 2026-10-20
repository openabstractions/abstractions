package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// A provider that exits before it reads ready, at every launch, is launched
// three times and then left stopped with one log line; a request inside the
// retry time reads start_failed:<name> without a launch, and the first
// request after it launches the program once more (R34 live test D1:
// Lemonade without Setup crash-looped every few seconds).
func TestAProviderThatCannotStartStopsAndRetriesOnRequest(t *testing.T) {
	program := copyTestBinary(t, "oa-fixture-provider")
	pidDir := t.TempDir()
	var mu sync.Mutex
	var reports []string
	p := &runtimeProviders{dir: filepath.Join(t.TempDir(), providersDir),
		report: func(err error) {
			mu.Lock()
			reports = append(reports, err.Error())
			mu.Unlock()
		},
		supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}), mediated: map[string]mediatedProvider{},
		timing: providerTiming{probeWaiting: 50 * time.Millisecond, probeReady: time.Second, backoffFirst: 10 * time.Millisecond,
			backoffMax: 20 * time.Millisecond, stable: time.Minute, probeBudget: time.Second, inventoryBudget: time.Second,
			startFailures: 3, startRetry: 2 * time.Second}}
	name := "unset"
	f := providerFile{Declaration: providerDeclaration{Name: name, Program: program, Endpoint: name, Transport: transportNative,
		Arguments:  []string{providerFixtureArg, "stderr-exit", providerEndpointToken, pidDir},
		Activation: wire.ActivationOnDemand.String()}}
	s := newFixtureProviderSupervisor(t, p, f)
	p.supervisors[name] = s
	s.start()
	defer s.halt()
	launches := func() int {
		entries, _ := os.ReadDir(pidDir)
		return len(entries)
	}

	if _, why := p.awaitReady(t.Context(), time.Minute, func() (*providerSupervisor, string) { return s, "" }); why != "start_failed:"+name {
		t.Fatalf("the first request read %q", why)
	}
	state := s.state()
	if state.Readiness != wire.DeclarationReadinessNotReady || !strings.HasPrefix(state.Why, "start_failed:exited:exit status 43: fixture provider: the lease endpoint does not answer") {
		t.Fatalf("stopped state %+v", state)
	}
	if n := launches(); n != 3 {
		t.Fatalf("%d launches before stopping, want 3", n)
	}
	// Inside the retry time a request is refused at once and nothing runs.
	if _, why := p.awaitReady(t.Context(), time.Minute, func() (*providerSupervisor, string) { return s, "" }); why != "start_failed:"+name {
		t.Fatalf("a request inside the retry time read %q", why)
	}
	time.Sleep(500 * time.Millisecond)
	if n := launches(); n != 3 {
		t.Fatalf("a stopped provider was relaunched: %d launches", n)
	}
	mu.Lock()
	stopped := slices.IndexFunc(reports, func(line string) bool { return strings.HasPrefix(line, "provider unset stopped after 3 failed starts") })
	mu.Unlock()
	if stopped < 0 {
		t.Fatalf("no stop line in %q", reports)
	}

	// After the retry time the next request launches the program once.
	time.Sleep(2 * time.Second)
	if why := s.startFailure(); why != "" {
		t.Fatalf("after the retry time the provider still reads %q", why)
	}
	if _, why := p.awaitReady(t.Context(), time.Minute, func() (*providerSupervisor, string) { return s, "" }); why != "start_failed:"+name {
		t.Fatalf("the retried request read %q", why)
	}
	if n := launches(); n != 4 {
		t.Fatalf("the retry launched %d times in all, want 4", n)
	}
	mu.Lock()
	defer mu.Unlock()
	lines := 0
	for _, line := range reports {
		if strings.Contains(line, "stopped after") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("the stop was logged %d times: %q", lines, reports)
	}
}
