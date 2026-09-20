package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/resolution"
)

func TestRuntimeArguments(t *testing.T) {
	for _, args := range [][]string{{"--without-models", "--model-endpoint", "ep"}, {"--jobs-root", "store"}, {"--jobs-owner", "owner"}, {"--without-jobs", "--jobs-endpoint", "ep"}, {"--without-jobs", "--jobs-run-downloads"}, {"--state-dir", "relative"}} {
		if _, err := parseRuntime(args, io.Discard); err == nil {
			t.Fatalf("accepted incomplete admission setup: %v", args)
		}
	}
	ep := fullEndpoint(t, "ep")
	jobs, err := parseRuntime([]string{"--jobs-root", "store", "--jobs-owner", "owner", "--jobs-endpoint", ep, "--endpoint", fullEndpoint(t, "r"),
		"--log-endpoint", fullEndpoint(t, "l"), "--config-endpoint", fullEndpoint(t, "c"), "--out", "file"}, io.Discard)
	if err != nil || jobs.jobRoot != "store" || jobs.jobOwner != "owner" || jobs.jobEndpoint != ep {
		t.Fatalf("admission flags: %+v %v", jobs, err)
	}
	if _, err := parseRuntime([]string{"--help"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
	if _, err := parseRuntime([]string{"extra"}, io.Discard); err == nil {
		t.Fatal("accepted extra argument")
	}
	resolver, logs, config := fullEndpoint(t, "resolver"), fullEndpoint(t, "logs"), fullEndpoint(t, "config")
	options, err := parseRuntime([]string{"--endpoint", resolver, "--log-endpoint", logs, "--config-endpoint", config, "--out", "file", "--without-jobs"}, io.Discard)
	if err != nil || options.endpoint != resolver || options.logEndpoint != logs || options.configEndpoint != config || options.out != "file" {
		t.Fatalf("%+v: %v", options, err)
	}
}

// The real entry point prints help after serve and names the refused endpoint.
func TestServeCommandHelpAndEndpointRefusal(t *testing.T) {
	run := func(args ...string) (string, string, int) {
		c := exec.Command(os.Args[0], "-test.run=^TestSupervisedProcessHelper$")
		c.Env = helperEnv("runtime", args)
		var stdout, stderr strings.Builder
		c.Stdout, c.Stderr = &stdout, &stderr
		err := c.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return stdout.String(), stderr.String(), exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return stdout.String(), stderr.String(), 0
	}
	for _, help := range []string{"--help", "-h"} {
		stdout, stderr, code := run("serve", help)
		if code != 0 || !strings.Contains(stdout, "EXIT") || strings.Contains(stderr, "no capability") || !strings.Contains(stderr, "openabstractions serve runtime") {
			t.Fatalf("serve %s: exit %d\nstdout %s\nstderr %s", help, code, stdout, stderr)
		}
	}
	_, stderr, code := run("serve", "runtime", "-endpoint", "l6-runtime")
	if code != 1 || !strings.Contains(stderr, `--endpoint "l6-runtime"`) || !strings.Contains(stderr, "--isolated <name>") {
		t.Fatalf("bare endpoint name: exit %d\n%s", code, stderr)
	}
	_, stderr, code = run("serve", "runtime", "--help")
	if code != 0 || !strings.Contains(stderr, "-isolated") {
		t.Fatalf("runtime help: exit %d\n%s", code, stderr)
	}
}

// fullEndpoint is an endpoint in the accepted platform form.
func fullEndpoint(t *testing.T, name string) string {
	if runtime.GOOS == "windows" {
		return `\\.\pipe\oa-test-` + name
	}
	return filepath.Join(t.TempDir(), name+".sock")
}

func TestRuntimeEndpointRefusesBareName(t *testing.T) {
	for _, flagName := range []string{"--endpoint", "--log-endpoint", "--config-endpoint", "--jobs-endpoint", "--model-endpoint"} {
		_, err := parseRuntime([]string{flagName, "l6-runtime"}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), flagName+` "l6-runtime"`) || !strings.Contains(err.Error(), "--isolated <name>") {
			t.Fatalf("%s bare name: %v", flagName, err)
		}
	}
	for _, c := range []struct {
		platform, value string
		accepted        bool
		want            string
	}{
		{"windows", `\\.\pipe\l6-runtime`, true, ""},
		{"windows", `\\.\PIPE\l6-runtime`, true, ""},
		{"windows", "l6-runtime", false, `give the full form \\.\pipe\<name>`},
		{"windows", `\.\pipe\l6-runtime`, false, "Git Bash"},
		{"windows", `\\.\pipe\`, false, "not a named pipe path"},
		{"linux", "/run/user/1000/l6.sock", true, ""},
		{"linux", "l6-runtime", false, "not an absolute socket path"},
		{"darwin", "run/l6.sock", false, "/absolute/dir/<name>.sock"},
	} {
		err := endpointForm(c.platform, "--endpoint", c.value)
		if c.accepted != (err == nil) || err != nil && !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s %q: %v", c.platform, c.value, err)
		}
	}
}

func TestRuntimePartialSelectionRefused(t *testing.T) {
	resolver, logs, config, jobs := fullEndpoint(t, "r"), fullEndpoint(t, "l"), fullEndpoint(t, "c"), fullEndpoint(t, "j")
	state := t.TempDir()
	_, err := parseRuntime([]string{"--endpoint", resolver}, io.Discard)
	if err == nil {
		t.Fatal("accepted --endpoint alone")
	}
	for _, name := range []string{"--log-endpoint", "--config-endpoint", "--jobs-endpoint", "--state-dir", "--out", "--isolated <name>"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("refusal does not name %s: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "--model-endpoint") {
		t.Fatalf("model endpoint derives from --endpoint: %v", err)
	}
	for _, args := range [][]string{
		{"--jobs-endpoint", jobs},
		{"--endpoint", resolver, "--log-endpoint", logs, "--config-endpoint", config, "--jobs-endpoint", jobs, "--state-dir", state},
		{"--endpoint", resolver, "--log-endpoint", logs, "--config-endpoint", config, "--out", "file", "--state-dir", state},
		{"--log-endpoint", logs, "--config-endpoint", config, "--out", "file", "--without-jobs"},
	} {
		if _, err := parseRuntime(args, io.Discard); err == nil {
			t.Fatalf("accepted partial selection %v", args)
		}
	}
	complete := []string{"--endpoint", resolver, "--log-endpoint", logs, "--config-endpoint", config, "--jobs-endpoint", jobs, "--state-dir", state, "--out", "file"}
	if options, err := parseRuntime(complete, io.Discard); err != nil || options.isolated != "" {
		t.Fatalf("complete explicit selection: %+v %v", options, err)
	}
}

func TestRuntimeIsolatedDerivesEverySelection(t *testing.T) {
	state := t.TempDir()
	options, err := parseRuntime([]string{"--isolated", "l6", "--state-dir", state}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, service := range []string{"runtime", "logging", "config", "jobs", "model"} {
		if want[service], err = bootstrap.Endpoint("l6-" + service); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]string{"runtime": options.endpoint, "logging": options.logEndpoint, "config": options.configEndpoint, "jobs": options.jobEndpoint, "model": options.modelEndpoint}
	installed := map[string]bool{}
	for _, service := range []string{"runtime-v1", "logging-v1", "config-v1", "job-acceptance-v1", "model-v1"} {
		endpoint, err := bootstrap.Endpoint(service)
		if err != nil {
			t.Fatal(err)
		}
		installed[endpoint] = true
	}
	for service, endpoint := range got {
		if endpoint != want[service] || installed[endpoint] {
			t.Fatalf("%s endpoint %q, want %q apart from the installed runtime", service, endpoint, want[service])
		}
	}
	if options.out != filepath.Join(state, "logging", "records.jsonl") {
		t.Fatalf("isolated log file %q", options.out)
	}
	explicitLog := fullEndpoint(t, "kept")
	kept, err := parseRuntime([]string{"--isolated", "l6", "--state-dir", state, "--log-endpoint", explicitLog, "--out", "file", "--without-models"}, io.Discard)
	if err != nil || kept.logEndpoint != explicitLog || kept.out != "file" || kept.modelEndpoint != "" || kept.jobEndpoint != want["jobs"] {
		t.Fatalf("explicit values beside --isolated: %+v %v", kept, err)
	}
	for _, args := range [][]string{
		{"--isolated", "l6"},
		{"--isolated", "L6", "--state-dir", state},
		{"--isolated", `\\.\pipe\l6`, "--state-dir", state},
		{"--isolated", strings.Repeat("a", 65), "--state-dir", state},
	} {
		if _, err := parseRuntime(args, io.Discard); err == nil || !strings.Contains(err.Error(), "--isolated") {
			t.Fatalf("accepted %v: %v", args, err)
		}
	}
	var described strings.Builder
	describeIsolated(&described, options)
	for _, text := range []string{`isolated runtime "l6"`, "ABSTRACTION_RUNTIME_ENDPOINT=" + want["runtime"], want["logging"], want["jobs"], state} {
		if !strings.Contains(described.String(), text) {
			t.Fatalf("description lacks %q:\n%s", text, described.String())
		}
	}
}

// An isolated runtime answers on its derived resolver and hands out its derived
// provider endpoints.
func TestRuntimeIsolatedServesDerivedEndpoints(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current platform")
	}
	name := fmt.Sprintf("test-%d-%d", os.Getpid(), endpointSeq.Add(1))
	state, err := os.MkdirTemp("", "oa-isolated-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	options, err := parseRuntime([]string{"--isolated", name, "--state-dir", state, "--without-models"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("isolated runtime: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer func() {
		cancel()
		if err := awaitStopped(t, done, "isolated runtime"); err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	resolver := resolution.NewClient(options.endpoint, 2*time.Second)
	for contract, endpoint := range map[string]string{"abstraction.logging/sink@1": options.logEndpoint, "abstraction.job/acceptance@1": options.jobEndpoint} {
		capability := contract[:strings.Index(contract, "/")]
		result, err := resolver.Resolve(ctx, wire.ResolveRequest{Capability: capability, Contracts: []string{contract}, Scope: wire.ScopeLocal})
		if err != nil || result.Status.String() != "resolved" || result.Reference.Endpoint != endpoint {
			t.Fatalf("%s: %+v %v, want endpoint %q", contract, result, err, endpoint)
		}
	}
}

func TestCanceledRuntimeCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "records.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runRuntime(ctx, runtimeFlags{out: path}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("canceled startup changed filesystem: %v", err)
	}
}

func TestRuntimeStateLocation(t *testing.T) {
	base := t.TempDir()
	for _, platform := range []string{"windows", "linux", "darwin"} {
		got, err := runtimeStateDir(platform, func(string) string { return base }, func() (string, error) { return base, nil })
		want := filepath.Join(base, "openabstractions", "runtime-v1")
		if platform == "darwin" {
			want = filepath.Join(base, "Library", "Application Support", "openabstractions", "runtime-v1")
		}
		if err != nil || got != want {
			t.Fatalf("%s: %q %v", platform, got, err)
		}
	}
	for _, platform := range []string{"windows", "linux", "darwin", "unknown"} {
		if _, err := runtimeStateDir(platform, func(string) string { return "relative" }, func() (string, error) { return "relative", nil }); err == nil {
			t.Fatalf("accepted relative state for %s", platform)
		}
	}
	got, err := runtimeStateDir("linux", func(string) string { return "" }, func() (string, error) { return base, nil })
	if err != nil || got != filepath.Join(base, ".local", "share", "openabstractions", "runtime-v1") {
		t.Fatalf("Linux default: %q %v", got, err)
	}
	for _, args := range [][]string{{}, {"--jobs-run-downloads"}, {"--without-jobs"}} {
		if _, err := parseRuntime(args, io.Discard); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
