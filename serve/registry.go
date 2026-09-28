package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// ActionProviderManage is the registry's one rights action, decided on
// resource account (facade CONTRACT.md FAC-R5).
const ActionProviderManage = "abstraction.facade/provider.manage"

// ResourceCard is the rights resource of abstraction.resource/hold for one
// table card, as a declaration names it in resources: card:<n>.
func ResourceCard(name string) string { return "card:" + name }

// RegistryContract is the registry profile's wire name.
const RegistryContract = "abstraction.facade/registry@1"

// maxObserveWait bounds Observe's wait_ms.
const maxObserveWait = 30000

// runtimeRegistry serves abstraction.facade/registry@1 over the runtime's
// provider declarations. Every call decides provider.manage on account for
// the bound caller through the runtime's rights policy.
type runtimeRegistry struct {
	providers *runtimeProviders
	// operator holds the rights helpers declarations write rules with.
	operator *inferenceOperator
}

func declarationListRefusal(word string) wire.DeclarationListOutcome {
	switch word {
	case "forbidden":
		return wire.DeclarationListOutcomeForbidden
	case "unavailable":
		return wire.DeclarationListOutcomeUnavailable
	}
	return 0
}

func declarationEditRefusal(word string) wire.DeclarationEditOutcome {
	switch word {
	case "forbidden":
		return wire.DeclarationEditOutcomeForbidden
	case "unavailable":
		return wire.DeclarationEditOutcomeUnavailable
	}
	return 0
}

func registryEndpoint(options runtimeFlags) (string, error) {
	switch {
	case options.isolated != "":
		return bootstrap.Endpoint(options.isolated + "-registry")
	case options.endpoint != "":
		return options.endpoint + "-registry", nil
	}
	return options.defaultEndpoint("registry-v1")
}

func (r *runtimeRegistry) Declarations(ctx context.Context, caller inference.Subject) wire.DeclarationList {
	if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
		return wire.DeclarationList{Outcome: declarationListRefusal(word), Declarations: []wire.DeclarationState{}}
	}
	list, revision, err := r.providers.list()
	if err != nil {
		r.providers.report(err)
		return wire.DeclarationList{Outcome: wire.DeclarationListOutcomeUnavailable, Declarations: []wire.DeclarationState{}}
	}
	return wire.DeclarationList{Outcome: wire.DeclarationListOutcomePage, Revision: revision, Declarations: list}
}

// Declare adds one declaration. A program never declares itself: an adopter
// cannot register its own program.
func (r *runtimeRegistry) Declare(ctx context.Context, caller inference.Subject, expected string, declaration wire.Declaration) wire.DeclarationChange {
	if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
		return wire.DeclarationChange{Outcome: declarationEditRefusal(word)}
	}
	d := declarationOf(declaration)
	if field := validProviderDeclaration(d); field != "" {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: field}
	}
	if d.remote() {
		if _, err := d.Remote.config(); err != nil {
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "remote"}
		}
	} else if samePrograms(d.Program, caller.Program) {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "program:self"}
	}
	change := r.providers.add(expected, providerFile{Version: providerFileVersion, Declaration: d, DeclaredBy: caller.Program, DeclaredAt: time.Now().UnixMilli()})
	if change.Outcome != wire.DeclarationEditOutcomeApplied {
		return change
	}
	// Each capability reads its own acceptance rule: the person accepts each
	// named store from this program, the resource table's card is held by the
	// program that declared it, and a remote runtime's hosts serve the
	// operator programs. An existing rule, a deny included, stays as it is.
	by := rwire.Subject{Account: caller.Account, Program: caller.Program}
	var errs []error
	for _, store := range d.resources("store") {
		errs = append(errs, r.operator.permitRuleWhy(by, d.Program, ActionInventoryProvide, ResourceStore(store), providerAddWhy))
	}
	for _, card := range d.resources("card") {
		errs = append(errs, r.operator.permitRuleWhy(by, d.Program, resourceservice.ActionHold, ResourceCard(card), providerAddWhy))
	}
	if d.host() {
		errs = append(errs, r.operator.writeHostRules(caller, (providerFile{Declaration: d}).hostEntry()))
	} else if d.remote() {
		errs = append(errs, r.operator.writeHostRules(caller, iwire.HostEntry{Name: d.Name, Hosted: true, Kind: router.WireRemote}))
	}
	if err := errors.Join(errs...); err != nil {
		r.providers.report(err)
		change.Reason = "rules:" + err.Error()
	}
	return change
}

