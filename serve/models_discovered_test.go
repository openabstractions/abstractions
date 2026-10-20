package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// heldGGUF is one model manifest a store holds: a GGUF with a descriptor.
func heldGGUF(store, id, digest string, descriptor map[string]string) content.ManifestHolders {
	d := map[string]string{"format": "gguf", "architecture": "llama"}
	for k, v := range descriptor {
		d[k] = v
	}
	evidence := content.EvidenceNone
	if digest != "" {
		evidence = content.EvidenceNamed
	}
	return content.ManifestHolders{Manifest: content.Manifest{ID: id, Kind: modelDescriptor, Store: store, Descriptor: d,
		Entries: []content.Entry{{Role: "weights", Digest: digest, Size: 1 << 20, Evidence: evidence}}}}
}

func readStores(names ...string) []content.Store {
	var out []content.Store
	for _, name := range names {
		out = append(out, content.Store{Name: name, Present: true, Placement: content.PlacementLocal})
	}
	return out
}

func names(list []hostedModel) []string {
	var out []string
	for _, m := range list {
		out = append(out, m.argument())
	}
	return out
}

// A GGUF a person added in another app's store is one entry named by its
// family; a projector, an embedding model and a safetensors file are not
// chat models and stay out; a model two stores hold as the same file is one
// entry, and two quantisations of one family are one entry from the store
// ranked first.
func TestReconcileHostsEveryChatGGUFOncePerFamilyAndDigest(t *testing.T) {
	composed := storageservice.ComposedInventory{Stores: readStores("huggingface", "lmstudio", "ollama"), Manifests: []content.ManifestHolders{
		heldGGUF("huggingface", "sha256:aa", "sha256:aa", map[string]string{"family": "qwen2.5-0.5b"}),
		heldGGUF("lmstudio", "lms-qwen", "sha256:aa", map[string]string{"family": "qwen2.5-0.5b"}),
		heldGGUF("lmstudio", "lms-smol-q8", "sha256:bb", map[string]string{"family": "smollm2-135m"}),
		heldGGUF("huggingface", "sha256:cc", "sha256:cc", map[string]string{"family": "smollm2-135m"}),
		heldGGUF("huggingface", "sha256:dd", "sha256:dd", map[string]string{"family": "qwen3-vl-4b", "role": "projector"}),
		heldGGUF("huggingface", "sha256:ee", "sha256:ee", map[string]string{"family": "nomic-embed-text-v1.5", "architecture": "nomic-bert"}),
		heldGGUF("ollama", "library/tiny:1b", "", map[string]string{"family": "tiny-1b"}),
		{Manifest: content.Manifest{ID: "sha256:ff", Kind: modelDescriptor, Store: "huggingface",
			Descriptor: map[string]string{"family": "flux1-dev", "format": "safetensors"}}},
	}}
	complete, found := discoveredModels(composed)
	got := names(reconcileHosted(nil, complete, found, nil, listedObjects(composed)))
	want := []string{"qwen2.5-0.5b=huggingface/sha256:aa", "smollm2-135m=huggingface/sha256:cc", "tiny-1b=ollama/library/tiny:1b"}
	if !slices.Equal(got, want) {
		t.Fatalf("hosted %v, want %v", got, want)
	}
}

// An entry already declared keeps its name and place, Setup's verified copy
// included, and no other copy of its family is added. An entry whose file
// left a store the inventory read completely is dropped; one in a store the
// inventory could not read stays.
func TestReconcileKeepsDeclaredEntriesAndDropsWhatLeft(t *testing.T) {
	current := []hostedModel{
		{Name: "qwen3.6-35b-a3b", Store: "oa-chat-1", Object: "setup-copy"},
		{Name: "gone-model", Store: "huggingface", Object: "sha256:99"},
		{Name: "away-model", Store: "jan", Object: "jan-1"},
	}
	stores := append(readStores("huggingface", "oa-chat-1"), content.Store{Name: "jan", Present: true, Placement: content.PlacementLocal,
		Errors: []content.StoreError{{Detail: "busy"}}})
	composed := storageservice.ComposedInventory{Stores: stores, Manifests: []content.ManifestHolders{
		heldGGUF("oa-chat-1", "setup-copy", "sha256:11", map[string]string{"family": "qwen3.6-35b-a3b"}),
		heldGGUF("huggingface", "sha256:12", "sha256:12", map[string]string{"family": "qwen3.6-35b-a3b"}),
		heldGGUF("huggingface", "sha256:11", "sha256:11", map[string]string{"family": "qwen3.6-35b-a3b-copy"}),
	}}
	complete, found := discoveredModels(composed)
	got := names(reconcileHosted(current, complete, found, nil, listedObjects(composed)))
	want := []string{"away-model=jan/jan-1", "qwen3.6-35b-a3b=oa-chat-1/setup-copy"}
	if !slices.Equal(got, want) {
		t.Fatalf("hosted %v, want %v", got, want)
	}
}

