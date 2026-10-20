package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

func shortMediationEndpoint(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\oa-mediation-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	dir, err := os.MkdirTemp("/tmp", "oa-mediation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

func TestProviderProjectionRetiresStartedSupervisorAndStartsReplacement(t *testing.T) {
	p, err := openFixtureProviders(t, t.TempDir(), false, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	previous := providerFile{Version: providerFileVersion, Generation: "before", Source: declarationSourceProduct,
		Declaration: providerDeclaration{Name: "projection-replacement", Program: program, Endpoint: "projection-replacement", Transport: transportNative,
			Contracts: []string{inference.Contract}, Activation: wire.ActivationOnDemand.String(), Arguments: []string{providerEndpointToken}}}
	next := previous
	next.Generation = "after"
	p.productsMu.Lock()
	p.product, p.productHas = []providerFile{next}, true
	p.productsMu.Unlock()
	old := newFixtureProviderSupervisor(t, p, previous)
	p.supervisors[previous.Declaration.Name] = old
	p.serving = true
	old.start()
	p.refreshProgramRegistrations(context.Background())
	select {
	case <-old.done:
	default:
		t.Fatal("projection retained its started predecessor")
	}
	replacement := p.supervisors[next.Declaration.Name]
	if replacement == nil || replacement == old || replacement.file.bindingID() != next.bindingID() {
		t.Fatal("projection did not install the replacement")
	}
	replacement.mu.Lock()
	started := replacement.started
	replacement.mu.Unlock()
	if !started || p.retiring[next.Declaration.Name] != nil {
		t.Fatal("projection did not finish retirement and start the replacement")
	}
}

func TestProviderBindingSurvivesRestartAndChangesOnRedeclaration(t *testing.T) {
	state := t.TempDir()
	report := func(err error) { t.Errorf("providers: %v", err) }
	p, err := openFixtureProviders(t, state, false, report)
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := providerFile{Version: providerFileVersion, DeclaredBy: program, DeclaredAt: 1,
		Declaration: providerDeclaration{Name: "binding", Program: program, Endpoint: "binding-fixture", Transport: transportNative,
			Contracts: []string{"abstraction.inference/chat@1"}, Activation: wire.ActivationAttach.String()}}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	added := p.add(revision, f)
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("add: %+v", added)
	}
	original := p.supervisors["binding"].file
	originalEndpoint := p.supervisors["binding"].endpoint
	if originalEndpoint == "" || p.supervisors["binding"].endpointErr != nil {
		t.Fatalf("injected constructor did not reach added supervisor: %q %v", originalEndpoint, p.supervisors["binding"].endpointErr)
	}
	if original.Generation == "" {
		t.Fatal("new declaration has no persisted generation")
	}
	reopened, err := openFixtureProviders(t, state, false, report)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.supervisors["binding"].file.bindingID() != original.bindingID() {
		t.Fatal("ordinary restart changed binding")
	}
	if reopened.supervisors["binding"].endpoint != originalEndpoint || reopened.supervisors["binding"].endpointErr != nil {
		t.Fatal("injected constructor did not preserve the loaded supervisor endpoint")
	}
	defaults, err := openProviders(state, false, report)
	if err != nil {
		t.Fatal(err)
	}
	refused := defaults.supervisors["binding"]
	if refused.endpoint != "" || !errors.Is(refused.endpointErr, bootstrap.ErrNoTrustedInstallation) {
		t.Fatalf("default constructor escaped test refusal: %q %v", refused.endpoint, refused.endpointErr)
	}
	removed := p.remove(added.Revision, "binding")
	if removed.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("remove: %+v", removed)
	}
	// Identical source configuration and timestamp still define a new generation.
	added = p.add(removed.Revision, f)
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("re-add: %+v", added)
	}
	if p.supervisors["binding"].file.bindingID() == original.bindingID() {
		t.Fatal("redeclaration reused binding")
	}
	changed := original
	changed.Declaration.Endpoint = "replacement-endpoint"
	if changed.bindingID() == original.bindingID() {
		t.Fatal("endpoint change retained binding")
	}
}

