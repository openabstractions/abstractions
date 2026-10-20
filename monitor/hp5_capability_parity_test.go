package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// The Panel and an application use the same public holder endpoint. Alternate
// the caller that writes each revision so neither side can pass by reading its
// own local state, then compare the two views of refusals and secret handling.
func TestHP5CredentialsPanelAndPublicClientShareRevisionsAndRefusals(t *testing.T) {
	own(t)
	policy := panelCredentialsRuntime(t)
	h := (&servicePanel{}).handler("test-key")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	holder, err := panelMachine().ResolveCredentials(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "hp5-parity-secret-never-in-replies"
	const name = "hp5-parity"
	registration := cwire.Registration{Name: name, Kind: "bearer", Scope: cwire.Scope{
		Targets: []string{"huggingface.co"}, Consumers: []string{"abstraction.download/http-execution@1"}}, Secret: []byte(secret)}
	stored, err := holder.Store(ctx, "", registration)
	if err != nil || stored.Outcome != cwire.StoreOutcomeStored || stored.Revision == "" {
		t.Fatalf("direct Store: %+v %v", stored, err)
	}
	if raw, err := json.Marshal(stored); err != nil || bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("direct Store echoed secret: %s %v", raw, err)
	}
	panelResponse := panelRequest(t, h, "/credentials?cursor=", nil)
	panelPage := decodePanel[cwire.MetadataPage](t, panelResponse.Code, panelResponse.Body.Bytes())
	if panelPage.Outcome != cwire.PageOutcomePage || len(panelPage.Records) != 1 || panelPage.Records[0].Revision != stored.Revision {
		t.Fatalf("Panel did not see direct Store revision: %+v", panelPage)
	}
	rotatedResponse := panelRequest(t, h, "/credentials", credentialEdit{Action: "rotate", Name: name, ExpectedRevision: stored.Revision, Secret: secret + "-rotated"})
	rotated := decodePanel[cwire.RotateResult](t, rotatedResponse.Code, rotatedResponse.Body.Bytes())
	if rotated.Outcome != cwire.RotateOutcomeRotated || rotated.Revision == "" || rotated.Revision == stored.Revision || bytes.Contains(rotatedResponse.Body.Bytes(), []byte(secret)) {
		t.Fatalf("Panel Rotate: %+v, body %s", rotated, rotatedResponse.Body.Bytes())
	}
	directPage, err := holder.List(ctx, "", 32)
	if err != nil || directPage.Outcome != cwire.PageOutcomePage || len(directPage.Records) != 1 || directPage.Records[0].Revision != rotated.Revision {
		t.Fatalf("direct List after Panel Rotate: %+v %v", directPage, err)
	}
	revoked, err := holder.Revoke(ctx, rotated.Revision, name)
	if err != nil || revoked.Outcome != cwire.RevokeOutcomeRevoked {
		t.Fatalf("direct Revoke: %+v %v", revoked, err)
	}
	panelResponse = panelRequest(t, h, "/credentials?cursor=", nil)
	panelPage = decodePanel[cwire.MetadataPage](t, panelResponse.Code, panelResponse.Body.Bytes())
	if len(panelPage.Records) != 1 || panelPage.Records[0].State != cwire.StateRevoked || panelPage.Records[0].Revision != revoked.Revision {
		t.Fatalf("Panel did not see direct Revoke: %+v", panelPage)
	}

	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	caller := rwire.Subject{Account: account.Uid, Program: filepath.Clean(exe)}
	if err := policy.Set(caller, credentials.ActionRead, credentials.ResourceAccount, false); err != nil {
		t.Fatal(err)
	}
	deniedDirect, err := holder.List(ctx, "", 32)
	if err != nil {
		t.Fatal(err)
	}
	panelResponse = panelRequest(t, h, "/credentials?cursor=", nil)
	deniedPanel := decodePanel[cwire.MetadataPage](t, panelResponse.Code, panelResponse.Body.Bytes())
	if deniedDirect.Outcome != cwire.PageOutcomeForbidden || deniedPanel.Outcome != deniedDirect.Outcome ||
		len(deniedDirect.Records) != 0 || len(deniedPanel.Records) != 0 || deniedDirect.Limits.SecureStore != "" || deniedPanel.Limits.SecureStore != "" {
		t.Fatalf("holder.read refusal diverged: direct=%+v Panel=%+v", deniedDirect, deniedPanel)
	}
	if err := policy.Set(caller, credentials.ActionManage, credentials.ResourceAccount, false); err != nil {
		t.Fatal(err)
	}
	registration.Name = "hp5-direct-denied"
	deniedEditDirect, err := holder.Store(ctx, "", registration)
	if err != nil {
		t.Fatal(err)
	}
	deniedResponse := panelRequest(t, h, "/credentials", credentialEdit{Action: "add", Name: "hp5-panel-denied", Kind: "bearer",
		Targets: registration.Scope.Targets, Consumers: registration.Scope.Consumers, Secret: secret})
	deniedEditPanel := decodePanel[cwire.StoreResult](t, deniedResponse.Code, deniedResponse.Body.Bytes())
	if deniedEditDirect.Outcome != cwire.StoreOutcomeForbidden || deniedEditPanel.Outcome != deniedEditDirect.Outcome ||
		deniedEditDirect.Current != nil || deniedEditPanel.Current != nil || bytes.Contains(deniedResponse.Body.Bytes(), []byte(secret)) {
		t.Fatalf("holder.manage refusal diverged: direct=%+v Panel=%+v", deniedEditDirect, deniedEditPanel)
	}
}

