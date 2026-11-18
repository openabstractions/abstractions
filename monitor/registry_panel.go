package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	rclient "github.com/openabstractions/abstraction-router/go/client"
)

// registryView is one reading of the runtime's catalogue sources: the
// configured and declared hosts, the provider declarations of
// abstraction.facade/registry@1, and the hosts remote runtimes report. Each
// part carries its own outcome or error, so one refusal leaves the others
// readable.
type registryView struct {
	Hosts         iclient.HostList      `json:"hosts"`
	HostsError    string                `json:"hosts_error,omitempty"`
	Providers     fwire.DeclarationList `json:"providers"`
	ProviderError string                `json:"providers_error,omitempty"`
	Remote        []rclient.HostState   `json:"remote"`
	RemoteError   string                `json:"remote_error,omitempty"`
	// Titles reads each declaration by its provider title (providerDisplay),
	// keyed by its name; the name itself stays on the row's details.
	Titles map[string]string `json:"titles"`
	// Paired reports a remote runtime declaration: only then is a failed
	// read of other PCs' providers an error the page shows.
	Paired bool `json:"paired"`
}

// registryRoutes serves the registry page and its reading. Hosts come from the
// resolved abstraction.inference/operator@1, decided as host.manage for this
// Panel; declarations from abstraction.facade/registry@1, decided as
// provider.manage; remote runtimes' hosts from abstraction.router/router@1,
// decided as inventory.read.
func (p *servicePanel) registryRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/registry", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("k") != key {
			http.Error(w, "panel key required", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		//unchecked: a failed write means the client is already gone; there is no response left to recover
		_, _ = io.WriteString(w, panelPage(r, key, "registry", "Registry", registryPage))
	})
	mux.HandleFunc("/registry/view", guard(key, p.registryView))
	mux.HandleFunc("/registry/providers", guard(key, p.registryProviders))
	mux.HandleFunc("/registry/programs", guard(key, p.programReviews))
	mux.HandleFunc("/registry/selection", guard(key, p.registrySelection))
}

// registryProviderEdit is the Panel's deliberately narrow provider form. It
// accepts the public Registry declaration fields for a managed native
// provider; hosts and remote runtimes have their own contracts and controls.
type registryProviderEdit struct {
	Edit             string   `json:"edit"`
	Revision         string   `json:"revision"`
	Name             string   `json:"name"`
	Program          string   `json:"program"`
	Arguments        []string `json:"arguments"`
	Endpoint         string   `json:"endpoint"`
	Activation       string   `json:"activation"`
	AuthorizeStartup bool     `json:"authorize_startup"`
	Contracts        []string `json:"contracts"`
	Guarantees       []string `json:"guarantees"`
	Resources        []string `json:"resources"`
	Models           []string `json:"models"`
}

