package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	watch "github.com/openabstractions/abstraction-watch/go"
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
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// Declarations: providers/<name>.json in the runtime state directory, served
// through abstraction.facade/registry@1 and registry@2 (research/provider-registry DECISION.md).
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
// ProductPresence is ephemeral observation provenance, also excluded from disk
// and binding identity; it grants no registry or capability authority.
type providerFile struct {
	Version         int                         `json:"version"`
	Generation      string                      `json:"generation,omitempty"`
	Declaration     providerDeclaration         `json:"declaration"`
	DeclaredBy      string                      `json:"declared_by"`
	DeclaredAt      int64                       `json:"declared_unix_ms"`
	Source          string                      `json:"-"`
	Disabled        bool                        `json:"-"`
	ShippedProgram  string                      `json:"-"`
	ProductPresence string                      `json:"-"` // ephemeral observer provenance; never registration authority
	ProductProgram  string                      `json:"-"` // native program already verified by modelobserver; no capability authority
	ProviderFacet   *registration.ProviderFacet `json:"-"` // immutable projection from the canonical registration spec
	RegistrationKey string                      `json:"-"`
	// disabledWhy is why the registration reads disabled (facade FAC-R7):
	// program:missing, program:changed or approval:pending; empty for a
	// person's or a recorded disabled name.
	disabledWhy string
	// waiting are the modality:model pairs a changed registration of this
	// served declaration adds, which wait for the person's Keep.
	waiting []string
	// serving is the declaration that serves while this one, a changed
	// registration, waits for the person's Keep; nil serves this one as
	// listed (servingView).
	serving  *providerFile
	observed []byte `json:"-"`
	// runtimeAllowlist marks the runtime's own reconcile of the model host's
	// allowlist (facade REG-CFG2); the registry carries a Keep only for it.
	// No caller-supplied declaration can set it.
	runtimeAllowlist bool `json:"-"`
}

// servingView is files as the runtime serves them: a changed registration
// waiting for Keep serves the declaration that served before it, unchanged.
// Listings and edits read files as declared.
func servingView(files []providerFile) []providerFile {
	out := make([]providerFile, len(files))
	for i, f := range files {
		if f.serving != nil {
			out[i] = *f.serving
			continue
		}
		out[i] = f
	}
	return out
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
	Name                string                `json:"name"`
	Program             string                `json:"program"`
	Arguments           []string              `json:"arguments"`
	Endpoint            string                `json:"endpoint"`
	Transport           string                `json:"transport"`
	Contracts           []string              `json:"contracts"`
	Guarantees          []string              `json:"guarantees,omitempty"`
	Resources           []string              `json:"resources,omitempty"`
	Models              []string              `json:"models,omitempty"`
	SupportedExtensions []providerExtension   `json:"supported_extensions,omitempty"`
	Activation          string                `json:"activation"`
	Remote              *inferenceRemoteTrust `json:"remote,omitempty"`
	Role                string                `json:"role,omitempty"`
	Host                *providerHost         `json:"host,omitempty"`
}

type providerExtension struct {
	Profile string `json:"profile"`
	Key     string `json:"key"`
}

func extensionsOf(in []wire.DeclarationExtension) []providerExtension {
	out := make([]providerExtension, len(in))
	for i, entry := range in {
		out[i] = providerExtension{Profile: entry.Profile, Key: entry.Key}
	}
	return out
}

