package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

type registryUnavailableResolver struct {
	mu        sync.Mutex
	contracts []string
}

func (r *registryUnavailableResolver) ResolveOnMachine(request wire.ResolveRequest, _ string) (wire.ResolveResult, error) {
	return r.Resolve(request)
}

func (r *registryUnavailableResolver) Resolve(request wire.ResolveRequest) (wire.ResolveResult, error) {
	r.mu.Lock()
	r.contracts = append(r.contracts, request.Contracts...)
	r.mu.Unlock()
	return wire.ResolveResult{Status: wire.ResolutionStatusUnavailable}, nil
}

func TestProviderRemoteRequiresV2WithoutFallback(t *testing.T) {
	provesProgram := statusTransportProvesProgram(t)
	endpoint := listen.Endpoint(fmt.Sprintf("registry-compat-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	listener, err := listen.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	resolver := &registryUnavailableResolver{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			call, e := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
			if e != nil {
				conn.Close()
				continue
			}
			reply, e := (&wire.ResolverDispatcher{Handler: resolver}).ExchangeFrame(call.Frame)
			if e == nil {
				_ = call.Reply(reply)
			}
			call.Close()
		}
	}()
	t.Cleanup(func() { cancel(); listener.Close(); <-done })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	err = providerCommand([]string{"add", "paired", "--remote", "127.0.0.1:1", "--server-key-sha256", strings.Repeat("a", 64), "--endpoint", endpoint, "--runtime-program", exe, "--timeout", "2s"}, &out, &diagnostics)
	if err == nil {
		t.Fatal("unsupported V2 accepted remote edit")
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if !provesProgram {
		// Native identity admission precedes contract resolution. Keep this
		// refusal observable on transports whose Program proof is unavailable.
		assertExit(t, err, exitNotResolved, "remote edit native proof refusal")
		if !strings.Contains(err.Error(), wrongRuntimeFirstLine) {
			t.Fatalf("remote edit lost independent server-proof refusal: %v", err)
		}
		if len(resolver.contracts) != 0 {
			t.Fatalf("proof-refused remote edit reached resolver: %v", resolver.contracts)
		}
		return
	}
	if len(resolver.contracts) != 1 || resolver.contracts[0] != "abstraction.facade/registry@2" {
		t.Fatalf("remote resolver contracts: %v", resolver.contracts)
	}
}

// The generated dispatcher itself is the in-process transport. Both sides
// execute the actual V1 codec; the published RemoteTrust decoder is also
// exercised in idl/gen/security965_test.go.
type registryCompatV1 struct {
	registry *runtimeRegistry
	caller   inference.Subject
}

func (f registryCompatV1) Declarations() (wire.DeclarationList, error) {
	return f.registry.Declarations(context.Background(), f.caller), nil
}
func (f registryCompatV1) Declare(revision string, d wire.Declaration) (wire.DeclarationChange, error) {
	return f.registry.Declare(context.Background(), f.caller, revision, d), nil
}
func (f registryCompatV1) Withdraw(revision, name string) (wire.DeclarationChange, error) {
	return f.registry.Withdraw(context.Background(), f.caller, revision, name), nil
}
func (f registryCompatV1) Observe(cursor string, wait int64) (wire.DeclarationObservation, error) {
	return f.registry.Observe(context.Background(), f.caller, cursor, wait), nil
}

func registryRemoteFixture(t *testing.T, name string) (wire.Declaration, inferenceRemoteTrust) {
	t.Helper()
	_, _, trust := remoteTrustFiles(t, t.TempDir())
	return wire.Declaration{Name: name, Arguments: []string{}, Endpoint: "tls://127.0.0.1:1", Transport: wire.DeclarationTransportRemote, Contracts: []string{inference.Contract}, Resources: []string{"profile:chat"}, Activation: wire.ActivationRemote, Remote: &wire.RemoteTrust{ServerName: trust.ServerName, Roots: trust.Roots, Certificate: trust.Certificate, Key: trust.Key}}, trust
}

func TestRegistryRemotePinV2AdmissionAndPublishedV1Projection(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	initial := r.DeclarationsPinned(ctx, caller)
	d, trust := registryRemoteFixture(t, "paired")
	policyBefore, err := os.ReadFile(r.operator.credentials.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		got := r.DeclarePinned(ctx, caller, initial.Revision, wire.PinnedDeclaration{Declaration: d, RemoteServerKeySHA256: pin})
		if got.Outcome != wire.DeclarationEditOutcomeInvalid || got.Reason != "remote_server_key_sha256" {
			t.Fatalf("pin admission %q: %+v", pin, got)
		}
	}
	native := extensionDeclaration()
	if got := r.DeclarePinned(ctx, caller, initial.Revision, wire.PinnedDeclaration{Declaration: native, RemoteServerKeySHA256: trust.ServerKeySHA256}); got.Outcome != wire.DeclarationEditOutcomeInvalid {
		t.Fatal("native accepted remote pin")
	}
	// V1 refuses even a structurally valid copied remote before opening trust
	// files; its five-field projection cannot erase a successor pin.
	if got := r.Declare(ctx, caller, initial.Revision, d); got.Outcome != wire.DeclarationEditOutcomeInvalid || got.Reason != "remote:registry@2" {
		t.Fatalf("V1 remote edit: %+v", got)
	}
	if after := r.DeclarationsPinned(ctx, caller); after.Revision != initial.Revision || len(after.Declarations) != 0 {
		t.Fatal("invalid edits wrote registration")
	}
	policyAfter, _ := os.ReadFile(r.operator.credentials.path)
	if !bytes.Equal(policyBefore, policyAfter) {
		t.Fatal("invalid edits wrote rules")
	}
	ext := wire.DeclarationExtension{Profile: "chat", Key: "vendor/steps"}
	added := r.DeclarePinned(ctx, caller, initial.Revision, wire.PinnedDeclaration{Declaration: d, SupportedExtensions: []wire.DeclarationExtension{ext}, RemoteServerKeySHA256: trust.ServerKeySHA256})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("V2 remote: %+v", added)
	}
	page := r.DeclarationsPinned(ctx, caller)
	if len(page.Declarations) != 1 || page.Declarations[0].RemoteServerKeySHA256 != trust.ServerKeySHA256 || len(page.Declarations[0].SupportedExtensions) != 1 || page.Declarations[0].SupportedExtensions[0] != ext {
		t.Fatalf("V2 atomic read: %+v", page)
	}
	stored, err := os.ReadFile(filepath.Join(r.providers.dir, "paired.json"))
	if err != nil || !bytes.Contains(stored, []byte(`"server_key_sha256": "`+trust.ServerKeySHA256+`"`)) {
		t.Fatal("single stored trust lost pin")
	}
	// Add ordinary V1 native and HTTP declarations beside the pinned remote.
	if got := r.Declare(ctx, caller, page.Revision, native); got.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("native V1 add: %+v", got)
	}
	afterNative := r.DeclarationsPinned(ctx, caller)
	for _, entry := range afterNative.Declarations {
		if entry.State.Declaration.Name == "paired" && entry.RemoteServerKeySHA256 != trust.ServerKeySHA256 {
			t.Fatal("V1 add changed existing remote pin")
		}
	}
	hosted := newHostFile(iwire.HostEntry{Name: "ordinary-http", Base: "http://127.0.0.1:1", Kind: router.WireOpenAICompatible, Hosted: true, Profiles: []string{"chat"}}, "fixture").Declaration.wire()
	if got := r.DeclarePinned(ctx, caller, afterNative.Revision, wire.PinnedDeclaration{Declaration: hosted, RemoteServerKeySHA256: trust.ServerKeySHA256}); got.Outcome != wire.DeclarationEditOutcomeInvalid {
		t.Fatal("HTTP accepted remote pin")
	}
	if got := r.Declare(ctx, caller, afterNative.Revision, hosted); got.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("HTTP V1 add: %+v", got)
	}
	oldClient := wire.NewRegistryClient(&wire.RegistryDispatcher{Handler: registryCompatV1{r, caller}})
	old, err := oldClient.Declarations()
	if err != nil || len(old.Declarations) != 3 {
		t.Fatalf("mixed V1 canonical read: %+v %v", old, err)
	}
	raw, _ := json.Marshal(old)
	if bytes.Contains(raw, []byte("server_key_sha256")) || bytes.Contains(raw, []byte("supported_extensions")) {
		t.Fatal("successor metadata leaked into published V1")
	}
	beforeCopy, _ := os.ReadFile(filepath.Join(r.providers.dir, "paired.json"))
	policyBefore, _ = os.ReadFile(r.operator.credentials.path)
	for _, entry := range old.Declarations {
		if entry.Declaration.Name == "paired" {
			got, e := oldClient.Declare(old.Revision, entry.Declaration)
			if e != nil || got.Outcome != wire.DeclarationEditOutcomeInvalid || got.Reason != "remote:registry@2" {
				t.Fatalf("copied V1 remote: %+v %v", got, e)
			}
		}
	}
	afterCopy, _ := os.ReadFile(filepath.Join(r.providers.dir, "paired.json"))
	policyAfter, _ = os.ReadFile(r.operator.credentials.path)
	if !bytes.Equal(beforeCopy, afterCopy) || !bytes.Equal(policyBefore, policyAfter) {
		t.Fatal("copied V1 remote changed state or rules")
	}
	d.Name = "stale"
	if got := r.DeclarePinned(ctx, caller, initial.Revision, wire.PinnedDeclaration{Declaration: d, RemoteServerKeySHA256: trust.ServerKeySHA256}); got.Outcome != wire.DeclarationEditOutcomeConflict || got.Reason != "revision" {
		t.Fatalf("stale full revision: %+v", got)
	}
}