var registryProviderName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var registryEndpoint = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
var registryCapability = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,255}$`)
var selectionPolicyName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type registrySelectionCandidate = iwire.SelectionCandidateV2

type registrySelectionEdit struct {
	Edit           string                       `json:"edit"`
	TargetProgram  string                       `json:"target_program"`
	AccountDefault bool                         `json:"account_default"`
	Name           string                       `json:"name"`
	Revision       string                       `json:"revision"`
	Candidates     []registrySelectionCandidate `json:"-"`
}

type registrySelectionResult struct {
	Read   string `json:"read,omitempty"`
	Change string `json:"change,omitempty"`
}

func (e registryProviderEdit) valid() bool {
	if !registryProviderName.MatchString(e.Name) || !rightsText(e.Revision, 128) {
		return false
	}
	if e.Edit == "withdraw" {
		return e.Activation == "" && !e.AuthorizeStartup && e.Program == "" && e.Endpoint == "" && len(e.Arguments) == 0 && len(e.Contracts) == 0 && len(e.Guarantees) == 0 && len(e.Resources) == 0 && len(e.Models) == 0
	}
	if e.Edit != "declare" || (e.Activation != "on_demand" && e.Activation != "attach") {
		return false
	}
	if !rightsText(e.Program, 4096) || !filepath.IsAbs(e.Program) || !registryEndpoint.MatchString(e.Endpoint) ||
		len(e.Contracts) == 0 || len(e.Contracts) > 16 || len(e.Arguments) > 64 || len(e.Guarantees) > 16 || len(e.Resources) > 64 || len(e.Models) > 64 {
		return false
	}
	if e.AuthorizeStartup != (e.Activation == "on_demand") {
		return false
	}
	for _, s := range append(append(append(append([]string{}, e.Contracts...), e.Guarantees...), e.Resources...), e.Models...) {
		if !rightsText(s, 256) || !registryCapability.MatchString(s) {
			return false
		}
	}
	for _, a := range e.Arguments {
		if !rightsText(a, 4096) {
			return false
		}
	}
	return true
}

func (e registrySelectionEdit) valid() bool {
	if e.AccountDefault && e.TargetProgram != "" {
		return false
	}
	if e.TargetProgram != "" && (!identity.ValidSubjectProgram(e.TargetProgram) || !filepath.IsAbs(e.TargetProgram)) || !selectionPolicyName.MatchString(e.Name) {
		return false
	}
	switch e.Edit {
	case "read":
		return e.Revision == "" && len(e.Candidates) == 0
	case "effective":
		// What an app without its own policy of this name follows: the
		// selection owner's resolution, read in this Panel's own scope.
		return !e.AccountDefault && e.TargetProgram == "" && e.Revision == "" && len(e.Candidates) == 0
	case "delete":
		return rightsText(e.Revision, 128) && len(e.Candidates) == 0
	case "put":
		if (e.Revision != "" && !rightsText(e.Revision, 128)) || len(e.Candidates) < 1 || len(e.Candidates) > 8 {
			return false
		}
		seen := map[string]bool{}
		for _, c := range e.Candidates {
			if !rightsText(c.Model, 256) || strings.TrimSpace(c.Model) != c.Model {
				return false
			}
			if c.Placement == iwire.CandidatePlacementLocal {
				if c.Credential != "" {
					return false
				}
			} else if c.Placement == iwire.CandidatePlacementHosted {
				if !validCredentialName(c.Credential) {
					return false
				}
			} else {
				return false
			}
			if c.Provider != "" && !registryProviderName.MatchString(c.Provider) {
				return false
			}
			key := c.Placement.String() + "\x00" + c.Model + "\x00" + c.Credential + "\x00" + c.Provider
			if seen[key] {
				return false
			}
			seen[key] = true
		}
		return true
	}
	return false
}

func (p *servicePanel) registryProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var edit registryProviderEdit
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	if err := decoder.Decode(&edit); err != nil || decoder.Decode(new(any)) != io.EOF || !edit.valid() {
		http.Error(w, "invalid provider edit", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	registry, err := panelMachine().ResolveRegistry(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	var result fwire.DeclarationChange
	switch edit.Edit {
	case "declare":
		activation := fwire.ActivationAttach
		if edit.Activation == "on_demand" {
			activation = fwire.ActivationOnDemand
		}
		declaration := fwire.Declaration{Name: edit.Name, Program: edit.Program, Arguments: edit.Arguments, Endpoint: edit.Endpoint,
			Transport: fwire.DeclarationTransportNative, Contracts: edit.Contracts, Guarantees: edit.Guarantees,
			Resources: edit.Resources, Models: edit.Models, Activation: activation, Role: fwire.DeclarationRoleProvider}
		p.logAction(ctx, "registry.provider.declare", map[string]string{"provider.name": edit.Name, "provider.program": edit.Program, "provider.endpoint": edit.Endpoint, "provider.activation": edit.Activation, "registry.revision": edit.Revision})
		result, err = registry.Declare(ctx, edit.Revision, declaration)
	case "withdraw":
		p.logAction(ctx, "registry.provider.withdraw", map[string]string{"provider.name": edit.Name, "registry.revision": edit.Revision})
		result, err = registry.Withdraw(ctx, edit.Revision, edit.Name)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}

func (p *servicePanel) registrySelection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var edit registrySelectionEdit
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	if err := decoder.Decode(&edit); err != nil || decoder.Decode(new(any)) != io.EOF || !edit.valid() {
		http.Error(w, "invalid selection policy edit", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveInferenceSelectionOperatorV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	result := registrySelectionResult{}
	scope := "program"
	if edit.AccountDefault {
		scope = "account default"
	}
	switch edit.Edit {
	case "read":
		p.logAction(ctx, "registry.selection.read", map[string]string{"selection.scope": scope, "selection.target_program": edit.TargetProgram, "selection.name": edit.Name})
		var read iclient.SelectionReadV2
		if edit.AccountDefault {
			read, err = operator.ReadAccountDefault(ctx, edit.Name)
		} else {
			read, err = operator.Read(ctx, edit.TargetProgram, edit.Name)
		}
		if err != nil {
			panelError(w, err)
			return
		}
		result.Read = string(iwire.EncodeSelectionReadV2(&read))
	case "effective":
		read, err := operator.ReadEffective(ctx, edit.Name)
		if err != nil {
			panelError(w, err)
			return
		}
		result.Read = string(iwire.EncodeSelectionReadV2(&read))
	case "put":
		candidates := edit.Candidates
		p.logAction(ctx, "registry.selection.put", map[string]string{"selection.scope": scope, "selection.target_program": edit.TargetProgram, "selection.name": edit.Name, "selection.revision": edit.Revision, "selection.candidates": strconv.Itoa(len(candidates))})
		var change iclient.SelectionChange
		if edit.AccountDefault {
			change, err = operator.PutAccountDefault(ctx, edit.Revision, iclient.SelectionPolicyV2{Name: edit.Name, Candidates: candidates})
		} else {
			change, err = operator.Put(ctx, edit.TargetProgram, edit.Revision, iclient.SelectionPolicyV2{Name: edit.Name, Candidates: candidates})
		}
		if err != nil {
			panelError(w, err)
			return
		}
		result.Change = string(iwire.EncodeSelectionChange(&change))
	case "delete":
		p.logAction(ctx, "registry.selection.delete", map[string]string{"selection.scope": scope, "selection.target_program": edit.TargetProgram, "selection.name": edit.Name, "selection.revision": edit.Revision})
		var change iclient.SelectionChange
		if edit.AccountDefault {
			change, err = operator.DeleteAccountDefault(ctx, edit.Revision, edit.Name)
		} else {
			change, err = operator.DeletePolicy(ctx, edit.TargetProgram, edit.Revision, edit.Name)
		}
		if err != nil {
			panelError(w, err)
			return
		}
		result.Change = string(iwire.EncodeSelectionChange(&change))
	}
	panelJSON(w, result)
}

func (p *servicePanel) registryView(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET required", 405)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	view := registryView{Hosts: iclient.HostList{Hosts: []iclient.HostState{}}, Providers: fwire.DeclarationList{Declarations: []fwire.DeclarationState{}},
		Remote: []rclient.HostState{}, Titles: map[string]string{}}
	machine := panelMachine()
	if operator, err := machine.ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal}); err != nil {
		view.HostsError = err.Error()
	} else if hosts, err := operator.Hosts(ctx); err != nil {
		view.HostsError = err.Error()
	} else {
		view.Hosts = hosts
	}
	if registry, err := machine.ResolveRegistry(ctx, facade.Requirements{Scope: facade.ScopeLocal}); err != nil {
		view.ProviderError = err.Error()
	} else if declarations, err := registry.Declarations(ctx); err != nil {
		view.ProviderError = err.Error()
	} else {
		view.Providers = declarations
		for _, state := range withStandings(declarations.Declarations, readProviderStandings(ctx, machine)) {
			title, _ := providerDisplay(state.Declaration.Name, shippedProvider(state))
			if reservedTitle(title) {
				title, _ = providerDisplay(state.Declaration.Name, false)
			}
			view.Titles[state.Declaration.Name] = title
			if state.Role == fwire.DeclarationRoleRemote {
				view.Paired = true
			}
		}
	}
	if routes, err := machine.ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal}); err != nil {
		view.RemoteError = err.Error()
	} else if snapshot, err := routes.HostsContext(ctx, false); err != nil {
		view.RemoteError = err.Error()
	} else {
		for _, h := range snapshot.Hosts {
			if h.Domain != "" && h.Domain != h.Host {
				view.Remote = append(view.Remote, h)
			}
		}
	}
	panelJSON(w, view)
}
