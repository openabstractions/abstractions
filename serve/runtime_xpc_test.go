package main

import (
	"errors"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
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
		{"isolated", "darwin", runtimeFlags{xpc: true, isolated: "test"}, true},
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

func TestInstalledEndpointsLeaveDisabledCapabilitiesAbsent(t *testing.T) {
	options := host.Options{}
	if err := installedHostEndpoints(&options, func(service string) (string, error) { return "xpc:test." + service, nil }); err != nil {
		t.Fatal(err)
	}
	if options.Endpoint != "xpc:test.runtime-v1" || options.LogEndpoint != "xpc:test.logging-v1" || options.ConfigEndpoint != "xpc:test.config-v1" {
		t.Fatalf("incomplete installed host: %+v", options)
	}
	if options.JobEndpoint != "" || options.ModelEndpoint != "" {
		t.Fatal("disabled providers received endpoints")
	}
	failure := errors.New("unavailable")
	if err := installedHostEndpoints(&options, func(string) (string, error) { return "", failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
