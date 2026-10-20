package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

func extensionDeclaration() wire.Declaration {
	return (providerDeclaration{Name: "image-provider", Program: absoluteFixtureProgram(), Endpoint: "image-provider",
		Transport: transportNative, Contracts: []string{inference.ImageContract}, Resources: []string{"profile:image"},
		Models: []string{"image-model"}, Activation: wire.ActivationOnDemand.String()}).wire()
}

func TestRegistryV2AtomicInventoryAndV1Projection(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	before := r.DeclarationsV2(ctx, caller)
	decl := extensionDeclaration()
	ext := wire.DeclarationExtension{Profile: "image", Key: "vendor/steps"}
	bad := r.DeclareV2(ctx, caller, before.Revision, wire.DeclarationWithExtensions{
		Declaration: decl, SupportedExtensions: []wire.DeclarationExtension{{Profile: "chat", Key: ext.Key}},
	})
	if bad.Outcome != wire.DeclarationEditOutcomeInvalid || bad.Reason != "supported_extensions" {
		t.Fatalf("invalid inventory: %+v", bad)
	}
	if after := r.DeclarationsV2(ctx, caller); after.Revision != before.Revision || len(after.Declarations) != 0 {
		t.Fatalf("invalid edit changed registration: %+v", after)
	}
	added := r.DeclareV2(ctx, caller, before.Revision, wire.DeclarationWithExtensions{Declaration: decl, SupportedExtensions: []wire.DeclarationExtension{ext}})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("v2 Declare: %+v", added)
	}
	v1 := r.Declarations(ctx, caller)
	v2 := r.DeclarationsV2(ctx, caller)
	if v1.Revision != added.Revision || v2.Revision != added.Revision || len(v1.Declarations) != 1 || len(v2.Declarations) != 1 ||
		v2.Declarations[0].State.Declaration.Name != decl.Name || len(v2.Declarations[0].SupportedExtensions) != 1 || v2.Declarations[0].SupportedExtensions[0] != ext {
		t.Fatalf("registries diverged: v1=%+v v2=%+v", v1, v2)
	}
	encoded, err := json.Marshal(v1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("supported_extensions")) {
		t.Fatalf("v1 projection exposed v2 field to strict old reader: %s", encoded)
	}
	if existing := r.Declare(ctx, caller, added.Revision, decl); existing.Outcome != wire.DeclarationEditOutcomeConflict || existing.Reason != "name" {
		t.Fatalf("v1 edit replaced v2 metadata: %+v", existing)
	}
	if after := r.DeclarationsV2(ctx, caller); after.Revision != added.Revision || len(after.Declarations[0].SupportedExtensions) != 1 {
		t.Fatalf("v1 conflict changed metadata: %+v", after)
	}
	observed := r.ObserveV2(ctx, caller, "", 0)
	if observed.Outcome != wire.DeclarationListOutcomePage || len(observed.Declarations) != 1 || observed.Declarations[0].SupportedExtensions[0] != ext {
		t.Fatalf("v2 Observe lost metadata: %+v", observed)
	}
	removed := r.Withdraw(ctx, caller, added.Revision, decl.Name)
	if removed.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("v1 Withdraw: %+v", removed)
	}
	if after := r.ObserveV2(ctx, caller, observed.Cursor, 0); after.Outcome != wire.DeclarationListOutcomePage || after.Cursor == observed.Cursor || len(after.Declarations) != 0 {
		t.Fatalf("v2 Observe after v1 Withdraw: %+v", after)
	}
	plain := r.Declare(ctx, caller, removed.Revision, decl)
	if plain.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("v1 Declare after withdrawal: %+v", plain)
	}
	if after := r.DeclarationsV2(ctx, caller); len(after.Declarations) != 1 || len(after.Declarations[0].SupportedExtensions) != 0 {
		t.Fatalf("v1 Declare inherited v2 metadata: %+v", after)
	}
	if err := r.operator.credentials.policy.Revoke(rwire.Subject{Account: caller.Account, Program: caller.Program}, ActionProviderManage, credentials.ResourceAccount); err != nil {
		t.Fatal(err)
	}
	if denied := r.DeclarationsV2(ctx, caller); denied.Outcome != wire.DeclarationListOutcomeForbidden || len(denied.Declarations) != 0 || denied.Revision != "" {
		t.Fatalf("v2 read after rights revoke: %+v", denied)
	}
	if denied := r.ObserveV2(ctx, caller, "", 0); denied.Outcome != wire.DeclarationListOutcomeForbidden || len(denied.Declarations) != 0 || denied.Cursor != "" {
		t.Fatalf("v2 observe after rights revoke: %+v", denied)
	}
	if denied := r.DeclareV2(ctx, caller, plain.Revision, wire.DeclarationWithExtensions{Declaration: decl}); denied.Outcome != wire.DeclarationEditOutcomeForbidden {
		t.Fatalf("v2 edit after rights revoke: %+v", denied)
	}
	if denied := r.Withdraw(ctx, caller, plain.Revision, decl.Name); denied.Outcome != wire.DeclarationEditOutcomeForbidden {
		t.Fatalf("shared withdrawal after rights revoke: %+v", denied)
	}
}

