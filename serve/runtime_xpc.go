package main

import (
	"fmt"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
)

func validateXPCOptions(options runtimeFlags, platform string) error {
	if !options.xpc {
		return nil
	}
	if platform != "darwin" {
		return fmt.Errorf("runtime: --xpc requires macOS")
	}
	if options.isolated != "" {
		return fmt.Errorf("runtime: --xpc uses installed Mach services and cannot combine with --isolated")
	}
	for _, item := range endpointFlags(options) {
		if item.value != "" {
			return fmt.Errorf("runtime: --xpc cannot combine with %s", item.flag)
		}
	}
	return nil
}

func (options runtimeFlags) defaultEndpoint(service string) (string, error) {
	if options.xpc {
		return bootstrap.InstalledEndpoint(service)
	}
	return bootstrap.Endpoint(service)
}

// Refuse unavailable transports before opening service-owned state.
func checkRuntimeTransport(options runtimeFlags) error {
	if !options.xpc {
		return nil
	}
	endpoint, err := options.defaultEndpoint("runtime-v1")
	if err != nil {
		return err
	}
	return listen.CanEver(endpoint, listen.Program)
}

func installedHostEndpoints(options *host.Options, derive func(string) (string, error)) error {
	for _, item := range []struct {
		value   *string
		service string
		enabled bool
	}{
		{&options.Endpoint, "runtime-v1", true},
		{&options.LogEndpoint, "logging-v1", true},
		{&options.ConfigEndpoint, "config-v1", true},
		{&options.JobEndpoint, "job-acceptance-v1", options.JobRoot != ""},
		{&options.ModelEndpoint, "model-v1", options.ModelRegistry != nil},
	} {
		if !item.enabled {
			continue
		}
		endpoint, err := derive(item.service)
		if err != nil {
			return err
		}
		*item.value = endpoint
	}
	return nil
}