func (d providerDeclaration) wireExtensions() []wire.DeclarationExtension {
	out := make([]wire.DeclarationExtension, len(d.SupportedExtensions))
	for i, entry := range d.SupportedExtensions {
		out[i] = wire.DeclarationExtension{Profile: entry.Profile, Key: entry.Key}
	}
	return out
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

// mediatedInferenceProfiles are the declared profiles this runtime can route
// through the corresponding public native inference contract. A profile name
// alone must not silently advertise an undeclared service.
func (d providerDeclaration) mediatedInferenceProfiles() []string {
	var profiles []string
	declared := d.resources("profile")
	if len(declared) == 0 && slices.Contains(d.Contracts, inference.Contract) {
		// Legacy chat-only native declarations predate explicit profile resources.
		return []string{router.ProfileChat}
	}
	for _, profile := range declared {
		if contract := profileContracts[profile]; contract != "" && slices.Contains(d.Contracts, contract) {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

// profileContracts is the public native inference contract of each profile
// the runtime mediates.
var profileContracts = map[string]string{
	router.ProfileChat:          inference.Contract,
	router.ProfileEmbed:         inference.EmbedContract,
	router.ProfileTranscription: inference.TranscriptionContract,
	router.ProfileSpeech:        inference.SpeechContract,
	router.ProfileImage:         inference.ImageContract,
	router.ProfileLive:          inference.LiveContract,
	// A video provider is reached by the runtime's job executor alone;
	// applications submit video as a job (inference CONTRACT.md INF-G5).
	router.ProfileVideo: inference.VideoContract,
}

// jobOnlyProfiles are mediated profiles the runtime offers applications no
// endpoint for: the job executor is their one caller.
var jobOnlyProfiles = map[string]bool{router.ProfileVideo: true}

// validScopedModels reports whether no model entry is scoped to a profile
// the declaration lists without serving it. Entries are read against the
// mediated profiles, the implicit chat profile of a legacy chat-only
// declaration included (router.DeclaredEntry, ROUTE-D9); an entry scoped to a
// listed profile whose contract is missing would read as a model named
// "<profile>:<model>" for every served profile, and is refused.
func (d providerDeclaration) validScopedModels() bool {
	listed := d.resources("profile")
	for _, entry := range d.Models {
		if _, profile := router.DeclaredEntry(entry, listed); profile != "" && !slices.Contains(d.mediatedInferenceProfiles(), profile) {
			return false
		}
	}
	return true
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
	case len(d.SupportedExtensions) > 0:
		return "supported_extensions"
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
	case !distinct(d.Models, 64, validProviderModel) || d.remote() && len(d.Models) > 0 || !d.validScopedModels():
		return "models"
	case !validProviderExtensions(d):
		return "supported_extensions"
	case d.remote() != (d.Activation == wire.ActivationRemote.String()),
		d.Activation != wire.ActivationOnDemand.String() && d.Activation != wire.ActivationAttach.String() && d.Activation != wire.ActivationRemote.String():
		return "activation"
	case d.remote() != (d.Remote != nil):
		return "remote"
	case d.Remote != nil && (d.Remote.ServerName == "" || len(d.Remote.ServerName) > 253 || strings.ContainsAny(d.Remote.ServerName, "/:@ ") ||
		d.Remote.Credential != "" && !validCredentialName(d.Remote.Credential)):
		return "remote"
	}
	return validProviderStores(d)
}

func validProviderExtensions(d providerDeclaration) bool {
	if len(d.SupportedExtensions) > 64 {
		return false
	}
	profiles := d.mediatedInferenceProfiles()
	seen := make(map[providerExtension]bool, len(d.SupportedExtensions))
	for _, entry := range d.SupportedExtensions {
		owner, name, ok := strings.Cut(entry.Key, "/")
		if !slices.Contains(profiles, entry.Profile) || !ok || owner == "" || name == "" || len(entry.Key) > 128 ||
			!utf8.ValidString(entry.Key) || strings.ContainsFunc(entry.Key, unicode.IsControl) ||
			strings.HasPrefix(entry.Key, inference.ReservedExtensionPrefix) || seen[entry] {
			return false
		}
		seen[entry] = true
	}
	return true
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
	// pictureStoreRead is set once the inventory has read OA's picture
	// store; the PyTorch set table applies to pytorchd hosts from then on
	// (provider_pytorch_sets.go).
	pictureStoreRead              atomic.Bool
	endpointFor                   func(string) (string, error)
	registrationDirectory         func() *applicationDirectory
	readyRegistrationDirectory    atomic.Pointer[applicationDirectory]
	shippedImageUpdated           func()
	shippedImagesUnresolved       func(context.Context, []shippedImageReviewCandidate)
	programReviewQuestion         func(key, revision string) string
	programReviewUnavailable      func(key, revision string) bool
	programReviewProviderQuestion func(name string) string
	// programReviewNeed raises the program review of registration key again
	// at a need for its model, when the question asked expired or none was.
	programReviewNeed   func(key string)
	observeShippedImage func(context.Context, string, *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error)
	// adoptMu serializes the startup adoption; adopted records that it
	// completed, so the runtime adopts before reporting ready and Serve
	// does not adopt a second time.
	adoptMu sync.Mutex
	adopted bool
	mu      sync.Mutex
	dir     string
	// installation is the directory the installation's declaration files live
	// in, beside the runtime executable; empty when it cannot be named.
	installation string
	// disabledPath records the names an operator withdrew.
	disabledPath string
	// removedPath records the models a person removed from the model host
	// (models_discovered.go), which the reconciler leaves out.
	removedPath string
	// routerHostStates reads the router's latest survey of its hosts; nil
	// before the router exists.
	routerHostStates func() []router.HostState
	// routerHostSnapshot includes the survey time for product promotion reuse.
	routerHostSnapshot func() ([]router.HostState, time.Time)
	// conversationContext answers the context one conversation gets for a
	// curated family the model host serves; nil reports none.
	conversationContext conversationContext
	// hostAliases is published once before watchers start. The native
	// inference adapter owns discovery/readiness; this directory owns binding.
	hostAliases func(context.Context, string, string, string) ([]string, error)
	// products reads the host declarations the products on this machine make
	// from their own records; nil reads none. A product record is read at
	// most once every productDeclarationAge, because a probe runs a product's
	// own status command and the registry is read on every call.
	products          func() []providerFile
	productsMu        sync.Mutex
	productRefreshMu  sync.Mutex
	productGeneration uint64
	productReadAt     time.Time // populated by the default reader under productRefreshMu
	productAt         time.Time
	productHas        bool
	product           []providerFile
	report            func(error)
	principal         identity.User
	supervisors       map[string]*providerSupervisor
	retiring          map[string]*providerSupervisor
	watchers          []func()
	serving           bool
	closed            bool
	closeDone         chan struct{}
	// changes is closed and replaced whenever a declaration or reading changes.
	changes chan struct{}
	// gate wakes registry observers on every notify (WATCH-W3).
	gate *watch.Gate
	// timing bounds supervision; tests shorten it.
	timing providerTiming
	// decide reports whether program holds a permit for action on resource;
	// nil permits nothing (provider_inventory.go).
	decide func(ctx context.Context, program, action, resource string) bool
	// remotesChanged is called after a remote declaration is added or removed,
	// outside p.mu, so the router reads the remote hosts again.
	remotesChanged      func() error
	pairedHostSource    func() ([]*router.Host, error)
	pairedCandidates    func() []resolution.Candidate
	pairedRefresh       func() error
	pairedNameAdmission func(string) error
	mediationMu         sync.Mutex
	// mediated maps a supported declaration generation to its OA-owned endpoint.
	mediated map[string]mediatedProvider
	// childEnv names this runtime's services to the providers it starts
	// (providerEnvironment); guarded by mu.
	childEnv []string
	// programLogged is the last program presence logged per declaration
	// name (provider_program_missing.go).
	programLogged sync.Map
}

// setPairedHosts supplies the private paired view to the existing host
// aggregation. It never publishes a second router host list.
func (p *runtimeProviders) setPairedHosts(source func() ([]*router.Host, error)) {
	p.mu.Lock()
	p.pairedHostSource = source
	p.mu.Unlock()
}

type pairedReadOutcome uint8

const (
	pairedReadCurrent pairedReadOutcome = iota
	pairedReadRefused
	pairedReadUnreachable
	pairedReadStale
	pairedReadClash
	pairedReadUnavailable
)

const pairedNameClashReason = "paired_name_conflict"

type pairedNameClash struct{ name string }

func (e *pairedNameClash) Error() string {
	return "pairing: local configuration conflicts with " + e.name
}
func (e *pairedNameClash) Unwrap() error { return errPairingConflict }

// pairedReadError keeps the original cause recoverable. The native pairing
// facts classify state/trust; Router alone projects listing/network causes.
type pairedReadError struct {
	Outcome pairedReadOutcome
	Cause   router.AvailabilityCause
	Name    string
	err     error
}

func (e *pairedReadError) Error() string { return e.err.Error() }
func (e *pairedReadError) Unwrap() error { return e.err }

func (p *runtimeProviders) readPairedHosts() ([]*router.Host, *pairedReadError) {
	if p == nil {
		return nil, nil
	}
	p.mu.Lock()
	source := p.pairedHostSource
	p.mu.Unlock()
	if source == nil {
		return nil, nil
	}
	hosts, err := source()
	if err == nil {
		return hosts, nil
	}
	failure := &pairedReadError{Outcome: pairedReadUnavailable, Cause: router.ListingCause(err), err: err}
	var clash *pairedNameClash
	switch {
	case errors.Is(err, errPairingConflict):
		failure.Outcome = pairedReadClash
		if errors.As(err, &clash) {
			failure.Name = clash.name
		}
	case errors.Is(err, errPairingState), errors.Is(err, errPairingRevision):
		failure.Outcome = pairedReadStale
	case failure.Cause == router.AvailabilityTransportUnavailable:
		failure.Outcome = pairedReadUnreachable
	case errors.Is(err, remote.ErrTrust):
		failure.Outcome = pairedReadRefused
	case failure.Cause == router.AvailabilityRights || failure.Cause == router.AvailabilityCredential:
		failure.Outcome = pairedReadRefused
	}
	return nil, failure
}

func (p *runtimeProviders) pairedChanged() error {
	p.mu.Lock()
	refresh := p.remotesChanged
	pairedRefresh := p.pairedRefresh
	p.mu.Unlock()
	if pairedRefresh != nil {
		if err := pairedRefresh(); err != nil {
			return err
		}
	}
	if refresh != nil {
		if err := refresh(); err != nil {
			return err
		}
	}
	if pairedRefresh != nil {
		p.notify()
	}
	return nil
}

type mediatedProvider struct {
	Endpoint string
	Ready    bool
}

// openProviders reads the declarations of state. declared selects whether the
// products on this machine declare hosts of their own (INF-H1).
func openProviders(state string, declared bool, report func(error)) (*runtimeProviders, error) {
	return openProvidersWithEndpoint(state, declared, report, providerEndpointPath)
}

func openProvidersWithEndpoint(state string, declared bool, report func(error), endpointFor func(string) (string, error)) (*runtimeProviders, error) {
	principal, err := currentPrincipal()
	if err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(filepath.Join(state, providersDir))
	if err != nil {
		return nil, err
	}
	p := &runtimeProviders{dir: dir, disabledPath: filepath.Join(state, disabledDeclarationsFile), removedPath: filepath.Join(state, modelHostRemovalsFile),
		report: report, principal: principal, endpointFor: endpointFor,
		supervisors: map[string]*providerSupervisor{}, timing: defaultProviderTiming, changes: make(chan struct{}), mediated: map[string]mediatedProvider{}}
	if declared {
		p.products = func() []providerFile {
			files, observed := productHostFiles(report, p.routerHostSnapshot)
			p.productReadAt = observed
			return files
		}
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
	for _, f := range servingView(files) {
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
	p.productRefreshMu.Lock()
	defer p.productRefreshMu.Unlock()
	p.productsMu.Lock()
	if p.productHas && time.Since(p.productAt) < productDeclarationAge {
		out := slices.Clone(p.product)
		p.productsMu.Unlock()
		return out
	}
	generation := p.productGeneration
	p.productsMu.Unlock()
	p.productReadAt = time.Time{}
	product := p.products()
	p.productsMu.Lock()
	defer p.productsMu.Unlock()
	if generation != p.productGeneration {
		return nil
	}
	p.product, p.productAt, p.productHas = product, time.Now(), true
	if !p.productReadAt.IsZero() && p.productReadAt.Before(p.productAt) {
		p.productAt = p.productReadAt
	}
	return slices.Clone(p.product)
}

// cachedProducts never waits for network discovery. Registry mutation callers
// refresh before taking p.mu and read only this snapshot while holding it.
func (p *runtimeProviders) cachedProducts() []providerFile {
	p.productsMu.Lock()
	defer p.productsMu.Unlock()
	if !p.productHas {
		return nil
	}
	return slices.Clone(p.product)
}

// forgetProducts drops the cached product reading, so the next read probes.
func (p *runtimeProviders) forgetProducts() {
	p.productsMu.Lock()
	p.productHas = false
	p.productGeneration++
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
		// A reset whose first target was a provider leaves its restore journal
		// beside the backup. This reserved name cannot name a valid provider.
		// Installation declarations never contain account reset journals.
		name, reset, journal := strings.Cut(e.Name(), ".json.oa-reset-")
		if !installed && journal && providerName.MatchString(name) &&
			strings.HasSuffix(reset, ".manifest.json") && len(reset) > len(".manifest.json") {
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
			if len(raw) <= maxRegistrationSpecBytes {
				if spec, err := registration.DecodeRegistrationSpec(raw); err == nil && len(spec.Providers) > 0 && len(spec.Servers)+len(spec.Remotes)+len(spec.Applications) == 0 && spec.Providers[0].Name+".json" == e.Name() {
					if installed {
						resolved, _, resolveErr := resolveBundledProgram(dir, providerDeclaration{Program: spec.Program, Transport: transportNative})
						if resolveErr != nil {
							p.report(resolveErr)
							continue
						}
						spec.Program = resolved
					}
					if reason := registrationSpecValid(*spec); reason != "" {
						p.report(fmt.Errorf("providers: %s: invalid %s", e.Name(), reason))
						continue
					}
					for _, facet := range spec.Providers {
						projection := providerFacetCompatibility(spec.Program, facet)
						if reason := validProviderFacet(spec.Program, facet); reason != "" {
							return nil, fmt.Errorf("providers: %s: invalid %s", e.Name(), reason)
						}
						projection.Generation = applicationV3Hash(raw)
						projection.observed = slices.Clone(raw)
						files = append(files, projection)
					}
					continue
				}
			}
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
		f.observed = raw
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
	p.productDeclarations()
	return p.readCached()
}

func (p *runtimeProviders) readCached() ([]providerFile, string, error) {
	files, revision, _, err := p.readWithDisabled(true)
	return files, revision, err
}

func (p *runtimeProviders) readWithDisabled(products bool) ([]providerFile, string, []byte, error) {
	var canonical []providerFile
	var removals []registration.RemovalReceipt
	var programRevision string
	if p.registrationDirectory != nil {
		if directory := p.registrationDirectory(); directory != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var err error
			canonical, removals, programRevision, err = directory.programProviderSnapshot(ctx)
			cancel()
			if err != nil {
				return nil, "", nil, err
			}
		}
	}
	files, revision, raw, err := p.readInputsWithDisabled(products, removals...)
	if err != nil {
		return nil, "", nil, err
	}
	files = shadow(p.withCurrentProductObservations(canonical), files)
	// The same observed disabled-name bytes govern lower sources after canonical
	// projection too. Canonical compatibility records supply no second name mask.
	var disabled []string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &disabled); err != nil {
			return nil, "", nil, err
		}
	}
	for i := range files {
		if files[i].Source != declarationSourceOperator && slices.Contains(disabled, files[i].Declaration.Name) {
			files[i].Disabled = true
		}
	}
	if programRevision != "" {
		digest := sha256.Sum256([]byte(revision + "\x00" + programRevision))
		revision = "providers-v2:" + hex.EncodeToString(digest[:12])
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Declaration.Name < files[j].Declaration.Name })
	return files, revision, raw, nil
}

func (p *runtimeProviders) readInputsWithDisabled(products bool, removals ...registration.RemovalReceipt) ([]providerFile, string, []byte, error) {
	digest := sha256.New()
	operator, err := p.readDeclarationDir(p.dir, digest, false)
	if err != nil {
		return nil, "", nil, err
	}
	for i := range operator {
		operator[i].Source = declarationSourceOperator
	}
	// Remove old operator bytes before precedence so independently owned lower
	// declarations remain visible. Adoption consumes this same filtered input.
	for _, removal := range removals {
		if removal.Facet.Kind == registration.FacetKindHost || removal.Facet.Kind == registration.FacetKindProvider {
			operator = slices.DeleteFunc(operator, func(f providerFile) bool {
				return f.Source == declarationSourceOperator && f.Declaration.Name == removal.Facet.Name
			})
		}
	}
	var product []providerFile
	if products {
		product = p.cachedProducts()
	}
	// Separate the declaration sources and hash installation bytes before
	// shadowing, so an upgrade invalidates edits based on an older reading.
	//unchecked: hash.Hash.Write never returns an error
	digest.Write([]byte("\x00" + declarationSourceInstallation + "\x00"))
	installed, err := p.readInstalledDeclarations(digest)
	if err != nil {
		return nil, "", nil, err
	}
	disabled, raw, err := p.disabledDeclarations()
	if err != nil {
		return nil, "", nil, err
	}
	//unchecked: hash.Hash.Write never returns an error
	digest.Write(raw)
	files := shadow(shadow(operator, product), installed)
	for i := range files {
		if files[i].Source != declarationSourceOperator && disabled[files[i].Declaration.Name] {
			files[i].Disabled = true
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Declaration.Name < files[j].Declaration.Name })
	return files, "providers-v2:" + hex.EncodeToString(digest.Sum(nil)[:12]), raw, nil
}

// Candidates offers only capability profiles the runtime can mediate. Each
// profile a native declaration serves resolves its contract (chat@1, embed@1,
// transcription@1, speech@1, image@1, live@1) to this runtime's mediated
// inference endpoint, which serves every one of them. Arbitrary declared
// contracts remain visible through registry@1 and expose no raw provider
// endpoint to applications.
func (p *runtimeProviders) Candidates() []resolution.Candidate {
	var out []resolution.Candidate
	p.mu.Lock()
	pairedCandidates := p.pairedCandidates
	p.mu.Unlock()
	hosts, pairedErr := p.readPairedHosts()
	if pairedErr != nil && p.report != nil {
		p.report(pairedErr)
	}
	if pairedErr == nil && pairedCandidates != nil {
		out = append(out, pairedCandidates()...)
	}
	if pairedErr == nil {
		for _, host := range hosts {
			endpoint, ok := p.mediatedEndpoint(host.BindingID)
			if !ok {
				continue
			}
			for _, contract := range []string{inference.Contract, inference.EmbedContract} {
				out = append(out, resolution.Candidate{Machine: host.Name, Ready: endpoint.Ready, Reference: wire.ServiceReference{
					Provider: host.Name, Capability: "abstraction.inference", Contract: contract, Scope: wire.ScopeRemote,
					Transport: resolution.LocalTransport, Endpoint: endpoint.Endpoint, Guarantees: []string{string(iwire.RequestGuaranteeHostedAllowed)}}})
			}
		}
	}
	for _, s := range p.sorted() {
		d := s.file.Declaration
		if d.remote() || len(d.Models) == 0 || len(d.mediatedInferenceProfiles()) == 0 || s.programMissing() {
			continue
		}
		mediation, ok := p.mediatedEndpoint(s.file.bindingID())
		if !ok {
			continue
		}
		ready := mediation.Ready && s.state().Readiness == wire.DeclarationReadinessReady
		activate := s.activate
		if d.Activation == wire.ActivationOnDemand.String() {
			// The OA mediator can answer before its downstream provider starts.
			// Inference admission activates that provider after request preflight.
			ready, activate = mediation.Ready, nil
		}
		guarantees := nonNil(d.Guarantees)
		if !slices.Contains(guarantees, inference.GuaranteeLocalOnly) {
			guarantees = append(guarantees, inference.GuaranteeLocalOnly)
		}
		for _, profile := range d.mediatedInferenceProfiles() {
			if jobOnlyProfiles[profile] {
				continue
			}
			contract := profileContracts[profile]
			out = append(out, resolution.Candidate{Ready: ready, Activate: activate, Reference: wire.ServiceReference{
				Provider: d.Name, Capability: "abstraction.inference", Contract: contract, Scope: wire.ScopeLocal, Transport: resolution.LocalTransport,
				Endpoint: mediation.Endpoint, Guarantees: guarantees}})
		}
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
	rank := map[*router.Host]int{}
	for _, s := range p.sorted() {
		d := s.file.Declaration
		models := d.Models
		ready := s.state().Readiness == wire.DeclarationReadinessReady
		onDemand := d.Activation == wire.ActivationOnDemand.String()
		profiles := d.mediatedInferenceProfiles()
		if d.remote() || len(profiles) == 0 || len(models) == 0 || (!ready && !onDemand) || s.programMissing() {
			continue
		}
		var surveyOptions []router.NativeOption
		if onDemand {
			surveyOptions = append(surveyOptions, router.WithSurveyReady(s.ready))
		}
		h, err := router.NewNative(d.Name, s.endpoint, listen.ServerExpectation{Principal: p.principal, Program: d.Program}, models, profiles, surveyOptions...)
		if err != nil {
			p.report(fmt.Errorf("providers: %s mediation: %w", d.Name, err))
			continue
		}
		h.BindingID, h.DeclaredBy = s.file.bindingID(), s.file.DeclaredBy
		// A provider stopped after failed starts lists its models as not
		// servable and takes no route (providerStartFailure).
		h.Unavailable = s.startFailure
		h.SupportedExtensions = selectedProviderExtensions(d.SupportedExtensions)
		setProviderEngineGuide(h, s.file)
		if d.Name == modelHostName {
			_, hosted := readServeArguments(d.Arguments)
			projectHostedObjects(h, hosted, models, profiles, engineSource(s.file.bindingID()))
		}
		if p.pictureStoreRead.Load() && p.shippedPyTorch(s.file) {
			projectPyTorchSets(h, models, profiles)
		}
		if d.Name == modelHostName && p.conversationContext != nil {
			_, hosted := readServeArguments(d.Arguments)
			h.ReportContexts(hostedContexts(hosted, codeCatalogue(context.Background()), p.conversationContext))
		}
		// A provider that declares preload@1 beside chat@1 evaluates a chat
		// prefix ahead of the request (inference INF-PL2).
		h.Preloads = slices.Contains(d.Contracts, inference.PreloadContract) && slices.Contains(profiles, router.ProfileChat)
		rank[h] = pictureProviderRank(d.Name, p.shippedProvider(s.file))
		out = append(out, h)
	}
	// The router asks hosts that would load a model in this order (router
	// candidates); a host already holding it still comes first.
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i]] < rank[out[j]] })
	return out
}

