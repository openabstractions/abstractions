package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	identityapi "github.com/openabstractions/abstraction-identity/go/abstraction/identity/api"
	"github.com/openabstractions/abstraction-identity/listen"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
	router "github.com/openabstractions/abstraction-router/go"
	rwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	routerclient "github.com/openabstractions/abstraction-router/go/client"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

func fixtureDigest(parts ...any) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(parts...)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// largeHeldInventory is a machine with many model stores: per store, single
// GGUF manifests (some families held in several stores), a sharded
// multi-entry snapshot and plain files with digests, plus OA's picture store
// holding all three picture sets.
func largeHeldInventory(stores, models int) storageservice.ComposedInventory {
	composed := pictureStoreWith(torch.Flux2Klein4BTorch, torch.Flux2Klein4BCompactTorch, torch.ZImageTurboTorch)
	for s := range stores {
		store := fmt.Sprintf("store-%02d-%s", s, "a-local-model-store-name")
		composed.Stores = append(composed.Stores, content.Store{Name: store, Present: true})
		for m := range models {
			family := fmt.Sprintf("family-%03d", (s*models/2+m)%(stores*models/2))
			name := fmt.Sprintf("publisher-%02d/%s-instruct-GGUF:%s-Q4_K_M.gguf", s, family, family)
			composed.Manifests = append(composed.Manifests, content.ManifestHolders{Manifest: content.Manifest{
				ID: fixtureDigest(store, m), Store: store, Kind: modelDescriptor, Names: []content.Name{{Scheme: "hf", Name: name}},
				Descriptor: map[string]string{"family": family, "format": "gguf", "architecture": "llama", "context_length": "32768"},
				Entries:    []content.Entry{{Role: "weights", Digest: fixtureDigest(family), Evidence: content.EvidenceHashed, Locator: name}}}})
		}
		sharded := content.Manifest{ID: fixtureDigest(store, "sharded"), Store: store, Kind: modelDescriptor,
			Names:      []content.Name{{Scheme: "hf", Name: fmt.Sprintf("publisher-%02d/sharded-70b", s)}},
			Descriptor: map[string]string{"family": "sharded-70b", "format": "safetensors"}}
		for e := range 8 {
			sharded.Entries = append(sharded.Entries, content.Entry{Role: "weights", Digest: fixtureDigest(store, "shard", e), Evidence: content.EvidenceHashed})
		}
		composed.Manifests = append(composed.Manifests, content.ManifestHolders{Manifest: sharded})
		for o := range 200 {
			composed.Objects = append(composed.Objects, content.ObjectHolders{Store: store,
				Object: content.Object{Locator: fmt.Sprintf("plain/%d.json", o), Digest: fixtureDigest(store, "plain", o), Evidence: content.EvidenceHashed}})
		}
	}
	return composed
}

// A machine with many stores, held files and providers that report engine
// facts under their real registration bindings still answers Models and
// Hosts within the reply bound. R23's oa_models_list failed with
// bound:reply_bytes because each engine fact carried the 84-byte binding ID
// where router@1 admits an 83-byte source.
func TestModelsReplyFitsManyStoresAndEngineFacts(t *testing.T) {
	pictureModels := []string{"image:flux2-klein-4b-compact", "image:z-image-turbo"}
	pictureFile := providerFile{Generation: "picture", Declaration: providerDeclaration{Name: "pytorch-image", Program: filepath.Join(t.TempDir(), "pytorchd")}}
	picture, err := router.NewNative("pytorch-image", "fixture", listen.ServerExpectation{}, pictureModels, []string{router.ProfileImage})
	if err != nil {
		t.Fatal(err)
	}
	picture.BindingID = pictureFile.bindingID()
	setProviderEngineGuide(picture, pictureFile)
	projectPyTorchSets(picture, pictureModels, []string{router.ProfileImage})

	facet := &registration.ProviderFacet{Engines: []registration.EngineFact{{ID: "llama.cpp", Name: "llama.cpp", Version: "b6000"}}}
	var chatModels []string
	for m := range 40 {
		family := fmt.Sprintf("family-%03d", m)
		chatModels = append(chatModels, "chat:"+family)
		facet.Recipes = append(facet.Recipes, registration.ModelRecipe{Model: family, Engine: "llama.cpp", Modalities: []string{router.ProfileChat},
			Objects: []registration.RecipeObject{{Role: "weights", Digest: fixtureDigest(family)}}})
	}
	chatFile := providerFile{Generation: "chat", Declaration: providerDeclaration{Name: "modelhost", Program: filepath.Join(t.TempDir(), "modelhostd")}, ProviderFacet: facet}
	chat, err := router.NewNative("modelhost", "fixture", listen.ServerExpectation{}, chatModels, []string{router.ProfileChat})
	if err != nil {
		t.Fatal(err)
	}
	chat.BindingID = chatFile.bindingID()
	setProviderEngineGuide(chat, chatFile)
	if len(picture.Engines) == 0 || len(chat.Engines) == 0 {
		t.Fatal("fixture projected no engine facts")
	}

	routes := router.New(picture, chat)
	routes.SetHeld(heldObjects(largeHeldInventory(19, 20)))
	routes.Survey()
	t.Run("native-endpoint", func(t *testing.T) { checkModelsReplyBounds(t, routes) })
}

func checkModelsReplyBounds(t *testing.T, routes *router.Router) {
	t.Helper()
	endpoint := listen.Endpoint(fmt.Sprintf("models-reply-bound-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	if err := listen.CanEver(endpoint, router.Bound); err != nil {
		t.Skipf("native Router proof unavailable: %v", err)
	}
	h, err := routerservice.Listen(endpoint, routes)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.EnablePolicy(func(context.Context, *identity.Peer, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() { cancel(); <-done }()

	client := routerclient.New(endpoint)
	for _, fresh := range []bool{false, true} {
		models, err := client.Models(fresh)
		if err != nil {
			t.Fatalf("Models(fresh=%v): %v", fresh, err)
		}
		encoded, err := rwire.ToJSON(models)
		if err != nil || len(encoded) >= int(identityapi.IPCDefaultMaxFrame) {
			t.Fatalf("Models reply %d bytes: %v", len(encoded), err)
		}
		t.Logf("Models(fresh=%v): %d families, %d bytes", fresh, len(models.Models), len(encoded))
		bound := 0
		for _, family := range models.Models {
			for _, object := range family.Objects {
				if object.Cause == rwire.EngineCauseBound {
					bound++
				}
			}
		}
		if bound == 0 {
			t.Fatal("no object carried its configured engine")
		}
	}
	hosts, err := client.Hosts(true)
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	for _, host := range hosts.Hosts {
		if (host.Host == "pytorch-image" || host.Host == "modelhost") && len(host.Engines) == 0 {
			t.Fatalf("host %s lost its engine facts", host.Host)
		}
	}
}
