package gateway

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
)

type callableModelFixture struct{ fixtureBackend }

func (b *callableModelFixture) Models(context.Context, ModelsInput) (ModelsOutput, error) {
	return ModelsOutput{Models: modelsFrom([]routerwire.Family{{
		Family: "qwen3.6-35b-a3b", Title: "Qwen3.6 35B",
		Names: []routerwire.Alias{{Name: "qwen3.6-35b-a3b", Host: "modelhost", Servable: true, Profiles: []string{"chat"}}},
	}})}, nil
}

func (b *callableModelFixture) Complete(_ context.Context, in CompleteInput) (CompleteOutput, error) {
	if in.Model != "qwen3.6-35b-a3b" {
		return CompleteOutput{Outcome: "no_host", Reason: "router:not-here"}, nil
	}
	return CompleteOutput{Outcome: "completed", Model: in.Model}, nil
}

func TestModelsListGuidesCallerToTheCallableAlias(t *testing.T) {
	_, session := connected(t, &callableModelFixture{}, "model-alias", "2026-07-28")
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := map[string]string{}
	for _, tool := range tools.Tools {
		descriptions[tool.Name] = tool.Description
	}
	if !strings.Contains(descriptions[ToolModels], "exact alias of a servable model in the model argument") ||
		!strings.Contains(descriptions[ToolModels], "name is its display title") ||
		!strings.Contains(descriptions[ToolComplete], "Copy an exact alias for the model from oa_models_list") {
		t.Fatalf("model guidance: %q / %q", descriptions[ToolModels], descriptions[ToolComplete])
	}
	_, listing := call[ModelsOutput](t, session, ToolModels, map[string]any{})
	if len(listing.Models) != 1 || listing.Models[0].Name != "Qwen3.6 35B" || len(listing.Models[0].Aliases) != 1 {
		t.Fatalf("listing: %+v", listing)
	}
	model := listing.Models[0].Aliases[0]
	_, result := call[CompleteOutput](t, session, ToolComplete, map[string]any{"model": model, "prompt": "hello", "hosting": "local"})
	if result.Outcome != "completed" || result.Model != model {
		t.Fatalf("completion by listed alias: %+v", result)
	}
}

// The structured listing carries the stores of every model on this machine,
// including one no host serves, and says which of them can be asked for.
func TestModelsListCarriesStoreProvenance(t *testing.T) {
	models := modelsFrom([]routerwire.Family{
		{Family: "smollm2-135m", HeldIn: []string{"huggingface", "lmstudio"},
			Names: []routerwire.Alias{{Host: "lmstudio", Name: "smollm2-135m-instruct", Servable: true,
				Profiles: []string{"chat"}, HeldIn: []string{"lmstudio"}}}},
		{Family: "wan2.2-5b-ti2v", HeldIn: []string{"comfyui-amd"},
			Names: []routerwire.Alias{{Name: "diffusion_models/wan2.2_ti2v_5B_fp16.safetensors", HeldIn: []string{"comfyui-amd"}}}},
		{Family: "nothing-anywhere", Names: []routerwire.Alias{{Host: "comfyui", Name: "nothing-anywhere"}}},
	})
	if len(models) != 2 {
		t.Fatalf("models %+v", models)
	}
	if models[0].Family != "smollm2-135m" || !models[0].Servable ||
		!slices.Equal(models[0].Aliases, []string{"smollm2-135m-instruct"}) ||
		!slices.Equal(models[0].HeldIn, []string{"huggingface", "lmstudio"}) {
		t.Fatalf("servable model %+v", models[0])
	}
	if models[1].Family != "wan2.2-5b-ti2v" || models[1].Servable || len(models[1].Aliases) != 0 ||
		!slices.Equal(models[1].HeldIn, []string{"comfyui-amd"}) {
		t.Fatalf("held model %+v", models[1])
	}
}

// A model host serving a store's object under its file name lists a digest
// alias (R34 live test D4: 55983c5a….gguf beside Qwen3.6 35B). The listing
// drops a digest alias when a plain one remains, and keeps a digest-only
// model askable by its digest alias.
func TestModelsListHidesDigestAliases(t *testing.T) {
	digest := "55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1.gguf"
	models := modelsFrom([]routerwire.Family{
		{Family: "qwen3.6-35b", Title: "Qwen3.6 35B", Names: []routerwire.Alias{
			{Host: "modelhost", Name: digest, Servable: true, Profiles: []string{"chat"}},
			{Host: "modelhost", Name: "qwen3.6-35b", Servable: true, Profiles: []string{"chat"}}}},
		{Family: "only-digest", Names: []routerwire.Alias{{Host: "modelhost", Name: digest, Servable: true}}},
	})
	if len(models) != 2 {
		t.Fatalf("models %+v", models)
	}
	if !models[1].Servable || !slices.Equal(models[1].Aliases, []string{"qwen3.6-35b"}) || models[1].Name != "Qwen3.6 35B" {
		t.Fatalf("named model %+v", models[1])
	}
	// R36 live listing: the hidden digest alias kept its capability line.
	if !slices.Equal(models[1].Capabilities, []string{"local qwen3.6-35b: chat"}) {
		t.Fatalf("named model capabilities %q", models[1].Capabilities)
	}
	if !models[0].Servable || !slices.Equal(models[0].Aliases, []string{digest}) {
		t.Fatalf("digest-only model %+v", models[0])
	}
}

