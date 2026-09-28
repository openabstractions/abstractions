package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

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
			return exec.CommandContext(ctx, path, args...).Output()
		},
		Log: func(product, reason string) {
			if report != nil {
				report(fmt.Errorf("inference: %s declaration unused, default kept: %s", product, reason))
			}
		}}
}

// declarationEnv is what the product declaration probes read. See
// defaultDeclarationEnv.
var declarationEnv = defaultDeclarationEnv

// declaredLocalHosts is the local hosts the products on this machine declare,
// used when hosts.json names no local list.
func declaredLocalHosts(report func(error)) []*router.Host {
	var out []*router.Host
	for _, h := range router.Declared(declarationEnv(report)) {
		if h.Servable() {
			out = append(out, h)
		}
	}
	return out
}
