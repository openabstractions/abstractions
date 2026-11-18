//go:build windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// keptModelHost is Setup's model host declared through `models host` and
// kept by the person, on the runtime's registry and config owners.
type keptModelHost struct {
	run      *selfRegisteredRun
	registry *runtimeRegistry
	caller   inference.Subject
	native   modelHostOperatorNative
	key      string
	program  string
	settings hostSettings
	models   []hostedModel
}

func newKeptModelHost(t *testing.T, adjust ...func(*hostSettings)) *keptModelHost {
	t.Helper()
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current native transport")
	}
	program := recordingModelHost(t, t.TempDir())
	t.Setenv("STANDIN_ARGS", filepath.Join(t.TempDir(), "executed"))
	run := newSelfRegisteredRun(t)
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	r, caller := registryObserverFixture(t)
	r.operator.credentials.runtimeRights.applications = run.d
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
	native := modelHostOperatorNative{listen.FrameClient{Endpoint: endpoint, Timeout: openTestWait(t), MaxFrame: 1 << 20, Server: &listen.ServerExpectation{Principal: principal, Program: self}}}
	k := &keptModelHost{run: run, registry: r, caller: caller, native: native, program: program,
		settings: hostSettings{program: program, endpoint: "configured-native", engine: filepath.Join(t.TempDir(), "llama-server.exe")},
		models:   []hostedModel{{Name: "m", Store: "store", Object: "object"}}}
	for _, a := range adjust {
		a(&k.settings)
	}
	k.declare(t, k.settings)
	page, err := native.Registrations(t.Context(), "", 64)
	if err != nil || len(page.Registrations) != 1 {
		t.Fatalf("pending readback: %+v %v", page, err)
	}
	k.key = page.Registrations[0].Key
	k.keep(t)
	return k
}

// declare writes the model host through Setup's `models host` path.
func (k *keptModelHost) declare(t *testing.T, settings hostSettings) {
	t.Helper()
	writer, err := newModelHostRegistryWriter(t.Context(), &modelHostOperatorFixture{}, k.native)
	if err != nil {
		t.Fatal(err)
	}
	w := newWaiting(openTestWait(t))
	defer w.stop()
	var out bytes.Buffer
	if err := declareHost(w, writer, wire.DeclarationList{}, settings, k.models, "models host", &out, false, "configured"); err != nil {
		t.Fatal(err)
	}
}

// keep answers the pending Keep question.
func (k *keptModelHost) keep(t *testing.T) {
	t.Helper()
	question := programKeepQuestion(t, k.run, k.key)
	answer(t, k.run.f, question.ID, "keep")
	waitDirectory(t, k.run.d, func() bool {
		row, ok := programRegistrationNow(k.run.d, k.key)
		return ok && row.State == registration.StateEnabled && !k.pending()
	})
	// The Keep becomes visible before its follower releases the held image.
	waitUnfollowed(t, k.run.k, question.ID)
	if !k.designated(t) {
		t.Fatal("the kept model host is not designated for model_context")
	}
}

func (k *keptModelHost) pending() bool {
	ctx := context.Background()
	if !k.run.d.lock(ctx) {
		return true
	}
	defer k.run.d.unlock()
	return k.run.d.v3.programApprovals[k.key].Pending
}

// designated reports whether the runtime designates the model host's image
// to read model_context (CFG-X1), the proof each read rechecks.
func (k *keptModelHost) designated(t *testing.T) bool {
	t.Helper()
	rows, err := k.run.d.providerConfigCandidates(t.Context(), k.program, "model_context")
	return err == nil && len(rows) == 1
}

func (k *keptModelHost) facet(t *testing.T) (registration.Registration, registration.ProviderFacet) {
	t.Helper()
	row, ok := programRegistrationNow(k.run.d, k.key)
	if !ok || len(row.Spec.Providers) != 1 {
		t.Fatalf("model host registration: %+v %v", row, ok)
	}
	return row, cloneProviderFacet(row.Spec.Providers[0])
}