// The router lists a name two servers serve once per server (ROUTE-D7). The
// structured listing offers that name once.
func TestModelsListNamesASharedAliasOnce(t *testing.T) {
	models := modelsFrom([]routerwire.Family{
		{Family: "qwen3-8b", Names: []routerwire.Alias{
			{Host: "a", Name: "qwen3-8b", Servable: true, Profiles: []string{"chat"}},
			{Host: "b", Name: "qwen3-8b", Servable: true, Profiles: []string{"chat"}},
		}},
	})
	if len(models) != 1 || !slices.Equal(models[0].Aliases, []string{"qwen3-8b"}) {
		t.Fatalf("models %+v", models)
	}
}

// The model-label digest guard for the MCP listing (owner, 2026-11-01): a
// family that is a file digest, as a store's file named by its SHA-256 has,
// stays the identifier and is never the name shown to a person.
func TestModelsListDigestLabelGuard(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	commit := "5bc3e238d916f48a861bac2f8a1990a0e9b7e98d"
	models := modelsFrom([]routerwire.Family{
		{Family: digest, HeldIn: []string{"oa-chat-1"}, Names: []routerwire.Alias{{Name: digest + ".gguf", HeldIn: []string{"oa-chat-1"}}}},
		{Family: "models--Qwen--x/snapshots/" + commit + "/qwen.gguf", HeldIn: []string{"huggingface"}},
		{Family: "qwen3.6-35b-a3b", Names: []routerwire.Alias{{Host: "modelhost", Name: "qwen3.6-35b-a3b", Servable: true, Profiles: []string{"chat"}}}},
	})
	hex := regexp.MustCompile(`(?i)[0-9a-f]{16,}`)
	names := map[string]string{}
	for _, m := range models {
		if hex.MatchString(m.Name) {
			t.Fatalf("a digest names a model: %+v", m)
		}
		names[m.Family] = m.Name
	}
	if len(names) != 3 || names[digest] != "Unnamed model" || names["models--Qwen--x/snapshots/"+commit+"/qwen.gguf"] != "qwen" ||
		names["qwen3.6-35b-a3b"] != "qwen3.6-35b-a3b" {
		t.Fatalf("names by family %q", names)
	}
}

// A model host serving a digest-named store file under the person's --as
// name lists one model named by that name. A held file no host serves and
// no name reads is named by what its header states, before "Unnamed model".
func TestModelsListNamesADigestFileByItsServedNameElseItsHeader(t *testing.T) {
	digest := strings.Repeat("5a", 32)
	other := strings.Repeat("6b", 32)
	models := modelsFrom([]routerwire.Family{
		{Family: digest, HeldIn: []string{"jrn-chat"}, Names: []routerwire.Alias{{Host: "modelhost", Name: "my-qwen", Servable: true,
			Profiles: []string{"chat"}, HeldIn: []string{"jrn-chat"}}},
			Objects: []routerwire.ModelObject{{Store: "jrn-chat", Name: digest + ".gguf", Architecture: "qwen35moe"}}},
		{Family: other, HeldIn: []string{"jrn-chat"}, Names: []routerwire.Alias{{Name: other + ".gguf", HeldIn: []string{"jrn-chat"}}},
			Objects: []routerwire.ModelObject{{Store: "jrn-chat", Name: other + ".gguf", Architecture: "gemma3"}}},
	})
	names := map[string]Model{}
	for _, m := range models {
		names[m.Family] = m
	}
	if m := names[digest]; len(models) != 2 || m.Name != "my-qwen" || !m.Servable || !slices.Equal(m.HeldIn, []string{"jrn-chat"}) {
		t.Fatalf("served file %+v", models)
	}
	if m := names[other]; m.Name != "gemma3" || m.Servable {
		t.Fatalf("held file %+v", m)
	}
}

// R34 live test D10: Qwen3.6 35B is held in the Hugging Face cache and in
// Setup's store, and the model host serves Setup's copy alone. The listing
// names the served store beside held_in, and says nothing when every held
// copy is served or no engine association names a store.
func TestModelsListNamesTheServedCopyBesideAnUnservedOne(t *testing.T) {
	engine := []routerwire.ModelEngine{{Host: "modelhost", Model: "qwen3.6-35b-a3b", Engine: "llamacpp", Profiles: []string{"chat"}, Role: "model"}}
	families := []routerwire.Family{
		{Family: "qwen3.6-35b-a3b", HeldIn: []string{"huggingface", "oa-chat-1"},
			Names: []routerwire.Alias{{Host: "modelhost", Name: "qwen3.6-35b-a3b", Servable: true, Profiles: []string{"chat"}}},
			Objects: []routerwire.ModelObject{
				{Store: "huggingface", Name: "unsloth/Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf", Cause: routerwire.EngineCauseUnknown},
				{Store: "oa-chat-1", Name: "945b.gguf", Engines: engine, Cause: routerwire.EngineCauseBound}}},
		{Family: "qwen3-0.6b", HeldIn: []string{"huggingface"},
			Names:   []routerwire.Alias{{Host: "modelhost", Name: "qwen3-0.6b", Servable: true}},
			Objects: []routerwire.ModelObject{{Store: "huggingface", Engines: []routerwire.ModelEngine{{Host: "modelhost", Model: "qwen3-0.6b"}}}}},
		{Family: "smollm2-135m", HeldIn: []string{"huggingface", "lmstudio"},
			Names: []routerwire.Alias{{Host: "lmstudio", Name: "smollm2-135m", Servable: true}}},
	}
	models := modelsFrom(families)
	if len(models) != 3 {
		t.Fatalf("models %+v", models)
	}
	for _, m := range models {
		want := []string(nil)
		if m.Family == "qwen3.6-35b-a3b" {
			want = []string{"oa-chat-1"}
		}
		if !slices.Equal(m.ServedFrom, want) {
			t.Fatalf("%s served_from %v, want %v", m.Family, m.ServedFrom, want)
		}
	}
}