// pictureProviderPreference is the order image@1 asks the picture providers
// for a model more than one of them holds. The product catalogue owns the
// names and the order shared by Setup's Engines list (architect rulings,
// 2026-11-09). Each provider is asked only for models in its declared sets.
var pictureProviderPreference = product.PictureEngines()

// pictureProviderRank is a shipped native host's place in
// pictureProviderPreference; every other host, including one that only
// chose a preferred name, ranks after them and keeps its name order.
func pictureProviderRank(name string, shipped bool) int {
	if i := slices.Index(pictureProviderPreference, name); shipped && i >= 0 {
		return i
	}
	return len(pictureProviderPreference)
}

// shippedProvider reports a declaration OpenAbstractions shipped or Setup
// made: one a product record supplied, one the installation placed in a
// folder only an administrator writes, or one whose program has product
// standing (product.Shipped, the rule the Panel's titles share): in the
// per-machine install folder by path, or in the runtime's own folder, the
// installation's tools folder or the per-user install folder with the
// runtime's own signer. A per-user installation's folder is the person's to
// write, so a declaration placed there counts only by its program. A
// declaration's name alone claims no product standing, since any holder of
// provider management may choose it.
func (p *runtimeProviders) shippedProvider(f providerFile) bool {
	if f.Source == declarationSourceProduct {
		return true
	}
	protected := standingInstallFolders()
	tools := ""
	if p.installation != "" {
		tools = filepath.Dir(p.installation)
	}
	if f.Source == declarationSourceInstallation && product.Protected(tools, protected) {
		return true
	}
	signed := []string{tools, standingUserInstallFolder()}
	if exe, err := os.Executable(); err == nil {
		signed = append(signed, filepath.Dir(exe))
	}
	return product.Shipped(f.Declaration.Program, protected, signed, standingSameSigner)
}