// reconcile runs the runtime's allowlist reconciler after Hugging Face
// gains qwen2.5-0.5b.
func (k *keptModelHost) reconcile(t *testing.T) string {
	t.Helper()
	composed := storageservice.ComposedInventory{Stores: readStores("huggingface"), Manifests: []content.ManifestHolders{
		heldGGUF("huggingface", "sha256:aa", "sha256:aa", map[string]string{"family": "qwen2.5-0.5b"}),
	}}
	wrote, err := k.registry.providers.reconcileModelHost(t.Context(), composed, func(string, string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	return wrote
}

// REG-CFG2: the runtime's reconcile after a store gains a model changes only
// the allowlist, keeps config_keys, engines and recipes, and carries the
// person's Keep, so the model host stays designated with no new question.
func TestModelHostReconcileKeepsTheKeepAndTheFacet(t *testing.T) {
	k := newKeptModelHost(t)
	before, kept := k.facet(t)
	if !slices.Equal(kept.ConfigKeys, []string{"model_context"}) || len(kept.Engines) != 1 || len(kept.Recipes) != 1 {
		t.Fatalf("kept facet %+v", kept)
	}
	if wrote := k.reconcile(t); wrote == "" {
		t.Fatal("the reconciler wrote nothing")
	}
	after, facet := k.facet(t)
	if after.Epoch == before.Epoch {
		t.Fatal("the reconcile minted no epoch")
	}
	if !slices.Equal(facet.ConfigKeys, kept.ConfigKeys) || !slices.Equal(facet.Engines, kept.Engines) || facet.Activation != kept.Activation ||
		!slices.Equal(facet.Services, kept.Services) || !slices.Equal(facet.Resources, kept.Resources) {
		t.Fatalf("reconcile changed more than the allowlist:\nkept %+v\nnow  %+v", kept, facet)
	}
	if !slices.ContainsFunc(facet.Models, func(m registration.ScopedModel) bool { return m.Model == "qwen2.5-0.5b" }) {
		t.Fatalf("the new model is not in the allowlist: %+v", facet.Models)
	}
	if !slices.ContainsFunc(facet.Recipes, func(r registration.ModelRecipe) bool {
		return slices.EqualFunc([]registration.ModelRecipe{r}, kept.Recipes, recipeEqual)
	}) {
		t.Fatalf("the kept recipe did not survive: %+v", facet.Recipes)
	}
	if !slices.ContainsFunc(facet.Recipes, func(r registration.ModelRecipe) bool { return r.Model == "qwen2.5-0.5b" }) {
		t.Fatalf("the new model has no recipe: %+v", facet.Recipes)
	}
	if k.pending() || !k.designated(t) {
		t.Fatal("the reconcile left the Keep behind")
	}
}

func recipeEqual(a, b registration.ModelRecipe) bool {
	x, err := registrationEncoded(registration.EncodeModelRecipe, &a)
	y, err2 := registrationEncoded(registration.EncodeModelRecipe, &b)
	return err == nil && err2 == nil && bytes.Equal(x, y)
}

// A reconcile of a model host whose Keep is missing carries nothing.
func TestModelHostReconcileWithoutAKeepCarriesNothing(t *testing.T) {
	k := newKeptModelHost(t)
	cleared := k.run.d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		approval := s.programApprovals[k.key]
		approval.KeptGeneration = ""
		s.programApprovals[k.key] = approval
		record, _ := s.programRecord(k.key)
		return registration.Change{Outcome: registration.OutcomeApplied, Revision: record.Revision}, nil
	})
	if cleared.Outcome != registration.OutcomeApplied || k.designated(t) {
		t.Fatalf("Keep not cleared: %+v", cleared)
	}
	if wrote := k.reconcile(t); wrote == "" {
		t.Fatal("the reconciler wrote nothing")
	}
	if k.designated(t) {
		t.Fatal("a reconcile without a Keep designated the model host")
	}
}

// A pending Keep is no Keep: the reconciler leaves a pending model host alone.
func TestModelHostReconcileWhileTheKeepIsPendingCarriesNothing(t *testing.T) {
	k := newKeptModelHost(t)
	changed := k.settings
	changed.engine = filepath.Join(t.TempDir(), "other-llama-server.exe")
	k.declare(t, changed)
	if !k.pending() || k.designated(t) {
		t.Fatal("a changed engine kept its Keep")
	}
	if wrote := k.reconcile(t); wrote != "" {
		t.Fatalf("the reconciler rewrote a pending model host: %q", wrote)
	}
	if !k.pending() || k.designated(t) {
		t.Fatal("a pending model host became designated")
	}
}

// A runtime edit that widens the declaration leaves the Keep behind, and the
// read is refused until the person keeps it again.
func TestModelHostWideningEditLeavesTheKeepBehind(t *testing.T) {
	for name, widen := range map[string]func(*registration.ProviderFacet){
		"activation": func(f *registration.ProviderFacet) { f.Activation = registration.ActivationAttach },
		"config key": func(f *registration.ProviderFacet) { f.ConfigKeys = append(f.ConfigKeys, "prompt_cache_persist") },
		"engine path": func(f *registration.ProviderFacet) {
			f.Arguments = append(f.Arguments, argEngineArg, "--ctx-size=8192")
		},
	} {
		t.Run(name, func(t *testing.T) {
			k := newKeptModelHost(t)
			_, facet := k.facet(t)
			widen(&facet)
			k.replaceFacet(t, facet, true)
			if k.carried(t) || k.designated(t) {
				t.Fatal("a widening edit carried the Keep")
			}
		})
	}
}

