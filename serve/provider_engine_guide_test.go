package main

import (
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	router "github.com/openabstractions/abstraction-router/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

func TestProviderEngineGuideProjectsAcceptedFacts(t *testing.T) {
	facet := &registration.ProviderFacet{Engines: []registration.EngineFact{{ID: "chosen", Name: "Producer engine", Version: "v2"}},
		Recipes: []registration.ModelRecipe{{Model: "literal:model", Engine: "chosen", Modalities: []string{"image"},
			Objects: []registration.RecipeObject{{Role: "weights", Store: "store", Object: "opaque/object"},
				{Role: "companion", Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}}}}
	file := providerFile{Generation: "one", ProviderFacet: facet}
	host := &router.Host{Name: "arbitrary-provider"}
	setProviderEngineGuide(host, file)
	if len(host.Engines) != 1 || host.Engines[0].Name != "Producer engine" || host.Engines[0].Version != "v2" || host.Engines[0].Source != engineSource(file.bindingID()) {
		t.Fatalf("engine assertion/provenance lost: %+v", host.Engines)
	}
	if len(host.EngineObjects) != 2 || host.EngineObjects[0].Store != "store" || host.EngineObjects[0].Object != "opaque/object" || host.EngineObjects[1].Digest != facet.Recipes[0].Objects[1].Digest {
		t.Fatalf("required object identities lost: %+v", host.EngineObjects)
	}
	for _, object := range host.EngineObjects {
		association := object.Engines[0]
		if association.Source != engineSource(file.bindingID()) || association.Host != host.Name || association.Model != "literal:model" || association.Engine != "chosen" || association.Role != object.Role {
			t.Fatalf("association changed: %+v", association)
		}
	}
	facet.Recipes[0].Modalities[0] = "mutated"
	if host.EngineObjects[0].Engines[0].Profiles[0] != "image" {
		t.Fatal("projection aliases producer modality slice")
	}
}

func TestProviderEngineGuideRequiresHashedWholeRecipe(t *testing.T) {
	const main = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const companion = "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	for _, evidence := range []content.Evidence{content.EvidenceNamed, content.EvidenceForeign, content.EvidenceHashed} {
		for _, complete := range []bool{false, true} {
			facet := &registration.ProviderFacet{Engines: []registration.EngineFact{{ID: "e", Name: "Engine"}},
				Recipes: []registration.ModelRecipe{{Model: "picture", Engine: "e", Modalities: []string{"image"},
					Objects: []registration.RecipeObject{{Role: "main", Digest: main}, {Role: "encoder", Digest: companion}}}}}
			host := router.OpenAICompatible("arbitrary", "inert", "/v1", []string{"picture"})
			host.Profiles = []string{"image"}
			setProviderEngineGuide(host, providerFile{ProviderFacet: facet})
			manifest := content.Manifest{ID: "recipe", Store: "source", Kind: modelDescriptor, Names: []content.Name{{Name: "picture"}}, Descriptor: map[string]string{"family": "picture"},
				Entries: []content.Entry{{Digest: main, Role: "main", Evidence: content.EvidenceHashed}}}
			if complete {
				manifest.Entries = append(manifest.Entries, content.Entry{Digest: companion, Role: "encoder", Evidence: evidence})
			}
			r := router.New(host)
			r.SetHeld(heldObjects(storageservice.ComposedInventory{Manifests: []content.ManifestHolders{{Manifest: manifest}}}))
			families, _ := r.Models(true)
			count := 0
			for _, family := range families {
				for _, object := range family.Objects {
					count++
					if (len(object.Engines) > 0) != (complete && evidence == content.EvidenceHashed) {
						t.Fatalf("complete=%v evidence=%s association: %+v", complete, evidence, object)
					}
				}
			}
			want := 1
			if complete {
				want++
			}
			if count != want {
				t.Fatalf("required recipe objects lost: got %d, want %d", count, want)
			}
		}
	}
}

func TestProviderEngineGuideKeepsUnknownAssociationsUnknown(t *testing.T) {
	for _, file := range []providerFile{{}, {ProviderFacet: &registration.ProviderFacet{}},
		{Declaration: providerDeclaration{Name: "modelhost", Arguments: []string{"serve", "--engine", "path", "--model", "m=s/o"}}}} {
		host := &router.Host{Name: "any"}
		setProviderEngineGuide(host, file)
		if len(host.Engines) != 0 || len(host.EngineObjects) != 0 {
			t.Fatalf("inferred an undeclared association: %+v", host)
		}
	}
}