// Withdraw removes one declaration.
func (r *runtimeRegistry) Withdraw(ctx context.Context, caller inference.Subject, expected, name string) wire.DeclarationChange {
	if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
		return wire.DeclarationChange{Outcome: declarationEditRefusal(word)}
	}
	if !providerName.MatchString(name) {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "name"}
	}
	return r.providers.remove(expected, name)
}

// Observe waits for the declarations or their readings to differ from cursor.
func (r *runtimeRegistry) Observe(ctx context.Context, caller inference.Subject, cursor string, waitMS int64) wire.DeclarationObservation {
	refused := func(outcome wire.DeclarationListOutcome) wire.DeclarationObservation {
		return wire.DeclarationObservation{Outcome: outcome, Declarations: []wire.DeclarationState{}}
	}
	if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
		return refused(declarationListRefusal(word))
	}
	if waitMS < 0 || waitMS > maxObserveWait {
		return refused(wire.DeclarationListOutcomeInvalid)
	}
	deadline := time.NewTimer(time.Duration(waitMS) * time.Millisecond)
	defer deadline.Stop()
	expired := false
	for {
		if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
			return refused(declarationListRefusal(word))
		}
		r.providers.mu.Lock()
		changes := r.providers.changes
		r.providers.mu.Unlock()
		list, _, err := r.providers.list()
		if err != nil {
			r.providers.report(err)
			return refused(wire.DeclarationListOutcomeUnavailable)
		}
		raw, err := json.Marshal(list)
		if err != nil {
			return refused(wire.DeclarationListOutcomeUnavailable)
		}
		sum := sha256.Sum256(raw)
		now := "registry-v1:" + hex.EncodeToString(sum[:12])
		if cursor == "" || now != cursor || expired {
			if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
				return refused(declarationListRefusal(word))
			}
			return wire.DeclarationObservation{Outcome: wire.DeclarationListOutcomePage, Cursor: now, Declarations: list}
		}
		select {
		case <-changes:
		case <-deadline.C:
			expired = true
		case <-ctx.Done():
			return refused(wire.DeclarationListOutcomeUnavailable)
		}
	}
}

// registryHost serves registry@1, and endpoint@1 beside it, on its endpoint.
type registryHost struct {
	listener listen.Listener
	registry *runtimeRegistry
	owner    string
	report   func(error)
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	workers  sync.WaitGroup
	slots    chan struct{}
}

func listenRegistry(endpoint string, registry *runtimeRegistry, owner string, report func(error)) (*registryHost, error) {
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &registryHost{listener: listen.Sessions(l, listen.SessionOptions{MaxSessions: 64}), registry: registry, owner: owner, report: report, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 64)}, nil
}

func (h *registryHost) Close() error {
	var err error
	h.once.Do(func() { h.cancel(); err = h.listener.Close() })
	return err
}

