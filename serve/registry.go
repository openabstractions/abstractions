package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	watch "github.com/openabstractions/abstraction-watch/go"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
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
const RegistryV2Contract = "abstraction.facade/registry@2"

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
	return options.companionEndpoint("registry-v1", "registry", "registry")
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

func (r *runtimeRegistry) DeclarationsV2(ctx context.Context, caller inference.Subject) wire.DeclarationListV2 {
	if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
		return wire.DeclarationListV2{Outcome: declarationListRefusal(word), Declarations: []wire.DeclarationStateV2{}}
	}
	list, revision, err := r.providers.listV2()
	if err != nil {
		r.providers.report(err)
		return wire.DeclarationListV2{Outcome: wire.DeclarationListOutcomeUnavailable, Declarations: []wire.DeclarationStateV2{}}
	}
	return wire.DeclarationListV2{Outcome: wire.DeclarationListOutcomePage, Revision: revision, Declarations: list}
}

func (r *runtimeRegistry) DeclarationsPinned(ctx context.Context, caller inference.Subject) wire.PinnedDeclarationList {
	if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
		return wire.PinnedDeclarationList{Outcome: declarationListRefusal(word), Declarations: []wire.PinnedDeclarationState{}}
	}
	list, revision, err := r.providers.listPinned()
	if err != nil {
		r.providers.report(err)
		return wire.PinnedDeclarationList{Outcome: wire.DeclarationListOutcomeUnavailable, Declarations: []wire.PinnedDeclarationState{}}
	}
	return wire.PinnedDeclarationList{Outcome: wire.DeclarationListOutcomePage, Revision: revision, Declarations: list}
}

// Declare adds one declaration. A program never declares itself: an adopter
// cannot register its own program.
func (r *runtimeRegistry) Declare(ctx context.Context, caller inference.Subject, expected string, declaration wire.Declaration) wire.DeclarationChange {
	return r.declare(ctx, caller, expected, declaration, nil, "", false, false)
}

func (r *runtimeRegistry) DeclareV2(ctx context.Context, caller inference.Subject, expected string, declaration wire.DeclarationWithExtensions) wire.DeclarationChange {
	return r.declare(ctx, caller, expected, declaration.Declaration, declaration.SupportedExtensions, "", false, false)
}

func (r *runtimeRegistry) DeclarePinned(ctx context.Context, caller inference.Subject, expected string, declaration wire.PinnedDeclaration) wire.DeclarationChange {
	return r.declare(ctx, caller, expected, declaration.Declaration, declaration.SupportedExtensions, declaration.RemoteServerKeySHA256, true, false)
}

func (r *runtimeRegistry) ReplacePinned(ctx context.Context, caller inference.Subject, expected string, declaration wire.PinnedDeclaration) wire.DeclarationChange {
	return r.declare(ctx, caller, expected, declaration.Declaration, declaration.SupportedExtensions, declaration.RemoteServerKeySHA256, true, true)
}

