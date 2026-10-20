package setup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
	localsource "github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// The picture store's catalogue names every file of every PyTorch image set
// Setup offers, at the path it stages it at, by the provider's own digest,
// so the router's installed check finds each role digest it asks for, and
// names each set as a model made of exactly those files.
func TestPictureStorePinsEveryFileOfEveryOfferedImageSet(t *testing.T) {
	files, models := pictureStoreCatalogue()
	pins := map[string]localsource.PinnedFile{}
	for _, pin := range files {
		pins[pin.Path] = pin
	}
	sets := map[string][]string{}
	for _, m := range models {
		sets[m.Name] = m.Files
	}
	offered := 0
	for _, offer := range PictureModelOffers(PyTorch) {
		set, ok := torch.TorchModelSetNamed(offer.Plan.Model)
		if !ok || set.Profile != "image" {
			t.Fatalf("offer %s names no PyTorch image set", offer.Plan.Model)
		}
		offered++
		var want []string
		for _, f := range set.Files {
			p := torch.Flux2Klein4BTorch.Name + "/" + f.Locators[len(f.Locators)-1]
			if pin, ok := pins[p]; !ok || pin.SHA256 != f.SHA256 || pin.Size != f.Size || pin.Role != f.Role {
				t.Fatalf("%s's %s is pinned as %+v", set.Name, f.Role, pins[p])
			}
			want = append(want, p)
		}
		if !slices.Equal(sets[set.Name], want) {
			t.Fatalf("model %s is made of %v", set.Name, sets[set.Name])
		}
	}
	if offered != 3 {
		t.Fatalf("%d PyTorch image sets offered", offered)
	}
}

// Setup registers OA's picture store once: the inventoryd configuration
// names the folder alone with the catalogue, and the declaration reads only
// that configuration for store oa-picture. A registration the person
// removed stays removed; one an earlier version declared differently is
// replaced while the registry lists it.
func TestPictureStoreRegistersOnceAndKeepsARemovalRemoved(t *testing.T) {
	ctx := context.Background()
	j, _, _, _, _ := pyTorchFixture(t)
	home, err := CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	registry := &theNoiseRegistryFake{readiness: fwire.DeclarationReadinessIdle, withdrawOutcome: fwire.DeclarationEditOutcomeApplied,
		declareOutcome: fwire.DeclarationEditOutcomeApplied, events: &events}
	old := setupEngineRegistry
	setupEngineRegistry = func(context.Context) (engineRegistry, error) { return registry, nil }
	t.Cleanup(func() { setupEngineRegistry = old })
	program := filepath.Join(t.TempDir(), "inventoryd.exe")

	// No staged picture model: no store.
	if err := registerPictureStoreAt(ctx, j, home, program); err != nil || registry.declares != 0 {
		t.Fatalf("registered without a picture folder: %v %d", err, registry.declares)
	}
	if err := os.MkdirAll(pyTorchModelStage(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registerPictureStoreAt(ctx, j, home, program); err != nil || registry.declares != 1 {
		t.Fatalf("register: %v %d", err, registry.declares)
	}
	got := registry.declaration.Declaration
	config := pictureStoreConfig(home)
	if got.Name != "oa-setup-picture" || got.Program != program || !slices.Equal(got.Resources, []string{"store:oa-picture"}) ||
		!slices.Equal(got.Contracts, []string{"abstraction.storage/inventory-source@1"}) ||
		!slices.Equal(got.Arguments, []string{"serve", "--endpoint", "{endpoint}", "--config", config, "--config-roots-only"}) {
		t.Fatalf("declared %+v", got)
	}
	loaded, err := localsource.LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	files, models := pictureStoreCatalogue()
	if len(loaded.ExtraRoots) != 1 || loaded.ExtraRoots[0].Store != "oa-picture" || loaded.ExtraRoots[0].ExpectedFileKey == "" ||
		!slices.Equal(loaded.ExtraRoots[0].Files, files) || len(loaded.ExtraRoots[0].Models) != len(models) {
		t.Fatalf("config %+v", loaded)
	}
	if state, err := j.Load(ctx); err != nil || state.PictureStore == nil || !state.PictureStore.Confirmed {
		t.Fatalf("journal %+v %v", state.PictureStore, err)
	}
	// Current: nothing changes.
	if err := registerPictureStoreAt(ctx, j, home, program); err != nil || registry.declares != 1 || len(events) != 0 {
		t.Fatalf("a current store was declared again: %v %d %v", err, registry.declares, events)
	}
	// An earlier version's declaration, still listed, is replaced.
	moved := filepath.Join(t.TempDir(), "inventoryd.exe")
	if err := registerPictureStoreAt(ctx, j, home, moved); err != nil || registry.declares != 2 ||
		!slices.Equal(events, []string{"withdraw/halt"}) || registry.declaration.Declaration.Program != moved {
		t.Fatalf("replace: %v %d %v", err, registry.declares, events)
	}
	// The person removes it: it stays removed.
	registry.declaration = nil
	if err := registerPictureStoreAt(ctx, j, home, moved); err != nil || registry.declares != 2 || registry.declaration != nil {
		t.Fatalf("a removed store was declared again: %v %d", err, registry.declares)
	}
	// The person approves Setup's plan again: the removal is kept.
	state, err := j.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.SavePlan(ctx, state.Plan); err != nil {
		t.Fatal(err)
	}
	if err := registerPictureStoreAt(ctx, j, home, moved); err != nil || registry.declares != 2 || registry.declaration != nil {
		t.Fatalf("a removed store was declared again after a plan save: %v %d", err, registry.declares)
	}
}

// Without a Setup-installed PyTorch engine the reconcile does nothing.
func TestReconcilePictureEngineWithoutPyTorchDoesNothing(t *testing.T) {
	j, _, _, _, _ := pyTorchFixture(t)
	old := setupEngineRegistry
	setupEngineRegistry = func(context.Context) (engineRegistry, error) {
		t.Fatal("the registry was read")
		return nil, nil
	}
	t.Cleanup(func() { setupEngineRegistry = old })
	if err := ReconcilePictureEngine(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}
