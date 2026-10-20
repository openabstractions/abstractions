package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

// registryOperator answers hosts from three sources and one provider.
type registryOperator struct{ *panelOperator }

func (o registryOperator) Hosts(_ context.Context, s inference.Subject) iwire.HostList {
	o.seen("hosts", s)
	return iwire.HostList{Outcome: iwire.ListOutcomePage, Revision: "hosts-v1:r", Hosts: []iwire.HostState{
		{Entry: iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:11500", Profiles: []string{"chat", "embed"}, DeclaredBy: "ollama"}, Up: true},
		{Entry: iwire.HostEntry{Name: "lemonade", Kind: "lemonade", Base: "http://127.0.0.1:13305", Profiles: []string{"chat"}, DeclaredBy: "default"}, Why: "unreachable"},
	}}
}

type fixedSelectionOperatorV2 struct {
	mu             sync.Mutex
	readName       string
	readTarget     string
	readCaller     inference.Subject
	readOutcome    iwire.SelectionReadOutcome
	putOutcome     iwire.EditOutcome
	deleteOutcome  iwire.EditOutcome
	denyOther      bool
	putRevision    string
	putTarget      string
	putPolicy      iwire.SelectionPolicyV2
	putCaller      inference.Subject
	deleteRevision string
	deleteTarget   string
	deleteName     string
	deleteCaller   inference.Subject
}

func (s panelInference) SelectionOperatorV2Available() bool { return s.Host.SelectionOperatorV2 != nil }

func (s *fixedSelectionOperatorV2) ReadSelectionV2(_ context.Context, caller inference.Subject, targetProgram, name string) iwire.SelectionReadV2 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readName, s.readTarget, s.readCaller = name, targetProgram, caller
	if s.denyOther && targetProgram != "" && targetProgram != caller.Program {
		return iwire.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeForbidden}
	}
	outcome := s.readOutcome
	if outcome == 0 {
		outcome = iwire.SelectionReadOutcomeFound
	}
	if outcome != iwire.SelectionReadOutcomeFound {
		return iwire.SelectionReadV2{Outcome: outcome}
	}
	return iwire.SelectionReadV2{Outcome: outcome, Revision: "selection-v1:7", Policy: &iwire.SelectionPolicyV2{Name: name, Candidates: []iwire.SelectionCandidateV2{{Model: "qwen/tiny", Placement: iwire.CandidatePlacementLocal, Provider: "native-runtime"}}}}
}
func (s *fixedSelectionOperatorV2) PutSelectionV2(_ context.Context, caller inference.Subject, targetProgram, expected string, policy iwire.SelectionPolicyV2) iwire.SelectionChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putRevision, s.putTarget, s.putPolicy, s.putCaller = expected, targetProgram, policy, caller
	if s.denyOther && targetProgram != "" && targetProgram != caller.Program {
		return iwire.SelectionChange{Outcome: iwire.EditOutcomeForbidden}
	}
	if s.putOutcome != 0 {
		change := iwire.SelectionChange{Outcome: s.putOutcome}
		if s.putOutcome == iwire.EditOutcomeConflict {
			change.Revision = "selection-v1:7"
		}
		return change
	}
	return iwire.SelectionChange{Outcome: iwire.EditOutcomeApplied, Revision: "selection-v1:8"}
}
func (s *fixedSelectionOperatorV2) DeleteSelectionV2(_ context.Context, caller inference.Subject, targetProgram, expected, name string) iwire.SelectionChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteRevision, s.deleteTarget, s.deleteName, s.deleteCaller = expected, targetProgram, name, caller
	if s.denyOther && targetProgram != "" && targetProgram != caller.Program {
		return iwire.SelectionChange{Outcome: iwire.EditOutcomeForbidden}
	}
	if s.deleteOutcome != 0 {
		change := iwire.SelectionChange{Outcome: s.deleteOutcome}
		if s.deleteOutcome == iwire.EditOutcomeConflict {
			change.Revision = "selection-v1:7"
		}
		return change
	}
	return iwire.SelectionChange{Outcome: iwire.EditOutcomeApplied, Revision: "selection-v1:9"}
}

