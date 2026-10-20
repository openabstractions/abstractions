package main

import (
	rclient "github.com/openabstractions/abstraction-router/go/client"
	"testing"
)

func TestLibraryCatalogueTitlePreservesActions(t *testing.T) {
	for _, title := range []string{"Friendly / model", ""} {
		catalog := rclient.ModelsSnapshot{Models: []rclient.Family{{Family: "key", Title: title, HeldIn: []string{"files"}, Names: []rclient.Alias{{Host: "engine", Name: "exact-alias", Servable: true, Resident: true}}}}}
		models, running := projectLibrary(catalog, libraryFiles{})
		want := "key"
		if title != "" {
			want = title
		}
		if len(models) != 1 || models[0].Name != want || models[0].Key != "key" || models[0].Names[0] != "exact-alias" || !models[0].Ready || len(running) != 1 || running[0].Name != want {
			t.Fatalf("title %q: %+v %+v", title, models, running)
		}
	}
}