func TestResolverSelectionRetainsTheChosenProviderEndpointForSharedModel(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	provider := func(name, generation, guarantee string) providerFile {
		return providerFile{Version: providerFileVersion, Generation: generation, DeclaredBy: program,
			Declaration: providerDeclaration{Name: name, Program: program, Endpoint: "private-" + name, Transport: transportNative,
				Contracts: []string{inference.Contract}, Guarantees: []string{guarantee}, Models: []string{"owner/shared-model"}, Activation: wire.ActivationAttach.String()}}
	}
	first := provider("first", "first-generation", "fixture/first@1")
	second := provider("second", "second-generation", "fixture/second@1")
	p.supervisors["first"] = newFixtureProviderSupervisor(t, p, first)
	p.supervisors["second"] = newFixtureProviderSupervisor(t, p, second)
	p.supervisors["first"].readiness = wire.DeclarationReadinessReady
	p.supervisors["second"].readiness = wire.DeclarationReadinessReady
	p.setMediated(map[string]mediatedProvider{first.bindingID(): {Endpoint: "oa-first", Ready: true}, second.bindingID(): {Endpoint: "oa-second", Ready: true}})

	catalog, err := resolution.New(p.Candidates())
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalog.ForCaller(func(wire.ServiceReference) bool { return true }).Resolve(wire.ResolveRequest{
		Capability: "abstraction.inference", Contracts: []string{inference.Contract}, Guarantees: []string{"fixture/second@1"}, Scope: wire.ScopeLocal,
	})
	if err != nil || result.Status != wire.ResolutionStatusResolved || result.Reference == nil {
		t.Fatalf("resolve: %+v, %v", result, err)
	}
	if result.Reference.Provider != "second" || result.Reference.Endpoint != "oa-second" {
		t.Fatalf("selected %+v", result.Reference)
	}
}

func TestImageOnlyNativeProviderRegistersMediatedProfile(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	file := providerFile{Version: providerFileVersion, Generation: "image-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: "image-only", Program: program, Endpoint: "private-image", Transport: transportNative,
			Contracts: []string{inference.ImageContract}, Resources: []string{"profile:image"}, Models: []string{"stability/sdxl"}, Activation: wire.ActivationOnDemand.String()}}
	p.supervisors[file.Declaration.Name] = newFixtureProviderSupervisor(t, p, file)
	p.setMediated(map[string]mediatedProvider{file.bindingID(): {Endpoint: "oa-image", Ready: true}})
	if hosts := p.nativeInferenceHosts(); len(hosts) != 1 || len(hosts[0].HostProfiles()) != 1 || hosts[0].HostProfiles()[0] != router.ProfileImage {
		t.Fatalf("image-only router hosts %+v", hosts)
	}
	refs := p.Candidates()
	if len(refs) != 1 || refs[0].Reference.Contract != inference.ImageContract || refs[0].Reference.Endpoint != "oa-image" {
		t.Fatalf("image-only candidates %+v", refs)
	}
	m := &providerMediation{providers: p}
	if _, ok := m.desired()[file.bindingID()]; !ok {
		t.Fatal("image-only declaration omitted from mediation")
	}
}