// fixedRegistry answers registry@1 with one declaration of each role -
// remote, provider and host, in name order as Declarations() promises - and
// records each call.
type fixedRegistry struct {
	mu               sync.Mutex
	calls            int
	declareCalls     int
	declaredRevision string
	declaration      fwire.Declaration
	withdrawRevision string
	withdrawName     string
}

func (r *fixedRegistry) Declarations() (fwire.DeclarationList, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Revision: "providers-v2:r", Declarations: []fwire.DeclarationState{
		{
			Declaration: fwire.Declaration{Name: "lab-remote", Arguments: []string{}, Endpoint: "tls://lab.example:8443", Transport: fwire.DeclarationTransportRemote,
				Contracts: []string{"abstraction.inference/chat@1"}, Activation: fwire.ActivationRemote, Role: fwire.DeclarationRoleRemote, Resources: []string{"profile:chat"},
				Remote: &fwire.RemoteTrust{ServerName: "lab.example", Roots: "/etc/oa/lab-roots.pem", Certificate: "/etc/oa/lab-cert.pem", Key: "/etc/oa/lab-key.pem"}},
			DeclaredBy: "/opt/oa/openabstractions", Readiness: fwire.DeclarationReadinessReady, Role: fwire.DeclarationRoleRemote,
			Described: []fwire.ServiceState{{Contract: "abstraction.inference/chat@1", Readiness: fwire.ServiceReadinessReady, Guarantees: []string{}, Capabilities: map[string]string{}}},
		},
		{
			Declaration: fwire.Declaration{Name: "local-stores", Program: "/opt/inventoryd", Arguments: []string{}, Endpoint: "inventoryd-v1", Transport: fwire.DeclarationTransportNative,
				Contracts: []string{"abstraction.storage/inventory-source@1"}, Activation: fwire.ActivationOnDemand, Role: fwire.DeclarationRoleProvider, Resources: []string{"store:ollama", "store:huggingface"}},
			DeclaredBy: "/opt/oa/openabstractions", Readiness: fwire.DeclarationReadinessReady, Role: fwire.DeclarationRoleProvider, Restarts: 2, Accepted: []string{"store:ollama"},
			Described: []fwire.ServiceState{{Contract: "abstraction.storage/inventory-source@1", Readiness: fwire.ServiceReadinessReady, Guarantees: []string{}, Capabilities: map[string]string{}}},
		},
		{
			Declaration: fwire.Declaration{Name: "ollama-engine", Arguments: []string{}, Transport: fwire.DeclarationTransportHTTP, Activation: fwire.ActivationAttach,
				Role: fwire.DeclarationRoleHost, Resources: []string{"profile:chat"}, Host: &fwire.DeclarationHost{Base: "http://127.0.0.1:11500", Kind: "ollama", Hosted: false}},
			DeclaredBy: "installation", Readiness: fwire.DeclarationReadinessReady, Role: fwire.DeclarationRoleHost, Host: &fwire.HostReading{Up: true},
			Described: []fwire.ServiceState{},
		},
	}}, nil
}
func (r *fixedRegistry) Declare(revision string, declaration fwire.Declaration) (fwire.DeclarationChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.declareCalls++
	r.declaredRevision, r.declaration = revision, declaration
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeApplied, Revision: "providers-v2:next"}, nil
}
func (r *fixedRegistry) Withdraw(revision, name string) (fwire.DeclarationChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.withdrawRevision, r.withdrawName = revision, name
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeApplied, Revision: "providers-v2:next"}, nil
}
func (r *fixedRegistry) Observe(string, int64) (fwire.DeclarationObservation, error) {
	return fwire.DeclarationObservation{Outcome: fwire.DeclarationListOutcomeForbidden, Declarations: []fwire.DeclarationState{}}, nil
}

// registryService serves a fixedRegistry on its endpoint.
type registryService struct {
	listener listen.Listener
	handler  fwire.Registry
	v2       fwire.RegistryV2
}

func TestRegistrySelectionPolicyNameMatchesRuntimeGrammar(t *testing.T) {
	valid := registrySelectionEdit{Edit: "read", Name: "oa-image-default"}
	if !valid.valid() {
		t.Fatal("lowercase policy name with hyphens should be accepted")
	}
	for _, name := range []string{"OA-image", "oa.image", "-image", "", "has space"} {
		t.Run(name, func(t *testing.T) {
			edit := valid
			edit.Name = name
			if edit.valid() {
				t.Fatalf("policy name %q accepted by Panel although runtime rejects it", name)
			}
		})
	}
}

