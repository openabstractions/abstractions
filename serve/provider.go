package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// Declarations: providers/<name>.json in the runtime state directory, served
// as abstraction.facade/registry@1 (research/provider-registry DECISION.md).
// The registry is the one directory of the programs the runtime knows, in
// three roles: a provider it launches on demand or attaches to and binds by
// program path, a host it reaches over HTTP through the router, and another
// runtime reached over mutual TLS. Every declared contract but an inventory
// source is a resolver candidate after the runtime's own services, and a
// remote runtime is a router host.
const (
	providersDir          = "providers"
	providerFileVersion   = 2
	transportNative       = "oa-native@1"
	transportRemote       = "oa-remote@1"
	transportHTTP         = "http@1"
	providerEndpointToken = "{endpoint}"
	maxProviders          = 64
)

var (
	providerName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	providerEndpoint = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	contractName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}/[a-z0-9][a-z0-9_.-]{0,63}@[1-9][0-9]{0,8}$`)
	resourceName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
)

// providerFile is one declaration as kept on disk. Source and Disabled are
// the runtime's reading of where the declaration came from and whether an
// operator withdrew it; ShippedProgram is the bare name a bundled program was
// shipped under before an installation reading resolved Declaration.Program
// to its absolute path beside the tools directory; none of the three is
// serialized, so a file's binding identity does not change with them.
type providerFile struct {
	Version        int                 `json:"version"`
	Generation     string              `json:"generation,omitempty"`
	Declaration    providerDeclaration `json:"declaration"`
	DeclaredBy     string              `json:"declared_by"`
	DeclaredAt     int64               `json:"declared_unix_ms"`
	Source         string              `json:"-"`
	Disabled       bool                `json:"-"`
	ShippedProgram string              `json:"-"`
}

// bindingID includes registration generation and its trusted configuration.
// Existing version-2 records without a generation retain a deterministic legacy
// identity. Every declaration made through add gets a fresh random generation.
func (f providerFile) bindingID() string {
	//unchecked: providerFile is plain int64/string/bool fields, which json.Marshal cannot fail on
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
	Role       string                `json:"role,omitempty"`
	Host       *providerHost         `json:"host,omitempty"`
}

func declarationOf(d wire.Declaration) providerDeclaration {
	out := providerDeclaration{Name: d.Name, Program: d.Program, Arguments: slices.Clone(d.Arguments), Endpoint: d.Endpoint, Transport: d.Transport.String(),
		Contracts: slices.Clone(d.Contracts), Guarantees: slices.Clone(d.Guarantees), Resources: slices.Clone(d.Resources), Models: slices.Clone(d.Models),
		Activation: d.Activation.String(), Host: hostOf(d.Host)}
	if d.Role != 0 {
		out.Role = d.Role.String()
	}
	if r := d.Remote; r != nil {
		out.Remote = &inferenceRemoteTrust{ServerName: r.ServerName, Roots: r.Roots, Certificate: r.Certificate, Key: r.Key, Credential: r.Credential}
	}
	return out
}

// role is what the declaration is. A file that names no role reads its shape:
// oa-remote@1 is a remote runtime, http@1 a host, and anything else a
// provider (facade CONTRACT.md FAC-R6).
func (d providerDeclaration) role() wire.DeclarationRole {
	if d.Role != "" {
		if role, ok := wire.ParseDeclarationRole(d.Role); ok {
			return role
		}
		return 0
	}
	switch d.Transport {
	case transportRemote:
		return wire.DeclarationRoleRemote
	case transportHTTP:
		return wire.DeclarationRoleHost
	}
	return wire.DeclarationRoleProvider
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
		Contracts: nonNil(d.Contracts), Guarantees: slices.Clone(d.Guarantees), Resources: slices.Clone(d.Resources), Models: slices.Clone(d.Models),
		Activation: activation, Role: d.role(), Host: d.Host.wire()}
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

func (d providerDeclaration) host() bool { return d.Transport == transportHTTP }

// validHostDeclaration returns the invalid field of a role host declaration,
// or "". A host carries no OA program, endpoint, contract or guarantee: what
// it is, is its engine (facade CONTRACT.md FAC-R6).
func validHostDeclaration(d providerDeclaration) string {
	switch {
	case d.Program != "" || len(d.Arguments) > 0:
		return "program"
	case d.Endpoint != "":
		return "endpoint"
	case len(d.Contracts) > 0:
		return "contracts"
	case len(d.Guarantees) > 0:
		return "guarantees"
	case len(d.Models) > 0:
		return "models"
	case d.Remote != nil:
		return "remote"
	case d.Activation != wire.ActivationAttach.String():
		return "activation"
	case d.Host == nil:
		return "host"
	}
	h := d.Host
	entry := iwire.HostEntry{Name: d.Name, Hosted: h.Hosted, Kind: h.Kind, Base: h.Base, Credential: h.Credential, Profiles: d.resources("profile")}
	if c := h.Ceiling; c != nil {
		entry.Ceiling = ceilingLimit(*c)
	}
	switch field := validEntry(entry); field {
	case "":
		return ""
	case "name", "profiles":
		return field
	}
	return "host"
}

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
	role := d.role()
	switch {
	case role == 0,
		role == wire.DeclarationRoleRemote != d.remote(),
		role == wire.DeclarationRoleHost != d.host():
		return "role"
	}
	if !distinct(d.Resources, 64, validResource) {
		return "resources"
	}
	if role == wire.DeclarationRoleHost {
		if !providerName.MatchString(d.Name) {
			return "name"
		}
		return validHostDeclaration(d)
	}
	if d.Host != nil {
		return "host"
	}
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
	mu  sync.Mutex
	dir string
	// installation is the directory the installation's declaration files live
	// in, beside the runtime executable; empty when it cannot be named.
	installation string
	// disabledPath records the names an operator withdrew.
	disabledPath string
	// routerHostStates reads the router's latest survey of its hosts; nil
	// before the router exists.
	routerHostStates func() []router.HostState
	// products reads the host declarations the products on this machine make
	// from their own records; nil reads none. A product record is read at
	// most once every productDeclarationAge, because a probe runs a product's
	// own status command and the registry is read on every call.
	products    func() []providerFile
	productsMu  sync.Mutex
	productAt   time.Time
	productHas  bool
	product     []providerFile
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

// openProviders reads the declarations of state. declared selects whether the
// products on this machine declare hosts of their own (INF-H1).
func openProviders(state string, declared bool, report func(error)) (*runtimeProviders, error) {
	principal, err := currentPrincipal()
	if err != nil {
		return nil, err
	}
	p := &runtimeProviders{dir: filepath.Join(state, providersDir), disabledPath: filepath.Join(state, disabledDeclarationsFile),
		report: report, principal: principal,
		supervisors: map[string]*providerSupervisor{}, timing: defaultProviderTiming, changes: make(chan struct{}), mediated: map[string]mediatedProvider{}}
	if declared {
		p.products = func() []providerFile { return productHostFiles(report) }
	}
	installation, err := installationDeclarationPath()
	if err != nil {
		report(fmt.Errorf("providers: the installation's declarations cannot be named: %w", err))
	}
	p.installation = installation
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return nil, err
	}
	files, _, err := p.read()
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		// A host declaration is no process: the router reads it and no
		// supervisor probes it.
		if f.Declaration.role() == wire.DeclarationRoleHost || f.Disabled {
			continue
		}
		p.supervisors[f.Declaration.Name] = newProviderSupervisor(p, f)
	}
	return p, nil
}

// productDeclarationAge bounds how often the product probes run.
const productDeclarationAge = 30 * time.Second

// productDeclarations is the host declarations the products on this machine
// make, re-probed when the last reading is older than productDeclarationAge.
func (p *runtimeProviders) productDeclarations() []providerFile {
	if p.products == nil {
		return nil
	}
	p.productsMu.Lock()
	defer p.productsMu.Unlock()
	if p.productHas && time.Since(p.productAt) < productDeclarationAge {
		return slices.Clone(p.product)
	}
	p.product, p.productAt, p.productHas = p.products(), time.Now(), true
	return slices.Clone(p.product)
}

// forgetProducts drops the cached product reading, so the next read probes.
func (p *runtimeProviders) forgetProducts() {
	p.productsMu.Lock()
	p.productHas = false
	p.productsMu.Unlock()
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

// readDeclarationDir returns the valid version 2 declarations of one
// directory in file-name order, writing each file's name and bytes into
// digest when it is given. An invalid file is reported and left out. installed
// selects the installation's rule for the program field: a bare file name
// resolves against the tools directory beside dir (provider_installation.go
// resolveBundledProgram) before the usual shape validation runs; an
// operator's own directory (installed false) keeps the absolute-path rule.
func (p *runtimeProviders) readDeclarationDir(dir string, digest io.Writer, installed bool) ([]providerFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Only an absent declaration directory is an empty registry. A
		// vanished child file or a path replaced by a file is a read failure.
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
				return nil, nil
			}
		}
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var files []providerFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if digest != nil {
			//unchecked: hash.Hash.Write never returns an error
			fmt.Fprintf(digest, "%s\x00%d\x00", e.Name(), len(raw))
			//unchecked: hash.Hash.Write never returns an error
			digest.Write(raw)
		}
		var f providerFile
		if err := json.Unmarshal(raw, &f); err != nil || f.Version != providerFileVersion {
			p.report(fmt.Errorf("providers: %s is not a version %d declaration", e.Name(), providerFileVersion))
			continue
		}
		if installed {
			if resolved, bundled, err := resolveBundledProgram(dir, f.Declaration); err != nil {
				p.report(fmt.Errorf("providers: %s: invalid program: %w", e.Name(), err))
				continue
			} else if bundled {
				f.ShippedProgram, f.Declaration.Program = f.Declaration.Program, resolved
			}
		}
		if field := validProviderDeclaration(f.Declaration); field != "" || f.Declaration.Name+".json" != e.Name() {
			p.report(fmt.Errorf("providers: %s: invalid %s", e.Name(), field))
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

// read returns the effective declarations in name order and their revision,
// the digest of every operator and installation declaration file's name and
// bytes and of the disabled names. An operator's declaration shadows a
// product's, which shadows the installation's; a disabled name leaves the
// declaration listed and inert.
func (p *runtimeProviders) read() ([]providerFile, string, error) {
	digest := sha256.New()
	operator, err := p.readDeclarationDir(p.dir, digest, false)
	if err != nil {
		return nil, "", err
	}
	for i := range operator {
		operator[i].Source = declarationSourceOperator
	}
	products := p.productDeclarations()
	// Separate the declaration sources and hash installation bytes before
	// shadowing, so an upgrade invalidates edits based on an older reading.
	//unchecked: hash.Hash.Write never returns an error
	digest.Write([]byte("\x00" + declarationSourceInstallation + "\x00"))
	installed := p.installedDeclarations(digest)
	disabled, raw := p.disabledDeclarations()
	//unchecked: hash.Hash.Write never returns an error
	digest.Write(raw)
	files := shadow(shadow(operator, products), installed)
	for i := range files {
		if files[i].Source != declarationSourceOperator && disabled[files[i].Declaration.Name] {
			files[i].Disabled = true
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Declaration.Name < files[j].Declaration.Name })
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
// ready declarations, plus an on-demand declaration still starting its first
// launch: the router then has a host to pick, and the provider's Admit hook
// (FAC-R8) waits for its readiness rather than never routing there at all.
// Models is its explicit trusted allowlist; the provider name remains the
// router host and provenance identity.
func (p *runtimeProviders) nativeInferenceHosts() []*router.Host {
	var out []*router.Host
	for _, s := range p.sorted() {
		d := s.file.Declaration
		models := d.Models
		ready := s.state().Readiness == wire.DeclarationReadinessReady
		onDemand := d.Activation == wire.ActivationOnDemand.String()
		if d.remote() || !slices.Contains(d.Contracts, inference.Contract) || len(models) == 0 || (!ready && !onDemand) {
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

// list is every declaration with its reading, in name order: a supervised
// provider or remote runtime from its supervisor, a host from the router's
// survey, and a declaration an operator withdrew as disabled.
func (p *runtimeProviders) list() ([]wire.DeclarationState, string, error) {
	p.mu.Lock()
	files, revision, err := p.read()
	p.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	hosts := map[string]wire.DeclarationState{}
	for _, state := range p.hostStates(files) {
		hosts[state.Declaration.Name] = state
	}
	supervised := map[string]wire.DeclarationState{}
	for _, s := range p.sorted() {
		supervised[s.file.Declaration.Name] = s.state()
	}
	out := []wire.DeclarationState{}
	for _, f := range files {
		name := f.Declaration.Name
		switch state, known := hosts[name]; {
		case known:
			out = append(out, state)
		default:
			state, known := supervised[name]
			if !known {
				state = wire.DeclarationState{Declaration: f.Declaration.wire(), DeclaredBy: f.DeclaredBy, DeclaredUnixMs: f.DeclaredAt,
					Role: f.Declaration.role(), Readiness: wire.DeclarationReadinessDisabled, Why: "operator", Described: []wire.ServiceState{}}
			}
			out = append(out, state)
		}
	}
	return out, revision, nil
}

// add writes a declaration and supervises it.
func (p *runtimeProviders) add(expected string, f providerFile) wire.DeclarationChange {
	p.mu.Lock()
	change := p.addLocked(expected, f)
	p.mu.Unlock()
	if change.Outcome == wire.DeclarationEditOutcomeApplied {
		p.notify()
		if (f.Declaration.remote() || f.Declaration.host()) && p.remotesChanged != nil {
			p.remotesChanged()
		}
	}
	return change
}

func (p *runtimeProviders) addLocked(expected string, f providerFile) wire.DeclarationChange {
	files, revision, err := p.read()
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
	// A declaration the installation or a product made is shadowed by the
	// operator's own, not conflicted with; an enabled one of either is the
	// name already taken.
	if i := slices.IndexFunc(files, func(e providerFile) bool { return e.Declaration.Name == f.Declaration.Name }); i >= 0 && !files[i].Disabled {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "name"}
	}
	if f.Declaration.Endpoint != "" {
		for _, s := range p.supervisors {
			if s.file.Declaration.Endpoint == f.Declaration.Endpoint {
				return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "endpoint"}
			}
		}
	}
	if len(files) >= maxProviders {
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
	// Declaring a name an operator withdrew from the installation or a
	// product enables it again; the operator's own file now shadows it.
	if err := p.enable(f.Declaration.Name); err != nil {
		p.report(err)
	}
	if f.Declaration.role() != wire.DeclarationRoleHost {
		s := newProviderSupervisor(p, f)
		p.supervisors[f.Declaration.Name] = s
		if p.serving && !p.closed {
			s.start()
		}
	}
	_, revision, err = p.read()
	if err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: revision}
}

// remove withdraws a declaration: it deletes the operator's file, withdraws
// its candidates and ends its child. A declaration the installation or a
// product made is disabled by name instead, so no reinstall and no later
// probe brings back what the operator removed (facade CONTRACT.md FAC-R7).
func (p *runtimeProviders) remove(expected, name string) wire.DeclarationChange {
	p.mu.Lock()
	files, revision, err := p.read()
	if err != nil {
		p.mu.Unlock()
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	if expected != revision {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "revision"}
	}
	i := slices.IndexFunc(files, func(f providerFile) bool { return f.Declaration.Name == name })
	if i < 0 || files[i].Disabled {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnknown, Revision: revision}
	}
	declaration, reason := files[i].Declaration, ""
	if files[i].Source == declarationSourceOperator {
		if err := os.Remove(filepath.Join(p.dir, name+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			p.mu.Unlock()
			p.report(err)
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
		}
	}
	// The name is disabled whenever anything other than the operator's own
	// file still declares it, so removing it once removes it for good.
	if p.declaredElsewhere(name) {
		if err := p.disable(name); err != nil {
			p.mu.Unlock()
			p.report(err)
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
		}
		reason = "disabled"
	}
	s := p.supervisors[name]
	delete(p.supervisors, name)
	_, revision, err = p.read()
	p.mu.Unlock()
	p.notify()
	if (declaration.remote() || declaration.host()) && p.remotesChanged != nil {
		p.remotesChanged()
	}
	if s != nil {
		s.halt()
	}
	if err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: revision, Reason: reason}
}

// declaredElsewhere reports whether a product or the installation declares
// name, so the operator's removal must be recorded rather than performed.
func (p *runtimeProviders) declaredElsewhere(name string) bool {
	p.forgetProducts()
	for _, f := range append(p.productDeclarations(), p.installedDeclarations(nil)...) {
		if f.Declaration.Name == name {
			return true
		}
	}
	return false
}

// providerEndpointPath is the platform endpoint of a declared endpoint name.
func providerEndpointPath(name string) string { return listen.Endpoint(name) }
