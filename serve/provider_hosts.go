package main

import (
	"fmt"
	"slices"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// A host declaration is a foreign HTTP engine in the runtime's one directory
// of programs: the fields abstraction.inference/operator@1 HostEntry carries,
// under the facade registry's role host (facade CONTRACT.md FAC-R6). The
// operator adds one through AddHost, a product declares one from its own
// record (INF-H1), and the installation ships the four default local engines
// as declaration files.

// providerHost is a host declaration's engine, as kept on disk.
type providerHost struct {
	Base       string             `json:"base"`
	Kind       string             `json:"kind"`
	Hosted     bool               `json:"hosted"`
	Credential string             `json:"credential,omitempty"`
	Ceiling    *inference.Ceiling `json:"ceiling,omitempty"`
}

func hostOf(h *wire.DeclarationHost) *providerHost {
	if h == nil {
		return nil
	}
	out := &providerHost{Base: h.Base, Kind: h.Kind, Hosted: h.Hosted, Credential: h.Credential}
	if c := h.Ceiling; c != nil {
		out.Ceiling = &inference.Ceiling{TokensPerDay: c.TokensPerDay, MicrosPerDay: c.MicrosPerDay, RequestsPerDay: c.RequestsPerDay,
			ImagesPerDay: c.ImagesPerDay, AudioSecondsPerDay: c.AudioSecondsPerDay, CharactersPerDay: c.CharactersPerDay}
	}
	return out
}

func (h *providerHost) wire() *wire.DeclarationHost {
	if h == nil {
		return nil
	}
	out := &wire.DeclarationHost{Base: h.Base, Kind: h.Kind, Hosted: h.Hosted, Credential: h.Credential}
	if c := h.Ceiling; c != nil {
		out.Ceiling = &wire.DeclarationCeiling{TokensPerDay: c.TokensPerDay, MicrosPerDay: c.MicrosPerDay, RequestsPerDay: c.RequestsPerDay,
			ImagesPerDay: c.ImagesPerDay, AudioSecondsPerDay: c.AudioSecondsPerDay, CharactersPerDay: c.CharactersPerDay}
	}
	return out
}

// hostDeclaration is the declaration a host entry is kept as. profiles are
// its profile:<name> resources, the form a remote declaration already uses.
func hostDeclaration(entry iwire.HostEntry) providerDeclaration {
	d := providerDeclaration{Name: entry.Name, Arguments: []string{}, Contracts: []string{}, Transport: transportHTTP,
		Activation: wire.ActivationAttach.String(), Role: wire.DeclarationRoleHost.String(),
		Host: &providerHost{Base: entry.Base, Kind: entry.Kind, Hosted: entry.Hosted, Credential: entry.Credential}}
	if c := entry.Ceiling; c != nil {
		limit := ceilingOf(c)
		d.Host.Ceiling = &limit
	}
	for _, profile := range entry.Profiles {
		d.Resources = append(d.Resources, "profile:"+profile)
	}
	return d
}

// hostEntry is the HostEntry a host declaration reports, with the profiles
// its wire defaults to when it names none.
func (f providerFile) hostEntry() iwire.HostEntry {
	d := f.Declaration
	h := d.Host
	entry := iwire.HostEntry{Name: d.Name, Hosted: h.Hosted, Kind: h.Kind, Base: h.Base, Credential: h.Credential, DeclaredBy: f.DeclaredBy}
	entry.Profiles = d.resources("profile")
	if len(entry.Profiles) == 0 {
		entry.Profiles = defaultProfiles(h.Hosted, h.Kind)
	}
	if c := h.Ceiling; c != nil {
		entry.Ceiling = ceilingLimit(*c)
	}
	return entry
}

// routerHost is the router host of a host declaration.
func (f providerFile) routerHost() (*router.Host, error) {
	d := f.Declaration
	h := d.Host
	if h == nil {
		return nil, fmt.Errorf("providers: %s declares role host without an engine", d.Name)
	}
	var out *router.Host
	if h.Hosted {
		out = router.NewHosted(d.Name, h.Base, h.Kind, h.Credential)
	} else {
		var err error
		if out, err = localRouterHost(inferenceLocalHost{Kind: h.Kind, Base: h.Base}); err != nil {
			return nil, err
		}
	}
	// A declaration that names no profile keeps the ones its wire serves.
	if profiles := d.resources("profile"); len(profiles) > 0 {
		out.Profiles = profiles
	}
	out.DeclaredBy = f.DeclaredBy
	out.BindingID = f.bindingID()
	return out, nil
}

// productHostFiles is the host declarations the products on this machine make
// from their own records (INF-H1). They are read at each refresh and never
// written: a product that moves its server moves its declaration with it.
func productHostFiles(report func(error)) []providerFile {
	var out []providerFile
	for _, h := range declaredLocalHosts(report) {
		entry := iwire.HostEntry{Name: h.Name, Kind: h.Name, Base: h.Base, Profiles: h.Profiles}
		out = append(out, providerFile{Version: providerFileVersion, Declaration: hostDeclaration(entry),
			DeclaredBy: h.DeclaredBy, Source: declarationSourceProduct})
	}
	return out
}

// hostFiles is every host declaration in name order, disabled ones included.
func (p *runtimeProviders) hostFiles() []providerFile {
	files, _, err := p.read()
	if err != nil {
		p.report(err)
		return nil
	}
	var out []providerFile
	for _, f := range files {
		if f.Declaration.role() == wire.DeclarationRoleHost {
			out = append(out, f)
		}
	}
	return out
}

// hostRouterHosts is the router hosts of every enabled host declaration.
func (p *runtimeProviders) hostRouterHosts() []*router.Host {
	var out []*router.Host
	for _, f := range p.hostFiles() {
		if f.Disabled {
			continue
		}
		h, err := f.routerHost()
		if err != nil {
			p.report(err)
			continue
		}
		out = append(out, h)
	}
	return out
}

// hostStates is the registry reading of every host declaration: the router's
// last survey of the engine, or disabled for one an operator withdrew.
func (p *runtimeProviders) hostStates(files []providerFile) []wire.DeclarationState {
	reading := p.hostReadings()
	out := []wire.DeclarationState{}
	for _, f := range files {
		if f.Declaration.role() != wire.DeclarationRoleHost {
			continue
		}
		state := wire.DeclarationState{Declaration: f.Declaration.wire(), DeclaredBy: f.DeclaredBy, DeclaredUnixMs: f.DeclaredAt,
			Role: wire.DeclarationRoleHost, Described: []wire.ServiceState{}}
		switch s, surveyed := reading[f.Declaration.Name]; {
		case f.Disabled:
			state.Readiness, state.Why = wire.DeclarationReadinessDisabled, "operator"
		case !surveyed:
			state.Readiness, state.Why = wire.DeclarationReadinessUnreachable, "host:not surveyed yet"
			state.Host = &wire.HostReading{Why: "not surveyed yet"}
		case s.Up:
			state.Readiness = wire.DeclarationReadinessReady
			state.Host = &wire.HostReading{Up: true, Why: s.Why}
		default:
			state.Readiness, state.Why = wire.DeclarationReadinessUnreachable, "host:"+s.Why
			state.Host = &wire.HostReading{Why: s.Why}
		}
		out = append(out, state)
	}
	return out
}

// hostReadings is the router's latest state of each host it holds.
func (p *runtimeProviders) hostReadings() map[string]router.HostState {
	out := map[string]router.HostState{}
	if p.routerHostStates == nil {
		return out
	}
	for _, s := range p.routerHostStates() {
		out[s.Host] = s
	}
	return out
}

// routerHostSurveyAge re-reads the hosts for a listing older than this.
const routerHostSurveyAge = 30 * time.Second

// newHostFile is a host declaration a caller's entry becomes.
func newHostFile(entry iwire.HostEntry, declaredBy string) providerFile {
	if len(entry.Profiles) == 0 {
		entry.Profiles = defaultProfiles(entry.Hosted, entry.Kind)
	}
	return providerFile{Version: providerFileVersion, Declaration: hostDeclaration(entry), DeclaredBy: declaredBy,
		DeclaredAt: time.Now().UnixMilli(), Source: declarationSourceOperator}
}

// hostNames is the names every host declaration holds, disabled included.
func (p *runtimeProviders) hostNames() []string {
	var out []string
	for _, f := range p.hostFiles() {
		if !slices.Contains(out, f.Declaration.Name) {
			out = append(out, f.Declaration.Name)
		}
	}
	return out
}
