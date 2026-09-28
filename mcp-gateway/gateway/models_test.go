package gateway

import (
	"slices"
	"testing"

	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
)

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