func TestRegistryOldCAOnlyRemoteRemainsVisibleAndWithdrawable(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	_, revision, err := r.providers.read()
	if err != nil {
		t.Fatal(err)
	}
	d, trust := registryRemoteFixture(t, "legacy")
	legacy := declarationOf(d)
	if _, err := legacy.Remote.config(); err == nil {
		t.Fatal("CA-only trust connected")
	}
	added := r.providers.add(revision, providerFile{Version: providerFileVersion, Declaration: legacy, DeclaredBy: "legacy-fixture"})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal("legacy file not retained")
	}
	page := r.DeclarationsPinned(ctx, caller)
	if len(page.Declarations) != 1 || page.Declarations[0].RemoteServerKeySHA256 != "" || page.Declarations[0].State.Declaration.Remote.Roots != trust.Roots {
		t.Fatal("CA-only record hidden or inferred pin")
	}
	if got := r.Withdraw(ctx, caller, page.Revision, "legacy"); got.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("legacy withdraw: %+v", got)
	}
	if after := r.Declarations(ctx, caller); len(after.Declarations) != 0 {
		t.Fatal("legacy remains after withdrawal")
	}
}

func TestRegistryV2RemotePinSnapshotAndObserve(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d, trust := registryRemoteFixture(t, "paired")
	first := declarationOf(d)
	first.Remote.ServerKeySHA256 = trust.ServerKeySHA256
	_, revision, err := r.providers.read()
	if err != nil {
		t.Fatal(err)
	}
	added := r.providers.add(revision, providerFile{Version: providerFileVersion, Declaration: first, DeclaredBy: "first"})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	observed := r.ObservePinned(ctx, caller, "", 0)
	second := first
	replacement := *first.Remote
	replacement.ServerKeySHA256 = strings.Repeat("b", 64)
	second.Remote = &replacement
	replaced := false
	r.providers.routerHostStates = func() []router.HostState {
		if !replaced {
			replaced = true
			removed := r.providers.remove(added.Revision, first.Name)
			next := r.providers.add(removed.Revision, providerFile{Version: providerFileVersion, Declaration: second, DeclaredBy: "second"})
			if next.Outcome != wire.DeclarationEditOutcomeApplied {
				t.Fatal(next)
			}
		}
		return nil
	}
	page, captured, err := r.providers.listPinned()
	if err != nil || captured != added.Revision || len(page) != 1 || page[0].RemoteServerKeySHA256 != trust.ServerKeySHA256 {
		t.Fatalf("mixed pin snapshot: %+v %s %v", page, captured, err)
	}
	current := r.ObservePinned(ctx, caller, observed.Cursor, 0)
	if current.Cursor == observed.Cursor || len(current.Declarations) != 1 || current.Declarations[0].RemoteServerKeySHA256 != replacement.ServerKeySHA256 {
		t.Fatalf("pin edit not observed: %+v", current)
	}
}