// The installed Lemonade declaration's shape: every profile it serves is a
// resolution candidate at the mediated endpoint, and each model is routed and
// admitted for its own profile only (router CONTRACT.md ROUTE-D9).
func TestNativeProviderOffersEveryDeclaredProfileAndScopesItsModels(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}, changes: make(chan struct{})}
	file := providerFile{Version: providerFileVersion, Generation: "speech-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: "lemonade-image", Program: program, Endpoint: "private-lemonade-image", Transport: transportNative,
			Contracts: []string{inference.ImageContract, inference.TranscriptionContract, inference.SpeechContract},
			Resources: []string{"profile:image", "profile:transcription", "profile:speech"},
			Models:    []string{"image:user.LocalApprovedKlein", "transcription:Whisper-Large-v3-Turbo", "speech:kokoro-v1"}, Activation: wire.ActivationOnDemand.String()}}
	if field := validProviderDeclaration(file.Declaration); field != "" {
		t.Fatalf("installed declaration shape invalid: %s", field)
	}
	p.supervisors[file.Declaration.Name] = newFixtureProviderSupervisor(t, p, file)
	p.setMediated(map[string]mediatedProvider{file.bindingID(): {Endpoint: "oa-lemonade", Ready: true}})

	contracts := map[string]bool{}
	for _, c := range p.Candidates() {
		if c.Reference.Endpoint != "oa-lemonade" || c.Reference.Provider != "lemonade-image" {
			t.Fatalf("candidate %+v", c.Reference)
		}
		contracts[c.Reference.Contract] = true
	}
	if len(contracts) != 3 || !contracts[inference.ImageContract] || !contracts[inference.TranscriptionContract] || !contracts[inference.SpeechContract] {
		t.Fatalf("candidate contracts %v", contracts)
	}
	catalog, err := resolution.New(p.Candidates())
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{inference.TranscriptionContract, inference.SpeechContract} {
		result, err := catalog.ForCaller(func(wire.ServiceReference) bool { return true }).Resolve(wire.ResolveRequest{
			Capability: "abstraction.inference", Contracts: []string{contract}, Guarantees: []string{}, Scope: wire.ScopeLocal})
		if err != nil || result.Status != wire.ResolutionStatusResolved || result.Reference == nil || result.Reference.Endpoint != "oa-lemonade" {
			t.Fatalf("resolve %s: %+v, %v", contract, result, err)
		}
	}

	hosts := p.nativeInferenceHosts()
	if len(hosts) != 1 {
		t.Fatalf("native hosts %+v", hosts)
	}
	routes := router.New(hosts...)
	routes.Survey()
	if d, _ := routes.Route(router.Request{Model: "kokoro-v1", Profile: router.ProfileImage}); d.Verdict != router.NoHost {
		t.Fatalf("image kokoro-v1: %s (%s)", d.Verdict, d.Why)
	}
	if d, _ := routes.Route(router.Request{Model: "Whisper-Large-v3-Turbo", Profile: router.ProfileTranscription}); d.Verdict != router.WouldLoad || d.Host != "lemonade-image" {
		t.Fatalf("transcription Whisper: %s %q (%s)", d.Verdict, d.Host, d.Why)
	}
	if hosts[0].TranscriptionURL() == "" || hosts[0].SpeechURL() == "" {
		t.Fatal("the router answers no audio address for the native host")
	}
	// The runtime's own admission is the second line, and refuses before any
	// launch.
	ctx := inference.WithAdmissionProfile(context.Background(), router.ProfileImage)
	if reason, ok := p.nativeAdmit(ctx, hosts[0], "kokoro-v1"); ok || reason != "model:undeclared" || activated(p.supervisors[file.Declaration.Name]) {
		t.Fatalf("image kokoro-v1 admitted: %q %v", reason, ok)
	}
}

// A model entry scoped to a profile the declaration lists without its
// contract would read as a model named "<profile>:<model>"; it is refused.
func TestScopedModelNeedsTheProfileContract(t *testing.T) {
	d := providerDeclaration{Name: "scoped", Program: filepath.Join(t.TempDir(), "provider.exe"), Endpoint: "scoped", Transport: transportNative,
		Contracts: []string{inference.ImageContract}, Resources: []string{"profile:image", "profile:speech"},
		Models: []string{"image:picture", "speech:voice"}, Activation: wire.ActivationOnDemand.String()}
	if field := validProviderDeclaration(d); field != "models" {
		t.Fatalf("speech-scoped model without speech@1: %q", field)
	}
	d.Contracts = append(d.Contracts, inference.SpeechContract)
	if field := validProviderDeclaration(d); field != "" {
		t.Fatalf("scoped declaration refused: %q", field)
	}
	// An Ollama-style tag is a model name, never a profile.
	d.Models = []string{"qwen3:8b"}
	if field := validProviderDeclaration(d); field != "" {
		t.Fatalf("tagged model name refused: %q", field)
	}
}

// A legacy chat-only declaration lists no profile and serves chat
// implicitly; its entries are read against that chat profile, by the same
// parser the router host uses (router.DeclaredEntry, ROUTE-D9).
func TestLegacyChatOnlyDeclarationReadsEntriesAgainstImplicitChat(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	file := providerFile{Version: providerFileVersion, Generation: "legacy-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: "legacy", Program: program, Endpoint: "private-legacy", Transport: transportNative,
			Contracts: []string{inference.Contract}, Models: []string{"chat:owner/scoped", "qwen3:8b", "chat:chat:x"}, Activation: wire.ActivationAttach.String()}}
	if field := validProviderDeclaration(file.Declaration); field != "" {
		t.Fatalf("legacy chat-only declaration with scoped entries refused: %s", field)
	}
	p.supervisors[file.Declaration.Name] = newFixtureProviderSupervisor(t, p, file)
	p.supervisors[file.Declaration.Name].readiness = wire.DeclarationReadinessReady
	hosts := p.nativeInferenceHosts()
	if len(hosts) != 1 || !slices.Equal(hosts[0].HostProfiles(), []string{router.ProfileChat}) {
		t.Fatalf("legacy hosts %+v", hosts)
	}
	routes := router.New(hosts...)
	routes.Survey()
	// chat:chat:x is the model literally named chat:x, for chat.
	for _, model := range []string{"owner/scoped", "qwen3:8b", "chat:x"} {
		if d, _ := routes.Route(router.Request{Model: model, Profile: router.ProfileChat}); d.Verdict != router.WouldLoad || d.Model != model {
			t.Fatalf("chat %s: %s %q (%s)", model, d.Verdict, d.Model, d.Why)
		}
		if !hosts[0].ServesModel(router.ProfileChat, model) {
			t.Fatalf("host does not serve %s for chat", model)
		}
	}
}