func (r *runtimeRegistry) declare(ctx context.Context, caller inference.Subject, expected string, declaration wire.Declaration, extensions []wire.DeclarationExtension, remotePin string, pinned, replace bool) wire.DeclarationChange {
	if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
		return wire.DeclarationChange{Outcome: declarationEditRefusal(word)}
	}
	d := declarationOf(declaration)
	d.SupportedExtensions = extensionsOf(extensions)
	if d.remote() && !pinned {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "remote:registry@2"}
	}
	if !d.remote() && remotePin != "" {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "remote_server_key_sha256"}
	}
	if d.Remote != nil {
		d.Remote.ServerKeySHA256 = remotePin
	}
	if field := validProviderDeclaration(d); field != "" {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: field}
	}
	if d.remote() {
		if _, err := d.Remote.serverKey(); err != nil {
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "remote_server_key_sha256"}
		}
		if _, err := d.Remote.config(); err != nil {
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "remote"}
		}
	} else if samePrograms(d.Program, caller.Program) {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeInvalid, Reason: "program:self"}
	}
	file := providerFile{Version: providerFileVersion, Declaration: d, DeclaredBy: caller.Program, DeclaredAt: time.Now().UnixMilli()}
	before := r.hostedBefore(d.Name)
	var change wire.DeclarationChange
	if replace {
		change = r.providers.replaceAdmitted(ctx, expected, file, r.operator.providerEditAdmission(ctx, caller, ActionProviderManage))
	} else {
		change = r.providers.addAdmitted(ctx, expected, file, r.operator.providerEditAdmission(ctx, caller, ActionProviderManage))
	}
	if change.Outcome != wire.DeclarationEditOutcomeApplied {
		return change
	}
	r.recordHosted(ctx, d.Name, before, hostedIn(d))
	// Each capability reads its own acceptance rule: the person accepts each
	// named store from this program, the resource table's card is held by the
	// program that declared it, and a remote runtime's hosts serve the
	// operator programs. An existing rule, a deny included, stays as it is.
	by := rwire.Subject{Account: caller.Account, Program: caller.Program}
	errs := r.operator.permitStoreRules(by, d.Program, d.resources("store"))
	errs = append(errs, r.operator.permitCardRules(by, d.Program, d.resources("card"))...)
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
	// Unhosting the last model withdraws the model host (models host's
	// declareHost): every entry it held is a removal the person made, and
	// it holds when a later model host is declared (DM-1).
	before := r.hostedBefore(name)
	change := r.providers.removeAdmitted(ctx, expected, name, r.operator.providerEditAdmission(ctx, caller, ActionProviderManage))
	if change.Outcome == wire.DeclarationEditOutcomeApplied {
		r.recordHosted(ctx, name, before, nil)
	}
	return change
}

// hostedBefore is the model host's allowlist before an edit of name; nil for
// any other declaration.
func (r *runtimeRegistry) hostedBefore(name string) []hostedModel {
	if name != modelHostName {
		return nil
	}
	r.providers.mu.Lock()
	files, _, err := r.providers.read()
	r.providers.mu.Unlock()
	if err != nil {
		return nil
	}
	for _, f := range files {
		if f.Declaration.Name == name && !f.Disabled {
			return hostedIn(f.Declaration)
		}
	}
	return nil
}

// recordHosted keeps the person's removals from the model host, so the
// reconciler leaves a removed model out until it is hosted again
// (models_discovered.go). A record that cannot be written is reported; the
// edit itself stands.
func (r *runtimeRegistry) recordHosted(ctx context.Context, name string, before, after []hostedModel) {
	if name != modelHostName {
		return
	}
	if err := r.providers.recordModelHostEdit(ctx, before, after); err != nil {
		r.providers.report(fmt.Errorf("model host removals: %w", err))
	}
}

// Observe waits for the declarations or their readings to differ from cursor.
func (r *runtimeRegistry) Observe(ctx context.Context, caller inference.Subject, cursor string, waitMS int64) wire.DeclarationObservation {
	outcome, now, list := r.observe(ctx, caller, cursor, waitMS, 0)
	states := make([]wire.DeclarationState, len(list))
	for i, entry := range list {
		states[i] = entry.State
	}
	return wire.DeclarationObservation{Outcome: outcome, Cursor: now, Declarations: states}
}

func (r *runtimeRegistry) ObserveV2(ctx context.Context, caller inference.Subject, cursor string, waitMS int64) wire.DeclarationObservationV2 {
	outcome, now, list := r.observe(ctx, caller, cursor, waitMS, 1)
	return wire.DeclarationObservationV2{Outcome: outcome, Cursor: now, Declarations: declarationStatesV2(list)}
}

func (r *runtimeRegistry) ObservePinned(ctx context.Context, caller inference.Subject, cursor string, waitMS int64) wire.PinnedDeclarationObservation {
	outcome, now, list := r.observe(ctx, caller, cursor, waitMS, 2)
	return wire.PinnedDeclarationObservation{Outcome: outcome, Cursor: now, Declarations: list}
}