func (s registryService) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { s.listener.Close() })
	defer stop()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return nil
		}
		go func() {
			defer conn.Close()
			call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err != nil {
				return
			}
			var envelope struct {
				Service string `json:"service"`
			}
			if json.Unmarshal(call.Frame, &envelope) != nil {
				return
			}
			var reply []byte
			if envelope.Service == "abstraction.facade/registry@2" && s.v2 != nil {
				reply, err = (&fwire.RegistryV2Dispatcher{Handler: s.v2}).ExchangeFrame(call.Frame)
			} else {
				reply, err = (&fwire.RegistryDispatcher{Handler: s.handler}).ExchangeFrame(call.Frame)
			}
			if err == nil {
				call.Reply(reply)
			}
		}()
	}
}
func (s registryService) Close() error { return s.listener.Close() }

// The registry view carries hosts with who declared each and their profiles,
// providers with readiness and accepted stores, and reports the router it
// could not resolve without hiding the rest.
func TestPanelRegistryReadsHostsProvidersAndRemotes(t *testing.T) {
	own(t)
	operator := registryOperator{&panelOperator{}}
	registry := &fixedRegistry{}
	selection := &fixedSelectionOperatorV2{}
	panelConfigRuntimeWith(t, func(o *host.Options) {
		provider, err := inference.New(inference.Config{Router: router.New(),
			Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "not_granted", nil },
			Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
				return nil, "unknown"
			}})
		if err != nil {
			t.Fatal(err)
		}
		endpoint := o.ConfigEndpoint + "-inference"
		h, err := inferenceservice.Listen(endpoint, provider)
		if err != nil {
			t.Fatal(err)
		}
		h.Operator = operator
		h.SelectionOperatorV2 = selection
		o.Inference, o.InferenceEndpoint = panelInference{Host: h, provider: provider}, endpoint
		registryAt := o.ConfigEndpoint + "-registry"
		l, err := listen.Listen(registryAt)
		if err != nil {
			t.Fatal(err)
		}
		o.Registry, o.RegistryEndpoint = registryService{listener: l, handler: registry}, registryAt
	})
	h := newTestPanel(t).handler("test-key")
	directSelection, err := panelMachine().ResolveInferenceSelectionOperatorV2(context.Background(), facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	if r := panelRequest(t, h, "/registry?k=wrong", nil); r.Code != 403 {
		t.Fatalf("page without the key: %d", r.Code)
	}
	if r := panelRequest(t, h, "/registry?k=test-key", nil); r.Code != 200 {
		t.Fatalf("page: %d", r.Code)
	}
	view := decodePanel[registryView](t, 200, panelRequest(t, h, "/registry/view", nil).Body.Bytes())
	if view.HostsError != "" || len(view.Hosts.Hosts) != 2 || view.Hosts.Hosts[0].Entry.DeclaredBy != "ollama" {
		t.Fatalf("hosts %+v", view)
	}
	p := view.Providers.Declarations
	if view.ProviderError != "" || len(p) != 3 {
		t.Fatalf("providers %+v %s", view.Providers, view.ProviderError)
	}
	if p[0].Role != fwire.DeclarationRoleRemote || p[0].Declaration.Role != fwire.DeclarationRoleRemote || p[0].Declaration.Remote.ServerName != "lab.example" {
		t.Fatalf("remote declaration %+v", p[0])
	}
	if p[1].Role != fwire.DeclarationRoleProvider || p[1].Readiness != fwire.DeclarationReadinessReady || p[1].Accepted[0] != "store:ollama" || p[1].Restarts != 2 ||
		len(p[1].Described) != 1 {
		t.Fatalf("provider declaration %+v", p[1])
	}
	if p[2].Role != fwire.DeclarationRoleHost || p[2].Declaration.Host.Base != "http://127.0.0.1:11500" || p[2].DeclaredBy != "installation" || p[2].Host == nil || !p[2].Host.Up {
		t.Fatalf("host declaration %+v", p[2])
	}
	badProvider := struct {
		Edit, Revision, Name, Program, Endpoint, Activation string
		Contracts                                           []string
	}{"declare", "providers-v2:r", "bad name", "/opt/provider", "provider-v1", "on_demand", []string{"abstraction.inference/chat@1"}}
	if got := panelRequest(t, h, "/registry/providers", badProvider); got.Code != 400 {
		t.Fatalf("invalid provider declaration: %d %s", got.Code, got.Body.String())
	}
	programPath := filepath.Join(t.TempDir(), "engine")
	provider := struct {
		Edit             string   `json:"edit"`
		Revision         string   `json:"revision"`
		Name             string   `json:"name"`
		Program          string   `json:"program"`
		Endpoint         string   `json:"endpoint"`
		Activation       string   `json:"activation"`
		AuthorizeStartup bool     `json:"authorize_startup"`
		Arguments        []string `json:"arguments"`
		Contracts        []string `json:"contracts"`
		Guarantees       []string `json:"guarantees"`
		Resources        []string `json:"resources"`
		Models           []string `json:"models"`
	}{"declare", "providers-v2:r", "local-engine", programPath, "engine-v1", "on_demand", true, []string{"--socket", "{endpoint}"}, []string{"abstraction.inference/chat@1"}, []string{"streaming"}, []string{"profile:chat"}, []string{"qwen/tiny"}}
	unconfirmed := provider
	unconfirmed.AuthorizeStartup = false
	if got := panelRequest(t, h, "/registry/providers", unconfirmed); got.Code != 400 {
		t.Fatalf("on-demand registration without explicit startup confirmation: %d %s", got.Code, got.Body.String())
	}
	registry.mu.Lock()
	unconfirmedDeclareCalls := registry.declareCalls
	registry.mu.Unlock()
	if unconfirmedDeclareCalls != 0 {
		t.Fatalf("unconfirmed on-demand POST reached Registry.Declare (%d calls)", unconfirmedDeclareCalls)
	}
	change := decodePanel[fwire.DeclarationChange](t, 200, panelRequest(t, h, "/registry/providers", provider).Body.Bytes())
	if change.Outcome != fwire.DeclarationEditOutcomeApplied {
		t.Fatalf("Declare outcome: %+v", change)
	}
	registry.mu.Lock()
	declaration, declaredRevision := registry.declaration, registry.declaredRevision
	registry.mu.Unlock()
	if declaredRevision != "providers-v2:r" || declaration.Name != "local-engine" || declaration.Program != programPath || declaration.Activation != fwire.ActivationOnDemand || declaration.Transport != fwire.DeclarationTransportNative || declaration.Role != fwire.DeclarationRoleProvider || len(declaration.Arguments) != 2 || declaration.Arguments[1] != "{endpoint}" || declaration.Contracts[0] != "abstraction.inference/chat@1" || declaration.Models[0] != "qwen/tiny" {
		t.Fatalf("Declare did not preserve explicit native provider configuration: %+v at %q", declaration, declaredRevision)
	}
	attachProvider := provider
	attachProvider.Name, attachProvider.Activation, attachProvider.AuthorizeStartup = "attached-engine", "attach", false
	attachChange := decodePanel[fwire.DeclarationChange](t, 200, panelRequest(t, h, "/registry/providers", attachProvider).Body.Bytes())
	if attachChange.Outcome != fwire.DeclarationEditOutcomeApplied {
		t.Fatalf("attach registration without launch consent: %+v", attachChange)
	}
	registry.mu.Lock()
	attachDeclaration, declareCalls := registry.declaration, registry.declareCalls
	registry.mu.Unlock()
	if attachDeclaration.Name != "attached-engine" || attachDeclaration.Activation != fwire.ActivationAttach || declareCalls != 2 {
		t.Fatalf("attach was not registered without an OA launch request: %+v calls=%d", attachDeclaration, declareCalls)
	}
	withdraw := struct {
		Edit     string `json:"edit"`
		Revision string `json:"revision"`
		Name     string `json:"name"`
	}{"withdraw", "providers-v2:r", "local-engine"}
	change = decodePanel[fwire.DeclarationChange](t, 200, panelRequest(t, h, "/registry/providers", withdraw).Body.Bytes())
	if change.Outcome != fwire.DeclarationEditOutcomeApplied {
		t.Fatalf("Withdraw outcome: %+v", change)
	}
	registry.mu.Lock()
	withdrawRevision, withdrawName := registry.withdrawRevision, registry.withdrawName
	registry.mu.Unlock()
	if withdrawRevision != withdraw.Revision || withdrawName != withdraw.Name {
		t.Fatalf("Withdraw arguments %q %q", withdrawRevision, withdrawName)
	}
	targetProgram := filepath.Join(t.TempDir(), "chat-app.exe")
	selectionRead := struct {
		Edit          string `json:"edit"`
		TargetProgram string `json:"target_program"`
		Name          string `json:"name"`
	}{"read", targetProgram, "work"}
	selectionReadResponse := panelRequest(t, h, "/registry/selection", selectionRead)
	selectionResult := decodePanel[registrySelectionResult](t, selectionReadResponse.Code, selectionReadResponse.Body.Bytes())
	if selectionResult.Read == nil || selectionResult.Read.Outcome != iwire.SelectionReadOutcomeFound || selectionResult.Read.Revision != "selection-v1:7" || selectionResult.Read.Policy == nil || selectionResult.Read.Policy.Name != "work" || selectionResult.Read.Policy.Candidates[0].Provider != "native-runtime" {
		t.Fatalf("V2 Read result %+v", selectionResult)
	}
	directRead, err := directSelection.Read(context.Background(), targetProgram, "work")
	if err != nil || !reflect.DeepEqual(*selectionResult.Read, directRead) {
		t.Fatalf("Panel and direct target-scoped V2 Read diverged: Panel=%+v direct=%+v err=%v", selectionResult.Read, directRead, err)
	}
	selection.mu.Lock()
	readName, readTarget, readCaller := selection.readName, selection.readTarget, selection.readCaller
	selection.mu.Unlock()
	if readName != "work" || readTarget != targetProgram || readCaller.Account == "" || readCaller.Program == "" || readCaller.Program == targetProgram {
		t.Fatalf("SelectionOperatorV2 did not keep proved caller separate from target scope: name=%q target=%q caller=%+v", readName, readTarget, readCaller)
	}
	selectionPut := struct {
		Edit          string                       `json:"edit"`
		TargetProgram string                       `json:"target_program"`
		Name          string                       `json:"name"`
		Revision      string                       `json:"revision"`
		Candidates    []registrySelectionCandidate `json:"candidates"`
	}{"put", targetProgram, "work", "selection-v1:7", []registrySelectionCandidate{{Model: "qwen/tiny", Placement: "local", Provider: "native-runtime"}, {Model: "cloud/model", Placement: "hosted", Credential: "cloudcred"}}}
	if got := panelRequest(t, h, "/registry/selection", selectionPut); got.Code != 200 {
		t.Fatalf("SelectionOperatorV2 Put: %d %s", got.Code, got.Body.String())
	}
	directPut, err := directSelection.Put(context.Background(), targetProgram, selectionPut.Revision, iwire.SelectionPolicyV2{Name: "work", Candidates: []iwire.SelectionCandidateV2{
		{Model: "qwen/tiny", Placement: iwire.CandidatePlacementLocal, Provider: "native-runtime"},
		{Model: "cloud/model", Placement: iwire.CandidatePlacementHosted, Credential: "cloudcred"}}})
	if err != nil || directPut.Outcome != iwire.EditOutcomeApplied || directPut.Revision != "selection-v1:8" {
		t.Fatalf("direct V2 Put: %+v %v", directPut, err)
	}
	selection.mu.Lock()
	putRevision, putTarget, putPolicy := selection.putRevision, selection.putTarget, selection.putPolicy
	selection.mu.Unlock()
	if putRevision != "selection-v1:7" || putTarget != targetProgram || len(putPolicy.Candidates) != 2 || putPolicy.Candidates[0].Provider != "native-runtime" || putPolicy.Candidates[1].Credential != "cloudcred" {
		t.Fatalf("V2 Put lost target or ordered candidate preferences: target=%q revision %q policy %+v", putTarget, putRevision, putPolicy)
	}
	selectionDelete := struct {
		Edit          string `json:"edit"`
		TargetProgram string `json:"target_program"`
		Name          string `json:"name"`
		Revision      string `json:"revision"`
	}{"delete", targetProgram, "work", "selection-v1:7"}
	deleted := decodePanel[registrySelectionResult](t, 200, panelRequest(t, h, "/registry/selection", selectionDelete).Body.Bytes())
	if deleted.Change == nil || deleted.Change.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("V2 Delete result %+v", deleted)
	}
	directDelete, err := directSelection.DeletePolicy(context.Background(), targetProgram, selectionDelete.Revision, selectionDelete.Name)
	if err != nil || !reflect.DeepEqual(*deleted.Change, directDelete) {
		t.Fatalf("Panel and direct V2 Delete diverged: Panel=%+v direct=%+v err=%v", deleted.Change, directDelete, err)
	}
	selection.mu.Lock()
	deleteRevision, deleteTarget, deleteName := selection.deleteRevision, selection.deleteTarget, selection.deleteName
	selection.mu.Unlock()
	if deleteRevision != "selection-v1:7" || deleteTarget != targetProgram || deleteName != "work" {
		t.Fatalf("V2 Delete args target=%q revision=%q name=%q", deleteTarget, deleteRevision, deleteName)
	}
	selection.mu.Lock()
	selection.readOutcome = iwire.SelectionReadOutcomeForbidden
	selection.mu.Unlock()
	deniedReadResponse := panelRequest(t, h, "/registry/selection", selectionRead)
	deniedSelection := decodePanel[registrySelectionResult](t, deniedReadResponse.Code, deniedReadResponse.Body.Bytes())
	if deniedSelection.Read == nil || deniedSelection.Read.Outcome != iwire.SelectionReadOutcomeForbidden {
		t.Fatalf("SelectionOperatorV2 refusal was obscured: %+v", deniedSelection)
	}
	directDenied, err := directSelection.Read(context.Background(), targetProgram, "work")
	if err != nil || !reflect.DeepEqual(*deniedSelection.Read, directDenied) {
		t.Fatalf("Panel and direct V2 Read refusal diverged: Panel=%+v direct=%+v err=%v", deniedSelection.Read, directDenied, err)
	}
	selection.mu.Lock()
	selection.readOutcome = iwire.SelectionReadOutcomeFound
	selection.putOutcome, selection.deleteOutcome = iwire.EditOutcomeConflict, iwire.EditOutcomeConflict
	selection.mu.Unlock()
	stalePutResponse := panelRequest(t, h, "/registry/selection", selectionPut)
	stalePanelPut := decodePanel[registrySelectionResult](t, stalePutResponse.Code, stalePutResponse.Body.Bytes())
	staleDirectPut, err := directSelection.Put(context.Background(), targetProgram, selectionPut.Revision, iwire.SelectionPolicyV2{Name: "work", Candidates: []iwire.SelectionCandidateV2{{Model: "qwen/tiny", Placement: iwire.CandidatePlacementLocal, Provider: "native-runtime"}}})
	if err != nil || stalePanelPut.Change == nil || stalePanelPut.Change.Outcome != iwire.EditOutcomeConflict || stalePanelPut.Change.Revision != staleDirectPut.Revision || staleDirectPut.Outcome != stalePanelPut.Change.Outcome {
		t.Fatalf("stale V2 Put diverged: Panel=%+v direct=%+v err=%v", stalePanelPut, staleDirectPut, err)
	}
	staleDeleteResponse := panelRequest(t, h, "/registry/selection", selectionDelete)
	stalePanelDelete := decodePanel[registrySelectionResult](t, staleDeleteResponse.Code, staleDeleteResponse.Body.Bytes())
	staleDirectDelete, err := directSelection.DeletePolicy(context.Background(), targetProgram, selectionDelete.Revision, selectionDelete.Name)
	if err != nil || stalePanelDelete.Change == nil || !reflect.DeepEqual(*stalePanelDelete.Change, staleDirectDelete) {
		t.Fatalf("stale V2 Delete diverged: Panel=%+v direct=%+v err=%v", stalePanelDelete, staleDirectDelete, err)
	}
	selection.mu.Lock()
	selection.putOutcome, selection.deleteOutcome = 0, 0
	selection.denyOther = true
	selection.mu.Unlock()
	otherReadResponse := panelRequest(t, h, "/registry/selection", selectionRead)
	otherDeniedPanel := decodePanel[registrySelectionResult](t, otherReadResponse.Code, otherReadResponse.Body.Bytes())
	otherDeniedDirect, err := directSelection.Read(context.Background(), targetProgram, "work")
	if err != nil || otherDeniedPanel.Read == nil || otherDeniedPanel.Read.Outcome != iwire.SelectionReadOutcomeForbidden || otherDeniedDirect.Outcome != otherDeniedPanel.Read.Outcome {
		t.Fatalf("other-program V2 Read refusal diverged: Panel=%+v direct=%+v err=%v", otherDeniedPanel, otherDeniedDirect, err)
	}
	otherPutResponse := panelRequest(t, h, "/registry/selection", selectionPut)
	otherPutPanel := decodePanel[registrySelectionResult](t, otherPutResponse.Code, otherPutResponse.Body.Bytes())
	otherPutDirect, err := directSelection.Put(context.Background(), targetProgram, selectionPut.Revision, iwire.SelectionPolicyV2{Name: "work", Candidates: []iwire.SelectionCandidateV2{{Model: "qwen/tiny", Placement: iwire.CandidatePlacementLocal, Provider: "native-runtime"}}})
	if err != nil || otherPutPanel.Change == nil || otherPutPanel.Change.Outcome != iwire.EditOutcomeForbidden || otherPutDirect.Outcome != otherPutPanel.Change.Outcome {
		t.Fatalf("other-program V2 Put refusal diverged: Panel=%+v direct=%+v err=%v", otherPutPanel, otherPutDirect, err)
	}
	otherDeleteResponse := panelRequest(t, h, "/registry/selection", selectionDelete)
	otherDeletePanel := decodePanel[registrySelectionResult](t, otherDeleteResponse.Code, otherDeleteResponse.Body.Bytes())
	otherDeleteDirect, err := directSelection.DeletePolicy(context.Background(), targetProgram, selectionDelete.Revision, selectionDelete.Name)
	if err != nil || otherDeletePanel.Change == nil || otherDeletePanel.Change.Outcome != iwire.EditOutcomeForbidden || otherDeleteDirect.Outcome != otherDeletePanel.Change.Outcome {
		t.Fatalf("other-program V2 Delete refusal diverged: Panel=%+v direct=%+v err=%v", otherDeletePanel, otherDeleteDirect, err)
	}
	selfRead := selectionRead
	selfRead.TargetProgram = ""
	selfReadResponse := panelRequest(t, h, "/registry/selection", selfRead)
	selfPanel := decodePanel[registrySelectionResult](t, selfReadResponse.Code, selfReadResponse.Body.Bytes())
	selfDirect, err := directSelection.Read(context.Background(), "", "work")
	if err != nil || selfPanel.Read == nil || selfPanel.Read.Outcome != iwire.SelectionReadOutcomeFound || !reflect.DeepEqual(*selfPanel.Read, selfDirect) {
		t.Fatalf("self V2 Read affected by other-program refusal: Panel=%+v direct=%+v err=%v", selfPanel, selfDirect, err)
	}
	if got := panelRequest(t, h, "/registry/providers", nil); got.Code != 405 {
		t.Fatalf("GET edit endpoint: %d", got.Code)
	}
	registry.mu.Lock()
	if registry.calls != 1 {
		t.Fatalf("registry@1 Declarations calls: %d", registry.calls)
	}
	registry.mu.Unlock()
	if view.ModelsError == "" || view.RemoteError == "" || len(view.Remote) != 0 {
		t.Fatalf("a runtime without router@1 reads models and remotes as errors beside the rest: %+v", view)
	}
	operator.mu.Lock()
	defer operator.mu.Unlock()
	for _, s := range operator.callers {
		if s.Program == "" || s.Account == "" {
			t.Fatalf("an operator call was not bound: %+v", s)
		}
	}
}