func TestRegistryV2ListKeepsCapturedGenerationDuringReplacement(t *testing.T) {
	r, _ := registryObserverFixture(t)
	p := r.providers
	_, revision, err := p.listV2()
	if err != nil {
		t.Fatal(err)
	}
	first := declarationOf(extensionDeclaration())
	first.SupportedExtensions = []providerExtension{{Profile: "image", Key: "vendor/first"}}
	added := p.add(revision, providerFile{Version: providerFileVersion, Declaration: first, DeclaredBy: "first", DeclaredAt: 111})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("first generation: %+v", added)
	}
	second := first
	second.SupportedExtensions = []providerExtension{{Profile: "image", Key: "vendor/second"}}
	replaced := false
	p.routerHostStates = func() []router.HostState {
		if !replaced {
			replaced = true
			removed := p.remove(added.Revision, first.Name)
			if removed.Outcome != wire.DeclarationEditOutcomeApplied {
				t.Fatalf("withdraw while listing: %+v", removed)
			}
			next := p.add(removed.Revision, providerFile{Version: providerFileVersion, Declaration: second, DeclaredBy: "second", DeclaredAt: 222})
			if next.Outcome != wire.DeclarationEditOutcomeApplied {
				t.Fatalf("replacement while listing: %+v", next)
			}
		}
		return nil
	}
	page, capturedRevision, err := p.listV2()
	if err != nil || !replaced || capturedRevision != added.Revision || len(page) != 1 ||
		page[0].State.DeclaredBy != "first" || page[0].State.DeclaredUnixMs != 111 ||
		len(page[0].SupportedExtensions) != 1 || page[0].SupportedExtensions[0].Key != "vendor/first" {
		t.Fatalf("mixed registration generations: revision=%s page=%+v err=%v", capturedRevision, page, err)
	}
	current, currentRevision, err := p.listV2()
	if err != nil || currentRevision == capturedRevision || len(current) != 1 || current[0].State.DeclaredBy != "second" ||
		current[0].State.DeclaredUnixMs != 222 || len(current[0].SupportedExtensions) != 1 || current[0].SupportedExtensions[0].Key != "vendor/second" {
		t.Fatalf("replacement generation unreadable: revision=%s page=%+v err=%v", currentRevision, current, err)
	}
}

func TestRegistryV2AndStrictV1ClientsShareEndpoint(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	r, caller := registryObserverFixture(t)
	endpoint := listen.Endpoint(fmt.Sprintf("registry-v2-%d", time.Now().UnixNano()))
	h, err := listenRegistry(endpoint, r, caller.Account, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	transport := (listen.FrameClient{Endpoint: endpoint, Timeout: time.Second, MaxFrame: 1 << 20}).WithContext(ctx)
	description, err := wire.NewEndpointClient(transport).Describe()
	if err != nil || description.Outcome != wire.DescriptionOutcomeDescribed || len(description.Services) != 2 ||
		description.Services[0].Contract != RegistryContract || description.Services[1].Contract != RegistryV2Contract ||
		description.Services[0].Readiness != wire.ServiceReadinessReady || description.Services[1].Readiness != wire.ServiceReadinessReady {
		t.Fatalf("shared endpoint description: %+v %v", description, err)
	}
	v1 := wire.NewRegistryClient(transport)
	v2 := wire.NewRegistryV2Client(transport)
	before, err := v2.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	ext := wire.DeclarationExtension{Profile: "image", Key: "vendor/steps"}
	added, err := v2.Declare(before.Revision, wire.DeclarationWithExtensions{Declaration: extensionDeclaration(), SupportedExtensions: []wire.DeclarationExtension{ext}})
	if err != nil || added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("v2 IPC Declare: %+v %v", added, err)
	}
	old, err := v1.Declarations() // The strict v1 decoder rejects unknown fields.
	if err != nil || old.Outcome != wire.DeclarationListOutcomePage || len(old.Declarations) != 1 || old.Declarations[0].Declaration.Name != "image-provider" {
		t.Fatalf("strict v1 IPC read: %+v %v", old, err)
	}
	newer, err := v2.Observe("", 0)
	if err != nil || len(newer.Declarations) != 1 || newer.Declarations[0].SupportedExtensions[0] != ext {
		t.Fatalf("v2 IPC Observe: %+v %v", newer, err)
	}
	missing, err := v1.Withdraw(old.Revision, "image-provider")
	if err != nil || missing.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("v1 IPC Withdraw: %+v %v", missing, err)
	}
}

func TestProviderAddSupportedExtensionUsesRegistryV2(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	output, err := runProvider(t, "add", "image-provider", "--program", absoluteFixtureProgram(), "--provider-endpoint", "image-provider",
		"--contract", inference.ImageContract, "--model", "image-model", "--profiles", "image",
		"--supported-extension", "image:vendor/steps", "--endpoint", options.endpoint, "--timeout", "10s")
	if err != nil {
		t.Fatalf("provider add with extension: %v\n%s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := client.New(options.endpoint).ResolveRegistryV2(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := registry.Declarations(ctx)
	if err != nil || page.Outcome != wire.DeclarationListOutcomePage || len(page.Declarations) != 1 ||
		len(page.Declarations[0].SupportedExtensions) != 1 || page.Declarations[0].SupportedExtensions[0] != (wire.DeclarationExtension{Profile: "image", Key: "vendor/steps"}) {
		t.Fatalf("v2 registry after CLI add: %+v %v", page, err)
	}
}
