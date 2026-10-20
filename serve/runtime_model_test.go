package main

import (
	"context"
	"runtime"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	modelclient "github.com/openabstractions/abstraction-model/go/client"
)

func TestCentralRuntimeModelLookup(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation")
	}
	opts, _ := isolatedRuntime(t)
	opts.withoutJobs = true
	opts.jobEndpoint = ""
	// Keep model service composition enabled; an unknown registry never probes a host.
	opts.withoutModels = false
	endpoint := opts.endpoint
	startInferenceRuntime(t, opts)
	ctx, cancel := context.WithTimeout(t.Context(), runtimeWait)
	defer cancel()
	lookup, err := client.New(endpoint).ResolveModel(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	// An ungranted registry is refused before lookup. Grant only this fixture's
	// invented registry to exercise wiring without an external lookup.
	r, err := lookup.ResolveContext(ctx, modelclient.Ref{Registry: "unconfigured-test-registry", Repo: "weights"})
	if err != nil || r.Outcome.String() != "forbidden" || r.Request != nil {
		t.Fatalf("ungranted lookup %+v %v", r, err)
	}
	if grant, err := runRightsCommand(t, "grant", "--endpoint", endpoint, "--program", probeSelf().Program,
		"--action", host.ModelLookupAction, "--resource", "unconfigured-test-registry"); err != nil || grant.Outcome != "applied" {
		t.Fatalf("fixture lookup grant %+v %v", grant, err)
	}
	r, err = lookup.ResolveContext(ctx, modelclient.Ref{Registry: "unconfigured-test-registry", Repo: "weights"})
	if err != nil || r.Outcome.String() != "unavailable" || r.Request != nil {
		t.Fatalf("lookup %+v %v", r, err)
	}
}
