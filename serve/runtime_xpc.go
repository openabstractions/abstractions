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
	for _, item := range endpointFlags(options) {
		if item.value != "" {
			return fmt.Errorf("runtime: --xpc cannot combine with %s", item.flag)
		}
	}
	return nil
}

func (options runtimeFlags) defaultEndpoint(service string) (string, error) {
	if options.xpc {
		if options.isolated != "" {
			return bootstrap.IsolatedXPCEndpoint(options.isolated, service)
		}
		return bootstrap.InstalledEndpoint(service)
	}
	return bootstrap.Endpoint(service)
}

// companionEndpoint applies the runtime's single namespace selection to each
// composed capability. Socket runtimes retain their existing names and suffixes.
func (options runtimeFlags) companionEndpoint(service, isolatedName, suffix string) (string, error) {
	if options.xpc {
		return options.defaultEndpoint(service)
	}
	if options.isolated != "" && isolatedName != "" {
		return bootstrap.Endpoint(options.isolated + "-" + isolatedName)
	}
	if options.endpoint != "" {
		return options.endpoint + "-" + suffix, nil
	}
	return options.defaultEndpoint(service)
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

func xpcHostEndpoints(options *host.Options, derive func(string) (string, error)) error {
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