func TestProviderMediationRepeatsTheRegistryRouteBound(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name   string
		paired int
	}{{"native", 0}, {"mixed", 1}, {"paired", maxProviders + 1}} {
		t.Run(row.name, func(t *testing.T) {
			p := &runtimeProviders{report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
			for i := 0; i < maxProviders+1-row.paired; i++ {
				name := fmt.Sprintf("provider-%02d", i)
				file := providerFile{Version: providerFileVersion, Generation: fmt.Sprintf("generation-%02d", i), DeclaredBy: program,
					Declaration: providerDeclaration{Name: name, Program: program, Endpoint: "private-" + name, Transport: transportNative,
						Contracts: []string{inference.Contract}, Models: []string{"shared-model"}, Activation: wire.ActivationAttach.String()}}
				p.supervisors[name] = newFixtureProviderSupervisor(t, p, file)
			}
			p.pairedHostSource = func() ([]*router.Host, error) {
				var hosts []*router.Host
				for i := 0; i < row.paired; i++ {
					hosts = append(hosts, &router.Host{Name: fmt.Sprintf("paired-%02d", i), BindingID: fmt.Sprintf("paired:%02d", i)})
				}
				return hosts, nil
			}
			m := &providerMediation{base: "unused", providers: p, routes: map[string]*providerMediationRoute{}}
			if err := m.sync(); err == nil {
				t.Fatalf("more than %d mediation listeners were accepted", maxProviders)
			}
			if len(m.routes) != 0 || len(p.mediated) != 0 {
				t.Fatalf("over-limit mediation published routes: %d, %d", len(m.routes), len(p.mediated))
			}
		})
	}
}

func TestProviderMediationEndpointIncludesTheOwningRuntime(t *testing.T) {
	binding := "provider-binding-v1:same"
	first := mediatedProviderEndpoint(listen.Endpoint("first-runtime"), binding)
	second := mediatedProviderEndpoint(listen.Endpoint("second-runtime"), binding)
	if first == second || first != mediatedProviderEndpoint(listen.Endpoint("first-runtime"), binding) {
		t.Fatalf("runtime endpoints %q and %q", first, second)
	}
}