// hp5RegistryState keeps one revision shared by registry@1 and registry@2.
// The test uses it only behind the public IPC dispatchers.
type hp5RegistryState struct {
	mu       sync.Mutex
	revision int
	entry    *fwire.Declaration
	refusal  fwire.DeclarationListOutcome
}

func (r *hp5RegistryState) revisionLocked() string { return "providers-v2:" + strconv.Itoa(r.revision) }

func (r *hp5RegistryState) listLocked() fwire.DeclarationList {
	if r.refusal != 0 {
		return fwire.DeclarationList{Outcome: r.refusal, Declarations: []fwire.DeclarationState{}}
	}
	list := fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Revision: r.revisionLocked(), Declarations: []fwire.DeclarationState{}}
	if r.entry != nil {
		list.Declarations = append(list.Declarations, fwire.DeclarationState{Declaration: *r.entry, Role: fwire.DeclarationRoleProvider,
			DeclaredBy: "fixture", Readiness: fwire.DeclarationReadinessReady, Described: []fwire.ServiceState{}, Accepted: []string{}})
	}
	return list
}

func (r *hp5RegistryState) Declarations() (fwire.DeclarationList, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listLocked(), nil
}

func (r *hp5RegistryState) editRefusal() fwire.DeclarationEditOutcome {
	if r.refusal == fwire.DeclarationListOutcomeForbidden {
		return fwire.DeclarationEditOutcomeForbidden
	}
	return fwire.DeclarationEditOutcomeUnavailable
}

func (r *hp5RegistryState) Declare(expected string, entry fwire.Declaration) (fwire.DeclarationChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refusal != 0 {
		return fwire.DeclarationChange{Outcome: r.editRefusal()}, nil
	}
	if expected != r.revisionLocked() || r.entry != nil {
		return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeConflict, Revision: r.revisionLocked(), Reason: "revision"}, nil
	}
	r.entry = &entry
	r.revision++
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeApplied, Revision: r.revisionLocked()}, nil
}

func (r *hp5RegistryState) Withdraw(expected, name string) (fwire.DeclarationChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refusal != 0 {
		return fwire.DeclarationChange{Outcome: r.editRefusal()}, nil
	}
	if expected != r.revisionLocked() {
		return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeConflict, Revision: r.revisionLocked(), Reason: "revision"}, nil
	}
	if r.entry == nil || r.entry.Name != name {
		return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeUnknown, Revision: r.revisionLocked()}, nil
	}
	r.entry = nil
	r.revision++
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeApplied, Revision: r.revisionLocked()}, nil
}

