package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/listen"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
	router "github.com/openabstractions/abstraction-router/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// pictureStoreWith is OA's picture store as inventoryd reports it when every
// file of sets lies at its pinned path and size: each file one object by its
// catalogue digest, and each set one model manifest named by it.
func pictureStoreWith(sets ...torch.TorchModelSet) storageservice.ComposedInventory {
	composed := storageservice.ComposedInventory{Stores: []content.Store{{Name: setupPictureStore, Present: true}}}
	seen := map[string]bool{}
	for _, set := range sets {
		manifest := content.Manifest{ID: "manifest-" + set.Name, Addressing: content.AddressingContent, Kind: modelDescriptor,
			Store: setupPictureStore, Names: []content.Name{{Scheme: setupPictureStore, Name: set.Name}},
			Descriptor: map[string]string{"family": set.Name, "format": "safetensors", "source": setupPictureStore}}
		for _, f := range set.Files {
			locator := "flux2-klein-4b/" + f.Locators[len(f.Locators)-1]
			digest := "sha256:" + f.SHA256
			manifest.Entries = append(manifest.Entries, content.Entry{Role: f.Role, Digest: digest, Size: f.Size,
				Evidence: content.EvidenceHashed, Locator: locator})
			if !seen[locator] {
				seen[locator] = true
				composed.Objects = append(composed.Objects, content.ObjectHolders{Store: setupPictureStore,
					Object: content.Object{Locator: locator, Size: f.Size, Digest: digest, Evidence: content.EvidenceHashed}})
			}
		}
		composed.Manifests = append(composed.Manifests, content.ManifestHolders{Manifest: manifest})
	}
	return composed
}

func familyNamed(families []router.Family, name string) (router.Family, bool) {
	for _, f := range families {
		if f.Family == name {
			return f, true
		}
	}
	return router.Family{}, false
}

// A pinned file that is no model of its own (a tokenizer, a vocabulary, an
// encoder) is never a model or family: not on Models, which
// oa_models_list reads, and not in Offer (router ROUTE-N4). Only the sets
// are families.
func TestPinnedPlainFilesAreNeverAModel(t *testing.T) {
	models := []string{"image:flux2-klein-4b-compact", "image:z-image-turbo"}
	routes := router.New(pyTorchImageHost(t, models...))
	routes.SetHeld(heldObjects(pictureStoreWith(torch.Flux2Klein4BCompactTorch, torch.ZImageTurboTorch)))
	if got := offeredNames(t, routes, router.ProfileImage); !slices.Equal(got, []string{"flux2-klein-4b-compact", "z-image-turbo"}) {
		t.Fatalf("image offers %v", got)
	}
	families, _ := routes.Models(true)
	var names []string
	for _, f := range families {
		names = append(names, f.Family)
		for _, alias := range f.Names {
			for _, word := range []string{"tokenizer", "vocab", "merges", "qwen", "vae", ".json", ".safetensors", "/"} {
				if strings.Contains(strings.ToLower(alias.Name), word) {
					t.Fatalf("family %s lists the file %q as a model", f.Family, alias.Name)
				}
			}
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"flux2-klein-4b-compact", "z-image-turbo"}) {
		t.Fatalf("families %v", names)
	}
}

// With three sets installed the declaration serves two; the third is
// reported as a held family, servable false, held in OA's picture store, and
// never offered (router ROUTE-H2).
func TestThirdInstalledSetIsHeldAndNotServable(t *testing.T) {
	models := []string{"image:flux2-klein-4b-compact", "image:z-image-turbo"}
	routes := router.New(pyTorchImageHost(t, models...))
	routes.SetHeld(heldObjects(pictureStoreWith(torch.Flux2Klein4BTorch, torch.Flux2Klein4BCompactTorch, torch.ZImageTurboTorch)))
	if got := offeredNames(t, routes, router.ProfileImage); !slices.Equal(got, []string{"flux2-klein-4b-compact", "z-image-turbo"}) {
		t.Fatalf("image offers %v", got)
	}
	families, _ := routes.Models(true)
	third, ok := familyNamed(families, "flux2-klein-4b")
	if !ok || len(third.Names) != 1 || third.Names[0].Servable || third.Names[0].Host != "" || !slices.Equal(third.HeldIn, []string{setupPictureStore}) {
		t.Fatalf("third set %+v (found %v)", third, ok)
	}
	served, ok := familyNamed(families, "z-image-turbo")
	if !ok || !slices.ContainsFunc(served.Names, func(a router.Alias) bool { return a.Servable && a.Host == "pytorch-image" }) ||
		!slices.Contains(served.HeldIn, setupPictureStore) {
		t.Fatalf("served set %+v", served)
	}
}

// An upgrade never hides a model the earlier runtime listed. Before this
// runtime has read OA's picture store a pytorchd host lists what it
// declares; once the store is read the set table applies, and the compact
// Klein, whose files the store holds, stays listed through Setup's swap to
// the declaration serving Klein and Z-Image.
func TestKleinStaysListedThroughTheUpgradeSwap(t *testing.T) {
	p := &runtimeProviders{changes: make(chan struct{})}
	host := func(models ...string) *router.Host {
		profiles := []string{router.ProfileImage}
		h, err := router.NewNative("pytorch-image", "fixture", listen.ServerExpectation{}, models, profiles)
		if err != nil {
			t.Fatal(err)
		}
		if p.pictureStoreRead.Load() {
			projectPyTorchSets(h, models, profiles)
		}
		return h
	}
	routes := router.New(host("image:flux2-klein-4b-compact"))
	// The new runtime starts; nothing is read yet.
	routes.SetHeld(heldObjects(storageservice.ComposedInventory{}))
	if got := offeredNames(t, routes, router.ProfileImage); !slices.Equal(got, []string{"flux2-klein-4b-compact"}) {
		t.Fatalf("before the store is read: %v", got)
	}
	// The store is registered and read; hosts are built again.
	read := pictureStoreWith(torch.Flux2Klein4BCompactTorch, torch.ZImageTurboTorch)
	if pictureStoreReadIn(storageservice.ComposedInventory{Stores: []content.Store{{Name: setupPictureStore}}}) {
		t.Fatal("an absent store reads as read")
	}
	if !pictureStoreReadIn(read) || !p.pictureStoreRead.CompareAndSwap(false, true) {
		t.Fatal("the store did not read")
	}
	routes.SetHeld(heldObjects(read))
	routes.SetHosts(host("image:flux2-klein-4b-compact"))
	if got := offeredNames(t, routes, router.ProfileImage); !slices.Equal(got, []string{"flux2-klein-4b-compact"}) {
		t.Fatalf("after the store is read: %v", got)
	}
	// Setup swaps the declaration in one edit.
	routes.SetHosts(host("image:flux2-klein-4b-compact", "image:z-image-turbo"))
	if got := offeredNames(t, routes, router.ProfileImage); !slices.Equal(got, []string{"flux2-klein-4b-compact", "z-image-turbo"}) {
		t.Fatalf("after the swap: %v", got)
	}
}
