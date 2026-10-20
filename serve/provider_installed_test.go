package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/listen"
	router "github.com/openabstractions/abstraction-router/go"
)

// withInstalledStoresPinned runs the package's tests with the file-backed
// stores' root in a temporary folder, so no test reads the developer's own
// providers folder.
func withInstalledStoresPinned(run func() int) int {
	dir, err := os.MkdirTemp("", "oa-installed-stores-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	installedStoresRoot = func() (string, error) { return dir, nil }
	return run()
}

// installedFolder points the file-backed stores at a temporary folder.
func installedFolder(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := installedStoresRoot
	installedStoresRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { installedStoresRoot = old })
	return root
}

type installedFixtureFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type installedFixtureModel struct {
	Model string                 `json:"model"`
	Files []installedFixtureFile `json:"files"`
}

// writeInstalledFixture writes each model's files under folder and the
// installed.json naming them.
func writeInstalledFixture(t *testing.T, folder string, models map[string]map[string]string) {
	t.Helper()
	record := struct {
		Version int                     `json:"version"`
		Models  []installedFixtureModel `json:"models"`
	}{Version: 1, Models: []installedFixtureModel{}}
	names := make([]string, 0, len(models))
	for model := range models {
		names = append(names, model)
	}
	slices.Sort(names)
	for _, model := range names {
		entry := installedFixtureModel{Model: model}
		for rel, body := range models[model] {
			full := filepath.Join(folder, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(body))
			entry.Files = append(entry.Files, installedFixtureFile{Path: rel, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])})
		}
		record.Models = append(record.Models, entry)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, installedRecordName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func installedDigestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// nativeRecipeHost is the router host the runtime builds for facet
// (nativeInferenceHosts with its engine guide), starting nothing.
func nativeRecipeHost(t *testing.T, facet registration.ProviderFacet) *router.Host {
	t.Helper()
	d := providerFacetCompatibility(`C:\OA\tools\lemonaded.exe`, facet).Declaration
	host, err := router.NewNative(d.Name, "fixture", listen.ServerExpectation{}, d.Models, d.mediatedInferenceProfiles())
	if err != nil {
		t.Fatal(err)
	}
	projectProviderEngineGuide(host, &facet, "provider-binding-v1:fixture")
	return host
}

func offeredNames(t *testing.T, routes *router.Router, profile string) []string {
	t.Helper()
	routes.Survey()
	offers, _, err := routes.Offer(router.Request{Profile: profile, Placement: router.PlacementLocal})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, offer := range offers {
		out = append(out, offer.Names...)
	}
	slices.Sort(out)
	return out
}

