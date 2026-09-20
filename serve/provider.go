package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
)

// Provider declarations: providers/<name>.json in the runtime state directory,
// served as abstraction.facade/registry@1 (research/provider-registry
// DECISION.md). Each names a process outside the runtime, which the runtime
// launches on demand or attaches to and binds by program path, or another
// runtime reached over mutual TLS. Every declared contract but an inventory
// source is a resolver candidate after the runtime's own services, and a
// remote runtime is a router host.
const (
	providersDir          = "providers"
	providerFileVersion   = 2
	transportNative       = "oa-native@1"
	transportRemote       = "oa-remote@1"
	providerEndpointToken = "{endpoint}"
	maxProviders          = 64
)

var (
	providerName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	providerEndpoint = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	contractName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}/[a-z0-9][a-z0-9_.-]{0,63}@[1-9][0-9]{0,8}$`)
	resourceName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
)

// providerFile is one declaration as kept on disk.
type providerFile struct {
	Version     int                 `json:"version"`
	Generation  string              `json:"generation,omitempty"`
	Declaration providerDeclaration `json:"declaration"`
	DeclaredBy  string              `json:"declared_by"`
	DeclaredAt  int64               `json:"declared_unix_ms"`
}

// bindingID includes registration generation and its trusted configuration.
// Existing version-2 records without a generation retain a deterministic legacy
// identity. Every declaration made through add gets a fresh random generation.
func (f providerFile) bindingID() string {
	raw, _ := json.Marshal(f)
	sum := sha256.Sum256(raw)
	return "provider-binding-v1:" + hex.EncodeToString(sum[:])
}

type providerDeclaration struct {
	Name       string                `json:"name"`
	Program    string                `json:"program"`
	Arguments  []string              `json:"arguments"`
	Endpoint   string                `json:"endpoint"`
	Transport  string                `json:"transport"`
	Contracts  []string              `json:"contracts"`
	Guarantees []string              `json:"guarantees,omitempty"`
	Resources  []string              `json:"resources,omitempty"`
	Models     []string              `json:"models,omitempty"`
	Activation string                `json:"activation"`
	Remote     *inferenceRemoteTrust `json:"remote,omitempty"`
}

func declarationOf(d wire.Declaration) providerDeclaration {
	out := providerDeclaration{Name: d.Name, Program: d.Program, Arguments: slices.Clone(d.Arguments), Endpoint: d.Endpoint, Transport: d.Transport.String(),
		Contracts: slices.Clone(d.Contracts), Guarantees: slices.Clone(d.Guarantees), Resources: slices.Clone(d.Resources), Models: slices.Clone(d.Models), Activation: d.Activation.String()}
	if r := d.Remote; r != nil {
		out.Remote = &inferenceRemoteTrust{ServerName: r.ServerName, Roots: r.Roots, Certificate: r.Certificate, Key: r.Key, Credential: r.Credential}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}

func (d providerDeclaration) wire() wire.Declaration {
	activation, _ := wire.ParseActivation(d.Activation)
	transport, _ := wire.ParseDeclarationTransport(d.Transport)
	out := wire.Declaration{Name: d.Name, Program: d.Program, Arguments: nonNil(d.Arguments), Endpoint: d.Endpoint, Transport: transport,
		Contracts: nonNil(d.Contracts), Guarantees: slices.Clone(d.Guarantees), Resources: slices.Clone(d.Resources), Models: slices.Clone(d.Models), Activation: activation}
	if r := d.Remote; r != nil {
		out.Remote = &wire.RemoteTrust{ServerName: r.ServerName, Roots: r.Roots, Certificate: r.Certificate, Key: r.Key, Credential: r.Credential}
	}
	return out
}

// resources returns the names of one resource kind, in declaration order.
func (d providerDeclaration) resources(kind string) []string {
	var out []string
	for _, r := range d.Resources {
		if k, name, ok := strings.Cut(r, ":"); ok && k == kind {
			out = append(out, name)
		}
	}
	return out
}

func (d providerDeclaration) remote() bool { return d.Transport == transportRemote }

func validProviderModel(name string) bool {
	if name == "" || len(name) > 256 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// validProviderDeclaration returns the invalid field of a declaration, or "".
func validProviderDeclaration(d providerDeclaration) string {
	distinct := func(values []string, max int, valid func(string) bool) bool {
		if len(values) > max {
			return false
		}
		seen := map[string]bool{}
		for _, v := range values {
			if seen[v] || !valid(v) {
				return false
			}
			seen[v] = true
		}
		return true
	}
	text := func(v string, max int) bool { return v != "" && len(v) <= max && !strings.ContainsAny(v, "\x00\r\n") }
	switch {
	case !providerName.MatchString(d.Name):
		return "name"
	case d.remote() && d.Program != "",
		!d.remote() && (!text(d.Program, 4096) || !filepath.IsAbs(d.Program) || filepath.Clean(d.Program) != d.Program):
		return "program"
	case len(d.Arguments) > 64 || slices.ContainsFunc(d.Arguments, func(a string) bool { return !text(a, 4096) }) || d.remote() && len(d.Arguments) > 0:
		return "arguments"
	case d.Transport != transportNative && d.Transport != transportRemote:
		return "transport"
	case !d.remote() && !providerEndpoint.MatchString(d.Endpoint):
		return "endpoint"
	case d.remote():
		if _, err := remoteAddress(d.Endpoint); err != nil {
			return "endpoint"
		}
	}
	switch {
	case len(d.Contracts) == 0 || !distinct(d.Contracts, 16, contractName.MatchString):
		return "contracts"
	case !distinct(d.Guarantees, 16, func(g string) bool { return text(g, 256) }):
		return "guarantees"
	case !distinct(d.Resources, 64, validResource):
		return "resources"
	case !distinct(d.Models, 64, validProviderModel) || d.remote() && len(d.Models) > 0:
		return "models"
	case d.remote() != (d.Activation == wire.ActivationRemote.String()),
		d.Activation != wire.ActivationOnDemand.String() && d.Activation != wire.ActivationAttach.String() && d.Activation != wire.ActivationRemote.String():
		return "activation"
	case d.remote() != (d.Remote != nil):
		return "remote"
	case d.Remote != nil && (d.Remote.ServerName == "" || len(d.Remote.ServerName) > 253 || strings.ContainsAny(d.Remote.ServerName, "/:@ ") ||
		d.Remote.Credential != "" && !credentialName.MatchString(d.Remote.Credential)):
		return "remote"
	}
	return validProviderStores(d)
}

// validResource reports whether r is <kind>:<name> of a declared resource kind.
func validResource(r string) bool {
	kind, name, ok := strings.Cut(r, ":")
	if !ok || !slices.Contains(wire.DeclarationResourceKinds, kind) {
		return false
	}
	if kind == "profile" {
		return validProfiles([]string{name})
	}
	return resourceName.MatchString(name)
}

// runtimeProviders reads the declarations, supervises each provider and
// offers its contracts as resolver candidates. It implements the facade
// runtime's DeclaredProviders.
type runtimeProviders struct {
	mu          sync.Mutex
	dir         string
	report      func(error)
	principal   identity.User
	supervisors map[string]*providerSupervisor
	watchers    []func()
	serving     bool
	closed      bool
	// changes is closed and replaced whenever a declaration or reading changes.
	changes chan struct{}
	// timing bounds supervision; tests shorten it.
	timing providerTiming
	// decide reports whether program holds a permit for action on resource;
	// nil permits nothing (provider_inventory.go).
	decide func(ctx context.Context, program, action, resource string) bool
	// remotesChanged is called after a remote declaration is added or removed,
	// outside p.mu, so the router reads the remote hosts again.
	remotesChanged func()
	mediationMu    sync.Mutex
	// mediated maps a supported declaration generation to its OA-owned endpoint.
	mediated map[string]mediatedProvider
}

type mediatedProvider struct {
	Endpoint string
	Ready    bool
}

func openProviders(state string, report func(error)) (*runtimeProviders, error) {
	principal, err := currentPrincipal()
	if err != nil {
		return nil, err
	}
	p := &runtimeProviders{dir: filepath.Join(state, providersDir), report: report, principal: principal,
		supervisors: map[string]*providerSupervisor{}, timing: defaultProviderTiming, changes: make(chan struct{}), mediated: map[string]mediatedProvider{}}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return nil, err
	}
	files, _, err := p.read()
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		p.supervisors[f.Declaration.Name] = newProviderSupervisor(p, f)
	}
	return p, nil
}

func currentPrincipal() (identity.User, error) {
	if runtime.GOOS == "windows" {
		u, err := user.Current()
		if err != nil || u.Uid == "" {
			return identity.User{}, fmt.Errorf("providers: runtime account unavailable: %v", err)
		}
		return identity.User{Kind: "windows", SID: u.Uid}, nil
	}
	return identity.User{Kind: "posix", UID: os.Getuid()}, nil
}

// read returns the valid declarations in name order and their revision, the
// digest of every declaration file's name and bytes. An invalid file is
// reported and left out.
func (p *runtimeProviders) read() ([]providerFile, string, error) {
	entries, err := os.ReadDir(p.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	digest := sha256.New()
	var files []providerFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p.dir, e.Name()))
		if err != nil {
			return nil, "", err
		}
		fmt.Fprintf(digest, "%s\x00%d\x00", e.Name(), len(raw))
		digest.Write(raw)
		var f providerFile
		if err := json.Unmarshal(raw, &f); err != nil || f.Version != providerFileVersion {
			p.report(fmt.Errorf("providers: %s is not a version %d declaration", e.Name(), providerFileVersion))
			continue
		}
		if field := validProviderDeclaration(f.Declaration); field != "" || f.Declaration.Name+".json" != e.Name() {
			p.report(fmt.Errorf("providers: %s: invalid %s", e.Name(), field))
			continue
		}
		files = append(files, f)
	}
	return files, "providers-v2:" + hex.EncodeToString(digest.Sum(nil)[:12]), nil
}

// Candidates offers only capability profiles the runtime can mediate. Native
// chat declarations resolve to this runtime's inference endpoint. Arbitrary
// declared contracts remain visible through registry@1 and expose no raw
// provider endpoint to applications.
func (p *runtimeProviders) Candidates() []resolution.Candidate {
	var out []resolution.Candidate
	for _, s := range p.sorted() {
		d := s.file.Declaration
		if d.remote() || !slices.Contains(d.Contracts, inference.Contract) || len(d.Models) == 0 {
			continue
		}
		mediation, ok := p.mediatedEndpoint(s.file.bindingID())
		if !ok {
			continue
		}
		ready := mediation.Ready && s.state().Readiness == wire.DeclarationReadinessReady
		guarantees := nonNil(d.Guarantees)
		if !slices.Contains(guarantees, inference.GuaranteeLocalOnly) {
			guarantees = append(guarantees, inference.GuaranteeLocalOnly)
		}
		out = append(out, resolution.Candidate{Ready: ready, Activate: s.activate, Reference: wire.ServiceReference{
			Provider: d.Name, Capability: "abstraction.inference", Contract: inference.Contract, Scope: wire.ScopeLocal, Transport: resolution.LocalTransport,
			Endpoint: mediation.Endpoint, Guarantees: guarantees}})
	}
	return out
}

func (p *runtimeProviders) mediatedEndpoint(bindingID string) (mediatedProvider, bool) {
	p.mediationMu.Lock()
	defer p.mediationMu.Unlock()
	endpoint, ok := p.mediated[bindingID]
	return endpoint, ok
}

func (p *runtimeProviders) setMediated(endpoints map[string]mediatedProvider) {
	p.mediationMu.Lock()
	p.mediated = endpoints
	p.mediationMu.Unlock()
}

// nativeInferenceHosts builds the supported local chat mediation profile from
// ready declarations. Models is its explicit trusted allowlist; the provider
// name remains the router host and provenance identity.
func (p *runtimeProviders) nativeInferenceHosts() []*router.Host {
	var out []*router.Host
	for _, s := range p.sorted() {
		d := s.file.Declaration
		models := d.Models
		if d.remote() || !slices.Contains(d.Contracts, inference.Contract) || len(models) == 0 || s.state().Readiness != wire.DeclarationReadinessReady {
			continue
		}
		h, err := router.NewNative(d.Name, s.endpoint, listen.ServerExpectation{Principal: p.principal, Program: d.Program}, models, d.resources("profile"))
		if err != nil {
			p.report(fmt.Errorf("providers: %s mediation: %w", d.Name, err))
			continue
		}
		h.BindingID, h.DeclaredBy = s.file.bindingID(), s.file.DeclaredBy
		out = append(out, h)
	}
	return out
}

// remoteHosts is a router host for each remote declaration whose trust files
// load; one that does not is reported and left out.
func (p *runtimeProviders) remoteHosts() []*router.Host {
	var out []*router.Host
	for _, s := range p.sorted() {
		d := s.file.Declaration
		if !d.remote() {
			continue
		}
		h, err := remoteRouterHost(d)
		if err != nil {
			p.report(err)
			continue
		}
		h.DeclaredBy, h.Profiles = s.file.DeclaredBy, d.resources("profile")
		h.BindingID = s.file.bindingID()
		out = append(out, h)
	}
	return out
}

func (p *runtimeProviders) sorted() []*providerSupervisor {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*providerSupervisor, 0, len(p.supervisors))
	for _, s := range p.supervisors {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].file.Declaration.Name < out[j].file.Declaration.Name })
	return out
}

func (p *runtimeProviders) Watch(changed func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.watchers = append(p.watchers, changed)
}

// notify tells every watcher and observer the declarations or their readings
// changed. It never runs under p.mu.
func (p *runtimeProviders) notify() {
	p.mu.Lock()
	watchers := slices.Clone(p.watchers)
	close(p.changes)
	p.changes = make(chan struct{})
	p.mu.Unlock()
	for _, w := range watchers {
		w()
	}
}

// Serve supervises every declaration until ctx ends.
func (p *runtimeProviders) Serve(ctx context.Context) error {
	p.mu.Lock()
	if p.serving || p.closed {
		p.mu.Unlock()
		return errors.New("providers: already served")
	}
	p.serving = true
	for _, s := range p.supervisors {
		s.start()
	}
	p.mu.Unlock()
	<-ctx.Done()
	return p.Close()
}

// Close stops every supervisor and the children it launched.
func (p *runtimeProviders) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	supervisors := make([]*providerSupervisor, 0, len(p.supervisors))
	for _, s := range p.supervisors {
		supervisors = append(supervisors, s)
	}
	p.mu.Unlock()
	for _, s := range supervisors {
		s.halt()
	}
	return nil
}

// lookup returns the supervisor of name.
func (p *runtimeProviders) lookup(name string) *providerSupervisor {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.supervisors[name]
}

// list is every declaration with its reading, in name order.
func (p *runtimeProviders) list() []wire.DeclarationState {
	out := []wire.DeclarationState{}
	for _, s := range p.sorted() {
		out = append(out, s.state())
	}
	return out
}

// add writes a declaration and supervises it.
func (p *runtimeProviders) add(expected string, f providerFile) wire.DeclarationChange {
	p.mu.Lock()
	change := p.addLocked(expected, f)
	p.mu.Unlock()
	if change.Outcome == wire.DeclarationEditOutcomeApplied {
		p.notify()
		if f.Declaration.remote() && p.remotesChanged != nil {
			p.remotesChanged()
		}
	}
	return change
}

func (p *runtimeProviders) addLocked(expected string, f providerFile) wire.DeclarationChange {
	_, revision, err := p.read()
	if err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	if expected != revision {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "revision"}
	}
	if _, exists := p.supervisors[f.Declaration.Name]; exists {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "name"}
	}
	if _, err := os.Stat(filepath.Join(p.dir, f.Declaration.Name+".json")); err == nil {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "name"}
	}
	for _, s := range p.supervisors {
		if s.file.Declaration.Endpoint == f.Declaration.Endpoint {
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "endpoint"}
		}
	}
	if len(p.supervisors) >= maxProviders {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "declarations"}
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	f.Generation = hex.EncodeToString(generation[:])
	if err := writeAtomically(filepath.Join(p.dir, f.Declaration.Name+".json"), f); err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	s := newProviderSupervisor(p, f)
	p.supervisors[f.Declaration.Name] = s
	if p.serving && !p.closed {
		s.start()
	}
	_, revision, err = p.read()
	if err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: revision}
}

// remove deletes a declaration, withdraws its candidates and ends its child.
func (p *runtimeProviders) remove(expected, name string) wire.DeclarationChange {
	p.mu.Lock()
	_, revision, err := p.read()
	if err != nil {
		p.mu.Unlock()
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	if expected != revision {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "revision"}
	}
	s, exists := p.supervisors[name]
	if !exists {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnknown, Revision: revision}
	}
	if err := os.Remove(filepath.Join(p.dir, name+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		p.mu.Unlock()
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	delete(p.supervisors, name)
	_, revision, err = p.read()
	p.mu.Unlock()
	p.notify()
	if s.file.Declaration.remote() && p.remotesChanged != nil {
		p.remotesChanged()
	}
	s.halt()
	if err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: revision}
}

// providerEndpointPath is the platform endpoint of a declared endpoint name.
func providerEndpointPath(name string) string { return listen.Endpoint(name) }