func TestProviderRoutingBusyPairingRetainsDirectAndWithdrawsPeer(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	p := &runtimeProviders{dir: root, disabledPath: filepath.Join(root, "disabled.json"), principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}}
	file := providerFile{Version: providerFileVersion, Generation: "direct-generation", DeclaredBy: program, Declaration: providerDeclaration{Name: "direct", Program: program, Endpoint: "inert-direct", Transport: transportNative, Contracts: []string{inference.Contract}, Models: []string{"shared-model"}, Activation: wire.ActivationAttach.String()}}
	p.supervisors["direct"] = newFixtureProviderSupervisor(t, p, file)
	p.supervisors["direct"].readiness = wire.DeclarationReadinessReady
	// The paired contribution is inert: a declared model list performs no IPC.
	peer, err := router.NewNative("peer", "inert-peer", listen.ServerExpectation{}, []string{"shared-model"}, []string{router.ProfileChat})
	if err != nil {
		t.Fatal(err)
	}
	peer.Domain, peer.BindingID = "peer", "paired:fixture"
	busy, duplicate, conflict := false, false, false
	p.pairedHostSource = func() ([]*router.Host, error) {
		if busy {
			return []*router.Host{peer}, errPairingBusy
		} // Even supplied rows are unusable with an error.
		if conflict {
			// pairedHosts reports a local declaration holding the peer's name.
			return nil, errPairingConflict
		}
		if duplicate {
			return []*router.Host{{Name: "direct"}}, nil
		}
		return []*router.Host{peer}, nil
	}
	routes := router.New()
	ceilings, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "ceilings.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var reported error
	op := &inferenceOperator{providers: p, router: routes, ceilings: ceilings, report: func(err error) { reported = err }}
	if err := op.refreshHosts(); err != nil {
		t.Fatal(err)
	}
	peerOnly := []string{"peer"}
	request := router.Request{Model: "shared-model", Profile: router.ProfileChat, Hosts: &peerOnly}
	if decision, _ := routes.Route(request); decision.Host != "peer" {
		t.Fatal("fixture peer route unavailable", decision)
	}
	busy = true
	if err := op.refreshHosts(); !errors.Is(err, errPairingBusy) || !errors.Is(reported, errPairingBusy) {
		t.Fatal("paired error lost", err, reported)
	}
	if hosts := routes.Hosts(); len(hosts) != 1 || hosts[0].Name != "direct" {
		t.Fatal("independent host lost or failed peer retained", hosts)
	}
	if decision, _ := routes.Route(request); decision.Verdict != router.Unauthorised || decision.Host != "" || !slices.Equal(decision.Withheld, []string{"direct"}) {
		t.Fatal("peer-scoped request substituted another host", decision)
	}
	directOnly := []string{"direct"}
	request.Hosts = &directOnly
	if decision, _ := routes.Route(request); decision.Host != "direct" {
		t.Fatal("independent route unavailable", decision)
	}
	busy, duplicate = false, true
	if err := op.refreshHosts(); err == nil || len(routes.Hosts()) != 0 {
		t.Fatal("ambiguous known names published", err, routes.Hosts())
	}
	// Source1903: a paired name conflict is a known ambiguous name, distinct
	// from an unavailable paired read, and publishes no host.
	duplicate, conflict = false, true
	if err := op.refreshHosts(); !errors.Is(err, errPairingConflict) || !errors.Is(reported, errPairingConflict) || len(routes.Hosts()) != 0 {
		t.Fatal("paired name conflict published hosts", err, reported, routes.Hosts())
	}
}

func TestProviderMediationStartsAPendingRetirementSweep(t *testing.T) {
	providers := &runtimeProviders{report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}, changes: make(chan struct{})}
	provider := &inference.Provider{}
	binding := inference.ExecutionBinding{Host: "retired", BindingID: "retired-before-start"}
	endpoint := shortMediationEndpoint(t)
	host, err := inferenceservice.ListenMediated(endpoint, provider, inference.ExecutionLocal, binding)
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		l, listenErr := listen.Listen(endpoint)
		if listenErr != nil {
			t.Fatalf("unproven mediation retained listener: %v", listenErr)
		}
		l.Close()
		t.Skip("retirement sweep requires a mediated Program listener; Unix startup refusal is checked separately")
	}
	if err != nil {
		t.Fatal(err)
	}
	m := &providerMediation{base: endpoint, provider: provider, providers: providers, report: func(error) {}, routes: map[string]*providerMediationRoute{
		binding.BindingID: {endpoint: endpoint, host: host, retired: true},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	eventually(t, 3*time.Second, "the pre-Serve retired route being swept", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, exists := m.routes[binding.BindingID]
		return !exists
	})
}

