package main

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	router "github.com/openabstractions/abstraction-router/go"
)

// engineSourceBytes is router@1's capacity for Engine.source and
// ModelEngine.source. One longer source refuses the whole Models and Hosts
// reply (unavailable, bound:reply_bytes).
const engineSourceBytes = 83

// engineSource is the provenance an engine fact carries for the registration
// binding that declared it: "provider:" and the binding's digest. A binding
// ID is 84 bytes, one over the router's source capacity; a source of any
// other form that would exceed it is named by its own SHA-256.
func engineSource(binding string) string {
	if digest, ok := strings.CutPrefix(binding, "provider-binding-v1:"); ok {
		binding = "provider:" + digest
	}
	if len(binding) > engineSourceBytes {
		sum := sha256.Sum256([]byte(binding))
		return "provider:" + hex.EncodeToString(sum[:])
	}
	return binding
}

// setProviderEngineGuide runs at the accepted registration owner, beside the
// same configuration that constructs the host. It reads no engine files and
// starts no provider. Unrecognised registrations keep their facts unknown.
func setProviderEngineGuide(host *router.Host, file providerFile) {
	projectProviderEngineGuide(host, file.ProviderFacet, engineSource(file.bindingID()))
}

func projectProviderEngineGuide(host *router.Host, facet *registration.ProviderFacet, source string) {
	if facet == nil {
		return
	}
	for _, engine := range facet.Engines {
		host.Engines = append(host.Engines, router.Engine{ID: engine.ID, Name: engine.Name, Version: engine.Version, Source: source})
	}
	for _, recipe := range facet.Recipes {
		for _, object := range recipe.Objects {
			host.EngineObjects = append(host.EngineObjects, router.ModelObject{Store: object.Store, Object: object.Object,
				Name: recipe.Model, Digest: object.Digest, Role: object.Role,
				Engines: []router.ModelEngine{{Host: host.Name, Model: recipe.Model, Engine: recipe.Engine,
					Profiles: append([]string{}, recipe.Modalities...), Role: object.Role, Source: source}}})
		}
	}
}

// projectHostedObjects names, for a model host declaration whose
// registration carries no recipe for a hosted model, that model's store
// object as the router's engine fact: the same recipe modelhostd's own
// declaration derives (hostdeclaration.Facts), llama.cpp serving the
// object as its model. A declaration written before recipes existed then
// tells the listing which copy of a model the engine serves (R34 live test
// D10). An object named by store and object id never gates installation.
func projectHostedObjects(host *router.Host, hosted []hostedModel, models, profiles []string, source string) {
	_, byProfile := router.DeclaredModels(models, profiles)
	projected := false
	for _, m := range hosted {
		if m.Store == "" || m.Object == "" || declaresRecipe(host, m.Name) {
			continue
		}
		var served []string
		for _, profile := range profiles {
			if slices.Contains(byProfile[profile], m.Name) {
				served = append(served, profile)
			}
		}
		if len(served) == 0 {
			continue
		}
		host.EngineObjects = append(host.EngineObjects, router.ModelObject{Store: m.Store, Object: m.Object, Name: m.Name, Role: "model",
			Engines: []router.ModelEngine{{Host: host.Name, Model: m.Name, Engine: hostedEngineID, Profiles: served, Role: "model", Source: source}}})
		projected = true
	}
	if projected && !slices.ContainsFunc(host.Engines, func(e router.Engine) bool { return e.ID == hostedEngineID }) {
		host.Engines = append(host.Engines, router.Engine{ID: hostedEngineID, Name: "llama.cpp", Source: source})
	}
}

// hostedEngineID is the engine id modelhostd's declaration names llama.cpp by.
const hostedEngineID = "llamacpp"