func TestPanelRegistryReadsModelsOverRouterIPC(t *testing.T) {
	own(t)
	var inventoryAllowed atomic.Bool
	inventoryAllowed.Store(true)
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen/tiny","type":"llm","state":"not-loaded"}]}`))
	}))
	defer models.Close()
	modelRouter := router.New(router.LMStudio(models.URL))
	modelRouter.SetHeld([]router.HeldObject{
		{Store: "lmstudio", Names: []string{"qwen/tiny"}},
		{Store: "comfyui", Names: []string{"diffusion_models/wan.safetensors"}},
	})
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.Router, o.RouterEndpoint = modelRouter, o.ConfigEndpoint+"-router"
		o.RouterPolicy = func(_ context.Context, _ *identity.Peer, action, resource string) error {
			if action != routerservice.ActionInventory || resource != routerservice.ResourceInventory {
				return errors.New("unexpected router action")
			}
			if !inventoryAllowed.Load() {
				return errors.New("router inventory denied by fixture policy")
			}
			return nil
		}
		operator := registryOperator{&panelOperator{}}
		provider, err := inference.New(inference.Config{Router: modelRouter,
			Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "not_granted", nil },
			Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
				return nil, "unknown"
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		inferenceAt := o.ConfigEndpoint + "-inference"
		inferenceHost, err := inferenceservice.Listen(inferenceAt, provider)
		if err != nil {
			t.Fatal(err)
		}
		inferenceHost.Operator = operator
		o.Inference, o.InferenceEndpoint = panelInference{Host: inferenceHost, provider: provider}, inferenceAt
		registryAt := o.ConfigEndpoint + "-registry"
		listener, err := listen.Listen(registryAt)
		if err != nil {
			t.Fatal(err)
		}
		o.Registry, o.RegistryEndpoint = registryService{listener: listener, handler: &fixedRegistry{}}, registryAt
	})
	h := newTestPanel(t).handler("test-key")
	viewResponse := panelRequest(t, h, "/registry/view", nil)
	view := decodePanel[registryView](t, viewResponse.Code, viewResponse.Body.Bytes())
	if view.ModelsError != "" || len(view.Models.Models) < 2 {
		t.Fatalf("model inventory %+v (%s)", view.Models, view.ModelsError)
	}
	ctx := context.Background()
	direct, err := panelMachine().ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	directModels, err := direct.ModelsContext(ctx, true)
	if err != nil || !reflect.DeepEqual(view.Models.Models, directModels.Models) {
		t.Fatalf("Panel and direct fresh model inventory diverged: Panel=%+v direct=%+v err=%v", view.Models.Models, directModels.Models, err)
	}
	directHosts, err := direct.HostsContext(ctx, false)
	if err != nil || len(directHosts.Hosts) == 0 || len(view.Remote) != 0 {
		t.Fatalf("direct hosts and Panel's local-only remote projection: direct=%+v Panel=%+v err=%v", directHosts, view.Remote, err)
	}
	var served, storedOnly bool
	for _, family := range view.Models.Models {
		for _, alias := range family.Names {
			if alias.Name == "qwen/tiny" && alias.Host != "" && alias.Servable && !alias.Resident && len(alias.HeldIn) != 0 {
				served = true
			}
			if alias.Name == "diffusion_models/wan.safetensors" && alias.Host == "" && !alias.Servable && !alias.Resident && len(alias.HeldIn) != 0 {
				storedOnly = true
			}
		}
	}
	if !served || !storedOnly {
		t.Fatalf("served model present=%t, stored-only model present=%t; snapshot %+v", served, storedOnly, view.Models.Models)
	}
	inventoryAllowed.Store(false)
	deniedResponse := panelRequest(t, h, "/registry/view", nil)
	denied := decodePanel[registryView](t, deniedResponse.Code, deniedResponse.Body.Bytes())
	if _, err := direct.ModelsContext(ctx, true); err == nil {
		t.Fatal("direct public router client read inventory after the same caller was denied")
	}
	if _, err := direct.HostsContext(ctx, false); err == nil {
		t.Fatal("direct public router client read hosts after the same caller was denied")
	}
	if denied.ModelsError == "" || denied.RemoteError == "" || denied.HostsError != "" || denied.ProviderError != "" || len(denied.Models.Models) != 0 {
		t.Fatalf("router inventory denial did not preserve independent Panel sections: %+v", denied)
	}
}