func extensionDeclaration() wire.Declaration {
	return (providerDeclaration{Name: "image-provider", Program: presentFixtureProgram(), Endpoint: "image-provider",
		Transport: transportNative, Contracts: []string{inference.ImageContract}, Resources: []string{"profile:image"},
		Models: []string{"image-model"}, Activation: wire.ActivationOnDemand.String()}).wire()
}

func TestRegistryV2AtomicInventoryAndV1Projection(t *testing.T) {
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	before := r.DeclarationsPinned(ctx, caller)
	decl := extensionDeclaration()
	ext := wire.DeclarationExtension{Profile: "image", Key: "vendor/steps"}
	bad := r.DeclarePinned(ctx, caller, before.Revision, wire.PinnedDeclaration{
		Declaration: decl, SupportedExtensions: []wire.DeclarationExtension{{Profile: "chat", Key: ext.Key}},
	})
	if bad.Outcome != wire.DeclarationEditOutcomeInvalid || bad.Reason != "supported_extensions" {
		t.Fatalf("invalid inventory: %+v", bad)
	}
	if after := r.DeclarationsPinned(ctx, caller); after.Revision != before.Revision || len(after.Declarations) != 0 {
		t.Fatalf("invalid edit changed registration: %+v", after)
	}
	added := r.DeclarePinned(ctx, caller, before.Revision, wire.PinnedDeclaration{Declaration: decl, SupportedExtensions: []wire.DeclarationExtension{ext}})
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("v2 Declare: %+v", added)
	}
	v1 := r.Declarations(ctx, caller)
	v2 := r.DeclarationsPinned(ctx, caller)
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
	if after := r.DeclarationsPinned(ctx, caller); after.Revision != added.Revision || len(after.Declarations[0].SupportedExtensions) != 1 {
		t.Fatalf("v1 conflict changed metadata: %+v", after)
	}
	observed := r.ObservePinned(ctx, caller, "", 0)
	if observed.Outcome != wire.DeclarationListOutcomePage || len(observed.Declarations) != 1 || observed.Declarations[0].SupportedExtensions[0] != ext {
		t.Fatalf("v2 Observe lost metadata: %+v", observed)
	}
	removed := r.Withdraw(ctx, caller, added.Revision, decl.Name)
	if removed.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("v1 Withdraw: %+v", removed)
	}
	if after := r.ObservePinned(ctx, caller, observed.Cursor, 0); after.Outcome != wire.DeclarationListOutcomePage || after.Cursor == observed.Cursor || len(after.Declarations) != 0 {
		t.Fatalf("v2 Observe after v1 Withdraw: %+v", after)
	}
	plain := r.Declare(ctx, caller, removed.Revision, decl)
	if plain.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("v1 Declare after withdrawal: %+v", plain)
	}
	if after := r.DeclarationsPinned(ctx, caller); len(after.Declarations) != 1 || len(after.Declarations[0].SupportedExtensions) != 0 {
		t.Fatalf("v1 Declare inherited v2 metadata: %+v", after)
	}
	if err := r.operator.credentials.policy.Revoke(rwire.Subject{Account: caller.Account, Program: caller.Program}, ActionProviderManage, credentials.ResourceAccount); err != nil {
		t.Fatal(err)
	}
	if denied := r.DeclarationsPinned(ctx, caller); denied.Outcome != wire.DeclarationListOutcomeForbidden || len(denied.Declarations) != 0 || denied.Revision != "" {
		t.Fatalf("v2 read after rights revoke: %+v", denied)
	}
	if denied := r.ObservePinned(ctx, caller, "", 0); denied.Outcome != wire.DeclarationListOutcomeForbidden || len(denied.Declarations) != 0 || denied.Cursor != "" {
		t.Fatalf("v2 observe after rights revoke: %+v", denied)
	}
	if denied := r.DeclarePinned(ctx, caller, plain.Revision, wire.PinnedDeclaration{Declaration: decl}); denied.Outcome != wire.DeclarationEditOutcomeForbidden {
		t.Fatalf("v2 edit after rights revoke: %+v", denied)
	}
	if denied := r.Withdraw(ctx, caller, plain.Revision, decl.Name); denied.Outcome != wire.DeclarationEditOutcomeForbidden {
		t.Fatalf("shared withdrawal after rights revoke: %+v", denied)
	}
}