func (r *hp5RegistryState) Observe(_ string, _ int64) (fwire.DeclarationObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	page := r.listLocked()
	return fwire.DeclarationObservation{Outcome: page.Outcome, Cursor: page.Revision, Declarations: page.Declarations}, nil
}

type hp5RegistryV2 struct{ state *hp5RegistryState }

func (v hp5RegistryV2) Declarations() (fwire.DeclarationListV2, error) {
	page, err := v.state.Declarations()
	result := fwire.DeclarationListV2{Outcome: page.Outcome, Revision: page.Revision, Declarations: []fwire.DeclarationStateV2{}}
	for _, entry := range page.Declarations {
		result.Declarations = append(result.Declarations, fwire.DeclarationStateV2{State: entry, SupportedExtensions: []fwire.DeclarationExtension{}})
	}
	return result, err
}

func (v hp5RegistryV2) Declare(expected string, entry fwire.DeclarationWithExtensions) (fwire.DeclarationChange, error) {
	return v.state.Declare(expected, entry.Declaration)
}

func (v hp5RegistryV2) Withdraw(expected, name string) (fwire.DeclarationChange, error) {
	return v.state.Withdraw(expected, name)
}

func (v hp5RegistryV2) Observe(cursor string, wait int64) (fwire.DeclarationObservationV2, error) {
	page, err := v.Declarations()
	return fwire.DeclarationObservationV2{Outcome: page.Outcome, Cursor: page.Revision, Declarations: page.Declarations}, err
}

