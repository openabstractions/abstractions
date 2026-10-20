package main

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
)

// Both local-stores declarations name every store an adapter can produce;
// the runtime drops an undeclared store's models.
func TestLocalStoresDeclarationsNameEveryAdapterStore(t *testing.T) {
	for _, path := range []string{
		privateInput(t, "..", "installer", "declarations", "local-stores.json"),
		privateInput(t, "..", "installer", "posix", "declarations", "local-stores.json"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Declaration struct {
				Resources []string `json:"resources"`
			} `json:"declaration"`
			Providers []struct {
				Resources []string `json:"resources"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		resources := f.Declaration.Resources
		if len(f.Providers) == 1 {
			resources = f.Providers[0].Resources
		}
		for _, store := range snapshot.Stores {
			if !slices.Contains(resources, "store:"+store) {
				t.Errorf("%v does not declare store:%s", path, store)
			}
		}
		for _, resource := range resources {
			if !validResource(resource) {
				t.Errorf("%v declares %s, which the declaration rules refuse", path, resource)
			}
		}
	}
}
