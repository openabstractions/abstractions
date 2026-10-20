package product

import (
	_ "embed"
	"encoding/json"
)

// The archive rows carry the release's reviewed portable package and the
// historical CPU archive. Setup tests compare them with the provider pins
// and install history. Neither provider selection nor the review is mutable.
//
//go:embed setup_engine_packages.json
var setupEnginePackageJSON []byte

var setupEnginePackages = func() map[string]json.RawMessage {
	var packages map[string]json.RawMessage
	if err := json.Unmarshal(setupEnginePackageJSON, &packages); err != nil {
		panic(err)
	}
	return packages
}()

type setupEngine struct {
	Component        string
	ChoiceTitle      string
	ComponentTitle   string
	ModelRequirement string
	Package          string
}

var setupEngines = map[string]setupEngine{
	Lemonade: {
		Component:        "lemonade-engine",
		ChoiceTitle:      "Lemonade (OA build)",
		ComponentTitle:   "Lemonade (OA build)",
		ModelRequirement: "Download and verify an image model, then configure Lemonade for local pictures",
	},
	TheNoise: {
		Component:        "thenoise-engine",
		ChoiceTitle:      "Run TheNoise pictures locally (CPU)",
		ComponentTitle:   "TheNoise local image engine (processor)",
		ModelRequirement: "Review the separate picture model and configure TheNoise for CPU pictures",
	},
	Diffusers: {
		Component:        "pytorch-engine",
		ChoiceTitle:      "Run PyTorch pictures locally (Strix Halo)",
		ComponentTitle:   "PyTorch 2.11.0 with ROCm 7.13.0 and CPython 3.12.10 (Windows x64, gfx1151)",
		ModelRequirement: "Review the separate picture model and configure PyTorch on Strix Halo",
	},
}

// SetupEngine returns the compiled captions and release package declaration
// for the Setup choice associated with a known provider. The captions include
// historical plan identity. Setup validates package and model compatibility.
func SetupEngine(provider string) (spec setupEngine) {
	spec = setupEngines[provider]
	spec.Package = string(setupEnginePackages[provider])
	return spec
}