func TestRegistryV2ListKeepsCapturedGenerationDuringReplacement(t *testing.T) {
	r, _ := registryObserverFixture(t)
	p := r.providers
	_, revision, err := p.listPinned()
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
	page, capturedRevision, err := p.listPinned()
	if err != nil || !replaced || capturedRevision != added.Revision || len(page) != 1 ||
		page[0].State.DeclaredBy != "first" || page[0].State.DeclaredUnixMs != 111 ||
		len(page[0].SupportedExtensions) != 1 || page[0].SupportedExtensions[0].Key != "vendor/first" {
		t.Fatalf("mixed registration generations: revision=%s page=%+v err=%v", capturedRevision, page, err)
	}
	current, currentRevision, err := p.listPinned()
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
	endpoint := listen.Endpoint(fmt.Sprintf("registry-v2-%d-%d", os.Getpid(), endpointSeq.Add(1)))
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
	if err != nil || description.Outcome != wire.DescriptionOutcomeDescribed || len(description.Services) != 3 ||
		description.Services[0].Contract != RegistryContract || description.Services[1].Contract != RegistryV2Contract ||
		description.Services[2].Contract != RegistryV3Contract || description.Services[0].Readiness != wire.ServiceReadinessReady || description.Services[1].Readiness != wire.ServiceReadinessReady || description.Services[2].Readiness != wire.ServiceReadinessReady {
		t.Fatalf("shared endpoint description: %+v %v", description, err)
	}
	v1 := wire.NewRegistryClient(transport)
	v2 := wire.NewRegistryV2Client(transport)
	before, err := v2.DeclarationsPinned()
	if err != nil {
		t.Fatal(err)
	}
	ext := wire.DeclarationExtension{Profile: "image", Key: "vendor/steps"}
	added, err := v2.DeclarePinned(before.Revision, wire.PinnedDeclaration{Declaration: extensionDeclaration(), SupportedExtensions: []wire.DeclarationExtension{ext}})
	if err != nil || added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("v2 IPC Declare: %+v %v", added, err)
	}
	old, err := v1.Declarations() // The strict v1 decoder rejects unknown fields.
	if err != nil || old.Outcome != wire.DeclarationListOutcomePage || len(old.Declarations) != 1 || old.Declarations[0].Declaration.Name != "image-provider" {
		t.Fatalf("strict v1 IPC read: %+v %v", old, err)
	}
	newer, err := v2.ObservePinned("", 0)
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