func (h *registryHost) Serve(ctx context.Context) error {
	//unchecked: Close is idempotent (sync.Once); this async cancellation callback has no caller to report the error to, and Serve's own deferred Close below is the same no-op afterward
	stop := context.AfterFunc(ctx, func() { h.Close() })
	defer stop()
	defer h.workers.Wait()
	defer h.Close()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || h.ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case h.slots <- struct{}{}:
		default:
			//unchecked: dropping a connection because the worker slots are full; nothing here can act on a failed close of the connection it is already refusing
			conn.Close()
			continue
		}
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			defer func() { <-h.slots }()
			defer conn.Close()
			callCtx, cancel := context.WithTimeout(h.ctx, (maxObserveWait+5000)*time.Millisecond)
			defer cancel()
			call, err := listen.ReceiveFramed(callCtx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err == nil {
				var reply []byte
				receiver := &registryReceiver{host: h, call: call, ctx: callCtx}
				if reply, err = (&wire.RegistryDispatcher{Handler: receiver}).ExchangeFrame(call.Frame); err == nil {
					err = call.Reply(reply)
				}
			}
			if err != nil && h.report != nil && h.ctx.Err() == nil {
				h.report(fmt.Errorf("registry: %w", err))
			}
		}()
	}
}

// registryReceiver binds each call's caller from the connection's Program proof.
type registryReceiver struct {
	host *registryHost
	call *listen.FramedCall
	ctx  context.Context
}

func (r *registryReceiver) caller() inference.Subject {
	peer, err := r.call.Peer()
	if err != nil {
		return inference.Subject{}
	}
	subject, err := inferenceservice.SubjectFromPeer(peer)
	if err != nil || subject.Account != r.host.owner || r.call.Recheck() != nil {
		return inference.Subject{}
	}
	return subject
}

func (r *registryReceiver) Declarations() (wire.DeclarationList, error) {
	return r.host.registry.Declarations(r.ctx, r.caller()), nil
}

func (r *registryReceiver) Declare(expectedRevision string, declaration wire.Declaration) (wire.DeclarationChange, error) {
	return r.host.registry.Declare(r.ctx, r.caller(), expectedRevision, declaration), nil
}

func (r *registryReceiver) Withdraw(expectedRevision, name string) (wire.DeclarationChange, error) {
	return r.host.registry.Withdraw(r.ctx, r.caller(), expectedRevision, name), nil
}

func (r *registryReceiver) Observe(cursor string, waitMs int64) (wire.DeclarationObservation, error) {
	return r.host.registry.Observe(r.ctx, r.caller(), cursor, waitMs), nil
}

// legacyProviderFile is a version 1 declaration, which the registration build
// wrote with stores and profiles.
type legacyProviderFile struct {
	Version     int `json:"version"`
	Declaration struct {
		providerDeclaration
		Stores   []string `json:"stores"`
		Profiles []string `json:"profiles"`
	} `json:"declaration"`
	DeclaredBy string `json:"declared_by"`
	DeclaredAt int64  `json:"declared_unix_ms"`
}

// legacyHosts is the part of a registration-build hosts.json the migration
// moves: its hosted entries, of wire oa-remote@1 or of any other wire.
type legacyHosts struct {
	Hosted []struct {
		Name       string                `json:"name"`
		Base       string                `json:"base"`
		Wire       string                `json:"wire"`
		Credential string                `json:"credential"`
		Profiles   []string              `json:"profiles"`
		DeclaredBy string                `json:"declared_by"`
		Remote     *inferenceRemoteTrust `json:"remote"`
	} `json:"hosted"`
}

// remoteContracts are the services a remote runtime serves over the remote
// transport, which a migrated remote declaration names.
var remoteContracts = []string{inference.Contract, inferenceservice.RouterContract}

