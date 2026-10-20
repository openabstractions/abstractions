package main

import (
	rclient "github.com/openabstractions/abstraction-router/go/client"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"testing"
)

func TestLibraryExternalHostedCopyAndComponents(t *testing.T) {
	catalog := rclient.ModelsSnapshot{Models: []rclient.Family{{Family: "base", HeldIn: []string{"external"}, Components: []rclient.Component{{Store: "external", Role: "projector", Name: "vision.gguf"}}}}}
	files := libraryFiles{Files: []libraryFile{{Family: "base", Store: "external", ID: "exact-object", Size: 42}}, Hosted: map[string]string{libraryHostedKey("external", "exact-object"): "served-base"}, Stores: map[string]content.Store{"external": {Name: "external", Locator: "C:/Models/External"}}}
	models, _ := projectLibrary(catalog, files)
	if len(models) != 1 || len(models[0].Objects) != 1 || !models[0].Objects[0].Hosted || models[0].Objects[0].Store != "external" || models[0].Objects[0].ID != "exact-object" {
		t.Fatalf("exact external hosted copy: %+v", models)
	}
	if len(models[0].Components) != 1 || models[0].Components[0].Name != "vision.gguf" || models[0].Components[0].Role != "projector" {
		t.Fatalf("component nested under base: %+v", models)
	}
	delete(files.Hosted, libraryHostedKey("external", "exact-object"))
	models, _ = projectLibrary(catalog, files)
	if models[0].Objects[0].Hosted {
		t.Fatal("a file absent from the hosted list offers removal")
	}
}

func TestLibraryRemoteOnlyFamilyOffersNoLocalRemoval(t *testing.T) {
	catalog := rclient.ModelsSnapshot{Models: []rclient.Family{{Family: "remote", Names: []rclient.Alias{{Host: "remote-host", Hosted: true, Servable: true}}}}}
	models, _ := projectLibrary(catalog, libraryFiles{})
	if len(models) != 0 {
		t.Fatalf("remote-only family has a local row: %+v", models)
	}
}
