package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
)

// A 0.3.0 Mac can keep the model host declaration "models host" wrote into
// the runtime's own declarations, naming a modelhostd the 0.4.0 package no
// longer installs. On a platform that runs no native provider the runtime
// reads it not_ready with the platform's reason, says so once, never launches
// or retries it, refuses a mediated call with that reason, and still removes
// it the way "models unhost" withdraws the last hosted model.
func TestANativeProviderOnALimitedPlatformIsUnavailableOnceAndRemovable(t *testing.T) {
	saved := nativeProviderLimit
	t.Cleanup(func() { nativeProviderLimit = saved })
	nativeProviderLimit = nativeProviderLimitOn("darwin")
	newInstallationDir(t)

	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, providersDir), 0o700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "bin", "modelhostd")
	f := providerFile{Version: providerFileVersion, DeclaredBy: "openabstractions models host", DeclaredAt: time.Now().UnixMilli(),
		Declaration: providerDeclaration{Name: modelHostName, Program: missing, Arguments: []string{"serve", "--endpoint", providerEndpointToken},
			Endpoint: modelHostName, Transport: transportNative, Contracts: []string{inference.Contract}, Activation: "on_demand"}}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	declarationFile := filepath.Join(state, providersDir, modelHostName+".json")
	if err := os.WriteFile(declarationFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var reports []string
	report := func(err error) {
		mu.Lock()
		reports = append(reports, err.Error())
		mu.Unlock()
	}
	p, err := openProviders(state, false, report)
	if err != nil {
		t.Fatal(err)
	}
	// The fastest restart timing: a launch loop would show many restarts.
	p.timing = providerTiming{probeWaiting: time.Millisecond, probeReady: time.Millisecond, backoffFirst: time.Millisecond,
		backoffMax: time.Millisecond, stable: time.Hour, probeBudget: time.Second, inventoryBudget: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- p.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	})

	s := p.lookup(modelHostName)
	if s == nil {
		t.Fatal("the model host declaration was not read")
	}
	want := "platform:" + nativeProviderLimit
	for i := 0; i < 20; i++ {
		s.activate()
		time.Sleep(10 * time.Millisecond)
	}
	got := s.state()
	if got.Readiness != fwire.DeclarationReadinessNotReady || got.Why != want || got.Restarts != 0 || s.pid() != 0 {
		t.Fatalf("reading %+v pid %d, want not_ready %q with no launch", got, s.pid(), want)
	}
	if _, err := os.Stat(providerLogPath(p.dir, modelHostName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a launch was attempted: provider log %v", err)
	}
	mu.Lock()
	said := 0
	for _, r := range reports {
		if strings.Contains(r, nativeProviderLimit) {
			said++
		}
	}
	mu.Unlock()
	if said != 1 {
		t.Fatalf("the limit was reported %d times: %q", said, reports)
	}
	if why := s.unavailableReason(); why != want {
		t.Fatalf("a mediated call reads %q, want %q", why, want)
	}
	listed, revision, err := p.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Readiness != fwire.DeclarationReadinessNotReady || listed[0].Why != want {
		t.Fatalf("listed %+v", listed)
	}

	// "models unhost" is not refused by the platform: it reaches the
	// runtime, which withdraws the declaration through remove.
	var output, diagnostics bytes.Buffer
	unhost := modelsCommand([]string{"unhost", "store/object", "--endpoint", "modelhost-limit-no-runtime", "--timeout", "1s"}, &output, &diagnostics)
	if unhost == nil || strings.Contains(unhost.Error()+diagnostics.String(), nativeProviderLimit) {
		t.Fatalf("models unhost on a limited platform: %v %s", unhost, diagnostics.String())
	}
	if change := p.remove(revision, modelHostName); change.Outcome != fwire.DeclarationEditOutcomeApplied {
		t.Fatalf("remove: %+v", change)
	}
	if _, err := os.Stat(declarationFile); !errors.Is(err, os.ErrNotExist) || p.lookup(modelHostName) != nil {
		t.Fatalf("the declaration stayed: %v", err)
	}
}
