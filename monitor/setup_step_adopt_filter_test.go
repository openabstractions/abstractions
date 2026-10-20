package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-identity/go/privatedir/privatetest"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-model/go/releasefiles"
	storage "github.com/openabstractions/abstraction-storage/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageclient "github.com/openabstractions/abstraction-storage/go/client"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// hashedHubSource is inventoryd's Hugging Face store holding the coding
// model as a plain file in a snapshot folder (no blobs/, so the store names
// no digest) after LS-13 hashed it on request: its one weights entry carries
// the recorded SHA-256 with hashed evidence.
type hashedHubSource struct {
	root, repository, locator string
	digest                    string
	size                      int64
}

func (s hashedHubSource) page() content.SourcePage {
	manifest := content.Manifest{ID: "hf-qwen3.6-coding", Addressing: content.AddressingName, Kind: "abstraction.model/descriptor@1",
		Store: "huggingface", Names: []content.Name{{Scheme: "hf", Name: s.repository}},
		Entries: []content.Entry{{Role: "weights", Digest: s.digest, Size: s.size, MediaType: "application/octet-stream",
			Evidence: content.EvidenceHashed, Locator: s.locator}}}
	return content.SourcePage{Outcome: content.SourcePageOutcomePage, Complete: true,
		Stores: []content.Store{{Name: "huggingface", Program: "huggingface", Locator: s.root, Rule: "default location ~/.cache/huggingface/hub",
			Present: true, Errors: []content.StoreError{}, Placement: content.PlacementLocal}},
		Manifests: []content.ManifestHolders{{Manifest: manifest, Holds: []content.Hold{}}},
		Objects:   []content.ObjectHolders{}, Dangling: []content.Dangling{}, Changes: []content.SourceChange{}}
}

func (s hashedHubSource) Snapshot(string, int64) (content.SourcePage, error) { return s.page(), nil }
func (s hashedHubSource) Verify(string) (content.SourcePage, error)          { return s.page(), nil }

// serveInventory runs the storage service's composed inventory@1 over source
// on a private endpoint, with read deciding each record's digest, and
// returns the client Setup resolves.
func serveInventory(t *testing.T, source storageservice.InventorySourceReader, read storageservice.Policy) *storageclient.Inventory {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation")
	}
	dir := privatetest.Dir(t)
	endpoint := fixtureEndpoint(t, "setup-inventory")
	host, err := storageservice.Listen(endpoint, storage.NewForeignStore("fixture", dir, ""), read)
	if err != nil {
		t.Fatal(err)
	}
	gate := func(context.Context, *identity.Peer, string) error { return nil }
	sources := func(context.Context) []storageservice.DesignatedSource {
		return []storageservice.DesignatedSource{{Program: "inventoryd", Stores: []string{"huggingface"}, Reader: source}}
	}
	if err := host.EnableInventory(gate, sources); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		//unchecked: the test is ending; Serve's own result is checked below
		host.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the storage service did not drain")
		}
	})
	return storageclient.NewInventory(endpoint)
}

// readDigests is the read decision installation leaves the Panel with:
// content.read on exactly these digests (serve's
// TestInstallationLetsOperatorsReadExactlyTheCatalogDigests).
func readDigests(digests []string) storageservice.Policy {
	return func(_ context.Context, _ *identity.Peer, digest string) error {
		if slices.Contains(digests, digest) {
			return nil
		}
		return errors.New("content.read not granted")
	}
}

// Setup finds the owner's hashed plain-file copy of the coding model through
// the storage inventory's own read filter. The inventory hides a record that
// carries a digest from a reader without content.read on it; with the
// catalog digests installation grants, Find answers the record and Setup
// adopts it before it asks the person. With no such rule (preview-1031l) the
// record is hidden from Find and List alike and Setup downloads.
func TestSetupFindsAHashedHubCopyThroughTheReadFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan := setup.CodingModelPlan()
	pin := plan.Assets[0].Download
	if !slices.Contains(releasefiles.ModelDigests(), pin.SHA256) {
		t.Fatalf("the plan's pin %s is not among the catalog digests", pin.SHA256)
	}
	root := filepath.Join(privatetest.Dir(t), "hub")
	folder := "models--" + strings.ReplaceAll(plan.Repository, "/", "--")
	source := hashedHubSource{root: root, repository: plan.Repository, digest: "sha256:" + pin.SHA256, size: pin.Bytes,
		locator: folder + "/snapshots/" + plan.Revision + "/" + plan.Assets[0].Path}
	trusted := ""
	stepTrustFile = func(path string) error { trusted = path; return nil }
	t.Cleanup(func() { stepTrustFile = defaultTrustFile })

	var catalog []string
	for _, d := range releasefiles.ModelDigests() {
		catalog = append(catalog, "sha256:"+d)
	}
	lookup, err := stepFindPinned(ctx, plan, serveInventory(t, source, readDigests(catalog)))
	if err != nil || lookup.Local == nil || *lookup.Local != (setup.StepLocal{Store: "huggingface", Object: "hf-qwen3.6-coding"}) {
		t.Fatalf("lookup through the read filter = %+v, %v", lookup, err)
	}
	if want := filepath.Join(root, folder, "snapshots", plan.Revision, plan.Assets[0].Path); trusted != want {
		t.Fatalf("checked %q, want %q", trusted, want)
	}

	hidden, err := stepFindPinned(ctx, plan, serveInventory(t, source, readDigests(nil)))
	if err != nil || hidden.Local != nil || hidden.Hashing {
		t.Fatalf("without the catalog rule the copy should be hidden: %+v, %v", hidden, err)
	}
}