func (r *runtimeRegistry) observe(ctx context.Context, caller inference.Subject, cursor string, waitMS int64, profile int) (wire.DeclarationListOutcome, string, []wire.PinnedDeclarationState) {
	if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
		return declarationListRefusal(word), "", []wire.PinnedDeclarationState{}
	}
	if waitMS < 0 || waitMS > maxObserveWait {
		return wire.DeclarationListOutcomeInvalid, "", []wire.PinnedDeclarationState{}
	}
	var now string
	var list []wire.PinnedDeclarationState
	outcome, err := r.providers.observers().Wait(ctx, time.Duration(waitMS)*time.Millisecond, watch.Probe{
		Read: func() (bool, error) {
			var err error
			list, _, err = r.providers.listPinned()
			if err != nil {
				r.providers.report(err)
				return false, err
			}
			var raw []byte
			if profile == 2 {
				raw, err = json.Marshal(list)
			} else if profile == 1 {
				raw, err = json.Marshal(declarationStatesV2(list))
			} else {
				states := make([]wire.DeclarationState, len(list))
				for i, entry := range list {
					states[i] = entry.State
				}
				raw, err = json.Marshal(states)
			}
			if err != nil {
				return false, err
			}
			sum := sha256.Sum256(raw)
			prefix := "registry-v1:"
			if profile == 2 {
				prefix = "registry-pinned:"
			} else if profile == 1 {
				prefix = "registry-v2:"
			}
			now = prefix + hex.EncodeToString(sum[:12])
			return cursor == "" || now != cursor, nil
		},
		Recheck: func() error {
			if word := r.operator.gate(ctx, caller, ActionProviderManage); word != "" {
				return registryRefusal(word)
			}
			return nil
		},
	})
	var refused registryRefusal
	switch {
	case errors.As(err, &refused):
		return declarationListRefusal(string(refused)), "", []wire.PinnedDeclarationState{}
	case err != nil, outcome == watch.Busy, outcome == watch.Failed:
		return wire.DeclarationListOutcomeUnavailable, "", []wire.PinnedDeclarationState{}
	}
	return wire.DeclarationListOutcomePage, now, list
}

// registryRefusal carries the operator gate's word through the shared waiter.
type registryRefusal string

func (r registryRefusal) Error() string { return "registry: " + string(r) }

// registryHost serves registry@1, and endpoint@1 beside it, on its endpoint.
type registryHost struct {
	listener listen.Listener
	registry *runtimeRegistry
	pairing  *runtimePairing
	owner    string
	report   func(error)
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	workers  sync.WaitGroup
	slots    listen.CallAdmission
}

// RegistryContracts reports only services composed into this local receiver.
func (h *registryHost) RegistryContracts() []string {
	contracts := []string{RegistryContract, RegistryV2Contract, RegistryV3Contract}
	if h.pairing != nil {
		contracts = append(contracts, machineControlContract)
	}
	return contracts
}