// managedHost reports whether h is a provider OpenAbstractions runs: its
// declaration, found by binding, is no observed product server and has
// product standing (shippedProvider). ASK-R6 reads it for the routine tier.
func (p *runtimeProviders) managedHost(h *router.Host) bool {
	if p == nil || h == nil || h.BindingID == "" {
		return false
	}
	for _, s := range p.sorted() {
		if s.file.bindingID() == h.BindingID {
			return s.file.Source != declarationSourceProduct && p.shippedProvider(s.file)
		}
	}
	return false
}

// standingInstallFolders, standingUserInstallFolder and standingSameSigner
// are product standing's inputs; a test replaces them.
var (
	standingInstallFolders    = product.InstallFolders
	standingUserInstallFolder = product.UserInstallFolder
	standingSameSigner        = product.SameSigner
)

// remoteHosts is a router host for each remote declaration whose trust files
// load; one that does not is reported and left out.
func (p *runtimeProviders) remoteHosts() []*router.Host {
	var out []*router.Host
	for _, s := range p.sorted() {
		d := s.file.Declaration
		if !d.remote() {
			continue
		}
		h, err := remoteRouterHost(d, p.remoteAdmit(s.file))
		if err != nil {
			p.report(err)
			continue
		}
		h.DeclaredBy, h.Profiles = s.file.DeclaredBy, d.resources("profile")
		h.BindingID = s.file.bindingID()
		h.SupportedExtensions = selectedProviderExtensions(d.SupportedExtensions)
		out = append(out, h)
	}
	return out
}

