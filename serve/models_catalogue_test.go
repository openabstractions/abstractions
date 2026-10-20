package main

import (
	"context"
	"reflect"
	"slices"
	"testing"

	modelwire "github.com/openabstractions/abstraction-model/go/abstraction/model/api"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

func catalogueFixture(t *testing.T) (modelwire.FitEntry, storageservice.ComposedInventory, []hostedModel) {
	t.Helper()
	entries := codeCatalogue(context.Background())
	if len(entries) == 0 {
		t.Fatal("no curated code model")
	}
	entry := entries[0]
	composed := storageservice.ComposedInventory{Stores: []content.Store{{Name: "oa-copy", Placement: content.PlacementLocal, Present: true}}, Manifests: []content.ManifestHolders{{Manifest: content.Manifest{Store: "oa-copy", ID: "opaque-object", Kind: modelDescriptor, Descriptor: map[string]string{"family": "legacy-hash", "format": "gguf", "architecture": "qwen"}, Names: []content.Name{{Scheme: "oa-copy", Name: "legacy-hash.gguf"}}, Entries: []content.Entry{{Role: "weights", Size: entry.DownloadBytes, Evidence: content.EvidenceNone}}}}}}
	return entry, composed, []hostedModel{{Name: "legacy-alias", Store: "oa-copy", Object: "opaque-object"}}
}

func TestCatalogueProjectionRequiresHashedProofAndPreservesExactAlias(t *testing.T) {
	entry, composed, hosted := catalogueFixture(t)
	entries := []modelwire.FitEntry{entry}
	before := composed.Manifests[0].Manifest
	for _, evidence := range []content.Evidence{content.EvidenceNone, content.EvidenceNamed, content.EvidenceForeign} {
		composed.Manifests[0].Manifest.Entries[0].Evidence = evidence
		composed.Manifests[0].Manifest.Entries[0].Digest = entry.Digest
		got := catalogueInventory(composed, hosted, entries)
		if got.Manifests[0].Manifest.Descriptor["family"] != "legacy-hash" {
			t.Fatalf("unverified %s became identity", evidence)
		}
	}
	composed.Manifests[0].Manifest.Entries[0].Evidence = content.EvidenceHashed
	got := catalogueInventory(composed, hosted, entries)
	m := got.Manifests[0].Manifest
	if hosted[0].Content != "" || slices.Contains(hostArguments(hostSettings{}, hosted, nil), argModelContent) {
		t.Fatal("Hashed inventory evidence created an explicit allowlist pin")
	}
	if m.Descriptor["family"] != entry.Family || m.ID != before.ID || m.Store != before.Store || len(m.Names) != 2 || m.Names[1].Name != "legacy-alias" {
		t.Fatalf("verified projection %+v", m)
	}
	if composed.Manifests[0].Manifest.Descriptor["family"] != "legacy-hash" || len(composed.Manifests[0].Manifest.Names) != 1 {
		t.Fatal("projection mutated source records")
	}
	wrong := catalogueInventory(composed, []hostedModel{{Name: "unrelated", Store: "oa-copy", Object: "other-object"}}, entries)
	if len(wrong.Manifests[0].Manifest.Names) != 1 {
		t.Fatal("opaque object mismatch created an alias")
	}
	composed.Manifests[0].Manifest.Entries[0].Size--
	if _, ok := catalogueModel(composed.Manifests[0].Manifest, entries); ok {
		t.Fatal("wrong size accepted")
	}
	composed.Manifests[0].Manifest.Entries[0].Size++
	composed.Manifests[0].Manifest.Entries = append(composed.Manifests[0].Manifest.Entries, composed.Manifests[0].Manifest.Entries[0])
	if _, ok := catalogueModel(composed.Manifests[0].Manifest, entries); ok {
		t.Fatal("multi-entry manifest became one catalogue model")
	}
}

type catalogueVerifySource struct{ asked []string }

func (*catalogueVerifySource) Snapshot(string, int64) (content.SourcePage, error) {
	return content.SourcePage{}, nil
}
func (s *catalogueVerifySource) Verify(id string) (content.SourcePage, error) {
	s.asked = append(s.asked, id)
	return content.SourcePage{Outcome: content.SourcePageOutcomePage}, nil
}

func TestCatalogueVerificationQueuesOnlyHostedCandidateAndWaitsForLaterProof(t *testing.T) {
	entry, composed, hosted := catalogueFixture(t)
	entries := []modelwire.FitEntry{entry}
	targets := catalogueVerifyTargets(composed, hosted, entries)
	if !reflect.DeepEqual(targets, []catalogueVerifyTarget{{store: "oa-copy", object: "opaque-object"}}) {
		t.Fatalf("targets %+v", targets)
	}
	foreign, accepted := &catalogueVerifySource{}, &catalogueVerifySource{}
	sources := []storageservice.DesignatedSource{{Stores: []string{"other-store"}, Reader: foreign}, {Stores: []string{"oa-copy"}, Reader: accepted}}
	if err := verifyCatalogueModels(context.Background(), sources, targets); err != nil {
		t.Fatal(err)
	}
	if len(foreign.asked) != 0 || !reflect.DeepEqual(accepted.asked, []string{"opaque-object"}) {
		t.Fatalf("accepted source fence %+v %+v", foreign, accepted)
	}
	if got := catalogueInventory(composed, hosted, entries); got.Manifests[0].Manifest.Descriptor["family"] != "legacy-hash" {
		t.Fatal("queueing work changed availability or identity")
	}
	composed.Manifests[0].Manifest.Entries[0].Digest = entry.Digest
	composed.Manifests[0].Manifest.Entries[0].Evidence = content.EvidenceHashed
	if len(catalogueVerifyTargets(composed, hosted, entries)) != 0 {
		t.Fatal("hashed object queued again")
	}
	if got := catalogueInventory(composed, hosted, entries); got.Manifests[0].Manifest.Descriptor["family"] != entry.Family {
		t.Fatal("later live proof did not normalize")
	}
	composed.Manifests[0].Manifest.Entries[0].Evidence = content.EvidenceNamed
	if len(catalogueVerifyTargets(composed, hosted, entries)) != 0 {
		t.Fatal("Named evidence cannot be upgraded by the existing Verify owner")
	}
}

func TestCatalogueCopyPreferenceKeepsAliasesAndDifferentOrUnverifiedBytes(t *testing.T) {
	entry, composed, _ := catalogueFixture(t)
	composed.Manifests[0].Manifest.Entries[0].Digest = entry.Digest
	composed.Manifests[0].Manifest.Entries[0].Evidence = content.EvidenceHashed
	other := composed.Manifests[0]
	other.Manifest.Store, other.Manifest.ID = "hf-copy", "hf-object"
	other.Manifest.Entries = append([]content.Entry(nil), other.Manifest.Entries...)
	composed.Stores = append(composed.Stores, content.Store{Name: "hf-copy", Placement: content.PlacementLocal, Present: true})
	composed.Manifests = append(composed.Manifests, other)
	current := []hostedModel{{Name: "legacy-alias", Store: "oa-copy", Object: "opaque-object"}, {Name: entry.Family, Store: "hf-copy", Object: "hf-object"}}
	projected := catalogueInventory(composed, current, []modelwire.FitEntry{entry})
	complete, found := discoveredModels(projected)
	got := reconcileHosted(current, complete, found, nil, listedObjects(projected))
	if len(got) != 2 {
		t.Fatalf("existing aliases lost: %+v", got)
	}
	for _, model := range got {
		if model.Store != "oa-copy" || model.Object != "opaque-object" {
			t.Fatalf("verified OA copy not preferred: %+v", got)
		}
	}
	if current[1].Store != "hf-copy" || composed.Manifests[1].Manifest.Store != "hf-copy" {
		t.Fatal("source inventory or current records were mutated")
	}
	for _, evidence := range []content.Evidence{content.EvidenceNamed, content.EvidenceHashed} {
		composed.Manifests[1].Manifest.Entries[0].Evidence = evidence
		if evidence == content.EvidenceHashed {
			composed.Manifests[1].Manifest.Entries[0].Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}
		projected = catalogueInventory(composed, current, []modelwire.FitEntry{entry})
		complete, found = discoveredModels(projected)
		got = reconcileHosted(current, complete, found, nil, listedObjects(projected))
		if !reflect.DeepEqual(got, current) {
			t.Fatalf("%s other bytes were collapsed: %+v", evidence, got)
		}
	}
}