// The shipped Lemonade declaration names every model by hash and its
// installed.json store; with no installed.json a fresh install offers no
// lemonade-image model, and the host is listed unavailable.
func TestShippedLemonadeDeclarationOffersNothingUntilInstalled(t *testing.T) {
	raw, err := os.ReadFile(privateInput(t, "..", "installer", "declarations", "lemonade-image.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := registration.DecodeRegistrationSpec(raw)
	if err != nil || len(spec.Providers) != 1 {
		t.Fatalf("declaration: %v", err)
	}
	facet := spec.Providers[0]
	if !slices.Contains(facet.Resources, "store:lemonade") || slices.Contains(facet.Services, storageInventorySource) {
		t.Fatalf("the declaration names no file-backed store: %v %v", facet.Resources, facet.Services)
	}
	for _, model := range facet.Models {
		if !slices.ContainsFunc(facet.Recipes, func(r registration.ModelRecipe) bool { return r.Model == model.Model && len(r.Objects) > 0 }) {
			t.Fatalf("%s has no recipe naming its files", model.Model)
		}
	}
	installedFolder(t)
	host := nativeRecipeHost(t, facet)
	routes := router.New(host)
	var stores installedStores
	routes.SetHeld(stores.held(context.Background(), []string{"lemonade"}, func(err error) { t.Fatal(err) }))
	for _, profile := range []string{router.ProfileImage, router.ProfileTranscription, router.ProfileSpeech} {
		if got := offeredNames(t, routes, profile); len(got) != 0 {
			t.Fatalf("%s offered %v with nothing installed", profile, got)
		}
	}
	states, _, _, _, _ := routes.Residency(false)
	if len(states) != 1 || states[0].Up || states[0].Servable || states[0].Why != "engine:not_installed:lemonade-image" {
		t.Fatalf("host state = %+v", states)
	}
}

// After Setup installs one model only that model is offered; a changed file
// withdraws it, and the runtime hashes a file again only when its size or
// write time changed.
func TestInstalledStoreOffersOnlyTheVerifiedModels(t *testing.T) {
	root := installedFolder(t)
	folder := filepath.Join(root, "lemonade")
	files := map[string]map[string]string{
		"kokoro-v1": {"engine-models/kokoro/index.json": "index", "engine-models/kokoro/model.onnx": "weights"},
	}
	facet := registration.ProviderFacet{Name: "lemonade-image", Endpoint: "lemonade-image", Activation: registration.ActivationOnDemand,
		Services:  []string{"abstraction.inference/speech@1", "abstraction.inference/transcription@1"},
		Resources: []string{"store:lemonade"},
		Models:    []registration.ScopedModel{{Modality: "speech", Model: "kokoro-v1"}, {Modality: "transcription", Model: "Whisper-Large-v3-Turbo"}},
		Engines:   []registration.EngineFact{{ID: "kokoros", Name: "Kokoros"}, {ID: "whispercpp", Name: "whisper.cpp"}},
		Recipes: []registration.ModelRecipe{
			{Model: "kokoro-v1", Engine: "kokoros", Modalities: []string{"speech"}, Objects: []registration.RecipeObject{
				{Role: "index", Digest: installedDigestOf("index")}, {Role: "main", Digest: installedDigestOf("weights")}}},
			{Model: "Whisper-Large-v3-Turbo", Engine: "whispercpp", Modalities: []string{"transcription"}, Objects: []registration.RecipeObject{
				{Role: "main", Digest: installedDigestOf("whisper")}}},
		}}
	host := nativeRecipeHost(t, facet)
	routes := router.New(host)
	cache := filepath.Join(t.TempDir(), "runtime-v1", installedHashesFile)
	stores := &installedStores{cache: cache}
	var reports []string
	allow := func(context.Context, string, string, string) bool { return true }
	declarations := []providerDeclaration{providerFacetCompatibility(`C:\OA\tools\lemonaded.exe`, facet).Declaration}
	read := func(decide func(context.Context, string, string, string) bool) {
		permitted := permittedFileStores(context.Background(), declarations, decide)
		routes.SetHeld(stores.held(context.Background(), permitted, func(err error) { reports = append(reports, err.Error()) }))
	}
	writeInstalledFixture(t, folder, files)
	read(allow)
	if got := offeredNames(t, routes, router.ProfileSpeech); !slices.Equal(got, []string{"kokoro-v1"}) || len(reports) != 0 {
		t.Fatalf("speech offers %v, reports %v", got, reports)
	}
	if got := offeredNames(t, routes, router.ProfileTranscription); len(got) != 0 {
		t.Fatalf("an uninstalled model was offered: %v", got)
	}
	// A deny on abstraction.storage/inventory.provide withdraws the store.
	deny := func(_ context.Context, program, action, resource string) bool {
		return !(action == ActionInventoryProvide && resource == ResourceStore("lemonade"))
	}
	weights := filepath.Join(folder, "engine-models", "kokoro", "model.onnx")
	read(deny)
	if got := offeredNames(t, routes, router.ProfileSpeech); len(got) != 0 {
		t.Fatalf("a denied store kept %v offered", got)
	}
	// A denied store keeps its digests, so allowing it again hashes nothing.
	if stores.hashes[weights].Digest == "" {
		t.Fatal("a deny dropped the store's digests")
	}
	read(allow)
	hashed := stores.hashes[weights]
	if hashed.Digest == "" {
		t.Fatal("the weights were not hashed")
	}
	// The digests persist: a new reader over the same cache re-hashes
	// nothing while size and write time are unchanged. Same-size bytes
	// under the remembered write time keep the remembered digest.
	if err := os.WriteFile(weights, []byte("WEIGHTZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(weights, hashed.Modified, hashed.Modified); err != nil {
		t.Fatal(err)
	}
	stores = &installedStores{cache: cache}
	read(allow)
	if got := offeredNames(t, routes, router.ProfileSpeech); !slices.Equal(got, []string{"kokoro-v1"}) {
		t.Fatalf("a restart re-hashed an unchanged file: %v", got)
	}
	// A new write time is hashed again; the mismatch withdraws the model and
	// is reported once.
	later := hashed.Modified.Add(2 * time.Second)
	if err := os.Chtimes(weights, later, later); err != nil {
		t.Fatal(err)
	}
	read(allow)
	read(allow)
	if got := offeredNames(t, routes, router.ProfileSpeech); len(got) != 0 {
		t.Fatalf("a hash mismatch kept %v offered", got)
	}
	if len(reports) != 1 || !strings.Contains(reports[0], "model.onnx") {
		t.Fatalf("mismatch reports = %v", reports)
	}
	states, _, _, _, _ := routes.Residency(false)
	if len(states) != 1 || states[0].Up {
		t.Fatalf("host with no installed model = %+v", states)
	}
}

// The inventory refresh never waits on hashing: current answers the last
// finished reading at once and wakes the next refresh when a reading changes.
func TestInstalledStoresReadOffTheRefresh(t *testing.T) {
	root := installedFolder(t)
	writeInstalledFixture(t, filepath.Join(root, "lemonade"), map[string]map[string]string{"kokoro-v1": {"m/index.json": "index"}})
	stores := &installedStores{}
	// Runs before installedFolder's cleanup restores the root (LIFO).
	t.Cleanup(stores.wait)
	woke := make(chan struct{}, 4)
	changed := func() { woke <- struct{}{} }
	ctx := context.Background()
	if first := stores.current(ctx, []string{"lemonade"}, func(err error) { t.Error(err) }, changed); len(first) != 0 {
		t.Fatalf("the first refresh waited on a reading: %v", first)
	}
	select {
	case <-woke:
	case <-time.After(30 * time.Second):
		t.Fatal("a finished reading woke no refresh")
	}
	if held := stores.current(ctx, []string{"lemonade"}, func(err error) { t.Error(err) }, changed); len(held) != 1 || held[0].Digest != installedDigestOf("index") {
		t.Fatalf("held = %+v", held)
	}
}

// A record that does not read contributes nothing; a path that leaves the
// store's folder is refused.
func TestInstalledRecordRefusesUnsafePaths(t *testing.T) {
	for _, p := range []string{"../escape", "/abs", `a\b`, "a/../b", "C:x", ""} {
		if validInstalledPath(p) {
			t.Fatalf("%q was admitted", p)
		}
	}
	if !validInstalledPath("engine-models/models--x/snapshots/r/f.bin") {
		t.Fatal("a clean relative path was refused")
	}
	root := installedFolder(t)
	folder := filepath.Join(root, "lemonade")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, installedRecordName), []byte(`{"version":1,"models":[{"model":"m","files":[{"path":"../x","size":1,"sha256":"`+installedDigestOf("x")[7:]+`"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stores installedStores
	reported := 0
	if held := stores.held(context.Background(), []string{"lemonade"}, func(error) { reported++ }); len(held) != 0 || reported != 1 {
		t.Fatalf("held %v, reported %d", held, reported)
	}
}
