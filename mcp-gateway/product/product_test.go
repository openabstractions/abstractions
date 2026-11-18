package product

import (
	"slices"
	"testing"
)

func TestPictureEnginesPreferDiffusersThenLemonadeThenTheNoise(t *testing.T) {
	want := []string{"pytorch-image", "pytorch", "lemonade-image", "thenoise-image"}
	if got := PictureEngines(); !slices.Equal(got, want) {
		t.Fatalf("PictureEngines() = %q, want %q", got, want)
	}
	PictureEngines()[0] = "changed"
	if PictureEngines()[0] != Diffusers {
		t.Fatal("PictureEngines returned the package's own slice")
	}
}

func TestPictureSetupRecipesPreserveReviewAndDefaultOrder(t *testing.T) {
	want := []PictureSetupRecipe{
		{Engine: "lemonade", DefaultRank: 2, InstalledFallback: true},
		{Engine: "pytorch", DefaultRank: 1, Standalone: true},
		{Engine: "thenoise", Standalone: true},
	}
	got := PictureSetupRecipes()
	if !slices.Equal(got, want) {
		t.Fatalf("Setup recipes = %+v, want %+v", got, want)
	}
	got[0] = PictureSetupRecipe{}
	if !slices.Equal(PictureSetupRecipes(), want) {
		t.Fatal("Setup recipe list shares the product's backing array")
	}
}

func TestEngineTitlesNameTheUpstreamProjects(t *testing.T) {
	want := map[string]string{"modelhost": "llama.cpp", "pytorch-image": "Diffusers", "pytorch": "Diffusers", "lemonade-image": "Lemonade", "thenoise-image": "TheNoise"}
	for provider, title := range want {
		if got, ok := EngineTitle(provider); !ok || got != title {
			t.Errorf("EngineTitle(%q) = %q, %v; want %q", provider, got, ok, title)
		}
	}
	if got, ok := EngineTitle("my-provider"); ok || got != "" {
		t.Errorf("EngineTitle(my-provider) = %q, %v", got, ok)
	}
	titles := EngineTitles()
	if len(titles) != len(want) {
		t.Fatalf("EngineTitles() = %v", titles)
	}
	titles[ModelHost] = "changed"
	if got, _ := EngineTitle(ModelHost); got != "llama.cpp" {
		t.Fatal("EngineTitles returned the package's own map")
	}
}

func TestModelTitlesKeepsSeparatePresentationAudiences(t *testing.T) {
	engines, resources := ModelTitles()
	if engines["pytorchd"] != "Diffusers" || engines["hf"] != "" || resources["hf"] != "Hugging Face" ||
		resources["modelhost"] != "llama.cpp" || resources["lm-studio"] != "" {
		t.Fatalf("unexpected model titles: engines %v, resources %v", engines, resources)
	}
	engines["pytorchd"], resources["hf"] = "changed", "changed"
	againEngines, againResources := ModelTitles()
	if againEngines["pytorchd"] != "Diffusers" || againResources["hf"] != "Hugging Face" {
		t.Fatal("ModelTitles returned shared maps")
	}
}

func TestEnginePresentationUsesSpecificCaptionsOnlyWhereDeclared(t *testing.T) {
	for provider, want := range map[string]string{
		ModelHost:           "Runs chat models on this device",
		Diffusers:           "Pictures and video on AMD graphics",
		DiffusersModelsHost: "",
		Lemonade:            "Pictures, speech and transcription",
		TheNoise:            "Pictures on the processor",
	} {
		title, caption, ok := EnginePresentation(provider)
		expectedTitle, _ := EngineTitle(provider)
		if !ok || title != expectedTitle || caption != want {
			t.Errorf("EnginePresentation(%q) = %q, %q, %v; want %q, %q, true", provider, title, caption, ok, expectedTitle, want)
		}
	}
	if title, caption, ok := EnginePresentation("my-provider"); title != "" || caption != "" || ok {
		t.Errorf("unknown provider got %q, %q, %v", title, caption, ok)
	}
}

func TestEngineRankListsChatThenPicturesByPreference(t *testing.T) {
	order := []string{ModelHost, Diffusers, Lemonade, TheNoise, "my-provider"}
	for i := 1; i < len(order); i++ {
		if EngineRank(order[i-1]) >= EngineRank(order[i]) {
			t.Fatalf("EngineRank(%q) = %d is not before EngineRank(%q) = %d", order[i-1], EngineRank(order[i-1]), order[i], EngineRank(order[i]))
		}
	}
	if EngineRank("my-provider") != EngineRank("another") {
		t.Fatal("unknown providers rank alike")
	}
}

func TestInstalledAgentConnectionKeepsVendorProtocolIdentity(t *testing.T) {
	party := InstalledAgentConnectionParty()
	read, connect, prepare := InstalledAgentConnectionCLI()
	if party != "opencode" || AgentConnectionTitle(party) != "OpenCode" ||
		read != "--read-opencode-connection" || connect != "connect-opencode" || prepare != "prepare-opencode-integration" {
		t.Fatalf("installed connection protocol changed: party %q, flags %q, %q, %q", party, read, connect, prepare)
	}
}