// A model the person removed from OA stays out, by its family and by its
// object, while another model is still added.
func TestReconcileLeavesARemovedModelOut(t *testing.T) {
	composed := storageservice.ComposedInventory{Stores: readStores("huggingface", "lmstudio"), Manifests: []content.ManifestHolders{
		heldGGUF("huggingface", "sha256:aa", "sha256:aa", map[string]string{"family": "qwen2.5-0.5b"}),
		heldGGUF("lmstudio", "lms-qwen-q8", "sha256:a8", map[string]string{"family": "qwen2.5-0.5b"}),
		heldGGUF("huggingface", "sha256:bb", "sha256:bb", map[string]string{"family": "smollm2-135m"}),
	}}
	removed := []modelHostRemoval{{Name: "qwen2.5-0.5b", Store: "huggingface", Object: "sha256:aa"}}
	complete, found := discoveredModels(composed)
	got := names(reconcileHosted(nil, complete, found, removed, listedObjects(composed)))
	if want := []string{"smollm2-135m=huggingface/sha256:bb"}; !slices.Equal(got, want) {
		t.Fatalf("hosted %v, want %v", got, want)
	}
}

// The registry's record: an edit that removes an entry records it, and an
// edit that hosts it again clears it, by name or by object.
func TestModelHostRemovalsFollowTheEdits(t *testing.T) {
	p := &runtimeProviders{removedPath: filepath.Join(t.TempDir(), modelHostRemovalsFile)}
	ctx := context.Background()
	qwen := hostedModel{Name: "qwen2.5-0.5b", Store: "huggingface", Object: "sha256:aa"}
	smol := hostedModel{Name: "smollm2-135m", Store: "huggingface", Object: "sha256:bb"}
	if err := p.recordModelHostEdit(ctx, []hostedModel{qwen, smol}, []hostedModel{smol}); err != nil {
		t.Fatal(err)
	}
	removed, _, err := p.modelHostRemovals()
	if err != nil || !slices.Equal(removed, []modelHostRemoval{{Name: qwen.Name, Store: qwen.Store, Object: qwen.Object}}) {
		t.Fatalf("removals %v (%v), want qwen2.5-0.5b", removed, err)
	}
	// Setup hosts the same family from its own copy: the removal clears.
	setup := hostedModel{Name: "qwen2.5-0.5b", Store: "oa-chat-1", Object: "setup-copy"}
	if err := p.recordModelHostEdit(ctx, []hostedModel{smol}, []hostedModel{smol, setup}); err != nil {
		t.Fatal(err)
	}
	if removed, _, err := p.modelHostRemovals(); err != nil || len(removed) != 0 {
		t.Fatalf("removals %v (%v) after hosting the family again, want none", removed, err)
	}
	if _, err := os.Stat(p.removedPath); !os.IsNotExist(err) {
		t.Fatalf("an empty record left its file: %v", err)
	}
}

// The runtime's reconciler writes the model host's declaration from the
// inventory, waits while the model host holds its card, and writes nothing
// when the inventory asks for what is declared.
func TestReconcileModelHostWritesTheDeclaration(t *testing.T) {
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
	programs, err := modelHostRuntimePrograms()
	if err != nil {
		t.Fatal(err)
	}
	settings := hostSettings{program: program, endpoint: "modelhost-discovered-test", engine: filepath.Join(state, "engine", "llama-server.exe")}
	declared := []hostedModel{{Name: "qwen3.6-35b-a3b", Store: "oa-chat-1", Object: "setup-copy"}}
	f := providerFile{Version: providerFileVersion, DeclaredBy: program, DeclaredAt: 1,
		Declaration: declarationOf(modelHostDeclaration(settings, declared, programs))}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	if added := p.add(revision, f); added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("add: %+v", added)
	}
	composed := storageservice.ComposedInventory{Stores: readStores("huggingface", "oa-chat-1"), Manifests: []content.ManifestHolders{
		heldGGUF("oa-chat-1", "setup-copy", "sha256:11", map[string]string{"family": "qwen3.6-35b-a3b"}),
		heldGGUF("huggingface", "sha256:aa", "sha256:aa", map[string]string{"family": "qwen2.5-0.5b"}),
	}}
	hosted := func() []string {
		files, _, err := p.read()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if f.Declaration.Name == modelHostName {
				return names(hostedIn(f.Declaration))
			}
		}
		return nil
	}
	holding := func(string, string) bool { return true }
	if wrote, err := p.reconcileModelHost(context.Background(), composed, holding); err != nil || wrote != "" {
		t.Fatalf("a model host holding its card was rewritten: %q %v", wrote, err)
	}
	idle := func(string, string) bool { return false }
	if wrote, err := p.reconcileModelHost(context.Background(), composed, idle); err != nil || wrote == "" {
		t.Fatalf("reconcile: %q %v", wrote, err)
	}
	if got, want := hosted(), []string{"qwen2.5-0.5b=huggingface/sha256:aa", "qwen3.6-35b-a3b=oa-chat-1/setup-copy"}; !slices.Equal(got, want) {
		t.Fatalf("declared %v, want %v", got, want)
	}
	if wrote, err := p.reconcileModelHost(context.Background(), composed, idle); err != nil || wrote != "" {
		t.Fatalf("an unchanged inventory rewrote the declaration: %q %v", wrote, err)
	}
}

