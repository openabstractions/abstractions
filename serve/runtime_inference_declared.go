package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	router "github.com/openabstractions/abstraction-router/go"
)

// declarationEnv is what the product declaration probes read: this account's
// environment, home directory and product records, and Foundry Local's own
// status command when it is on PATH. Tests replace it with fixtures, so no
// test reads a real product record (runtime_inference_declared_test.go).
var declarationEnv = func(report func(error)) router.ProbeEnv {
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
