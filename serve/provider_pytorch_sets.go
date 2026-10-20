package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/fileid"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
	router "github.com/openabstractions/abstraction-router/go"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// pyTorchEngineID names the PyTorch engine in the router's engine facts.
const pyTorchEngineID = "pytorch"

// shippedPyTorch checks the explicit installed image. Legacy declarations
// for a custom program can still run their native host without these recipes.
func (p *runtimeProviders) shippedPyTorch(file providerFile) bool {
	return p.shippedPyTorchAt(file, os.Executable, shippedProviderImages)
}

func (p *runtimeProviders) shippedPyTorchAt(file providerFile, executable func() (string, error), embedded string) bool {
	if p.installation == "" || (file.Declaration.Name != product.Diffusers && file.Declaration.Name != product.DiffusersModelsHost) {
		return false
	}
	ctx := context.Background()
	strict := fileid.Options{Links: fileid.RefuseLinks}
	expected := filepath.Join(filepath.Dir(p.installation), oaProgramFiles([]string{pytorchProgram})[0])
	installed, err := fileid.Open(ctx, fileid.Self, expected, strict)
	if err != nil {
		return false
	}
	defer installed.Close()
	program, err := fileid.Open(ctx, fileid.Self, file.Declaration.Program, strict)
	if err != nil {
		return false
	}
	defer program.Close()
	info, err := program.File().Stat()
	if err != nil || !info.Mode().IsRegular() || !program.FileID().Equal(installed.FileID()) {
		return false
	}
	if runtime.GOOS != "windows" || p.shippedProvider(file) {
		return true
	}
	if embedded == "" {
		return false
	}
	self, err := executable()
	return err == nil && matchesEmbeddedShippedImage(ctx, file.Declaration.Program, program.FileID().Key(), self, embedded)
}

func pyTorchRecipe(model, modality string) (registration.ModelRecipe, bool) {
	set, ok := torch.TorchModelSetNamed(model)
	if !ok || set.Profile != modality {
		return registration.ModelRecipe{}, false
	}
	recipe := registration.ModelRecipe{Model: model, Engine: pyTorchEngineID, Modalities: []string{modality}}
	for _, file := range set.Files {
		recipe.Objects = append(recipe.Objects, registration.RecipeObject{Role: file.Role, Digest: "sha256:" + file.SHA256})
	}
	return recipe, true
}

// attachPyTorchRecipes writes ordinary Registry recipes for the configured
// model sets. Already declared recipes keep the model's own facts.
func attachPyTorchRecipes(facet *registration.ProviderFacet) {
	added := false
	for _, model := range facet.Models {
		if slices.ContainsFunc(facet.Recipes, func(recipe registration.ModelRecipe) bool { return recipe.Model == model.Model }) {
			continue
		}
		if recipe, ok := pyTorchRecipe(model.Model, model.Modality); ok {
			facet.Recipes = append(facet.Recipes, recipe)
			added = true
		}
	}
	if added && !slices.ContainsFunc(facet.Engines, func(e registration.EngineFact) bool { return e.ID == pyTorchEngineID }) {
		facet.Engines = append(facet.Engines, registration.EngineFact{ID: pyTorchEngineID, Name: "PyTorch"})
	}
}

// managedPyTorchRecipes identifies the reserved provider's exact table
// recipes for the receiver's durable image pin.
func managedPyTorchRecipes(spec registration.RegistrationSpec) bool {
	for _, facet := range spec.Providers {
		if facet.Name != pytorchName {
			continue
		}
		for _, recipe := range facet.Recipes {
			if recipe.Engine != pyTorchEngineID || len(recipe.Modalities) != 1 {
				continue
			}
			expected, ok := pyTorchRecipe(recipe.Model, recipe.Modalities[0])
			if ok && len(expected.Objects) == len(recipe.Objects) {
				if slices.Equal(expected.Objects, recipe.Objects) {
					return true
				}
				all := true
				for _, object := range expected.Objects {
					all = all && slices.Contains(recipe.Objects, object)
				}
				if all {
					return true
				}
			}
		}
	}
	return false
}

// projectPyTorchSets gives a pytorchd host the files of each model set it
// declares, by role and SHA-256, from the provider's own pinned set table
// (pytorch CONTRACT.md PYTORCH-T2). The router's installed rule then lists a
// set only while storage holds every one of its files by hashed evidence,
// in any store, and hides it when they leave (router ROUTE-E1). A
// registration's own recipes keep their account; a model the table does not
// name keeps none, and an other program's declaration gets nothing here.
func projectPyTorchSets(host *router.Host, models, profiles []string) {
	if host == nil {
		return
	}
	_, byProfile := router.DeclaredModels(models, profiles)
	facet := registration.ProviderFacet{}
	for _, profile := range profiles {
		for _, name := range byProfile[profile] {
			recipe, ok := pyTorchRecipe(name, profile)
			if !ok || declaresRecipe(host, recipe.Model) {
				continue
			}
			facet.Recipes = append(facet.Recipes, recipe)
		}
	}
	if len(facet.Recipes) > 0 && !slices.ContainsFunc(host.Engines, func(e router.Engine) bool { return e.ID == pyTorchEngineID }) {
		facet.Engines = []registration.EngineFact{{ID: pyTorchEngineID, Name: "PyTorch"}}
	}
	projectProviderEngineGuide(host, &facet, engineSource(host.BindingID))
}

// declaresRecipe reports whether the host's registration already names
// model's files.
func declaresRecipe(host *router.Host, model string) bool {
	for _, object := range host.EngineObjects {
		for _, engine := range object.Engines {
			if engine.Model == model {
				return true
			}
		}
	}
	return false
}

// setupPictureStore is OA's picture store, the reserved Setup store of OA's
// own picture model folder (local-stores source.SetupPictureStore).
const setupPictureStore = "oa-picture"

// pictureStoreReadIn reports whether this composition read OA's picture
// store: present, answered now and without error. Until a runtime has read
// it, a pytorchd host lists what it declares, so an upgrade never hides a
// model the earlier runtime listed before the store's files are known.
func pictureStoreReadIn(composed storageservice.ComposedInventory) bool {
	for _, s := range composed.Stores {
		if s.Name == setupPictureStore && s.Present && len(s.Errors) == 0 {
			return true
		}
	}
	return false
}