func listenRegistry(endpoint string, registry *runtimeRegistry, owner string, report func(error)) (*registryHost, error) {
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &registryHost{listener: listen.Sessions(l, listen.SessionOptions{MaxSessions: 64}), registry: registry, owner: owner, report: report, ctx: ctx, cancel: cancel, slots: listen.NewCallAdmission(64)}, nil
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
		release, admitted := h.slots.Take(conn)
		if !admitted {
			continue
		}
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			defer release()
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
				v1 := &wire.RegistryDispatcher{Handler: receiver}
				v2 := &wire.RegistryV2Dispatcher{Handler: &registryV2Receiver{receiver}}
				v3 := &registration.RegistryV3Dispatcher{Handler: &registryV3Receiver{receiver}}
				var machines *mwire.MachineControlDispatcher
				if h.pairing != nil {
					machines = &mwire.MachineControlDispatcher{Handler: &machineControlReceiver{receiver}}
				}
				var envelope struct {
					Service string `json:"service"`
				}
				if err = json.Unmarshal(call.Frame, &envelope); err == nil {
					switch envelope.Service {
					case machineControlContract:
						if machines != nil {
							reply, err = machines.ExchangeFrame(call.Frame)
						} else {
							reply, err = v1.ExchangeFrame(call.Frame)
						}
					case RegistryV2Contract:
						reply, err = v2.ExchangeFrame(call.Frame)
					case RegistryV3Contract:
						reply, err = v3.ExchangeFrame(call.Frame)
					case wire.EndpointContract, "abstraction.facade/endpoint@2":
						if machines != nil {
							reply, err = wire.DescribeEndpoint(call.Frame, "", "", v1, v2, v3, machines)
						} else {
							reply, err = wire.DescribeEndpoint(call.Frame, "", "", v1, v2, v3)
						}
					default:
						reply, err = v1.ExchangeFrame(call.Frame)
					}
				}
				if err == nil {
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

type registryV2Receiver struct{ *registryReceiver }

// machineControlReceiver retains native Program evidence and rechecks it at
// each admission and again at each state or Rights effect.
type machineControlReceiver struct{ *registryReceiver }

func (r *machineControlReceiver) authorize() error {
	if err := r.call.Recheck(); err != nil {
		return err
	}
	peer, err := r.call.Peer()
	if err != nil {
		return err
	}
	return r.host.pairing.rights.authorizeOperator(r.ctx, peer)
}
func (r *machineControlReceiver) StartOffer(request mwire.MachineRequestIdentity, address string, preparation mwire.MachinePreparation) (mwire.MachineStart, error) {
	return r.host.pairing.StartOffer(r.ctx, request, address, preparation, r.authorize), nil
}
func (r *machineControlReceiver) Enter(request mwire.MachineRequestIdentity, code string, preparation mwire.MachinePreparation) (mwire.MachineStart, error) {
	return r.host.pairing.Enter(r.ctx, request, code, preparation, r.authorize), nil
}
func (r *machineControlReceiver) Observe(operation, cursor string, wait int64) (mwire.MachineObservation, error) {
	return r.host.pairing.Observe(r.ctx, operation, cursor, wait, r.authorize), nil
}
func (r *machineControlReceiver) Cancel(operation string) (mwire.MachineCancellation, error) {
	return r.host.pairing.Cancel(r.ctx, operation, r.authorize), nil
}
func (r *machineControlReceiver) List(cursor string, limit int64) (mwire.MachinePage, error) {
	return r.host.pairing.List(r.ctx, cursor, limit, r.authorize), nil
}
func (r *machineControlReceiver) Remove(revision, key string) (mwire.MachineRemoval, error) {
	return r.host.pairing.Remove(r.ctx, revision, key, r.authorize), nil
}

func (r *registryV2Receiver) Declarations() (wire.DeclarationListV2, error) {
	return r.host.registry.DeclarationsV2(r.ctx, r.caller()), nil
}

func (r *registryV2Receiver) Declare(expectedRevision string, declaration wire.DeclarationWithExtensions) (wire.DeclarationChange, error) {
	return r.host.registry.DeclareV2(r.ctx, r.caller(), expectedRevision, declaration), nil
}

func (r *registryV2Receiver) Observe(cursor string, waitMs int64) (wire.DeclarationObservationV2, error) {
	return r.host.registry.ObserveV2(r.ctx, r.caller(), cursor, waitMs), nil
}

func (r *registryV2Receiver) DeclarationsPinned() (wire.PinnedDeclarationList, error) {
	return r.host.registry.DeclarationsPinned(r.ctx, r.caller()), nil
}
func (r *registryV2Receiver) DeclarePinned(expected string, declaration wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	return r.host.registry.DeclarePinned(r.ctx, r.caller(), expected, declaration), nil
}
func (r *registryV2Receiver) ReplacePinned(expected string, declaration wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	return r.host.registry.ReplacePinned(r.ctx, r.caller(), expected, declaration), nil
}
func (r *registryV2Receiver) ObservePinned(cursor string, waitMs int64) (wire.PinnedDeclarationObservation, error) {
	return r.host.registry.ObservePinned(r.ctx, r.caller(), cursor, waitMs), nil
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
