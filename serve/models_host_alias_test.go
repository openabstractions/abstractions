package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	rwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// ggufFixtureStore holds one chat GGUF stored under its file digest, the way
// Setup and the journeys store a downloaded model.
const ggufFixtureStore = "jrn-chat"

// ggufFixtureDigest is the file's SHA-256, its file name; ggufFixtureID is
// the manifest id the inventory lists the object by.
var (
	ggufFixtureDigest = strings.Repeat("5a", 32)
	ggufFixtureID     = strings.Repeat("e7", 32)
	ggufFixtureFile   = ggufFixtureDigest + ".gguf"
)

// fixtureGGUFInventory is a source whose one present local store holds a
// chat GGUF named by its digest: the family read from the file name is the
// digest, and the manifest id is another hex string.
type fixtureGGUFInventory struct{}

func (fixtureGGUFInventory) Describe() (content.SourceDescription, error) {
	stores := []content.Store{{Name: ggufFixtureStore, Program: "fixture", Rule: "fixture", Errors: []content.StoreError{},
		Placement: content.PlacementLocal, Present: true}}
	return content.SourceDescription{Outcome: content.SourceDescriptionOutcomeDescribed, Name: "fixture-gguf", Stores: stores,
		Schemes: []string{}, Capabilities: []string{}}, nil
}

func (f fixtureGGUFInventory) Snapshot(string, int64) (content.SourcePage, error) {
	description, _ := f.Describe()
	manifest := content.Manifest{ID: ggufFixtureID, Addressing: content.AddressingName, Kind: modelDescriptor, Store: ggufFixtureStore,
		Entries: []content.Entry{{Role: "weights", Size: 4096, MediaType: "application/octet-stream", Evidence: content.EvidenceNone,
			Locator: ggufFixtureFile, Annotations: map[string]string{}}},
		Names:      []content.Name{{Scheme: ggufFixtureStore, Name: ggufFixtureFile}},
		Descriptor: map[string]string{"format": "gguf", "family": ggufFixtureDigest, "architecture": "qwen35moe", "size_label": "35B-A3B"}}
	return content.SourcePage{Outcome: content.SourcePageOutcomePage, Stores: description.Stores,
		Manifests: []content.ManifestHolders{{Manifest: manifest, Holds: []content.Hold{}}}, Objects: []content.ObjectHolders{},
		Dangling: []content.Dangling{}, Changes: []content.SourceChange{}, Complete: true, Cursor: "1"}, nil
}
func (fixtureGGUFInventory) Observe(string, int64, int64) (content.SourcePage, error) {
	return unavailableSourcePage(), nil
}
func (fixtureGGUFInventory) Verify(string) (content.SourcePage, error) {
	return unavailableSourcePage(), nil
}
func (fixtureGGUFInventory) Remove(string) (content.RemoveResult, error) {
	return content.RemoveResult{Outcome: content.RemoveOutcomeUnsupported, Holders: []content.Holder{}}, nil
}

