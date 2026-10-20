package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/listen"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
	router "github.com/openabstractions/abstraction-router/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// pyTorchImageHost is the router host the runtime builds for a pytorchd
// declaration serving models, with the provider's set table projected.
func pyTorchImageHost(t *testing.T, models ...string) *router.Host {
	t.Helper()
	profiles := []string{router.ProfileImage}
	host, err := router.NewNative("pytorch-image", "fixture", listen.ServerExpectation{}, models, profiles)
	if err != nil {
		t.Fatal(err)
	}
	projectPyTorchSets(host, models, profiles)
	return host
}

// pictureStoreHolds is the composed inventory of OA's picture store holding
// files by hashed evidence, as inventoryd reports a Setup store's pinned
// catalogue files.
func pictureStoreHolds(files []torch.VideoModelFile, evidence content.Evidence) storageservice.ComposedInventory {
	var composed storageservice.ComposedInventory
	for _, f := range files {
		composed.Objects = append(composed.Objects, content.ObjectHolders{Store: "oa-picture",
			Object: content.Object{Locator: "flux2-klein-4b/" + f.Locators[len(f.Locators)-1], Size: f.Size,
				Digest: "sha256:" + f.SHA256, Evidence: evidence}})
	}
	return composed
}

// The router lists a declared PyTorch image set only while the inventory
// holds every one of its files by hashed evidence, in any store; with its
// files present Z-Image-Turbo is offered under image and a pick would load
// it, and it is hidden once one file leaves.
func TestPyTorchImageSetListedOnlyWhileItsFilesAreHeld(t *testing.T) {
	models := []string{"image:flux2-klein-4b-compact", "image:z-image-turbo"}
	host := pyTorchImageHost(t, models...)
	if len(host.EngineObjects) != len(torch.Flux2Klein4BCompactTorch.Files)+len(torch.ZImageTurboTorch.Files) {
		t.Fatalf("projected %d files", len(host.EngineObjects))
	}
	routes := router.New(host)

	// Nothing held: the host serves nothing and says why.
	routes.SetHeld(heldObjects(storageservice.ComposedInventory{}))
	if got := offeredNames(t, routes, router.ProfileImage); len(got) != 0 {
		t.Fatalf("offered %v with no files held", got)
	}
	states, _, _, _, _ := routes.Residency(false)
	if len(states) != 1 || states[0].Servable || states[0].Why != "engine:not_installed:pytorch-image" {
		t.Fatalf("host state = %+v", states)
	}

	// Z-Image's files held; the compact Klein's transformer is not.
	held := pictureStoreHolds(torch.ZImageTurboTorch.Files, content.EvidenceHashed)
	routes.SetHeld(heldObjects(held))
	if got := offeredNames(t, routes, router.ProfileImage); !slices.Equal(got, []string{"z-image-turbo"}) {
		t.Fatalf("image offers %v", got)
	}
	decision, _ := routes.Route(router.Request{Model: "z-image-turbo", Profile: router.ProfileImage})
	if decision == nil || decision.Verdict != router.WouldLoad {
		t.Fatalf("pick = %+v", decision)
	}

	// Every compact Klein file is held too, in another store.
	both := pictureStoreHolds(torch.ZImageTurboTorch.Files, content.EvidenceHashed)
	for _, o := range pictureStoreHolds(torch.Flux2Klein4BCompactTorch.Files, content.EvidenceHashed).Objects {
		o.Store = "comfyui"
		both.Objects = append(both.Objects, o)
	}
	routes.SetHeld(heldObjects(both))
	if got := offeredNames(t, routes, router.ProfileImage); !slices.Equal(got, []string{"flux2-klein-4b-compact", "z-image-turbo"}) {
		t.Fatalf("image offers %v", got)
	}

	// The Z-Image transformer leaves: Z-Image is hidden.
	gone := both
	gone.Objects = slices.DeleteFunc(slices.Clone(both.Objects), func(o content.ObjectHolders) bool {
		return o.Object.Digest == "sha256:"+torch.ZImageTurboTorch.Files[0].SHA256
	})
	routes.SetHeld(heldObjects(gone))
	if got := offeredNames(t, routes, router.ProfileImage); !slices.Equal(got, []string{"flux2-klein-4b-compact"}) {
		t.Fatalf("image offers %v after the transformer left", got)
	}

	// Named evidence establishes nothing.
	routes.SetHeld(heldObjects(pictureStoreHolds(torch.ZImageTurboTorch.Files, content.EvidenceNamed)))
	if got := offeredNames(t, routes, router.ProfileImage); len(got) != 0 {
		t.Fatalf("named evidence offered %v", got)
	}
}

func TestPyTorchSetsRequireShippedImageOrExplicitRecipe(t *testing.T) {
	tools := installedPerMachine(t)
	name := oaProgramFiles([]string{pytorchProgram})[0]
	installed := filepath.Join(tools, name)
	custom := filepath.Join(t.TempDir(), name)
	for _, program := range []string{installed, custom} {
		if err := os.WriteFile(program, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := &runtimeProviders{installation: filepath.Join(tools, installationDeclarationsDir)}
	file := providerFile{Declaration: providerDeclaration{Name: pytorchName, Program: custom}}
	if p.shippedPyTorch(file) {
		t.Fatal("a custom image with the same program and provider names claimed installed recipes")
	}
	file.Declaration.Program = installed
	if !p.shippedPyTorch(file) {
		t.Fatal("the exact installed image lost its recipe projection")
	}
	models := []string{"image:z-image-turbo", "image:unknown-set"}
	legacy := pyTorchImageHost(t, models...)
	typed, err := router.NewNative(legacy.Name, "fixture", listen.ServerExpectation{}, models, []string{router.ProfileImage})
	if err != nil {
		t.Fatal(err)
	}
	facet := registration.ProviderFacet{
		Engines: []registration.EngineFact{{ID: pyTorchEngineID, Name: "PyTorch"}},
		Models:  []registration.ScopedModel{{Modality: router.ProfileImage, Model: "z-image-turbo"}},
	}
	projectProviderEngineGuide(typed, &facet, engineSource(typed.BindingID))
	if len(typed.EngineObjects) != 0 {
		t.Fatal("an engine fact alone projected managed recipe objects")
	}
	typed.Engines = nil
	attachPyTorchRecipes(&facet)
	projectProviderEngineGuide(typed, &facet, engineSource(typed.BindingID))
	if len(typed.EngineObjects) != len(torch.ZImageTurboTorch.Files) ||
		!reflect.DeepEqual(typed.EngineObjects, legacy.EngineObjects) || !slices.Equal(typed.Engines, legacy.Engines) {
		t.Fatalf("typed recipes differ from installed projection: %+v %+v", typed.EngineObjects, typed.Engines)
	}
}