func (p *runtimeProviders) sorted() []*providerSupervisor {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*providerSupervisor, 0, len(p.supervisors))
	bindings, scoped := p.registrationBindingsLocked()
	for _, s := range p.supervisors {
		if scoped && bindings[s.file.Declaration.Name] != s.file.bindingID() {
			continue
		}
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

// observers is the registry's shared waiter, made on first use.
func (p *runtimeProviders) observers() *watch.Gate {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate == nil {
		p.gate = watch.NewGate(watch.DefaultWaiters)
	}
	return p.gate
}

// notify tells every watcher and observer the declarations or their readings
// changed. It never runs under p.mu.
func (p *runtimeProviders) notify() {
	p.mu.Lock()
	watchers := slices.Clone(p.watchers)
	close(p.changes)
	p.changes = make(chan struct{})
	p.mu.Unlock()
	p.observers().Advance()
	if p.registrationDirectory != nil {
		if directory := p.registrationDirectory(); directory != nil && directory.gate != nil {
			directory.gate.Advance()
		}
	}
	for _, w := range watchers {
		w()
	}
}

// Serve supervises every declaration until ctx ends.
func (p *runtimeProviders) Serve(ctx context.Context) error {
	if err := p.adoptAtStartup(ctx); err != nil {
		return err
	}
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

// adoptAtStartup moves the declaration files into canonical registrations
// once. Adoption gives a newly adopted registration its generation, and with
// it a new binding ID, so the runtime runs it before reporting ready: a
// request admitted earlier would carry the replaced binding.
func (p *runtimeProviders) adoptAtStartup(ctx context.Context) error {
	p.adoptMu.Lock()
	defer p.adoptMu.Unlock()
	if p.adopted {
		return nil
	}
	if err := p.adoptProgramRegistrations(ctx); err != nil {
		return err
	}
	p.adopted = true
	return nil
}

// Close stops every supervisor and the children it launched.
func (p *runtimeProviders) Close() error {
	p.mu.Lock()
	if p.closed {
		done := p.closeDone
		p.mu.Unlock()
		if done != nil {
			<-done
		}
		return nil
	}
	p.closed = true
	p.closeDone = make(chan struct{})
	done := p.closeDone
	supervisors := make([]*providerSupervisor, 0, len(p.supervisors))
	for _, s := range p.supervisors {
		supervisors = append(supervisors, s)
	}
	for _, s := range p.retiring {
		supervisors = append(supervisors, s)
	}
	p.mu.Unlock()
	for _, s := range supervisors {
		s.halt()
	}
	close(done)
	return nil
}

// lookup returns the supervisor of name.
func (p *runtimeProviders) lookup(name string) *providerSupervisor {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.supervisors[name]
	if s != nil && !p.registrationBindingCurrentLocked(name, s.file.bindingID()) {
		return nil
	}
	return s
}

// awaitReady is the one wait for an on-demand provider's first readiness
// (FAC-R8). Each pass reads the supervisor through lookup, which returns a
// refusal reason when there is none to wait for, asks it to activate, and
// returns it once ready. A provider stopped after failed starts returns
// start_failed:<name> at once (providerStartFailure). When ctx or budget
// passes first it returns the supervisor's activating:<name> reason; the
// launch keeps running.
func (p *runtimeProviders) awaitReady(ctx context.Context, budget time.Duration, lookup func() (*providerSupervisor, string)) (*providerSupervisor, string) {
	deadline := time.Now().Add(budget)
	for {
		s, why := lookup()
		if why != "" {
			return nil, why
		}
		s.activate()
		if s.ready() {
			return s, ""
		}
		if why := s.startFailure(); why != "" {
			return nil, why
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, s.activatingReason()
		}
		p.mu.Lock()
		changes := p.changes
		p.mu.Unlock()
		wait := 200 * time.Millisecond
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-changes:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
	}
}

// list is every declaration with its reading, in name order: a supervised
// provider or remote runtime from its supervisor, a host from the router's
// survey, and a declaration an operator withdrew as disabled.
func (p *runtimeProviders) list() ([]wire.DeclarationState, string, error) {
	v2, revision, err := p.listV2()
	if err != nil {
		return nil, "", err
	}
	out := make([]wire.DeclarationState, len(v2))
	for i, entry := range v2 {
		out[i] = entry.State
	}
	return out, revision, nil
}

// listV2 joins each reading to its declaration and extension inventory from
// the same registration snapshot. A concurrent supervisor update cannot make a
// reading claim metadata from another generation.
func (p *runtimeProviders) listV2() ([]wire.DeclarationStateV2, string, error) {
	list, revision, err := p.listPinned()
	return declarationStatesV2(list), revision, err
}

func declarationStatesV2(list []wire.PinnedDeclarationState) []wire.DeclarationStateV2 {
	out := make([]wire.DeclarationStateV2, len(list))
	for i, entry := range list {
		out[i] = wire.DeclarationStateV2{State: entry.State, SupportedExtensions: entry.SupportedExtensions}
	}
	return out
}

func (p *runtimeProviders) listPinned() ([]wire.PinnedDeclarationState, string, error) {
	p.productDeclarations()
	p.recheckPrograms()
	p.mu.Lock()
	files, revision, err := p.readCached()
	// Capture the supervisors under the same lock as the registration files.
	// A name may be withdrawn and declared again while host readings run.
	supervisors := make(map[string]*providerSupervisor, len(p.supervisors))
	for name, supervisor := range p.supervisors {
		supervisors[name] = supervisor
	}
	p.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	hosts := map[string]wire.DeclarationState{}
	for _, state := range p.hostStates(files) {
		hosts[state.Declaration.Name] = state
	}
	out := []wire.PinnedDeclarationState{}
	for _, f := range files {
		name := f.Declaration.Name
		state, known := hosts[name]
		if !known {
			if supervisor := supervisors[name]; supervisor != nil && supervisor.file.bindingID() == f.bindingID() && !f.Disabled {
				state, known = supervisor.state(), true
			}
			if !known {
				state = wire.DeclarationState{Declaration: f.Declaration.wire(), DeclaredBy: f.DeclaredBy, DeclaredUnixMs: f.DeclaredAt,
					Role: f.Declaration.role(), Readiness: wire.DeclarationReadinessDisabled, Why: f.disabledReason(), Described: []wire.ServiceState{}}
				if !f.Disabled {
					state.Readiness, state.Why = wire.DeclarationReadinessUnreachable, "generation:unavailable"
				}
			}
		}
		state.Declaration = f.Declaration.wire()
		entry := wire.PinnedDeclarationState{State: state, SupportedExtensions: f.Declaration.wireExtensions()}
		if f.Declaration.Remote != nil {
			entry.RemoteServerKeySHA256 = f.Declaration.Remote.ServerKeySHA256
		}
		out = append(out, entry)
	}
	return out, revision, nil
}

// add writes a declaration and supervises it through the existing owner.
func (p *runtimeProviders) add(expected string, f providerFile) wire.DeclarationChange {
	return p.addAdmitted(context.Background(), expected, f, nil)
}

func (p *runtimeProviders) addAdmitted(ctx context.Context, expected string, f providerFile, admit func() error) wire.DeclarationChange {
	return p.addStore(ctx, expected, f, admit, nil)
}

func (p *runtimeProviders) addWith(expected string, f providerFile, mutate providerMutation) wire.DeclarationChange {
	return p.addStore(context.Background(), expected, f, nil, mutate)
}

func (p *runtimeProviders) addStore(ctx context.Context, expected string, f providerFile, admit func() error, mutate providerMutation) wire.DeclarationChange {
	if change, handled := p.editCanonicalLegacy(ctx, "add", expected, f.Declaration.Name, f, admit, mutate); handled {
		return change
	}
	p.productDeclarations()
	p.mu.Lock()
	change := p.addLockedAdmitted(ctx, expected, f, admit, mutate)
	next := p.supervisors[f.Declaration.Name]
	start := next != nil && p.serving && !p.closed
	p.mu.Unlock()
	if change.Outcome == wire.DeclarationEditOutcomeApplied {
		p.notify()
		if start {
			next.start()
		}
		if p.remotesChanged != nil {
			if err := p.remotesChanged(); err != nil {
				p.report(err)
				change.Reason = "hosts:unavailable"
			}
		}
	}
	return change
}

func (p *runtimeProviders) addLockedAdmitted(ctx context.Context, expected string, f providerFile, admit func() error, mutate providerMutation) wire.DeclarationChange {
	if err := providerAdmit(ctx, admit); err != nil {
		return p.editFailureLocked(err)
	}
	if p.closed {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	files, revision, disabled, err := p.readWithDisabled(true)
	if err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	if expected != revision {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "revision"}
	}
	if p.retiring[f.Declaration.Name] != nil {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "retiring"}
	}
	if err := p.admitPairedName(f.Declaration.Name); err != nil {
		return p.editFailureLocked(err)
	}
	if _, exists := p.supervisors[f.Declaration.Name]; exists {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "name"}
	}
	path := filepath.Join(p.dir, f.Declaration.Name+".json")
	if _, err := os.Lstat(path); err == nil {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "name"}
	} else if !errors.Is(err, os.ErrNotExist) {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	if i := slices.IndexFunc(files, func(old providerFile) bool { return old.Declaration.Name == f.Declaration.Name }); i >= 0 && !files[i].Disabled {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "name"}
	}
	for _, old := range files {
		if !old.Disabled && f.Declaration.Endpoint != "" && old.Declaration.Endpoint == f.Declaration.Endpoint {
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "endpoint"}
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
	if mutate == nil {
		mutate = providerMutator(ctx, p.commitAdmission(ctx, expected, f.Declaration.Name, true, admit))
	}
	if err := mutate(path, nil, f); err != nil {
		p.report(err)
		return p.editFailureLocked(err)
	}
	// Operator precedence already enables this record. Failure of the separate
	// disabled-name cleanup is postcommit uncertainty, never rollback.
	if err := p.setDisabledObserved(ctx, f.Declaration.Name, false, disabled, admit); err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	if f.Declaration.role() != wire.DeclarationRoleHost {
		p.supervisors[f.Declaration.Name] = newProviderSupervisor(p, f)
	}
	_, revision, err = p.readCached()
	if err != nil {
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: revision}
}