// DM-1: removing the person's last hosted model withdraws the model host,
// and that removal holds. A model host declared again by hosting another
// model leaves the removed one out, though the inventory still holds it. A
// change of engine or arguments replaces the declaration and records no
// removal.
func TestTheLastRemovedModelStaysOutWhenTheModelHostIsDeclaredAgain(t *testing.T) {
	registry, caller := registryObserverFixture(t)
	p := registry.providers
	ctx := context.Background()
	// The model host is another program than the caller that declares it.
	program := filepath.Join(t.TempDir(), "modelhostd.exe")
	programs, err := modelHostRuntimePrograms()
	if err != nil {
		t.Fatal(err)
	}
	settings := hostSettings{program: program, endpoint: "modelhost-withdraw-test", engine: filepath.Join(t.TempDir(), "llama-server.exe")}
	qwen := hostedModel{Name: "qwen2.5-0.5b", Store: "huggingface", Object: "sha256:aa"}
	smol := hostedModel{Name: "smollm2-135m", Store: "huggingface", Object: "sha256:bb"}
	revision := func() string {
		_, revision, err := p.read()
		if err != nil {
			t.Fatal(err)
		}
		return revision
	}
	declare := func(replace bool, s hostSettings, models ...hostedModel) {
		t.Helper()
		pinned := wire.PinnedDeclaration{Declaration: modelHostDeclaration(s, models, programs)}
		var change wire.DeclarationChange
		if replace {
			change = registry.ReplacePinned(ctx, caller, revision(), pinned)
		} else {
			change = registry.DeclarePinned(ctx, caller, revision(), pinned)
		}
		if change.Outcome != wire.DeclarationEditOutcomeApplied {
			t.Fatalf("declare %v: %+v", names(models), change)
		}
	}
	removals := func() []string {
		t.Helper()
		removed, _, err := p.modelHostRemovals()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range removed {
			out = append(out, r.Name)
		}
		return out
	}

	// An engine or argument change replaces the declaration: no removal.
	declare(false, settings, qwen)
	moved := settings
	moved.engine = filepath.Join(t.TempDir(), "llama-server.exe")
	moved.extra = []string{"--ctx-size", "8192"}
	declare(true, moved, qwen)
	if got := removals(); len(got) != 0 {
		t.Fatalf("an engine and argument change recorded removals %v", got)
	}

	// The person removes the last model: the model host is withdrawn.
	if change := registry.Withdraw(ctx, caller, revision(), modelHostName); change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("withdraw: %+v", change)
	}
	if got := removals(); !slices.Equal(got, []string{qwen.Name}) {
		t.Fatalf("removals %v after the last model was removed, want qwen2.5-0.5b", got)
	}

	// Hosting another model declares the model host again; the reconciler
	// finds both in the inventory and leaves the removed one out.
	declare(false, moved, smol)
	if got := removals(); !slices.Equal(got, []string{qwen.Name}) {
		t.Fatalf("removals %v after hosting another model, want qwen2.5-0.5b still", got)
	}
	composed := storageservice.ComposedInventory{Stores: readStores("huggingface"), Manifests: []content.ManifestHolders{
		heldGGUF("huggingface", qwen.Object, qwen.Object, map[string]string{"family": qwen.Name}),
		heldGGUF("huggingface", smol.Object, smol.Object, map[string]string{"family": smol.Name}),
	}}
	if wrote, err := p.reconcileModelHost(ctx, composed, func(string, string) bool { return false }); err != nil || wrote != "" {
		t.Fatalf("the reconciler wrote %q (%v); the removed model must stay out", wrote, err)
	}
	files, _, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Declaration.Name == modelHostName {
			if got := names(hostedIn(f.Declaration)); !slices.Equal(got, []string{smol.argument()}) {
				t.Fatalf("declared %v, want smollm2-135m alone", got)
			}
		}
	}
}