// `models host --as` on an object the person names by its file name keeps
// the person's name through the runtime's inventory refreshes. The journeys
// of 2026-11-11 saw the runtime log "added [<digest>], dropped
// [qwen3.6-35b-a3b]" four seconds after hosting: the declaration kept the
// file name, the store lists the manifest by id, and the reconciler read
// the object as gone. The declaration now binds the id, and the hosted list
// still names the model qwen3.6-35b-a3b after the refreshes that follow.
func TestModelsHostAsKeepsThePersonsNameThroughTheRefresh(t *testing.T) {
	requireNativeModelHostRegistration(t)
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	source := copyTestBinary(t, "oa-fixture-inventory")
	options, _ := isolatedRuntime(t)
	options.isolated = fmt.Sprintf("oatest-alias-%d-%d", os.Getpid(), endpointSeq.Add(1))
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}

	name := fmt.Sprintf("gguf-source-%d", time.Now().UnixNano()%1_000_000_000)
	if out, err := runProvider(t, append([]string{"add", name, "--program", source, "--provider-endpoint", name,
		"--contract", storageInventorySource, "--store", ggufFixtureStore,
		"--arg", providerFixtureArg, "--arg", "inventory-gguf", "--arg", providerEndpointToken, "--arg", t.TempDir()}, endpoint...)...); err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	eventually(t, 60*time.Second, "the source's store being accepted", func() bool {
		return len(providerStates(t, endpoint)[name].Accepted) > 0
	})
	if granted, err := runRightsCommand(t, "grant", "--program", probeSelf().Program, "--action", ActionInventoryRead,
		"--resource", storageservice.InventoryResource, "--endpoint", options.endpoint); err != nil || granted.Outcome != "applied" {
		t.Fatalf("grant inventory read: %+v %v", granted, err)
	}

	host := recordingModelHost(t, t.TempDir())
	var out, diagnostics strings.Builder
	if err := modelsHostCommand("models host", []string{ggufFixtureStore + "/" + ggufFixtureFile, "--as", "qwen3.6-35b-a3b",
		"--engine", unpinnedEngine(t), "--program", host, "--endpoint", options.endpoint, "--timeout", "30s"}, &out, &diagnostics); err != nil {
		t.Fatalf("models host: %v\n%s%s", err, out.String(), diagnostics.String())
	}
	keepPendingProgram(t, options, host)
	hosted := func() []hostedModel {
		t.Helper()
		call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
		if err != nil {
			t.Fatal(err)
		}
		list, err := registry.Declarations(call)
		if err != nil {
			t.Fatal(err)
		}
		_, models := readHostDeclaration(list)
		return models
	}
	named := func(list []hostedModel) bool {
		return len(list) == 1 && list[0].Name == "qwen3.6-35b-a3b" && list[0].Store == ggufFixtureStore
	}
	if got := hosted(); !named(got) {
		t.Fatalf("models host declared %v", names(got))
	}
	// Each declaration change wakes a refresh, and the drop came within
	// seconds of the write; 15 s pass with the person's name in place.
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if got := hosted(); !named(got) {
			t.Fatalf("after the refresh the model host declares %v, want qwen3.6-35b-a3b alone", names(got))
		}
	}
	want := []string{hostedModel{Name: "qwen3.6-35b-a3b", Store: ggufFixtureStore, Object: ggufFixtureID}.argument()}
	if got := names(hosted()); !slices.Equal(got, want) {
		t.Fatalf("the model host declares %v, want the manifest id bound: %v", got, want)
	}
	var listing strings.Builder
	if err := modelsHostCommand("models hosts", endpoint, &listing, &diagnostics); err != nil {
		t.Fatalf("models hosts: %v\n%s", err, diagnostics.String())
	}
	if !strings.Contains(listing.String(), "qwen3.6-35b-a3b") || strings.Contains(listing.String(), ggufFixtureDigest) {
		t.Fatalf("models hosts lists:\n%s", listing.String())
	}
}

// A declaration an earlier `models host` wrote with the object's file name
// is bound to the manifest id by the reconciler, under the person's name; no
// digest-named entry takes its place.
func TestReconcileBindsAFileNamedEntryToItsManifestID(t *testing.T) {
	state := t.TempDir()
	p, err := openFixtureProviders(t, state, false, func(err error) { t.Errorf("providers: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	programs, err := modelHostRuntimePrograms()
	if err != nil {
		t.Fatal(err)
	}
	settings := hostSettings{program: program, endpoint: "modelhost-alias-test", engine: filepath.Join(state, "engine", "llama-server.exe")}
	declared := []hostedModel{{Name: "qwen3.6-35b-a3b", Store: ggufFixtureStore, Object: ggufFixtureFile}}
	f := providerFile{Version: providerFileVersion, DeclaredBy: program, DeclaredAt: 1,
		Declaration: declarationOf(modelHostDeclaration(settings, declared, programs))}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	if added := p.add(revision, f); added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("add: %+v", added)
	}
	page, _ := fixtureGGUFInventory{}.Snapshot("", 0)
	composed := storageservice.ComposedInventory{Stores: page.Stores, Manifests: page.Manifests}
	wrote, err := p.reconcileModelHost(context.Background(), composed, func(string, string) bool { return false })
	if err != nil || !strings.Contains(wrote, "rebound [qwen3.6-35b-a3b]") || strings.Contains(wrote, ggufFixtureDigest) {
		t.Fatalf("reconcile: %q %v", wrote, err)
	}
	files, _, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Declaration.Name != modelHostName {
			continue
		}
		want := []string{hostedModel{Name: "qwen3.6-35b-a3b", Store: ggufFixtureStore, Object: ggufFixtureID}.argument()}
		if got := names(hostedIn(f.Declaration)); !slices.Equal(got, want) {
			t.Fatalf("declared %v, want %v", got, want)
		}
	}
	if wrote, err := p.reconcileModelHost(context.Background(), composed, func(string, string) bool { return false }); err != nil || wrote != "" {
		t.Fatalf("the bound declaration was rewritten: %q %v", wrote, err)
	}
}

// A GGUF a store keeps under its digest is discovered under the
// architecture and size label its header states.
func TestReconcileNamesADigestNamedDiscoveryPlainly(t *testing.T) {
	page, _ := fixtureGGUFInventory{}.Snapshot("", 0)
	composed := storageservice.ComposedInventory{Stores: page.Stores, Manifests: page.Manifests}
	complete, found := discoveredModels(composed)
	got := names(reconcileHosted(nil, complete, found, nil, listedObjects(composed)))
	if want := []string{"qwen35moe-35B-A3B=" + ggufFixtureStore + "/" + ggufFixtureID}; !slices.Equal(got, want) {
		t.Fatalf("hosted %v, want %v", got, want)
	}
}

// keepPendingProgram answers Keep, as the person does, to the pending
// question about program: `models host` declares a model host program the
// person keeps before the runtime starts it or reconciles its models.
func keepPendingProgram(t *testing.T, options runtimeFlags, program string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	operator, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveAsksOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		page, err := operator.ListQuestionsContext(ctx, "", 64)
		if err != nil {
			t.Fatal(err)
		}
		seen = seen[:0]
		for _, r := range page.Records {
			seen = append(seen, fmt.Sprintf("%s %s %q %q", r.ID, r.Key, r.About, r.Option))
			if r.Option != "" || !slices.Contains(r.Options, "keep") || !strings.EqualFold(filepath.Clean(r.About), filepath.Clean(program)) {
				continue
			}
			decision, err := operator.AnswerQuestionIfPendingContext(ctx, r.ID, "keep")
			if err != nil || decision.Outcome.String() != "answered" {
				t.Fatalf("keep %s: %+v %v", r.ID, decision, err)
			}
			return
		}
	}
	t.Fatalf("no pending Keep question about %s: %v", program, seen)
}

