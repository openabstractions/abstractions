package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledXPCOptionsPreventMixedNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		options        runtimeFlags
		refused        bool
	}{
		{"darwin", "darwin", runtimeFlags{xpc: true}, false},
		{"windows", "windows", runtimeFlags{xpc: true}, true},
		{"linux", "linux", runtimeFlags{xpc: true}, true},
		{"isolated", "darwin", runtimeFlags{xpc: true, isolated: "test", stateDir: "/tmp/private-state"}, false},
		{"resolver", "darwin", runtimeFlags{xpc: true, endpoint: "/tmp/runtime"}, true},
		{"logging", "darwin", runtimeFlags{xpc: true, logEndpoint: "/tmp/logging"}, true},
		{"config", "darwin", runtimeFlags{xpc: true, configEndpoint: "/tmp/config"}, true},
		{"jobs", "darwin", runtimeFlags{xpc: true, jobEndpoint: "/tmp/jobs"}, true},
		{"models", "darwin", runtimeFlags{xpc: true, modelEndpoint: "/tmp/model"}, true},
		{"socket compatibility", "darwin", runtimeFlags{endpoint: "/tmp/runtime"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateXPCOptions(tc.options, tc.platform); (err != nil) != tc.refused {
				t.Fatalf("refused=%v, error=%v", tc.refused, err)
			}
		})
	}
}

func TestIsolatedXPCSelectsEveryRuntimeEndpointInItsOwnNamespace(t *testing.T) {
	options := runtimeFlags{xpc: true, isolated: "permission-demo", stateDir: t.TempDir()}
	if err := isolateRuntime(&options, options.defaultEndpoint); err != nil {
		t.Fatal(err)
	}
	h := host.Options{Endpoint: options.endpoint, LogEndpoint: options.logEndpoint, ConfigEndpoint: options.configEndpoint,
		JobEndpoint: options.jobEndpoint, JobRoot: "enabled", ModelEndpoint: options.modelEndpoint}
	if err := xpcHostEndpoints(&h, options.defaultEndpoint); err != nil {
		t.Fatal(err)
	}
	credentials, rights, namespace, err := credentialsEndpoints(options)
	if err != nil || namespace != "oa-isolated-permission-demo" {
		t.Fatalf("credentials namespace = %q: %v", namespace, err)
	}
	endpoints := map[string]string{"runtime-v1": h.Endpoint, "logging-v1": h.LogEndpoint, "config-v1": h.ConfigEndpoint,
		"job-acceptance-v1": h.JobEndpoint, "model-v1": options.modelEndpoint, "credentials-v1": credentials, "rights-authorization-v1": rights}
	for service, derive := range map[string]func(runtimeFlags) (string, error){
		"asks-application-v1": asksEndpoint, "inference-v1": inferenceEndpoint, "inference-remote-v1": inferenceRemoteEndpoint,
		"router-v1": runtimeRouterEndpoint, "resource-table-v1": resourceTableEndpoint, "lend-v1": lendingEndpoint, "registry-v1": registryEndpoint,
	} {
		endpoint, err := derive(options)
		if err != nil {
			t.Fatal(err)
		}
		endpoints[service] = endpoint
	}
	for service, suffix := range map[string]string{"applications-v1": "applications", "storage-content-v1": "content"} {
		endpoint, err := options.companionEndpoint(service, "", suffix)
		if err != nil {
			t.Fatal(err)
		}
		endpoints[service] = endpoint
	}
	if len(endpoints) != 16 {
		t.Fatalf("incomplete runtime inventory: %v", endpoints)
	}
	for service, endpoint := range endpoints {
		want, err := bootstrap.IsolatedXPCEndpoint(options.isolated, service)
		if err != nil || endpoint != want {
			t.Errorf("%s = %q, want %q: %v", service, endpoint, want, err)
		}
	}
	if state, err := credentialsState(options); err != nil || state != options.stateDir {
		t.Fatalf("selected installed state: %q %v", state, err)
	}
	if options.out != filepath.Join(options.stateDir, "logging", "records.jsonl") {
		t.Fatalf("selected installed logs: %q", options.out)
	}
}

func TestIsolatedXPCRefusesPartialAndCustomSelections(t *testing.T) {
	for _, name := range []string{"", "Demo", "a/b", strings.Repeat("a", 65)} {
		options := runtimeFlags{xpc: true, isolated: name, stateDir: t.TempDir()}
		if err := isolateRuntime(&options, options.defaultEndpoint); err == nil {
			t.Errorf("invalid isolated name %q accepted", name)
		}
	}
	options := runtimeFlags{xpc: true, isolated: "demo"}
	if err := isolateRuntime(&options, options.defaultEndpoint); err == nil {
		t.Error("isolated XPC accepted installed state default")
	}
	for _, set := range []func(*runtimeFlags){
		func(o *runtimeFlags) { o.endpoint = "/tmp/custom" }, func(o *runtimeFlags) { o.logEndpoint = "/tmp/custom" },
		func(o *runtimeFlags) { o.configEndpoint = "/tmp/custom" }, func(o *runtimeFlags) { o.jobEndpoint = "/tmp/custom" },
		func(o *runtimeFlags) { o.modelEndpoint = "/tmp/custom" },
	} {
		options := runtimeFlags{xpc: true, isolated: "demo", stateDir: t.TempDir()}
		set(&options)
		if err := validateXPCOptions(options, "darwin"); err == nil {
			t.Errorf("mixed XPC namespace accepted: %+v", options)
		}
	}
}

func TestUnavailableIsolatedXPCRefusesBeforeState(t *testing.T) {
	options := runtimeFlags{xpc: true, isolated: "demo", stateDir: filepath.Join(t.TempDir(), "not-created")}
	if listen.CanEver("xpc:com.openabstractions.isolated.demo.runtime-v1", listen.Program) == nil {
		t.Skip("native XPC transport available; registered live runtime proof runs on macOS")
	}
	if err := runRuntimeReady(context.Background(), options, nil); err == nil {
		t.Fatal("unavailable XPC accepted")
	}
	if _, err := os.Stat(options.stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unavailable XPC opened state: %v", err)
	}
}

func TestInstalledEndpointsLeaveDisabledCapabilitiesAbsent(t *testing.T) {
	options := host.Options{}
	if err := xpcHostEndpoints(&options, func(service string) (string, error) { return "xpc:test." + service, nil }); err != nil {
		t.Fatal(err)
	}
	if options.Endpoint != "xpc:test.runtime-v1" || options.LogEndpoint != "xpc:test.logging-v1" || options.ConfigEndpoint != "xpc:test.config-v1" {
		t.Fatalf("incomplete installed host: %+v", options)
	}
	if options.JobEndpoint != "" || options.ModelEndpoint != "" {
		t.Fatal("disabled providers received endpoints")
	}
	failure := errors.New("unavailable")
	if err := xpcHostEndpoints(&options, func(string) (string, error) { return "", failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