// replace changes one enabled operator record without publishing absence.
func (p *runtimeProviders) replace(expected string, f providerFile) wire.DeclarationChange {
	return p.replaceAdmitted(context.Background(), expected, f, nil)
}
func (p *runtimeProviders) replaceAdmitted(ctx context.Context, expected string, f providerFile, admit func() error) wire.DeclarationChange {
	return p.replaceStore(ctx, expected, f, admit, nil)
}
func (p *runtimeProviders) replaceWith(expected string, f providerFile, mutate providerMutation) wire.DeclarationChange {
	return p.replaceStore(context.Background(), expected, f, nil, mutate)
}
func (p *runtimeProviders) replaceStore(ctx context.Context, expected string, f providerFile, admit func() error, mutate providerMutation) wire.DeclarationChange {
	if change, handled := p.editCanonicalLegacy(ctx, "replace", expected, f.Declaration.Name, f, admit, mutate); handled {
		return change
	}
	p.productDeclarations()
	p.mu.Lock()
	if err := providerAdmit(ctx, admit); err != nil {
		change := p.editFailureLocked(err)
		p.mu.Unlock()
		return change
	}
	if p.closed {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	files, revision, err := p.readCached()
	if err != nil {
		p.mu.Unlock()
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	refuse := func(outcome wire.DeclarationEditOutcome, reason string) wire.DeclarationChange {
		p.mu.Unlock()
		change := wire.DeclarationChange{Outcome: outcome, Reason: reason}
		if outcome == wire.DeclarationEditOutcomeConflict {
			change.Revision = revision
		}
		return change
	}
	if revision != expected {
		return refuse(wire.DeclarationEditOutcomeConflict, "revision")
	}
	if p.retiring[f.Declaration.Name] != nil {
		return refuse(wire.DeclarationEditOutcomeConflict, "retiring")
	}
	i := slices.IndexFunc(files, func(old providerFile) bool { return old.Declaration.Name == f.Declaration.Name })
	if i < 0 {
		return refuse(wire.DeclarationEditOutcomeUnknown, "name")
	}
	if files[i].Disabled || files[i].Source != declarationSourceOperator {
		return refuse(wire.DeclarationEditOutcomeForbidden, "source")
	}
	if err := p.admitPairedName(f.Declaration.Name); err != nil {
		change := p.editFailureLocked(err)
		p.mu.Unlock()
		return change
	}
	for _, other := range files {
		if other.Declaration.Name != f.Declaration.Name && !other.Disabled && f.Declaration.Endpoint != "" && other.Declaration.Endpoint == f.Declaration.Endpoint {
			return refuse(wire.DeclarationEditOutcomeConflict, "endpoint")
		}
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		p.mu.Unlock()
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	f.Generation = hex.EncodeToString(generation[:])
	if mutate == nil {
		mutate = providerMutator(ctx, p.commitAdmission(ctx, expected, f.Declaration.Name, true, admit))
	}
	writeErr := mutate(filepath.Join(p.dir, f.Declaration.Name+".json"), files[i].observed, f)
	if writeErr != nil {
		p.report(writeErr)
		if providerKnownPrecommit(writeErr) {
			change := p.editFailureLocked(writeErr)
			p.mu.Unlock()
			return change
		}
		actual, _, readErr := p.readCached()
		if readErr == nil && slices.ContainsFunc(actual, func(current providerFile) bool {
			return current.Source == declarationSourceOperator && !current.Disabled && current.bindingID() == files[i].bindingID()
		}) {
			p.mu.Unlock()
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
		}
		if readErr != nil || !slices.ContainsFunc(actual, func(current providerFile) bool {
			return current.Source == declarationSourceOperator && !current.Disabled && current.bindingID() == f.bindingID()
		}) {
			old := p.beginRetirementLocked(f.Declaration.Name)
			p.mu.Unlock()
			p.notify()
			p.finishRetirement(f.Declaration.Name, old, nil)
			if readErr != nil {
				p.report(readErr)
			}
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
		}
	}
	old := p.beginRetirementLocked(f.Declaration.Name)
	var next *providerSupervisor
	if f.Declaration.role() != wire.DeclarationRoleHost {
		next = newProviderSupervisor(p, f)
		p.supervisors[f.Declaration.Name] = next
	}
	_, revision, err = p.readCached()
	p.mu.Unlock()
	p.notify()
	p.finishRetirement(f.Declaration.Name, old, next)
	reason := ""
	if p.remotesChanged != nil {
		if changed := p.remotesChanged(); changed != nil {
			p.report(changed)
			reason = "hosts:unavailable"
		}
	}
	if err != nil || writeErr != nil {
		if err != nil {
			p.report(err)
		}
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: revision, Reason: reason}
}

func (p *runtimeProviders) remove(expected, name string) wire.DeclarationChange {
	return p.removeAdmitted(context.Background(), expected, name, nil)
}
func (p *runtimeProviders) removeAdmitted(ctx context.Context, expected, name string, admit func() error) wire.DeclarationChange {
	return p.removeStore(ctx, expected, name, admit, nil)
}
func (p *runtimeProviders) removeWith(expected, name string, mutate providerMutation) wire.DeclarationChange {
	return p.removeStore(context.Background(), expected, name, nil, mutate)
}
func (p *runtimeProviders) removeStore(ctx context.Context, expected, name string, admit func() error, mutate providerMutation) wire.DeclarationChange {
	if change, handled := p.editCanonicalLegacy(ctx, "remove", expected, name, providerFile{}, admit, mutate); handled {
		return change
	}
	p.forgetProducts()
	p.productDeclarations()
	p.mu.Lock()
	if err := providerAdmit(ctx, admit); err != nil {
		change := p.editFailureLocked(err)
		p.mu.Unlock()
		return change
	}
	if p.closed {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	files, revision, disabled, err := p.readWithDisabled(true)
	if err != nil {
		p.mu.Unlock()
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	if expected != revision {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "revision"}
	}
	if p.retiring[name] != nil {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: "retiring"}
	}
	i := slices.IndexFunc(files, func(f providerFile) bool { return f.Declaration.Name == name })
	if i < 0 || files[i].Disabled {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnknown, Revision: revision}
	}
	elsewhere, err := p.declaredElsewhere(name)
	if err != nil {
		p.mu.Unlock()
		p.report(err)
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	declaration, reason := files[i].Declaration, ""
	committed := false
	if mutate == nil {
		mutate = providerMutator(ctx, p.commitAdmission(ctx, expected, name, false, admit))
	}
	var editErr error
	if files[i].Source == declarationSourceOperator {
		editErr = mutate(filepath.Join(p.dir, name+".json"), files[i].observed, nil)
		if editErr == nil {
			committed = true
		}
	}
	if editErr == nil && elsewhere && files[i].Source != declarationSourceOperator {
		check := admit
		if !committed {
			check = p.commitAdmission(ctx, expected, name, false, admit)
		}
		editErr = p.setDisabledObserved(ctx, name, true, disabled, check)
		if editErr == nil {
			committed = true
			reason = "disabled"
		}
	}
	if editErr != nil && !committed {
		if providerKnownPrecommit(editErr) {
			change := p.editFailureLocked(editErr)
			p.mu.Unlock()
			return change
		}
		current, _, readErr := p.readCached()
		if readErr == nil && slices.ContainsFunc(current, func(f providerFile) bool { return !f.Disabled && f.bindingID() == files[i].bindingID() }) {
			p.mu.Unlock()
			p.report(editErr)
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
		}
	}
	old := p.beginRetirementLocked(name)
	_, revision, readErr := p.readCached()
	p.mu.Unlock()
	p.notify()
	p.finishRetirement(name, old, nil)
	if (declaration.remote() || declaration.host()) && p.remotesChanged != nil {
		if err := p.remotesChanged(); err != nil {
			p.report(err)
		}
	}
	if editErr != nil || readErr != nil {
		if editErr != nil {
			p.report(editErr)
		}
		if readErr != nil {
			p.report(readErr)
		}
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	}
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: revision, Reason: reason}
}

// declaredElsewhere reports whether a product or the installation declares
// name, so the operator's removal must be recorded rather than performed.
func (p *runtimeProviders) declaredElsewhere(name string) (bool, error) {
	installed, err := p.readInstalledDeclarations(nil)
	if err != nil {
		return false, err
	}
	for _, f := range append(p.cachedProducts(), installed...) {
		if f.Declaration.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// providerEndpointPath is the platform endpoint of a declared endpoint name.
func providerEndpointPath(name string) (string, error) {
	return providerEndpointPathFor(name, bootstrap.Endpoint)
}

func providerEndpointPathFor(name string, endpointFor func(string) (string, error)) (string, error) {
	if !providerEndpoint.MatchString(name) {
		return "", errors.New("invalid provider endpoint name")
	}
	const service = "provider-v1"
	anchor, err := endpointFor(service)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(anchor, `\\.\pipe\`) || !strings.HasSuffix(anchor, "-"+service) {
			return "", errors.New("unexpected provider bootstrap address")
		}
		return strings.TrimSuffix(anchor, service) + name, nil
	}
	if !filepath.IsAbs(anchor) || filepath.Base(anchor) != service+".sock" {
		return "", errors.New("unexpected provider bootstrap address")
	}
	return filepath.Join(filepath.Dir(anchor), name+".sock"), nil
}
