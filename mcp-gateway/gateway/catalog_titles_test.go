package gateway

import (
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	"testing"
)

func TestModelsCatalogueTitlePreservesIdentifiers(t *testing.T) {
	for _, title := range []string{"Friendly / model", ""} {
		models := modelsFrom([]routerwire.Family{{Family: "key", Title: title, Names: []routerwire.Alias{{Name: "exact-alias", Servable: true}}}})
		want := "key"
		if title != "" {
			want = title
		}
		if len(models) != 1 || models[0].Name != want || models[0].Family != "key" || models[0].Aliases[0] != "exact-alias" || !models[0].Servable {
			t.Fatalf("title %q: %+v", title, models)
		}
	}
}