func TestOneMediationListenerFailureLeavesOtherCandidatesAvailable(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	provider := func(name string) providerFile {
		return providerFile{Version: providerFileVersion, Generation: name + "-generation", DeclaredBy: program,
			Declaration: providerDeclaration{Name: name, Program: program, Endpoint: "private-" + name, Transport: transportNative,
				Contracts: []string{inference.Contract}, Models: []string{"shared-model"}, Activation: wire.ActivationAttach.String()}}
	}
	blocked, healthy := provider("blocked"), provider("healthy")
	p.supervisors["blocked"] = newFixtureProviderSupervisor(t, p, blocked)
	p.supervisors["healthy"] = newFixtureProviderSupervisor(t, p, healthy)
	p.supervisors["blocked"].readiness = wire.DeclarationReadinessReady
	p.supervisors["healthy"].readiness = wire.DeclarationReadinessReady
	base := listen.Endpoint(fmt.Sprintf("mediation-isolation-%d", time.Now().UnixNano()))
	occupied, err := listen.Listen(mediatedProviderEndpoint(base, blocked.bindingID()))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var reported []error
	m, err := newProviderMediation(base, &inference.Provider{}, p, func(err error) { reported = append(reported, err) }, func(context.Context, inference.Subject, []string) (bool, error) { return false, nil }, func(context.Context, inference.Subject, []string) ([]string, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if runtime.GOOS == "darwin" {
		if len(reported) != 2 || !errors.Is(reported[0], identity.ErrNotProven) || !errors.Is(reported[1], identity.ErrNotProven) {
			t.Fatalf("Unix mediation proof refusals: %v", reported)
		}
		for _, candidate := range p.Candidates() {
			if candidate.Ready {
				t.Fatalf("unproven mediation published ready candidate: %+v", candidate)
			}
		}
		t.Skip("healthy mediation candidate requires Program proof; Unix startup refusal is checked separately")
	}
	if len(reported) != 1 {
		t.Fatalf("listener failures reported %d times: %v", len(reported), reported)
	}
	if m.routes[healthy.bindingID()].host.AwaitQuestions == nil {
		t.Fatal("application-facing mediated host omitted native question owner")
	}
	states := map[string]bool{}
	for _, candidate := range p.Candidates() {
		states[candidate.Reference.Provider] = candidate.Ready
	}
	if len(states) != 2 || states["blocked"] || !states["healthy"] {
		t.Fatalf("candidate readiness %v", states)
	}
	candidates := append(p.Candidates(), resolution.Candidate{Ready: true, Reference: wire.ServiceReference{
		Provider: "unrelated", Capability: "abstraction.logging", Contract: "abstraction.logging/sink@1",
		Scope: wire.ScopeLocal, Transport: resolution.LocalTransport, Endpoint: "logging-endpoint", Guarantees: []string{},
	}})
	catalog, err := resolution.New(candidates)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := catalog.ForCaller(func(wire.ServiceReference) bool { return true }).Resolve(wire.ResolveRequest{
		Capability: "abstraction.logging", Contracts: []string{"abstraction.logging/sink@1"}, Guarantees: []string{}, Scope: wire.ScopeLocal,
	})
	if err != nil || unrelated.Status != wire.ResolutionStatusResolved || unrelated.Reference == nil || unrelated.Reference.Provider != "unrelated" {
		t.Fatalf("unrelated candidate after mediation failure: %+v, %v", unrelated, err)
	}
}

func TestNativeCandidatesUseMediationAndUnsupportedContractsStayPrivate(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	chat := providerFile{Version: providerFileVersion, Generation: "chat-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: "chat", Program: program, Endpoint: "private-chat", Transport: transportNative,
			Contracts: []string{inference.Contract}, Guarantees: []string{"fixture"}, Resources: []string{"profile:chat"}, Models: []string{"owner/fixture-model"}, Activation: wire.ActivationAttach.String()}}
	unsupported := providerFile{Version: providerFileVersion, Generation: "other-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: "other", Program: program, Endpoint: "private-other", Transport: transportNative,
			Contracts: []string{"example.unsupported/service@1"}, Models: []string{"owner/fixture-model"}, Activation: wire.ActivationAttach.String()}}
	p.supervisors["chat"] = newFixtureProviderSupervisor(t, p, chat)
	p.supervisors["other"] = newFixtureProviderSupervisor(t, p, unsupported)
	p.supervisors["chat"].readiness = wire.DeclarationReadinessReady
	p.supervisors["other"].readiness = wire.DeclarationReadinessReady
	p.setMediated(map[string]mediatedProvider{chat.bindingID(): {Endpoint: "oa-inference", Ready: true}})

	candidates := p.Candidates()
	if len(candidates) != 1 || candidates[0].Reference.Provider != "chat" || candidates[0].Reference.Endpoint != "oa-inference" || candidates[0].Reference.Endpoint == p.supervisors["chat"].endpoint {
		t.Fatalf("mediated candidates %+v", candidates)
	}
	hosts := p.nativeInferenceHosts()
	if len(hosts) != 1 || hosts[0].Name != "chat" || hosts[0].BindingID != chat.bindingID() {
		t.Fatalf("native hosts %+v", hosts)
	}
	transport, ok := hosts[0].NativeTransport()
	if !ok || transport.Endpoint != p.supervisors["chat"].endpoint || transport.Server == nil || transport.Server.Program != program {
		t.Fatalf("native transport %+v, %v", transport, ok)
	}
}