// replaceFacet writes facet as the model host's declaration, as the runtime's
// own reconcile when runtime is set and as a caller's declaration otherwise.
func (k *keptModelHost) replaceFacet(t *testing.T, facet registration.ProviderFacet, runtime bool) {
	t.Helper()
	file := providerFacetCompatibility(k.program, facet)
	file.runtimeAllowlist = runtime
	_, revision, err := k.registry.providers.read()
	if err != nil {
		t.Fatal(err)
	}
	if change := k.registry.providers.replace(revision, file); change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("replace: %+v", change)
	}
}

// carried reports whether the person's Keep names the registration's current
// epoch.
func (k *keptModelHost) carried(t *testing.T) bool {
	t.Helper()
	if !k.run.d.lock(t.Context()) {
		t.Fatal("registry lock")
	}
	defer k.run.d.unlock()
	record, ok := k.run.d.v3.programRecord(k.key)
	kept := k.run.d.v3.programApprovals[k.key].KeptGeneration
	return ok && kept != "" && kept == record.Epoch
}

// withAnotherModel is facet with one more hosted model and nothing else.
func withAnotherModel(facet registration.ProviderFacet) registration.ProviderFacet {
	facet = cloneProviderFacet(facet)
	extra := hostedModel{Name: "extra", Store: "store", Object: "extra-object"}
	rest, _, at := splitServeAllowlist(facet.Arguments)
	_, entries, _ := splitServeAllowlist(facet.Arguments)
	entries = append(entries, argModel, extra.argument())
	facet.Arguments = slices.Concat(rest[:at], entries, rest[at:])
	facet.Models = append(facet.Models, registration.ScopedModel{Modality: "chat", Model: extra.Name})
	return facet
}

// A caller-supplied declaration never carries a Keep, even one that changes
// only the allowlist; the runtime's own edit of the same change does.
func TestModelHostCallerDeclarationCarriesNoKeep(t *testing.T) {
	k := newKeptModelHost(t)
	_, facet := k.facet(t)
	k.replaceFacet(t, withAnotherModel(facet), false)
	if k.carried(t) || k.designated(t) {
		t.Fatal("a caller-supplied allowlist change carried the Keep")
	}

	control := newKeptModelHost(t)
	_, facet = control.facet(t)
	control.replaceFacet(t, withAnotherModel(facet), true)
	if !control.carried(t) || !control.designated(t) {
		t.Fatal("the runtime's own allowlist change did not carry the Keep")
	}
}

// A reconcile after the model host's program file was replaced, so its image
// has another FileKey, carries nothing.
func TestModelHostReconcileAfterTheImageChangedCarriesNothing(t *testing.T) {
	k := newKeptModelHost(t)
	raw, err := os.ReadFile(k.program)
	if err != nil {
		t.Fatal(err)
	}
	replacement := k.program + ".new"
	if err := os.WriteFile(replacement, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(k.program); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, k.program); err != nil {
		t.Fatal(err)
	}
	if wrote := k.reconcile(t); wrote == "" {
		t.Fatal("the reconciler wrote nothing")
	}
	if k.carried(t) || k.designated(t) {
		t.Fatal("a reconcile over a changed image carried the Keep")
	}
}

// REG-CFG2: a store configuration the kept declaration does not name needs a
// new Keep, while a model landing in a store it already names carries it.
func TestModelHostNewStoreConfigurationNeedsAKeep(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "kept", "inventoryd.json")
	withConfig := func(s *hostSettings) { s.configs = []string{configured} }

	k := newKeptModelHost(t, withConfig)
	if wrote := k.reconcile(t); wrote == "" {
		t.Fatal("the reconciler wrote nothing")
	}
	_, facet := k.facet(t)
	if _, entries, _ := splitServeAllowlist(facet.Arguments); !slices.Equal(allowlistValues(entries, argConfig), []string{configured}) {
		t.Fatalf("store configurations %v, want the kept one", allowlistValues(entries, argConfig))
	}
	if !k.carried(t) || !k.designated(t) {
		t.Fatal("a model in an already configured store left the Keep behind")
	}

	added := newKeptModelHost(t, withConfig)
	_, facet = added.facet(t)
	rest, entries, at := splitServeAllowlist(facet.Arguments)
	entries = append(entries, argConfig, filepath.Join(t.TempDir(), "new-store", "inventoryd.json"))
	facet.Arguments = slices.Concat(rest[:at], entries, rest[at:])
	added.replaceFacet(t, facet, true)
	if added.carried(t) || added.designated(t) {
		t.Fatal("a new store configuration carried the Keep")
	}
}
