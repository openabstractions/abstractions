package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	configservice "github.com/openabstractions/abstraction-config/go/service"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	hostdeclaration "github.com/openabstractions/abstraction-provider-modelhost/go/declaration"
)

func allowlistTestFacet(t *testing.T, engine string, models ...hostedModel) registration.ProviderFacet {
	t.Helper()
	facet := registration.ProviderFacet{Name: modelHostName, Endpoint: "modelhost", Activation: registration.ActivationOnDemand,
		Arguments: []string{"serve", argEndpoint, providerEndpointToken, argEngine, engine, argEngineArg, "--model"},
		Services:  []string{"abstraction.inference/chat@1"}}
	var configured []hostdeclaration.Model
	for _, m := range models {
		facet.Arguments = append(facet.Arguments, argModel, m.argument())
		facet.Models = append(facet.Models, registration.ScopedModel{Modality: "chat", Model: m.Name})
		configured = append(configured, hostdeclaration.Model{Name: m.Name, Store: m.Store, Object: m.Object})
	}
	spec, err := hostdeclaration.Build("modelhostd.exe", facet, engine, configured)
	if err != nil {
		t.Fatal(err)
	}
	return spec.Providers[0]
}

func recipeObject(facet registration.ProviderFacet, model string) string {
	for _, r := range facet.Recipes {
		if r.Model == model {
			for _, o := range r.Objects {
				if o.Role == "model" {
					return o.Store + "/" + o.Object
				}
			}
		}
	}
	return ""
}

// A model the reconcile rebinds to another object gets that object's recipe:
// the kept recipe names the old object and is not kept (REG-CFG2).
func TestAllowlistFacetRebindsAModelsRecipe(t *testing.T) {
	kept := allowlistTestFacet(t, "llama-server.exe", hostedModel{Name: "qwen", Store: "s", Object: "a.gguf"})
	if got := recipeObject(kept, "qwen"); got != "s/a.gguf" {
		t.Fatalf("kept recipe names %q", got)
	}
	rebound := []hostedModel{{Name: "qwen", Store: "s", Object: "b.gguf"}}
	next := allowlistTestFacet(t, "llama-server.exe", rebound...)
	out := allowlistFacet(kept, next, "llama-server.exe", rebound)
	if got := recipeObject(out, "qwen"); got != "s/b.gguf" || len(out.Recipes) != 1 {
		t.Fatalf("rebound recipe names %q (%d recipes), want s/b.gguf", got, len(out.Recipes))
	}
	// A kept facet without engine facts derives none, and still drops the
	// stale recipe.
	kept.Engines = nil
	if out := allowlistFacet(kept, next, "llama-server.exe", rebound); len(out.Recipes) != 0 {
		t.Fatalf("a stale recipe survived: %+v", out.Recipes)
	}
}

// The allowlist split reads the serve line as modelhostd does: an
// --engine-arg whose value looks like --model stays that engine argument.
func TestAllowlistKeepsAnEngineArgumentThatLooksLikeAModel(t *testing.T) {
	kept := allowlistTestFacet(t, "llama-server.exe", hostedModel{Name: "qwen", Store: "s", Object: "a.gguf"})
	rest, entries, _ := splitServeAllowlist(kept.Arguments)
	if !slices.Equal(allowlistValues(entries, argModel), []string{"qwen=s/a.gguf"}) {
		t.Fatalf("models %v", allowlistValues(entries, argModel))
	}
	if i := slices.Index(rest, argEngineArg); i < 0 || i+1 >= len(rest) || rest[i+1] != "--model" {
		t.Fatalf("the engine argument was split: %v", rest)
	}
	// Only the allowlist differs: the kept facet with another model passes.
	more := []hostedModel{{Name: "qwen", Store: "s", Object: "a.gguf"}, {Name: "smol", Store: "s", Object: "c.gguf"}}
	next := allowlistFacet(kept, allowlistTestFacet(t, "llama-server.exe", more...), "llama-server.exe", more)
	if !allowlistOnly(kept, next) {
		t.Fatal("an added model is more than an allowlist change")
	}
	changed := cloneProviderFacet(next)
	changed.Arguments = append(changed.Arguments, argEngineArg, "--ctx-size=8192")
	if allowlistOnly(kept, changed) {
		t.Fatal("an added engine argument passed as an allowlist change")
	}
}

// A key read the registry cannot reach is unavailable, never the
// undesignated refusal that names a missing registration key (CFG-E4).
func TestProviderConfigReadUnreachableRegistryIsUnavailable(t *testing.T) {
	d := registration1459Directory(t, t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := d.providerConfigCandidates(ctx, "modelhostd.exe", "model_context")
	if !errors.Is(err, configservice.ErrKeyReadPolicyUnavailable) || errors.Is(err, configservice.ErrKeyNotDesignated) {
		t.Fatalf("an unreachable registry answered %v, want unavailable", err)
	}
	d.programConfigKeyKnown = func(string) bool { return true }
	if _, err := d.providerConfigCandidates(t.Context(), "modelhostd.exe", "model_context"); !errors.Is(err, configservice.ErrKeyNotDesignated) {
		t.Fatalf("no registration naming the key answered %v, want the undesignated refusal", err)
	}
}

// The reconcile's allowlist edit of a line hostArguments wrote is exactly the
// line hostArguments writes for the new models, with an sd-server video (whose
// flags follow the models) and without one, so Setup's next `models host`
// finds the same declaration and asks no Keep (REG-CFG2).
func TestAllowlistEditKeepsTheHostArgumentsOrder(t *testing.T) {
	first := hostedModel{Name: "qwen", Store: "s", Object: "a.gguf", Content: "sha256:aa"}
	second := hostedModel{Name: "smol", Store: "h", Object: "b.gguf"}
	programs := []string{`C:\oa\openabstractions.exe`}
	base := hostSettings{program: "modelhostd.exe", endpoint: "modelhost", engine: "llama-server.exe", idle: "30m0s",
		extra: []string{"--ctx-size", "8192"}, configs: []string{`C:\setup\inventoryd.json`}}
	video := base
	video.media = hostMedia{videoModel: "wan", videoKind: videoKindSdCpp, videoEngine: "sd-server.exe", engineArgs: []string{"--vae-tiling"}}
	for name, settings := range map[string]hostSettings{"sd-server video": video, "no video": base} {
		t.Run(name, func(t *testing.T) {
			kept := registration.ProviderFacet{Name: modelHostName, Arguments: hostArguments(settings, []hostedModel{first}, programs),
				Models: []registration.ScopedModel{{Modality: "chat", Model: first.Name}}}
			next := []hostedModel{first, second}
			want := hostArguments(settings, next, programs)
			nextFacet := registration.ProviderFacet{Name: modelHostName, Arguments: want,
				Models: []registration.ScopedModel{{Modality: "chat", Model: first.Name}, {Modality: "chat", Model: second.Name}}}
			if got := allowlistFacet(kept, nextFacet, settings.engine, next).Arguments; !slices.Equal(got, want) {
				t.Fatalf("reconciled line\n got %q\nwant %q", got, want)
			}
		})
	}
}
