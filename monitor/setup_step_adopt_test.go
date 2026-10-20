package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// inventoryFake is the storage inventory's Find, List and Holders.
type inventoryFake struct {
	found, listed content.InventoryPage
	asked, held   []string
}

func (f *inventoryFake) Find(_ context.Context, digest string) (content.InventoryPage, error) {
	f.asked = append(f.asked, digest)
	return f.found, nil
}
func (f *inventoryFake) List(context.Context, string, int64) (content.InventoryPage, error) {
	return f.listed, nil
}
func (f *inventoryFake) Holders(_ context.Context, target string) (content.InventoryPage, error) {
	f.held = append(f.held, target)
	return content.InventoryPage{Outcome: content.InventoryOutcomePage, Complete: true}, nil
}

func modelPage(digest string, size int64) content.InventoryPage {
	return content.InventoryPage{Outcome: content.InventoryOutcomePage, Complete: true, Manifests: []content.ManifestHolders{{Manifest: content.Manifest{
		Store: "hub", ID: "object-1", Kind: "abstraction.model/descriptor@1",
		Entries: []content.Entry{{Role: "weights", Digest: digest, Size: size, Locator: "models--acme--coder/snapshots/r1/coder.gguf"}}}}}}
}

var defaultTrustFile = stepTrustFile

var hubRoot = filepath.Join(os.TempDir(), "hub")

// The step asks the storage inventory for the pinned digest, then for a
// same-size file still being hashed; an inventory it cannot read is an
// error, which leaves the download.
func TestSetupStepFindsThePinnedFileThroughTheInventory(t *testing.T) {
	ctx := context.Background()
	plan := setup.CodingModelPlan()
	pin := plan.Assets[0].Download
	trusted := ""
	stepTrustFile = func(path string) error { trusted = path; return nil }
	t.Cleanup(func() { stepTrustFile = defaultTrustFile })
	stores := content.InventoryPage{Outcome: content.InventoryOutcomePage, Stores: []content.Store{{Name: "hub", Locator: hubRoot}}}
	found := &inventoryFake{found: modelPage("sha256:"+pin.SHA256, pin.Bytes), listed: stores}
	lookup, err := stepFindPinned(ctx, plan, found)
	if err != nil || lookup.Local == nil || *lookup.Local != (setup.StepLocal{Store: "hub", Object: "object-1"}) || len(found.asked) != 1 || found.asked[0] != "sha256:"+pin.SHA256 {
		t.Fatalf("found %+v, %v asked %v", lookup, err, found.asked)
	}
	if trusted != filepath.Join(hubRoot, "models--acme--coder", "snapshots", "r1", "coder.gguf") {
		t.Fatalf("checked %q", trusted)
	}
	// Coordinator decision: a copy other accounts can change is not adopted.
	stepTrustFile = func(string) error { return errors.New("S-1-5-11 may change it") }
	if lookup, err := stepFindPinned(ctx, plan, found); err != nil || lookup.Local != nil || !lookup.Untrusted {
		t.Fatalf("untrusted copy = %+v, %v", lookup, err)
	}
	stepTrustFile = func(string) error { return nil }
	pending := &inventoryFake{found: content.InventoryPage{Outcome: content.InventoryOutcomePage, Complete: true}, listed: modelPage("", pin.Bytes)}
	if lookup, err := stepFindPinned(ctx, plan, pending); err != nil || lookup.Local != nil || !lookup.Hashing || len(pending.held) != 1 || pending.held[0] != "object-1" {
		t.Fatalf("pending candidate = %+v, %v; asked to hash %v", lookup, err, pending.held)
	}
	other := &inventoryFake{found: content.InventoryPage{Outcome: content.InventoryOutcomePage, Complete: true}, listed: modelPage("", pin.Bytes-1)}
	if lookup, err := stepFindPinned(ctx, plan, other); err != nil || lookup.Local != nil || lookup.Hashing || len(other.held) != 0 {
		t.Fatalf("another size = %+v, %v; asked to hash %v", lookup, err, other.held)
	}
	refused := &inventoryFake{found: content.InventoryPage{Outcome: content.InventoryOutcomeForbidden}}
	if lookup, err := stepFindPinned(ctx, plan, refused); lookup.Local != nil || err == nil {
		t.Fatalf("a refused inventory gave %+v, %v", lookup, err)
	}
}

// Setup selects the object; ModelHost applies verified per-model defaults.
// A mixed-model declaration receives no global catalogue optimization flags.
func TestSetupStepHostsTheCodingModelWithProviderDefaults(t *testing.T) {
	plan := setup.CodingModelPlan()
	want := []string{"models", "host", "hub/object-1", "--engine", "engine.exe", "--program", "modelhostd.exe",
		"--json", "--timeout", "30s"}
	if got := modelHostHostArgs("hub", "object-1", "engine.exe", "modelhostd.exe", plan.EngineArgs, ""); !slices.Equal(got, want) {
		t.Fatalf("host command %q, want %q", got, want)
	}
	if again := setup.CodingModelPlan(); again.ID != plan.ID || len(again.EngineArgs) != 0 {
		t.Fatalf("plan identity or arguments moved: %s %v", again.ID, again.EngineArgs)
	}
	// A model the Setup page hosts by hand keeps ModelHost's defaults.
	if got := modelHostHostArgs("hub", "o", "e", "p", nil, ""); slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, "--engine-arg") || a == "--as" }) {
		t.Fatalf("page host command %q", got)
	}
}

// The step hosts the coding model under the catalogue's family, never under
// the name its file has in the store: Setup stages its download as
// <sha256>.gguf, and the router lists a hosted model by the name it is
// hosted under, so OpenCode showed the digest (owner, 2026-11-01). The
// provider applies its catalogue context and MTP only after current proof.
func TestSetupStepHostsTheCodingModelUnderTheCatalogueFamily(t *testing.T) {
	plan := setup.CodingModelPlan()
	hosting := setup.StepPick{Family: "qwen3.6-35b-a3b", Context: 32768}.Hosting()
	got := modelHostHostArgs("oa-chat-1", "object-1", "engine.exe", "modelhostd.exe", plan.EngineArgs, hosting.Name)
	want := []string{"models", "host", "oa-chat-1/object-1", "--engine", "engine.exe", "--program", "modelhostd.exe", "--as", "qwen3.6-35b-a3b",
		"--json", "--timeout", "30s"}
	if !slices.Equal(got, want) {
		t.Fatalf("host command %q, want %q", got, want)
	}
	if len(plan.EngineArgs) != 0 || hosting.Context != 32768 {
		t.Fatalf("the plan's own arguments changed: %v", plan.EngineArgs)
	}
	for _, arg := range got {
		if digestLabel.MatchString(arg) {
			t.Fatalf("a digest reached the hosted name: %q", got)
		}
	}
}

// digestLabel matches a 64-hex SHA-256, the shape that must never name a
// model to a person.
var digestLabel = regexp.MustCompile(`(?i)[0-9a-f]{64}`)
