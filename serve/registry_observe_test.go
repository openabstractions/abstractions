package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"testing/synctest"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

func registryObserverFixture(t *testing.T) (*runtimeRegistry, inference.Subject) {
	t.Helper()
	r := testRights(t, t.TempDir())
	if err := r.install([]string{ActionProviderManage}, []installationRule{{ActionProviderManage, credentials.ResourceAccount}}); err != nil {
		t.Fatal(err)
	}
	p, err := openProviders(t.TempDir(), false, func(err error) { t.Logf("provider: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: r}, providers: p}
	return &runtimeRegistry{providers: p, operator: o}, inference.Subject{Account: r.owner, Program: r.operators[0]}
}

func TestRegistryObservePagesAndWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, caller := registryObserverFixture(t)
		ctx := context.Background()
		first := r.Observe(ctx, caller, "", 0)
		if first.Outcome != wire.DeclarationListOutcomePage || first.Cursor == "" || len(first.Declarations) != 0 {
			t.Fatalf("initial: %+v", first)
		}
		for _, wait := range []int64{-1, maxObserveWait + 1} {
			if got := r.Observe(ctx, caller, first.Cursor, wait); got.Outcome != wire.DeclarationListOutcomeInvalid {
				t.Fatalf("wait %d: %+v", wait, got)
			}
		}
		start := time.Now()
		if got := r.Observe(ctx, caller, first.Cursor, 1000); got.Outcome != wire.DeclarationListOutcomePage || got.Cursor != first.Cursor || time.Since(start) != time.Second {
			t.Fatalf("unchanged timeout: %+v, elapsed %s", got, time.Since(start))
		}
		result := make(chan wire.DeclarationObservation, 1)
		go func() { result <- r.Observe(ctx, caller, first.Cursor, maxObserveWait) }()
		synctest.Wait()
		select {
		case got := <-result:
			t.Fatalf("returned before change: %+v", got)
		default:
		}
		_, revision, err := r.providers.read()
		if err != nil {
			t.Fatal(err)
		}
		entry := iwire.HostEntry{Name: "fixture", Hosted: true, Kind: "openai-compatible", Base: "https://fixture.invalid"}
		if change := r.providers.add(revision, newHostFile(entry, caller.Program)); change.Outcome != wire.DeclarationEditOutcomeApplied {
			t.Fatalf("add: %+v", change)
		}
		got := <-result
		if got.Outcome != wire.DeclarationListOutcomePage || got.Cursor == first.Cursor || len(got.Declarations) != 1 || got.Declarations[0].Declaration.Name != entry.Name {
			t.Fatalf("changed: %+v", got)
		}
		previous := got.Cursor
		go func() { result <- r.Observe(ctx, caller, previous, maxObserveWait) }()
		synctest.Wait()
		r.providers.routerHostStates = func() []router.HostState {
			return []router.HostState{{Host: entry.Name, Up: true}}
		}
		r.providers.notify()
		got = <-result
		if got.Outcome != wire.DeclarationListOutcomePage || got.Cursor == previous || len(got.Declarations) != 1 || got.Declarations[0].Readiness != wire.DeclarationReadinessReady {
			t.Fatalf("readiness changed: %+v", got)
		}
		call, cancel := context.WithCancel(ctx)
		go func() { result <- r.Observe(call, caller, got.Cursor, maxObserveWait) }()
		synctest.Wait()
		cancel()
		if got := <-result; got.Outcome != wire.DeclarationListOutcomeUnavailable || len(got.Declarations) != 0 {
			t.Fatalf("canceled: %+v", got)
		}
	})
}

func TestRegistryObserveRefusesUnreadableDeclarations(t *testing.T) {
	r, caller := registryObserverFixture(t)
	if err := os.Rename(r.providers.dir, r.providers.dir+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.providers.dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := r.Observe(context.Background(), caller, "", 0); got.Outcome != wire.DeclarationListOutcomeUnavailable || got.Cursor != "" || len(got.Declarations) != 0 {
		t.Fatalf("unreadable store: %+v", got)
	}
}

func TestRegistryObserveRevocationDuringWait(t *testing.T) {
	for _, wake := range []string{"change", "timeout"} {
		t.Run(wake, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, caller := registryObserverFixture(t)
				first := r.Observe(context.Background(), caller, "", 0)
				result := make(chan wire.DeclarationObservation, 1)
				go func() { result <- r.Observe(context.Background(), caller, first.Cursor, 1000) }()
				synctest.Wait()
				if err := r.operator.credentials.policy.Revoke(rwire.Subject{Account: caller.Account, Program: caller.Program}, ActionProviderManage, credentials.ResourceAccount); err != nil {
					t.Fatal(err)
				}
				if wake == "change" {
					r.providers.notify()
				}
				if got := <-result; got.Outcome != wire.DeclarationListOutcomeForbidden || got.Cursor != "" || len(got.Declarations) != 0 {
					t.Fatalf("revoked: %+v", got)
				}
			})
		})
	}
}

func TestRegistryObserveThroughIPC(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	r, caller := registryObserverFixture(t)
	endpoint := listen.Endpoint(fmt.Sprintf("registry-observe-%d", time.Now().UnixNano()))
	h, err := listenRegistry(endpoint, r, caller.Account, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	c := wire.NewRegistryClient((listen.FrameClient{Endpoint: endpoint, Timeout: time.Second, MaxFrame: 1 << 20}).WithContext(ctx))
	first, err := c.Observe("", 0)
	if err != nil || first.Outcome != wire.DeclarationListOutcomePage || first.Cursor == "" {
		t.Fatalf("IPC initial: %+v %v", first, err)
	}
	list := r.Declarations(ctx, caller)
	entry := iwire.HostEntry{Name: "fixture", Hosted: true, Kind: "openai-compatible", Base: "https://fixture.invalid"}
	if added := r.providers.add(list.Revision, newHostFile(entry, caller.Program)); added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("add: %+v", added)
	}
	next, err := c.Observe(first.Cursor, 0)
	if err != nil || next.Outcome != wire.DeclarationListOutcomePage || next.Cursor == first.Cursor || len(next.Declarations) != 1 || next.Declarations[0].Declaration.Name != entry.Name {
		t.Fatalf("IPC changed: %+v %v", next, err)
	}
}
