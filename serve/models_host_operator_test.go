package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	inferenceapi "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

type modelHostOperatorFixture struct {
	page     registration.Page
	spec     registration.RegistrationSpec
	expected string
	writes   int
	outcome  registration.Outcome
}

// This adapter keeps per-call contexts on the real generated native channel.
type modelHostOperatorNative struct{ transport listen.FrameClient }

func (f modelHostOperatorNative) Registrations(ctx context.Context, cursor string, limit int64) (registration.Page, error) {
	return registration.NewRegistryV3Client(f.transport.WithContext(ctx)).Registrations(cursor, limit)
}
func (f modelHostOperatorNative) Register(ctx context.Context, spec registration.RegistrationSpec) (registration.Change, error) {
	return registration.NewRegistryV3Client(f.transport.WithContext(ctx)).Register(spec)
}
func (f modelHostOperatorNative) Replace(ctx context.Context, revision string, spec registration.RegistrationSpec) (registration.Change, error) {
	return registration.NewRegistryV3Client(f.transport.WithContext(ctx)).Replace(revision, spec)
}

func (f *modelHostOperatorFixture) Registrations(context.Context, string, int64) (registration.Page, error) {
	return f.page, nil
}
func (f *modelHostOperatorFixture) Register(_ context.Context, spec registration.RegistrationSpec) (registration.Change, error) {
	f.spec = spec
	f.writes++
	return registration.Change{Outcome: f.outcome, Revision: "new"}, nil
}
func (f *modelHostOperatorFixture) Replace(ctx context.Context, expected string, spec registration.RegistrationSpec) (registration.Change, error) {
	f.expected = expected
	return f.Register(ctx, spec)
}
func (*modelHostOperatorFixture) DeclarePinned(context.Context, string, wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	panic("unexpected legacy declaration")
}
func (*modelHostOperatorFixture) ReplacePinned(context.Context, string, wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	panic("unexpected legacy replace")
}
func (*modelHostOperatorFixture) Withdraw(context.Context, string, string) (wire.DeclarationChange, error) {
	panic("unexpected withdrawal")
}