// migrateProviderState rewrites, once, the state the registration build
// wrote: version 1 declarations become version 2 with resources, and each
// oa-remote@1 entry of hosts.json becomes a remote declaration and leaves
// hosts.json. Declarations are written before hosts.json, so an interrupted
// rewrite resumes on the next start.
func migrateProviderState(state string, report func(error)) error {
	dir := filepath.Join(state, providersDir)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var legacy legacyProviderFile
		if json.Unmarshal(raw, &legacy) != nil || legacy.Version != 1 {
			continue
		}
		d := legacy.Declaration.providerDeclaration
		d.Resources = nil
		for _, profile := range legacy.Declaration.Profiles {
			d.Resources = append(d.Resources, "profile:"+profile)
		}
		for _, store := range legacy.Declaration.Stores {
			d.Resources = append(d.Resources, ResourceStore(store))
		}
		if err := writeAtomically(path, providerFile{Version: providerFileVersion, Declaration: d, DeclaredBy: legacy.DeclaredBy, DeclaredAt: legacy.DeclaredAt}); err != nil {
			return err
		}
	}

	hostsPath := filepath.Join(state, "inference", inferenceHostsFile)
	raw, err := os.ReadFile(hostsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var legacy legacyHosts
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil
	}
	moved := map[string]bool{}
	for _, h := range legacy.Hosted {
		if h.Wire != router.WireRemote {
			continue
		}
		moved[h.Name] = true
		path := filepath.Join(dir, h.Name+".json")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		d := providerDeclaration{Name: h.Name, Arguments: []string{}, Endpoint: h.Base, Transport: transportRemote, Contracts: slices.Clone(remoteContracts),
			Activation: wire.ActivationRemote.String(), Remote: h.Remote}
		for _, profile := range h.Profiles {
			d.Resources = append(d.Resources, "profile:"+profile)
		}
		if d.Remote != nil {
			d.Remote.Credential = h.Credential
		}
		if field := validProviderDeclaration(d); field != "" {
			report(fmt.Errorf("providers: remote host %s in %s cannot become a declaration: invalid %s; it is removed", h.Name, inferenceHostsFile, field))
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := writeAtomically(path, providerFile{Version: providerFileVersion, Declaration: d, DeclaredBy: h.DeclaredBy, DeclaredAt: time.Now().UnixMilli()}); err != nil {
			return err
		}
	}
	config, err := loadInferenceHosts(hostsPath)
	if err != nil {
		return err
	}
	// Every host entry hosts.json still carries becomes a declaration of role
	// host, keeping its declared_by as the declaration's provenance. The file
	// then keeps only the ceilings, the bounds and the declared switch.
	hosts := []iwire.HostEntry{}
	declaredBy := map[string]string{}
	if config.Local != nil {
		for _, l := range *config.Local {
			hosts = append(hosts, iwire.HostEntry{Name: l.Kind, Kind: l.Kind, Base: l.Base, Profiles: l.Profiles})
			declaredBy[l.Kind] = l.DeclaredBy
		}
	}
	for _, h := range config.Hosted {
		if moved[h.Name] || h.Wire == router.WireRemote {
			continue
		}
		host := iwire.HostEntry{Name: h.Name, Hosted: true, Kind: h.Wire, Base: h.Base, Credential: h.Credential, Profiles: h.Profiles}
		if limit, ok := config.Ceilings[h.Credential]; ok && h.Credential != "" {
			host.Ceiling = ceilingLimit(limit)
		}
		hosts = append(hosts, host)
		declaredBy[h.Name] = h.DeclaredBy
	}
	if len(moved) == 0 && len(hosts) == 0 && config.Local != nil && config.Declared != nil {
		return nil
	}
	for _, entry := range hosts {
		path := filepath.Join(dir, entry.Name+".json")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		by := declaredBy[entry.Name]
		if by == "" {
			by = declaredByOperator
		}
		file := newHostFile(entry, by)
		if field := validProviderDeclaration(file.Declaration); field != "" {
			report(fmt.Errorf("providers: host %s in %s cannot become a declaration: invalid %s; it is removed", entry.Name, inferenceHostsFile, field))
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := writeAtomically(path, file); err != nil {
			return err
		}
	}
	// The declarations switch is written out before the entries leave, so the
	// products keep declaring exactly what they declared before.
	declared := config.usesDeclarations()
	empty := []inferenceLocalHost{}
	config.Declared, config.Local, config.Hosted = &declared, &empty, nil
	return writeAtomically(hostsPath, config)
}