// The router's model list names a digest-named GGUF the model host serves
// under the person's --as name once: servable, by that name, held in the
// store that holds the file. The journeys of 2026-11-12 (J-COLD-CHAT,
// J-PIC-COLD) listed the file held and unservable under its digest, and
// the model host's name servable and held nowhere, because the name and the
// file's descriptor family share no spelling. The declaration's object
// association joins them.
func TestModelsHostAsListsOneServableModelHeldInTheStore(t *testing.T) {
	requireNativeModelHostRegistration(t)
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	const alias = "my-qwen"
	source := copyTestBinary(t, "oa-fixture-inventory")
	options, _ := isolatedRuntime(t)
	options.isolated = fmt.Sprintf("oatest-join-%d-%d", os.Getpid(), endpointSeq.Add(1))
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}

	name := fmt.Sprintf("gguf-source-%d", time.Now().UnixNano()%1_000_000_000)
	if out, err := runProvider(t, append([]string{"add", name, "--program", source, "--provider-endpoint", name,
		"--contract", storageInventorySource, "--store", ggufFixtureStore,
		"--arg", providerFixtureArg, "--arg", "inventory-gguf", "--arg", providerEndpointToken, "--arg", t.TempDir()}, endpoint...)...); err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	eventually(t, 60*time.Second, "the source's store being accepted", func() bool {
		return len(providerStates(t, endpoint)[name].Accepted) > 0
	})
	if granted, err := runRightsCommand(t, "grant", "--program", probeSelf().Program, "--action", ActionInventoryRead,
		"--resource", storageservice.InventoryResource, "--endpoint", options.endpoint); err != nil || granted.Outcome != "applied" {
		t.Fatalf("grant inventory read: %+v %v", granted, err)
	}
	host := recordingModelHost(t, t.TempDir())
	var out, diagnostics strings.Builder
	if err := modelsHostCommand("models host", []string{ggufFixtureStore + "/" + ggufFixtureFile, "--as", alias,
		"--engine", unpinnedEngine(t), "--program", host, "--endpoint", options.endpoint, "--timeout", "30s"}, &out, &diagnostics); err != nil {
		t.Fatalf("models host: %v\n%s%s", err, out.String(), diagnostics.String())
	}
	keepPendingProgram(t, options, host)

	call, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	routes, err := client.New(options.endpoint).ResolveRouter(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	var listed []rwire.Family
	joined := func() bool {
		snapshot, err := routes.ModelsContext(call, true)
		if err != nil {
			t.Fatalf("router models: %v", err)
		}
		listed = snapshot.Models
		for _, f := range listed {
			for _, a := range f.Names {
				if a.Name == alias && a.Servable && slices.Contains(f.HeldIn, ggufFixtureStore) && slices.Contains(a.HeldIn, ggufFixtureStore) {
					return true
				}
			}
		}
		return false
	}
	for deadline := time.Now().Add(90 * time.Second); !joined(); time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("no servable %s held in %s: %+v", alias, ggufFixtureStore, listed)
		}
	}
	entries := 0
	for _, f := range listed {
		if slices.Contains(f.HeldIn, ggufFixtureStore) || slices.ContainsFunc(f.Names, func(a rwire.Alias) bool { return a.Name == alias }) {
			entries++
		}
	}
	if entries != 1 {
		t.Fatalf("the hosted file is listed %d times: %+v", entries, listed)
	}
}
