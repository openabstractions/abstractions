package main

import (
	"os"
	"runtime"
	"strings"

	configclient "github.com/openabstractions/abstraction-config/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	resourceclient "github.com/openabstractions/abstraction-resource/go/client"
	routerclient "github.com/openabstractions/abstraction-router/go/client"
)

// providerEnvironment names this runtime's own services to the providers it
// starts on demand, in the variables each service's client reads: the lease
// and table service, the log, configuration and the router, and the runtime
// itself when it does not listen where the installed runtime does. A
// declaration then names no endpoint of this runtime, and an isolated
// runtime's provider holds its leases and logs where that runtime serves
// them. An argument the declaration carries still wins over the variable in
// a provider that reads both (modelhostd's --resource-endpoint). A runtime on
// installed Mach services (--xpc) names none: its clients find those by name.
// Isolated Mach services supply their own endpoint variables to every child.
// A name this runtime does not set reaches no child (childEnvironment).
func providerEnvironment(options runtimeFlags, o host.Options) []string {
	if options.xpc && options.isolated == "" {
		return nil
	}
	var env []string
	add := func(name, value string) {
		if value != "" {
			env = append(env, name+"="+value)
		}
	}
	if o.ResourceTable != nil {
		add(resourceclient.EnvEndpoint, o.ResourceTableEndpoint)
	}
	add(logclient.EnvEndpoint, o.LogEndpoint)
	add(configclient.EnvEndpoint, o.ConfigEndpoint)
	if o.Router != nil {
		add(routerclient.EnvEndpoint, o.RouterEndpoint)
	}
	if options.endpoint != "" {
		add(runtimeEndpointVar, o.Endpoint)
	}
	return env
}

// runtimeEndpointNames are the variables providerEnvironment may set. A child
// never inherits one this runtime does not set: a value the runtime's own
// parent carried would send the provider to another runtime's services.
var runtimeEndpointNames = []string{
	resourceclient.EnvEndpoint, logclient.EnvEndpoint, configclient.EnvEndpoint,
	routerclient.EnvEndpoint, runtimeEndpointVar,
}

// childEnvironment is base, the whole parent environment, with each of set
// replacing a variable of the same name and every runtimeEndpointNames
// variable that set does not name removed. Names compare without case on
// Windows, as its environment does. Every other variable passes through,
// also when set is empty.
func childEnvironment(base, set []string) []string {
	name := func(entry string) string {
		key, _, _ := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" {
			return strings.ToUpper(key)
		}
		return key
	}
	dropped := map[string]bool{}
	for _, n := range runtimeEndpointNames {
		dropped[name(n)] = true
	}
	for _, entry := range set {
		dropped[name(entry)] = true
	}
	out := make([]string, 0, len(base)+len(set))
	for _, entry := range base {
		// Windows keeps per-drive directories as "=C:=C:\..."; they have no
		// name to replace and stay.
		if !strings.HasPrefix(entry, "=") && dropped[name(entry)] {
			continue
		}
		out = append(out, entry)
	}
	return append(out, set...)
}

// providerChildEnvironment is this process's environment as a provider this
// runtime starts receives it (childEnvironment).
func (p *runtimeProviders) providerChildEnvironment() []string {
	p.mu.Lock()
	set := p.childEnv
	p.mu.Unlock()
	return childEnvironment(os.Environ(), set)
}

// setChildEnvironment records the runtime variables every provider this
// runtime starts afterwards receives.
func (p *runtimeProviders) setChildEnvironment(env []string) {
	p.mu.Lock()
	p.childEnv = append([]string(nil), env...)
	p.mu.Unlock()
}
