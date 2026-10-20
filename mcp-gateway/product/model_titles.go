package product

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed model_titles.json
var modelTitleJSON []byte

type modelTitle struct {
	Provider  string   `json:"provider"`
	Title     string   `json:"title"`
	Engines   []string `json:"engines"`
	Resources []string `json:"resources"`
}

// ModelTitles returns fresh maps of display names for known model engines and
// resources. The audiences have separate alias sets. These names describe
// presentation only.
func ModelTitles() (engines, resources map[string]string) {
	engines = EngineTitles()
	for name, title := range enginePresentation {
		if alias, ok := strings.CutSuffix(name, "-image"); ok && engines[alias] == "" {
			engines[alias] = title.title
		}
	}
	var names []modelTitle
	if err := json.Unmarshal(modelTitleJSON, &names); err != nil {
		panic(err)
	}
	resources = make(map[string]string)
	for _, name := range names {
		title := name.Title
		if name.Provider != "" {
			var ok bool
			title, ok = EngineTitle(name.Provider)
			if !ok {
				panic("unknown model title provider: " + name.Provider)
			}
		}
		for _, id := range name.Engines {
			engines[id] = title
		}
		for _, id := range name.Resources {
			resources[id] = title
		}
	}
	return engines, resources
}
