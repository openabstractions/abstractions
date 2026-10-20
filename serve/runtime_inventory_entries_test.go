package main

import (
	"testing"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

func TestHeldObjectsPreservesEveryManifestEntry(t *testing.T) {
	manifest := content.Manifest{ID: "recipe", Kind: modelDescriptor, Store: "source",
		Names: []content.Name{{Name: "picture"}}, Descriptor: map[string]string{"family": "picture"},
		Entries: []content.Entry{
			{Role: "weights", Digest: "sha256:main", Evidence: content.EvidenceNamed},
			{Role: "text_encoder", Digest: "sha256:encoder", Evidence: content.EvidenceForeign},
			{Role: "vae", Evidence: content.EvidenceNone},
		}}
	got := heldObjects(storageservice.ComposedInventory{Manifests: []content.ManifestHolders{{Manifest: manifest}}})
	if len(got) != 3 {
		t.Fatalf("kept %d entries, want all three", len(got))
	}
	for index, entry := range got {
		if entry.Manifest != "recipe" || entry.Entry != index || entry.EntryRole != manifest.Entries[index].Role || entry.Object != "" || entry.Evidence != manifest.Entries[index].Evidence {
			t.Fatalf("entry %d became a whole-manifest object or lost its role: %+v", index, entry)
		}
	}
	if got[0].Digest != "sha256:main" || got[1].Digest != "sha256:encoder" || got[2].Digest != "" {
		t.Fatalf("entry evidence collapsed or invented: %+v", got)
	}
}
