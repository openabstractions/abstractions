package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

func TestPanelReadinessMatchesSDKWithIndependentFailures(t *testing.T) {
	own(t)
	panelConfigRuntime(t)
	machine := panelMachine()
	evidence := wire.BootstrapObservation{State: wire.BootstrapStateInstalled, Detail: "isolated test registration"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	want, err := machine.Observe(ctx, facade.DefaultStatusRequests(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	got := collectReadiness(ctx, machine, func(context.Context) wire.BootstrapObservation { return evidence })
	expected := readinessPresentation(want, nil)
	if got.Bootstrap != "installed" || got.Error != "" || !reflect.DeepEqual(got.Capabilities, expected.Capabilities) {
		t.Fatalf("panel changed SDK observation: %+v expected %+v", got, expected)
	}
	states := map[string]string{}
	for _, row := range got.Capabilities {
		states[row.Contract] = row.Status
	}
	if states["abstraction.config/editor@1"] != "resolved" || states["abstraction.logging/sink@1"] != "not_ready" || states["abstraction.job/acceptance@1"] != "unavailable" {
		t.Fatalf("independent capabilities collapsed: %+v", states)
	}
}

func TestPanelReadinessRetainsPartialFailure(t *testing.T) {
	requests := facade.DefaultStatusRequests()
	observation := wire.RuntimeObservation{Bootstrap: wire.BootstrapObservation{State: wire.BootstrapStateRunning}}
	for _, request := range requests {
		observation.Capabilities = append(observation.Capabilities, wire.CapabilityObservation{Request: request})
	}
	observation.Capabilities[0].Result = &wire.ResolveResult{Status: wire.ResolutionStatusForbidden}
	view := readinessPresentation(observation, errors.New("resolver disconnected"))
	if view.Bootstrap != "running" || view.Capabilities[0].Status != "forbidden" || view.Capabilities[1].Status != "unobserved" || view.Error == "" {
		t.Fatalf("partial result invented readiness or lost refusal: %+v", view)
	}
}

// TestPanelReadinessNamesTheExplicitRuntime keeps the "Check the service"
// sentence from contradicting itself: when this Panel was started against a
// named runtime (-runtime-endpoint/-runtime-program), the view must say so,
// not report on installed-runtime detection that never ran for that runtime
// (2026-09-23 walkthrough finding 1).
func TestPanelReadinessNamesTheExplicitRuntime(t *testing.T) {
	previous := panelExplicit
	panelExplicit = &explicitRuntime{Endpoint: `\\.\pipe\iso`, Program: `C:\oa\openabstractions.exe`, Account: "S-1-5-21-1"}
	t.Cleanup(func() { panelExplicit = previous })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	view := collectReadiness(ctx, facade.New("unused"), func(context.Context) wire.BootstrapObservation {
		return wire.BootstrapObservation{State: wire.BootstrapStateUnknown}
	})
	if view.Explicit == nil || *view.Explicit != *panelExplicit {
		t.Fatalf("readiness view lost the explicit runtime: %+v", view)
	}
}

func TestPanelReadinessSharesCancelledBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	view := collectReadiness(ctx, facade.New("unused"), func(ctx context.Context) wire.BootstrapObservation {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("bootstrap observation lost cancellation")
		}
		return wire.BootstrapObservation{State: wire.BootstrapStateUnknown}
	})
	if view.Error == "" {
		t.Fatal("cancelled observation reported success")
	}
	for _, row := range view.Capabilities {
		if row.Status != "unobserved" {
			t.Fatalf("cancelled query claimed %s", row.Status)
		}
	}
}