func TestHP5RegistryPanelAndPublicV1V2ShareRevisionAndRefusals(t *testing.T) {
	own(t)
	state := &hp5RegistryState{revision: 1}
	panelConfigRuntimeWith(t, func(o *host.Options) {
		provider, err := inference.New(inference.Config{Router: router.New(),
			Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "not_granted", nil },
			Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
				return nil, "unknown"
			}})
		if err != nil {
			t.Fatal(err)
		}
		inferenceAt := o.ConfigEndpoint + "-hp5-inference"
		inferenceHost, err := inferenceservice.Listen(inferenceAt, provider)
		if err != nil {
			t.Fatal(err)
		}
		inferenceHost.Operator = registryOperator{&panelOperator{}}
		o.Inference, o.InferenceEndpoint = panelInference{Host: inferenceHost, provider: provider}, inferenceAt
		endpoint := o.ConfigEndpoint + "-hp5-registry"
		listener, err := listen.Listen(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		o.Registry, o.RegistryEndpoint = registryService{listener: listener, handler: state, v2: hp5RegistryV2{state}}, endpoint
	})
	panel := newTestPanel(t).handler("test-key")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	v1, err := panelMachine().ResolveRegistry(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := panelMachine().ResolveRegistryV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	before1, err := v1.Declarations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before2, err := v2.Declarations(ctx)
	if err != nil || before1.Revision != before2.Revision || before1.Outcome != fwire.DeclarationListOutcomePage {
		t.Fatalf("public Registry versions disagree: v1=%+v v2=%+v err=%v", before1, before2, err)
	}
	program := filepath.Join(t.TempDir(), "provider.exe")
	edit := registryProviderEdit{Edit: "declare", Revision: before1.Revision, Name: "hp5-provider", Program: program,
		Endpoint: "hp5-provider", Activation: "attach", Contracts: []string{"abstraction.inference/chat@1"}, Models: []string{"qwen/tiny"}}
	response := panelRequest(t, panel, "/registry/providers", edit)
	added := decodePanel[fwire.DeclarationChange](t, response.Code, response.Body.Bytes())
	if added.Outcome != fwire.DeclarationEditOutcomeApplied || added.Revision == before1.Revision {
		t.Fatalf("Panel Declare: %+v", added)
	}
	listedV1, listedV1Err := v1.Declarations(ctx)
	if listedV1Err != nil || listedV1.Revision != added.Revision || len(listedV1.Declarations) != 1 {
		t.Fatalf("registry@1 did not see Panel Declare: %+v %v", listedV1, listedV1Err)
	}
	current, err := v2.Declarations(ctx)
	if err != nil || current.Revision != added.Revision || len(current.Declarations) != 1 ||
		current.Declarations[0].State.Declaration.Name != edit.Name || current.Declarations[0].State.Declaration.Program != program {
		t.Fatalf("registry@2 did not see Panel Declare: %+v %v", current, err)
	}
	staleDirect, err := v2.Withdraw(ctx, before1.Revision, edit.Name)
	if err != nil {
		t.Fatal(err)
	}
	staleResponse := panelRequest(t, panel, "/registry/providers", registryProviderEdit{Edit: "withdraw", Revision: before1.Revision, Name: edit.Name})
	stalePanel := decodePanel[fwire.DeclarationChange](t, staleResponse.Code, staleResponse.Body.Bytes())
	if staleDirect.Outcome != fwire.DeclarationEditOutcomeConflict || stalePanel != staleDirect || stalePanel.Revision != added.Revision {
		t.Fatalf("stale Withdraw diverged: direct=%+v Panel=%+v", staleDirect, stalePanel)
	}
	removed, err := v1.Withdraw(ctx, added.Revision, edit.Name)
	if err != nil || removed.Outcome != fwire.DeclarationEditOutcomeApplied {
		t.Fatalf("direct Withdraw: %+v %v", removed, err)
	}
	viewResponse := panelRequest(t, panel, "/registry/view", nil)
	view := decodePanel[registryView](t, viewResponse.Code, viewResponse.Body.Bytes())
	if view.Providers.Revision != removed.Revision || len(view.Providers.Declarations) != 0 {
		t.Fatalf("Panel did not see direct Withdraw: %+v", view.Providers)
	}
	for _, tc := range []struct {
		list fwire.DeclarationListOutcome
		edit fwire.DeclarationEditOutcome
	}{
		{fwire.DeclarationListOutcomeForbidden, fwire.DeclarationEditOutcomeForbidden},
		{fwire.DeclarationListOutcomeUnavailable, fwire.DeclarationEditOutcomeUnavailable},
	} {
		state.mu.Lock()
		state.refusal = tc.list
		state.mu.Unlock()
		directList, err := v2.Declarations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		viewResponse := panelRequest(t, panel, "/registry/view", nil)
		view := decodePanel[registryView](t, viewResponse.Code, viewResponse.Body.Bytes())
		if directList.Outcome != tc.list || view.Providers.Outcome != tc.list || directList.Revision != "" || view.Providers.Revision != "" {
			t.Fatalf("Registry list refusal diverged: direct=%+v Panel=%+v", directList, view.Providers)
		}
		directEdit, err := v2.Declare(ctx, removed.Revision, fwire.DeclarationWithExtensions{Declaration: fwire.Declaration{
			Name: edit.Name, Program: program, Endpoint: edit.Endpoint, Transport: fwire.DeclarationTransportNative,
			Activation: fwire.ActivationAttach, Role: fwire.DeclarationRoleProvider, Contracts: edit.Contracts}})
		if err != nil {
			t.Fatal(err)
		}
		edit.Revision = removed.Revision
		panelResponse := panelRequest(t, panel, "/registry/providers", edit)
		panelEdit := decodePanel[fwire.DeclarationChange](t, panelResponse.Code, panelResponse.Body.Bytes())
		if directEdit.Outcome != tc.edit || panelEdit != directEdit || directEdit.Revision != "" {
			t.Fatalf("Registry edit refusal diverged: direct=%+v Panel=%+v", directEdit, panelEdit)
		}
	}
}
