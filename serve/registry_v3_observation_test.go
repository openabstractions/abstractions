package main

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	watch "github.com/openabstractions/abstraction-watch/go"
)

func registryV3WatchFixture(t *testing.T) (*runtimeRegistry, inference.Subject, *applicationDirectory) {
	t.Helper()
	p, d, program := registry3LegacyFixture(t)
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("watched", program))
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, caller := registryObserverFixture(t)
	r.providers, r.operator.providers = p, p
	r.operator.credentials.runtimeRights.applications = d
	return r, caller, d
}

func TestRegistryV3WatchBoundsCancelTimeoutAndGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, caller, d := registryV3WatchFixture(t)
		d.gate = watch.NewGate(1)
		first := r.programPage(t.Context(), caller, "", 64, true, 0)
		if first.Outcome != registration.OutcomePage {
			t.Fatal(first)
		}
		if got := r.programPage(t.Context(), caller, "foreign.cursor", 64, true, 30000); got.Outcome != registration.OutcomeGap {
			t.Fatal(got)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan registration.Page, 1)
		go func() { done <- r.programPage(ctx, caller, first.Cursor, 64, true, 30000) }()
		synctest.Wait()
		if d.gate.Waiting() != 1 {
			t.Fatal("observer did not enter shared gate")
		}
		if got := r.programPage(t.Context(), caller, first.Cursor, 64, true, 30000); got.Outcome != registration.OutcomeUnavailable || len(got.Registrations) != 0 {
			t.Fatal("gate bound", got)
		}
		cancel()
		if got := <-done; got.Outcome != registration.OutcomeUnavailable || got.Reason != "cancelled" || len(got.Registrations) != 0 {
			t.Fatal("cancel", got)
		}
		if d.gate.Waiting() != 0 {
			t.Fatal("cancel retained waiter")
		}
		before := time.Now()
		if got := r.programPage(t.Context(), caller, first.Cursor, 64, true, 1000); got.Outcome != registration.OutcomePage || got.Cursor != first.Cursor || time.Since(before) != time.Second {
			t.Fatal("timeout", got, time.Since(before))
		}
	})
}

func TestRegistryV3WatchAdvanceDuringReadIsNotLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, caller, d := registryV3WatchFixture(t)
		first := r.programPage(t.Context(), caller, "", 64, true, 0)
		reads := 0
		d.v3.describe = func(_ context.Context, records []registration.Registration) []registration.Registration {
			reads++
			if reads == 1 {
				d.gate.Advance()
			} else {
				records[0].Reason = "changed-during-read"
			}
			return records
		}
		before := time.Now()
		got := r.programPage(t.Context(), caller, first.Cursor, 64, true, 30000)
		if got.Outcome != registration.OutcomePage || len(got.Registrations) != 1 || got.Registrations[0].Reason != "changed-during-read" || reads != 2 || time.Now() != before {
			t.Fatal(got, reads, time.Since(before))
		}
	})
}

func TestRegistryV3WatchRechecksRevocationAfterReadAndWake(t *testing.T) {
	for _, when := range []string{"read", "change", "timeout"} {
		t.Run(when, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, caller, d := registryV3WatchFixture(t)
				first := r.programPage(t.Context(), caller, "", 64, true, 0)
				revoke := func() {
					if err := r.operator.credentials.policy.Revoke(rights.Subject{Account: caller.Account, Program: caller.Program}, ActionProviderManage, credentials.ResourceAccount); err != nil {
						t.Fatal(err)
					}
				}
				if when == "read" {
					d.v3.describe = func(_ context.Context, records []registration.Registration) []registration.Registration {
						revoke()
						return records
					}
				}
				done := make(chan registration.Page, 1)
				go func() { done <- r.programPage(t.Context(), caller, first.Cursor, 64, true, 1000) }()
				if when != "read" {
					synctest.Wait()
					revoke()
					if when == "change" {
						d.gate.Advance()
					}
				}
				if got := <-done; got.Outcome != registration.OutcomeForbidden || got.Refusal == nil || got.Cursor != "" || len(got.Registrations) != 0 {
					t.Fatal("revoked", got)
				}
			})
		})
	}
}

func TestRegistryV3EndpointChecksWakeAndFenceReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, d, program := registry3LegacyFixture(t)
		file := registry3LegacyFile("observed", program)
		registry3WriteLegacy(t, p.dir, file)
		if err := p.adoptProgramRegistrations(t.Context()); err != nil {
			t.Fatal(err)
		}
		r, caller := registryObserverFixture(t)
		r.providers = p
		r.operator.providers = p
		r.operator.credentials.runtimeRights.applications = d
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		first := r.programPage(ctx, caller, "", 64, true, 0)
		if first.Outcome != registration.OutcomePage || len(first.Registrations) != 1 || len(first.Registrations[0].Checks) != 0 {
			t.Fatal(first)
		}
		result := make(chan registration.Page, 1)
		go func() { result <- r.programPage(ctx, caller, first.Cursor, 64, true, 30000) }()
		synctest.Wait()
		p.mu.Lock()
		supervisor := p.supervisors["observed"]
		p.mu.Unlock()
		description := providerEndpoint2Fixture()
		supervisor.recordEndpoint(ctx, nil, nil, &description)
		synctest.Wait()
		select {
		case page := <-result:
			if page.Outcome != registration.OutcomePage || len(page.Registrations) != 1 || len(page.Registrations[0].Checks) != 1 || page.Registrations[0].Checks[0].State != registration.ProducerCheckStateCurrent || page.Registrations[0].Described[0].Readiness != wire.ServiceReadinessNotReady || page.Cursor == first.Cursor {
				t.Fatal("checks-only update", page)
			}
		default:
			t.Fatal("checks-only supervisor observation did not wake registry@3")
		}
		_, revision, err := p.readCached()
		if err != nil {
			t.Fatal(err)
		}
		file.Declaration.Arguments = []string{"replacement"}
		if out := p.replaceAdmitted(ctx, revision, file, nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
			t.Fatal(out)
		}
		rows, _, _, _, _, err := d.programRegistrations(ctx)
		if err != nil || len(rows) != 1 || len(rows[0].Checks) != 0 || len(rows[0].Described) != 0 {
			t.Fatal("old generation borrowed", rows, err)
		}
	})
}

func TestRegistryV3ConcurrentCurrentObservationHasNoDirectoryReentry(t *testing.T) {
	p, d, program := registry3LegacyFixture(t)
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("concurrent", program))
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 8 {
				if _, _, _, _, _, err := d.programRegistrations(ctx); err != nil {
					t.Error(err)
					return
				}
			}
		})
		wg.Go(func() {
			for range 8 {
				p.mu.Lock()
				_, _, err := p.readCached()
				p.mu.Unlock()
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("directory/current-binding read reentered its own lock")
	}
}