func TestModelHostOperatorConfiguredDefaultJourney(t *testing.T) {
	// Reading the existing configuration supplies the engine; this management
	// invocation contributes no --engine flag and starts no producer child.
	settings := hostSettings{program: filepath.Join(t.TempDir(), "producer.exe"), engine: filepath.Join(t.TempDir(), "configured-engine.exe"), endpoint: "logical"}
	models := []hostedModel{{Name: "m", Store: "store", Object: "object"}}
	old := modelHostDeclaration(settings, models, nil)
	list := wire.DeclarationList{Revision: "legacy", Declarations: []wire.DeclarationState{{Declaration: old}}}
	inherited, configured := readHostDeclaration(list)
	if inherited.engine != settings.engine || len(configured) != 1 {
		t.Fatalf("configured default lost: %+v %+v", inherited, configured)
	}
	f := &modelHostOperatorFixture{page: registration.Page{Outcome: registration.OutcomePage}, outcome: registration.OutcomeApplied}
	w := newWaiting(0)
	defer w.stop()
	writer, err := newModelHostRegistryWriter(context.Background(), f, f)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := declareHost(w, writer, list, inherited, configured, "models host", &out, false, "configured"); err != nil {
		t.Fatal(err)
	}
	if f.writes != 1 || len(f.spec.Providers) != 1 {
		t.Fatalf("typed declaration missing: %+v", f)
	}
	if reason := registrationSpecValid(f.spec); reason != "" {
		t.Fatalf("invalid operator spec: %s", reason)
	}
	facet := f.spec.Providers[0]
	if facet.Endpoint != settings.endpoint || len(facet.Engines) != 1 || facet.Engines[0].Version != "" || len(facet.Recipes) != 1 {
		t.Fatalf("configured claims: %+v", facet)
	}
	if len(facet.Services) < 2 || facet.Services[1] != "abstraction.inference/preload@1" {
		t.Fatalf("preload missing: %+v", facet.Services)
	}
	host := &router.Host{Name: facet.Name}
	setProviderEngineGuide(host, providerFile{Generation: "operator", ProviderFacet: &facet})
	if len(host.Engines) != 1 || len(host.EngineObjects) != 1 || host.EngineObjects[0].Engines[0].Model != "m" {
		t.Fatalf("accepted guide facts: %+v", host)
	}
	// The inert source fixture answers endpoint@2 over the native framing
	// owner. It supplies live readiness independently from configured facts.
	endpoint := shortMediationEndpoint(t)
	listener, err := listen.ListenFramed(endpoint, listen.Program)
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	answered := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			answered <- err
			return
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		call, err := listen.ReceiveFramed(ctx, conn, listen.Program, providerDescriptionFrameLimit)
		if err != nil {
			answered <- err
			return
		}
		defer call.Close()
		value := registration.Description{Outcome: registration.DescriptionOutcomeDescribed, Program: "fixture", Version: "fixture-live", Services: []registration.ServiceState{{Service: facet.Services[0], Readiness: wire.ServiceReadinessNotReady, Reason: "engine:unobserved", DefinitionHash: (&inferenceapi.ChatDispatcher{}).DescribeServiceDefinitionHash()}}}
		reply, err := (&registration.EndpointV2Dispatcher{Handler: providerEndpoint2Answer{value: value}}).ExchangeFrame(call.Frame)
		if err == nil {
			err = call.Reply(reply)
		}
		answered <- err
	}()
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	legacy, observed, err := providerDescribeEndpoint((listen.FrameClient{Endpoint: endpoint, Timeout: 2 * time.Second, MaxFrame: providerDescriptionFrameLimit, Server: &listen.ServerExpectation{Principal: principal, Program: program}}).WithContext(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
	if observed == nil || len(observed.Checks) != 0 || facet.Engines[0].Version != "" || legacy.Services[0].Readiness != wire.ServiceReadinessNotReady {
		t.Fatalf("configured facts inferred live verification: %+v %+v", observed, legacy)
	}
	s := &providerSupervisor{p: &runtimeProviders{changes: make(chan struct{})}, file: providerFile{Generation: "operator", ProviderFacet: &facet}}
	s.recordEndpoint(t.Context(), legacy.Services, nil, observed)
	reading, ok := s.registrationDescription(s.file.bindingID())
	if !ok || len(reading.Checks) != 0 || reading.Services[0].Reason != "engine:unobserved" {
		t.Fatalf("native description observation lost: %+v %v", reading, ok)
	}
}

func TestModelHostOperatorConditionalMerge(t *testing.T) {
	program := filepath.Join(t.TempDir(), "producer.exe")
	selected := registration.ProviderFacet{Name: "selected", Endpoint: "pipe:logical", Activation: registration.ActivationOnDemand, Services: []string{"test/service@1"}}
	other := selected
	other.Name = "unrelated"
	current := registration.Registration{Revision: "captured", Spec: registration.RegistrationSpec{Program: program, Providers: []registration.ProviderFacet{other}, Servers: []registration.ServerFacet{{Name: "retained", Hosted: true, Kind: router.WireOpenAICompatible, Base: "https://fixture.invalid", Modalities: []string{"chat"}}}}}
	f := &modelHostOperatorFixture{page: registration.Page{Outcome: registration.OutcomePage, Registrations: []registration.Registration{current}}, outcome: registration.OutcomeConflict}
	writer, err := newModelHostRegistryWriter(context.Background(), f, f)
	if err != nil {
		t.Fatal(err)
	}
	change, err := writer.WriteConfigured(context.Background(), registration.RegistrationSpec{Program: program, Providers: []registration.ProviderFacet{selected}}, false)
	if err != nil || change.Outcome != wire.DeclarationEditOutcomeConflict || f.expected != "captured" || f.writes != 1 {
		t.Fatalf("conditional update: %+v %+v %v", change, f, err)
	}
	if len(f.spec.Providers) != 2 || f.spec.Providers[0].Name != "unrelated" || len(f.spec.Servers) != 1 {
		t.Fatalf("unrelated facets erased: %+v", f.spec)
	}
}

func TestModelHostOperatorPyTorchKeepPinsFirstRegistration(t *testing.T) {
	requireNativeModelHostRegistration(t)
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current native transport")
	}
	program := recordingModelHost(t, t.TempDir())
	t.Setenv("STANDIN_ARGS", filepath.Join(t.TempDir(), "engine-check"))
	run := newSelfRegisteredRunWith(t, true)
	r, caller := registryObserverFixture(t)
	r.operator.credentials.runtimeRights.applications = run.d
	r.providers.registrationDirectory = func() *applicationDirectory { return run.d }
	r.providers.installation = ""
	endpoint := shortMediationEndpoint(t)
	served, err := listenRegistry(endpoint, r, caller.Account, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- served.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = served.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	native := modelHostOperatorNative{listen.FrameClient{Endpoint: endpoint, Timeout: 3 * time.Second, MaxFrame: 1 << 20, Server: &listen.ServerExpectation{Principal: principal, Program: self}}}
	settings := hostSettings{
		torch: torchSettings{program: program, endpoint: "pytorch-recipe-review"},
		media: hostMedia{imageModel: "z-image-turbo", torchPython: filepath.Join(t.TempDir(), "venv-python.exe")},
	}
	configure := func() (string, string) {
		t.Helper()
		writer, err := newModelHostRegistryWriter(ctx, &modelHostOperatorFixture{}, native)
		if err != nil {
			t.Fatal(err)
		}
		check := settings
		if err := proveConfiguredMedia("models host", &check, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		defer check.torch.proved.Close()
		w := newWaiting(10 * time.Second)
		defer w.stop()
		var output bytes.Buffer
		if err := declareHost(w, writer, wire.DeclarationList{}, check, nil, "models host", &output, false, "configured"); err != nil {
			t.Fatal(err)
		}
		digest, err := registryHeldImageSHA256(ctx, check.torch.proved)
		if err != nil {
			t.Fatal(err)
		}
		return digest, output.String()
	}
	digest, _ := configure()
	page, err := native.Registrations(ctx, "", 64)
	if err != nil || page.Outcome != registration.OutcomePage || len(page.Registrations) != 1 {
		t.Fatalf("pending registration: %+v %v", page, err)
	}
	row := page.Registrations[0]
	recipe, _ := pyTorchRecipe("z-image-turbo", router.ProfileImage)
	if row.State != registration.StatePendingApproval || len(row.Spec.Providers) != 1 ||
		len(row.Spec.Providers[0].Recipes) != 1 || !slices.Equal(row.Spec.Providers[0].Recipes[0].Objects, recipe.Objects) {
		t.Fatalf("missing the configured registration's exact recipe: %+v", row)
	}
	question := programKeepQuestion(t, run, row.Key)
	if same, output := configure(); same != digest || !strings.Contains(output, "already says this") {
		t.Fatalf("exact same pending image and configuration did not reuse its receipt: %q", output)
	}
	// A compatibility observation keeps the native program evidence but
	// cannot supply a configured recipe's expected bytes.
	if refreshed := run.d.editProgramRegistration(ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		observed := s.programSources[row.Key]
		observed.ImageSHA256 = ""
		s.keepProgramSource(row.Key, observed)
		return registration.Change{Outcome: registration.OutcomeApplied, Revision: row.Revision}, nil
	}); refreshed.Outcome != registration.OutcomeApplied {
		t.Fatal(refreshed)
	}
	run.start(t)
	r.operator.credentials.runtimeRights.applications = run.d
	_, source, mark, ok := run.d.programSelfEntry(row.Key)
	if !ok || source.ImageSHA256 == "" || source.ImageSHA256 != digest || mark.Question != question.ID {
		t.Fatalf("first registration's image pin or question was not durable: %+v %+v", source, mark)
	}
	// Modify the contents of the existing fixture file, leaving its ID and
	// unsigned trust result intact. Keep has no live handle from Register.
	image, err := os.OpenFile(program, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = image.WriteString("changed bytes")
	if err = errors.Join(err, image.Close()); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || id.Key() != source.FileKey {
		t.Fatalf("the fixture changed file identity: %v %v", id, err)
	}
	legacy := source
	legacy.ImageSHA256 = ""
	withoutPin, err := recaptureProgramKeepImage(ctx, legacy)
	if err != nil {
		t.Fatalf("the original file ID and signature evidence no longer match: %v", err)
	}
	if err := withoutPin.Close(); err != nil {
		t.Fatal(err)
	}
	if !run.k.followed(question.ID) {
		t.Fatal("the restarted runtime is not following the pending Keep")
	}
	answer(t, run.f, question.ID, "keep")
	waitUnfollowed(t, run.k, question.ID)
	if kept, captured, _, ok := run.d.programSelfEntry(row.Key); !ok ||
		kept.State != registration.StatePendingApproval || captured.ImageSHA256 != digest {
		t.Fatalf("the old Keep accepted rewritten bytes: %+v %+v", kept, captured)
	}
	current, _ := configure()
	if current == digest {
		t.Fatal("a repeated configuration did not observe rewritten bytes")
	}
	newQuestion := programKeepQuestion(t, run, row.Key)
	if newQuestion.ID == question.ID {
		t.Fatal("verification of the current image reused the old Keep")
	}
	if renewed, captured, _, ok := run.d.programSelfEntry(row.Key); !ok ||
		renewed.Revision == row.Revision || captured.ImageSHA256 != current || captured.FileKey != source.FileKey {
		t.Fatalf("the repeated configuration did not retain its new image proof: %+v %+v", renewed, captured)
	}
	answer(t, run.f, newQuestion.ID, "keep")
	waitDirectory(t, run.d, func() bool {
		kept, ok := programRegistrationNow(run.d, row.Key)
		return ok && kept.State == registration.StateEnabled
	})
	kept, _ := programRegistrationNow(run.d, row.Key)
	if same, output := configure(); same != current || !strings.Contains(output, "already says this") {
		t.Fatalf("a recheck of the kept current image requested another Keep: %q", output)
	}
	if after, ok := programRegistrationNow(run.d, row.Key); !ok || after.State != registration.StateEnabled || after.Revision != kept.Revision {
		t.Fatalf("an unchanged kept configuration changed its registry state: %+v", after)
	}
}

func TestModelHostOperatorUnsignedNativePersonKeep(t *testing.T) {
	requireNativeModelHostRegistration(t)
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current native transport")
	}
	// A real unsigned PE in temporary source-fixture state; no installed image
	// or fabricated publisher/signature evidence participates in admission.
	program := recordingModelHost(t, t.TempDir())
	record := filepath.Join(t.TempDir(), "executed")
	t.Setenv("STANDIN_ARGS", record)
	run := newSelfRegisteredRun(t)
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	r, caller := registryObserverFixture(t)
	r.operator.credentials.runtimeRights.applications = run.d
	// The runtime's config owners, as production wires them: the model host
	// facet names model_context, and Keep grants that read.
	rights := r.operator.credentials.runtimeRights
	if err := rights.policy.RegisterAction(host.ConfigKeyReadAction); err != nil {
		t.Fatal(err)
	}
	run.d.programConfigKeyKnown = func(key string) bool {
		return slices.ContainsFunc(configwire.ConfigKeyDescriptors(), func(v configwire.KeyDescriptor) bool { return v.Key == key })
	}
	run.d.programConfigKeep = rights.keepProviderConfig
	run.d.programConfigRemove = rights.removeProviderConfig
	run.d.programConfigReconcile = rights.retireProviderConfig
	r.providers.registrationDirectory = func() *applicationDirectory { return run.d }
	r.providers.installation = ""
	endpoint := shortMediationEndpoint(t)
	served, err := listenRegistry(endpoint, r, caller.Account, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- served.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = served.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	native := modelHostOperatorNative{listen.FrameClient{Endpoint: endpoint, Timeout: 3 * time.Second, MaxFrame: 1 << 20, Server: &listen.ServerExpectation{Principal: principal, Program: self}}}
	legacy := &modelHostOperatorFixture{}
	writer, err := newModelHostRegistryWriter(t.Context(), legacy, native)
	if err != nil {
		t.Fatal(err)
	}
	settings := hostSettings{program: program, endpoint: "configured-native", engine: filepath.Join(t.TempDir(), "explicit-configured-engine.exe")}
	var output bytes.Buffer
	w := newWaiting(10 * time.Second)
	defer w.stop()
	if err := declareHost(w, writer, wire.DeclarationList{}, settings, []hostedModel{{Name: "m", Store: "store", Object: "object"}}, "models host", &output, false, "configured"); err != nil {
		t.Fatal(err)
	}
	page, err := native.Registrations(t.Context(), "", 64)
	if err != nil || page.Outcome != registration.OutcomePage || len(page.Registrations) != 1 {
		t.Fatalf("native pending readback: %+v %v", page, err)
	}
	pending := page.Registrations[0]
	if pending.Source != registration.SourceOperator || pending.State != registration.StatePendingApproval || pending.Verification != registration.VerificationUnverified || pending.Publisher != nil || len(pending.Checks) != 0 || pending.Spec.Providers[0].Engines[0].Version != "" {
		t.Fatalf("unsigned configured claims: %+v", pending)
	}
	question := programKeepQuestion(t, run, pending.Key)
	if question.PublisherTrusted || question.Publisher != "" {
		t.Fatalf("unsigned question invented publisher: %+v", question)
	}
	answer(t, run.f, question.ID, "keep")
	waitDirectory(t, run.d, func() bool {
		row, ok := programRegistrationNow(run.d, pending.Key)
		return ok && row.State == registration.StateEnabled
	})
	page, err = native.Registrations(t.Context(), "", 64)
	if err != nil || len(page.Registrations) != 1 {
		t.Fatal(page, err)
	}
	kept := page.Registrations[0]
	if kept.State != registration.StateEnabled || kept.Verification != registration.VerificationUnverified || kept.Publisher != nil || len(kept.Checks) != 0 || kept.Spec.Providers[0].Engines[0].Version != "" {
		t.Fatalf("Keep invented verification: %+v", kept)
	}
	if !slices.Equal(kept.Spec.Providers[0].ConfigKeys, []string{"model_context"}) {
		t.Fatalf("kept model host config keys %v", kept.Spec.Providers[0].ConfigKeys)
	}
	if rule := rights.policy.ReadRule(rwire.Subject{Account: rights.owner, Program: kept.Spec.Program}, host.ConfigKeyReadAction, "model_context"); rule.Outcome != rwire.RuleReadOutcomeFound {
		t.Fatalf("Keep wrote no model_context read rule: %+v", rule)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("registration/Keep launched the fixture: %v", err)
	}
}
