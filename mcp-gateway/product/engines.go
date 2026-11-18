// Package product holds the facts serve and the Panel must state alike: the
// provider names of the engines OpenAbstractions ships or adopts and the
// order those engines are asked and listed in (architect ruling 2026-11-09,
// Panel polish ticket). Each provider module that also names its own
// provider is pinned equal by a test beside its consumer.
package product

import "slices"

// The engine providers' names: each one's declaration, router host and
// provider identity. A person reads each by its upstream project
// (llama.cpp, Diffusers, Lemonade, TheNoise); these names are plumbing.
const (
	// ModelHost is llama.cpp's model host (modelhostd): chat and
	// embeddings.
	ModelHost = "modelhost"
	// Diffusers is the Diffusers provider (pytorchd) as Setup declares it.
	Diffusers = "pytorch-image"
	// DiffusersModelsHost is the same program as serve's models host
	// declares it (pytorch CONTRACT.md PYTORCH-D1).
	DiffusersModelsHost = "pytorch"
	// Lemonade is the core's Lemonade provider (lemonaded): pictures,
	// speech and transcription.
	Lemonade = "lemonade-image"
	// TheNoise is TheNoise's provider: pictures on the processor.
	TheNoise = "thenoise-image"
)

// enginePresentation gives the upstream name and any specific short caption
// the product uses when showing a shipped provider.
var enginePresentation = map[string]struct{ title, caption string }{
	ModelHost:           {"llama.cpp", "Runs chat models on this device"},
	Diffusers:           {"Diffusers", "Pictures and video on AMD graphics"},
	DiffusersModelsHost: {title: "Diffusers"},
	Lemonade:            {"Lemonade", "Pictures, speech and transcription"},
	TheNoise:            {"TheNoise", "Pictures on the processor"},
}

// EnginePresentation returns the upstream title and optional specific caption.
// The caller determines whether the provider is a shipped declaration.
func EnginePresentation(provider string) (title, caption string, ok bool) {
	presentation, ok := enginePresentation[provider]
	return presentation.title, presentation.caption, ok
}

// EngineTitle is the upstream project a person reads an engine provider by.
// It is false for any provider OpenAbstractions does not ship as an engine.
func EngineTitle(provider string) (string, bool) {
	title, _, ok := EnginePresentation(provider)
	return title, ok
}

// EngineTitles is every engine provider's title, keyed by provider name.
func EngineTitles() map[string]string {
	titles := make(map[string]string, len(enginePresentation))
	for provider, presentation := range enginePresentation {
		titles[provider] = presentation.title
	}
	return titles
}

// pictureEngines is the order image@1 asks the picture engines for a model
// more than one of them holds: Diffusers, then Lemonade, then TheNoise. A
// provider is asked only for a model its declared set holds.
var pictureEngines = []string{Diffusers, DiffusersModelsHost, Lemonade, TheNoise}

// PictureEngines is the picture engines' providers in preference order.
func PictureEngines() []string { return slices.Clone(pictureEngines) }

// EngineRank is a provider's place among the engines as Setup lists them:
// llama.cpp for chat first, then the picture engines in preference order.
// Any other provider ranks after them all.
func EngineRank(provider string) int {
	if provider == ModelHost {
		return 0
	}
	if i := slices.Index(pictureEngines, provider); i >= 0 {
		return 1 + i
	}
	return 1 + len(pictureEngines)
}

// PictureSetupRecipe is the passive Setup policy for an engine's catalogue
// ID. It carries no controller, installer or model approval.
type PictureSetupRecipe struct {
	Engine            string
	DefaultRank       int
	Standalone        bool
	InstalledFallback bool
}

// Setup's reviewed model and archive roster keeps its own order. Account
// defaults use DefaultRank. Image requests retain the routing order above.
var pictureSetupRecipes = []PictureSetupRecipe{
	{Engine: "lemonade", DefaultRank: 2, InstalledFallback: true},
	{Engine: DiffusersModelsHost, DefaultRank: 1, Standalone: true},
	{Engine: "thenoise", Standalone: true},
}

// PictureSetupRecipes returns the reviewed Setup roster in lookup order.
func PictureSetupRecipes() []PictureSetupRecipe { return slices.Clone(pictureSetupRecipes) }
