package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	"runtime"
	"time"

	"github.com/openabstractions/abstraction-provider-kit/go/modelobserver"
	router "github.com/openabstractions/abstraction-router/go"
)

// defaultDeclarationEnv reads the product declaration probes' real evidence:
// this account's environment, home directory and product records, and
// Foundry Local's own status command when it is on PATH. declarationEnv
// starts bound to it, and the package-wide test stub
// (runtime_inference_declared_test.go's init) rebinds declarationEnv to a
// fixture for this test binary's whole run, so no test reads a real product
// record by accident. isolatedRuntime's ProductHosts option rebinds it back
// to defaultDeclarationEnv for one runtime, where a test wants the composition
// a person's `openabstractions serve runtime --isolated` gets.
func defaultDeclarationEnv(report func(error)) router.ProbeEnv {
	//unchecked: a failure just leaves Home empty, which the probe treats as no home known
	home, _ := os.UserHomeDir()
	return router.ProbeEnv{GOOS: runtime.GOOS, Home: home, AppData: os.Getenv("APPDATA"), Getenv: os.Getenv, ReadFile: os.ReadFile,
		Stat: func(path string) bool { _, err := os.Stat(path); return err == nil },
		Status: func(ctx context.Context, program string, args ...string) ([]byte, error) {
			path, err := exec.LookPath(program)
			if err != nil {
				return nil, err
			}
			return runDeclarationProbe(ctx, path, args)
		},
		Log: func(product, reason string) {
			if report != nil {
				report(fmt.Errorf("inference: %s declaration unused, default kept: %s", product, reason))
			}
		}}
}

// declarationProbeStart starts a declaration status probe. Tests replace it
// to see that the probe goes through supervision.
var declarationProbeStart = supervision.Start

// runDeclarationProbe runs one status command inside its own supervision job
// and returns its standard output, read through a pipe. Where supervision is
// unavailable (outside Windows) it runs the command directly.
func runDeclarationProbe(ctx context.Context, path string, args []string) ([]byte, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	p, err := declarationProbeStart(ctx, supervision.Options{Executable: path, Args: args, Stdout: w, ForceTimeout: 5 * time.Second})
	w.Close()
	if errors.Is(err, supervision.ErrUnsupported) {
		return childproc.Hidden(exec.CommandContext(ctx, path, args...)).Output()
	}
	if err != nil {
		return nil, err
	}
	//unchecked: Stop's error only says the tree was already gone
	stop := context.AfterFunc(ctx, func() { _ = p.Stop() })
	defer stop()
	out, readErr := io.ReadAll(r)
	code, err := p.Wait()
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if readErr != nil {
		return out, readErr
	}
	if code != 0 {
		return out, fmt.Errorf("%s exited with code %d", path, code)
	}
	return out, nil
}

// declarationEnv is what the product declaration probes read. See
// defaultDeclarationEnv.
var declarationEnv = defaultDeclarationEnv

// Private runtime authority comes from its guarded state. Missing private
// configuration contributes no implicit account products. Explicit hosts,
// registrations and the selected carrier's packaged declarations still load.
func openRuntimeProviders(options runtimeFlags, state string, config inferenceHosts, report func(error)) (*runtimeProviders, error) {
	declared := config.usesDeclarations()
	if options.privateState && config.Declared == nil {
		declared = false
	}
	if options.endpointFor != nil {
		return openProvidersWithEndpoint(state, declared, report, func(name string) (string, error) {
			return providerEndpointPathFor(name, options.bootstrapEndpoint)
		})
	}
	return openProviders(state, declared, report)
}

// declaredLocalHosts is the local hosts the products on this machine declare,
// used when hosts.json names no local list.
func declaredLocalHosts(report func(error), snapshot func() ([]router.HostState, time.Time)) ([]modelobserver.Reading, time.Time) {
	return liveProductHosts(context.Background(), router.Declared(declarationEnv(report)), snapshot)
}

// Every product candidate uses the same kit observer for present program and
// responding-server evidence before catalogue promotion.
func liveProductHosts(ctx context.Context, candidates []*router.Host, snapshot func() ([]router.HostState, time.Time)) ([]modelobserver.Reading, time.Time) {
	return modelobserver.Observe(ctx, candidates, snapshot, productDeclarationAge)
}
