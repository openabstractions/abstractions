package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
	rclient "github.com/openabstractions/abstraction-router/go/client"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// libraryInventory is the storage inventory's List for the Models page:
// one page, a refusal, or a read that runs until its caller gives up.
type libraryInventory struct {
	page  content.InventoryPage
	err   error
	block bool
}

func (f *libraryInventory) Find(context.Context, string) (content.InventoryPage, error) {
	return content.InventoryPage{}, errors.New("not used")
}
func (f *libraryInventory) List(ctx context.Context, _ string, _ int64) (content.InventoryPage, error) {
	if f.block {
		<-ctx.Done()
		return content.InventoryPage{}, ctx.Err()
	}
	return f.page, f.err
}
func (f *libraryInventory) Holders(context.Context, string) (content.InventoryPage, error) {
	return content.InventoryPage{}, errors.New("not used")
}

// useLibrary replaces the route's two owners for one test.
func useLibrary(t *testing.T, catalog rclient.ModelsSnapshot, catalogErr error, inventory *libraryInventory) {
	t.Helper()
	readCatalog, readCard, resolve, wait := libraryReadCatalog, libraryReadCard, stepResolveInventory, libraryInventoryWait
	libraryReadCatalog = func(context.Context) (rclient.ModelsSnapshot, error) { return catalog, catalogErr }
	libraryReadCard = func(context.Context) ([]cardResourceState, error) {
		// The model host holds 21.4 GB of the graphics card; the memory
		// resource reports the same bytes on an APU and is not added.
		return []cardResourceState{
			{Resource: "card:0", Holders: []cardHolder{{Program: inference.ResourceHost("modelhost"), Amount: 21_400_000_000, Evidence: cardEvidenceVerified}, {Program: inference.ResourceHost("modelhost"), Amount: 20_000_000_000, Evidence: cardEvidenceClaimed}}},
			{Resource: "memory", Holders: []cardHolder{{Program: inference.ResourceHost("modelhost"), Amount: 40_000_000_000, Evidence: cardEvidenceVerified}}},
		}, nil
	}
	stepResolveInventory = func(context.Context) (stepInventory, error) { return inventory, nil }
	libraryInventoryWait = 200 * time.Millisecond
	readHosted, readPrograms := libraryReadHosted, libraryReadPrograms
	libraryReadHosted = func(context.Context, *servicePanel) (map[string]string, error) {
		return nil, errors.New("no model host in this test")
	}
	libraryReadPrograms = func(context.Context) map[string][]string { return nil }
	t.Cleanup(func() {
		libraryReadCatalog, libraryReadCard, stepResolveInventory, libraryInventoryWait = readCatalog, readCard, resolve, wait
		libraryReadHosted, libraryReadPrograms = readHosted, readPrograms
	})
}

// Owner, 2026-11-01: Setup's download is stored as <sha256>.gguf, so the
// inventory's family for the file is the digest. ModelHost hosts it under
// the catalogue's family, which the router lists; the file joins that
// family through ModelHost's hosted list, one row, with its object for
// Remove from OA, and no digest names the row.
func TestModelsLibraryJoinsADigestNamedFileToTheFamilyItIsHostedAs(t *testing.T) {
	const digest = "55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1"
	catalog := rclient.ModelsSnapshot{Models: []rclient.Family{
		{Family: "qwen3.6-35b-a3b", Names: []rclient.Alias{{Host: "modelhost", Name: "qwen3.6-35b-a3b", Servable: true, Profiles: []string{"chat"}}}},
	}}
	inventory := &libraryInventory{page: content.InventoryPage{Outcome: content.InventoryOutcomePage, Complete: true,
		Stores:    []content.Store{{Name: "oa-chat-1", Locator: `C:\Users\p\AppData\Local\OpenAbstractions\models`, Placement: content.PlacementLocal}},
		Manifests: []content.ManifestHolders{libraryManifest("oa-chat-1", "object-coder", digest, "", 22_853_663_008)}}}
	useLibrary(t, catalog, nil, inventory)
	libraryReadHosted = func(context.Context, *servicePanel) (map[string]string, error) {
		return map[string]string{libraryHostedKey("oa-chat-1", "object-coder"): "qwen3.6-35b-a3b"}, nil
	}
	view := readLibrary(t)
	if len(view.Models) != 1 {
		t.Fatalf("models %+v", view.Models)
	}
	m := view.Models[0]
	if m.Key != "qwen3.6-35b-a3b" || m.Name != "qwen3.6-35b-a3b" || !m.Ready ||
		!reflect.DeepEqual(m.Objects, []libraryObject{{Store: "oa-chat-1", ID: "object-coder", Size: 22_853_663_008, Hosted: true}}) {
		t.Fatalf("the hosted download %+v", m)
	}
	if strings.Contains(m.Key+m.Name, digest) {
		t.Fatalf("a digest names the row: %+v", m)
	}
}

func libraryManifest(store, id, family, role string, size int64) content.ManifestHolders {
	descriptor := map[string]string{"family": family}
	if role != "" {
		descriptor["role"] = role
	}
	return content.ManifestHolders{Manifest: content.Manifest{Store: store, ID: id, Kind: "abstraction.model/descriptor@1", Descriptor: descriptor,
		Names:   []content.Name{{Scheme: store, Name: family + ".gguf"}},
		Entries: []content.Entry{{Role: "weights", Size: size, Locator: family + ".gguf"}}}}
}

// libraryFixture is a computer with a chat model OA's model host runs, a
// picture model in another app's folder no engine runs, a model only a
// hosted API serves, an embedding model a local engine serves with no file
// the inventory knows, and a model on another computer the router does not
// list.
func libraryFixture() (rclient.ModelsSnapshot, *libraryInventory) {
	catalog := rclient.ModelsSnapshot{Models: []rclient.Family{
		{Family: "qwen3", HeldIn: []string{"oa-chat-1"}, Names: []rclient.Alias{{Host: "modelhost", Name: "qwen3-8b", Servable: true, Resident: true, Profiles: []string{"chat"}, HeldIn: []string{"oa-chat-1"}}}},
		{Family: "flux2-klein", HeldIn: []string{"comfy"}, Names: []rclient.Alias{{Name: "diffusion_models/flux2-klein.safetensors", HeldIn: []string{"comfy"}}}},
		{Family: "cloud-model", Names: []rclient.Alias{{Host: "cloud", Name: "cloud-model", Servable: true, Hosted: true, Profiles: []string{"chat"}}}},
		{Family: "nomic-embed", Names: []rclient.Alias{{Host: "ollama", Name: "nomic-embed", Servable: true, Profiles: []string{"embed"}}}},
	}}
	inventory := &libraryInventory{page: content.InventoryPage{Outcome: content.InventoryOutcomePage, Complete: true,
		Stores: []content.Store{
			{Name: "oa-chat-1", Locator: `C:\Users\p\AppData\Local\Programs\OpenAbstractions\models`, Placement: content.PlacementLocal},
			{Name: "comfy", Locator: "/home/p/ComfyUI/models", Placement: content.PlacementLocal},
			{Name: "peer", Locator: "opaque-peer-store", Placement: content.PlacementRemote, Domain: "mac"},
		},
		Manifests: []content.ManifestHolders{
			libraryManifest("oa-chat-1", "object-qwen", "qwen3", "", 5_000_000_000),
			libraryManifest("oa-chat-1", "object-mmproj", "qwen3", "projector", 600_000_000),
			libraryManifest("comfy", "object-flux", "flux2-klein", "", 8_000_000_000),
			libraryManifest("peer", "object-phi", "phi4", "", 2_000_000_000),
		}}}
	return catalog, inventory
}

func readLibrary(t *testing.T) libraryView {
	t.Helper()
	h := newTestPanel(t).handler("test-key")
	got := panelRequest(t, h, "/inference/library", nil)
	return decodePanel[libraryView](t, got.Code, got.Body.Bytes())
}

func libraryKeys(models []libraryModel) []string {
	keys := []string{}
	for _, m := range models {
		keys = append(keys, m.Key)
	}
	return keys
}

// Found: one row per model on this computer, joined by family; a model
// only a hosted API serves is not on this computer, and a part (a
// projector) is no row of its own.
func TestModelsLibraryJoinsFilesWithWhatRunsThem(t *testing.T) {
	catalog, inventory := libraryFixture()
	useLibrary(t, catalog, nil, inventory)
	view := readLibrary(t)
	if !view.FilesKnown || !view.CatalogKnown || view.Reading || view.FilesError != "" || view.CatalogError != "" {
		t.Fatalf("both owners answered: %+v", view)
	}
	if got := libraryKeys(view.Models); !reflect.DeepEqual(got, []string{"flux2-klein", "nomic-embed", "phi4", "qwen3"}) {
		t.Fatalf("models %v", got)
	}
	byKey := map[string]libraryModel{}
	for _, m := range view.Models {
		byKey[m.Key] = m
	}
	qwen := byKey["qwen3"]
	if !qwen.Ready || !qwen.Running || !reflect.DeepEqual(qwen.Uses, []string{"chat"}) || !reflect.DeepEqual(qwen.Engines, []string{"modelhost"}) ||
		qwen.Size != 5_000_000_000 || !reflect.DeepEqual(qwen.Objects, []libraryObject{{Store: "oa-chat-1", ID: "object-qwen", Size: 5_000_000_000}}) ||
		!reflect.DeepEqual(qwen.Places, []libraryPlace{{Name: "OpenAbstractions › models", Path: `C:\Users\p\AppData\Local\Programs\OpenAbstractions\models`}}) ||
		!slices.Contains(qwen.Names, "qwen3-8b") {
		t.Fatalf("the running chat model %+v", qwen)
	}
	flux := byKey["flux2-klein"]
	if flux.Ready || flux.Running || len(flux.Uses) != 0 || flux.Size != 8_000_000_000 ||
		!reflect.DeepEqual(flux.Places, []libraryPlace{{Name: "ComfyUI › models", Path: "/home/p/ComfyUI/models"}}) {
		t.Fatalf("the stored picture model no engine runs %+v", flux)
	}
	if embed := byKey["nomic-embed"]; !embed.Ready || len(embed.Places) != 0 || embed.Size != 0 {
		t.Fatalf("a model a local engine serves with no known file %+v", embed)
	}
	if phi := byKey["phi4"]; phi.Ready || !reflect.DeepEqual(phi.Places, []libraryPlace{{Name: "another computer", Path: "mac"}}) {
		t.Fatalf("a file only the inventory knows %+v", phi)
	}
	if !reflect.DeepEqual(view.Running, []libraryRunning{{Key: "qwen3", Name: "qwen3", Engine: "modelhost", Uses: []string{"chat"}, Memory: 21_400_000_000}}) {
		t.Fatalf("running %+v", view.Running)
	}
	for _, m := range view.Models {
		for _, p := range m.Places {
			if strings.ContainsAny(p.Name, `\/`) {
				t.Fatalf("a place name is a path: %q", p.Name)
			}
		}
	}
}

// Reading: while the inventory's first read runs, the route answers
// Reading with what the router already knows, never an empty answer
// passed off as the list.
func TestModelsLibrarySaysReadingWhileTheFoldersAreRead(t *testing.T) {
	catalog, _ := libraryFixture()
	useLibrary(t, catalog, nil, &libraryInventory{block: true})
	view := readLibrary(t)
	if !view.Reading || view.FilesKnown || view.FilesError != "" || !view.CatalogKnown {
		t.Fatalf("a running first read: %+v", view)
	}
	if got := libraryKeys(view.Models); !reflect.DeepEqual(got, []string{"flux2-klein", "nomic-embed", "qwen3"}) {
		t.Fatalf("the router's models stay on the page: %v", got)
	}
}

// Refused: each owner's refusal is reported with its reason and marked as a
// permission refusal; the other owner's answer stays.
func TestModelsLibraryReportsEachRefusalWithItsReason(t *testing.T) {
	_, inventory := libraryFixture()
	useLibrary(t, rclient.ModelsSnapshot{}, errors.New("router forbidden: abstraction.router/inventory.read on abstraction.router/inventory"), inventory)
	view := readLibrary(t)
	if view.CatalogKnown || !view.CatalogRefused || !strings.Contains(view.CatalogError, "inventory.read") || !view.FilesKnown {
		t.Fatalf("router refusal: %+v", view)
	}
	if got := libraryKeys(view.Models); !reflect.DeepEqual(got, []string{"flux2-klein", "phi4", "qwen3"}) || view.Models[2].Ready || len(view.Running) != 0 {
		t.Fatalf("the files stay, with nothing claimed about what runs them: %+v", view.Models)
	}
	catalog, _ := libraryFixture()
	useLibrary(t, catalog, nil, &libraryInventory{page: content.InventoryPage{Outcome: content.InventoryOutcomeForbidden}})
	view = readLibrary(t)
	if view.FilesKnown || !view.FilesRefused || view.FilesError != "storage inventory forbidden" || view.Reading || !view.CatalogKnown {
		t.Fatalf("inventory refusal: %+v", view)
	}
	useLibrary(t, catalog, nil, &libraryInventory{page: content.InventoryPage{Outcome: content.InventoryOutcomeUnavailable}})
	if view = readLibrary(t); view.FilesRefused || view.FilesError != "storage inventory unavailable" || view.Reading {
		t.Fatalf("an unavailable inventory is no refusal and no read in progress: %+v", view)
	}
}

// Owner, 2026-11-10: "oa panel models loaded shows nothing although i am
// talking with local OA model". The model host's resident model is under
// Loaded now with its engine and the graphics memory its lease holds: the
// lease is written under the model host's declared program.
func TestModelsLibraryShowsTheModelHostsResidentWithItsLease(t *testing.T) {
	catalog := rclient.ModelsSnapshot{Models: []rclient.Family{
		{Family: "qwen3.6-35b-a3b", Title: "Qwen3.6 35B", Names: []rclient.Alias{
			{Host: "modelhost", Name: "55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1.gguf", Servable: true, Resident: true, Profiles: []string{"chat"}, HeldIn: []string{"oa-chat-1"}},
			{Host: "modelhost", Name: "qwen3.6-35b-a3b", Servable: true, Profiles: []string{"chat"}, HeldIn: []string{"oa-chat-1"}}}},
		{Family: "qwen3-0.6b", Names: []rclient.Alias{{Host: "modelhost", Name: "qwen3-0.6b", Servable: true, Profiles: []string{"chat"}, HeldIn: []string{"huggingface"}}}},
	}}
	useLibrary(t, catalog, nil, &libraryInventory{page: content.InventoryPage{Outcome: content.InventoryOutcomePage, Complete: true}})
	program := platformProgram(`C:\Program Files\OpenAbstractions\tools\modelhostd.exe`)
	engine := platformProgram(`C:\Users\p\AppData\Local\openabstractions\engines\llama.cpp-b10825\llama-server.exe`)
	libraryReadPrograms = func(context.Context) map[string][]string { return map[string][]string{"modelhost": {program, engine}} }
	holders := []cardHolder{
		{Program: engine, Amount: 25_736_691_712, Evidence: cardEvidenceVerified},
		{Program: program, Amount: 24_464_275_744, Evidence: cardEvidenceClaimed},
	}
	libraryReadCard = func(context.Context) ([]cardResourceState, error) {
		return []cardResourceState{{Resource: "card:0", Holders: holders}}, nil
	}
	view := readLibrary(t)
	want := []libraryRunning{{Key: "qwen3.6-35b-a3b", Name: "Qwen3.6 35B", Title: "Qwen3.6 35B", Engine: "modelhost", Uses: []string{"chat"}, Memory: 25_736_691_712}}
	if !reflect.DeepEqual(view.Running, want) {
		t.Fatalf("measured: running %+v, want %+v", view.Running, want)
	}
	holders = holders[1:]
	view = readLibrary(t)
	want[0].Memory = 24_464_275_744
	if !reflect.DeepEqual(view.Running, want) {
		t.Fatalf("running %+v, want %+v", view.Running, want)
	}
}

// Empty: both owners answered and nothing is on this computer.
func TestModelsLibraryEmptyIsAnAnswer(t *testing.T) {
	useLibrary(t, rclient.ModelsSnapshot{}, nil, &libraryInventory{page: content.InventoryPage{Outcome: content.InventoryOutcomePage, Complete: true}})
	view := readLibrary(t)
	if !view.FilesKnown || !view.CatalogKnown || view.Reading || view.Models == nil || len(view.Models) != 0 || view.Running == nil {
		t.Fatalf("empty: %+v", view)
	}
	h := newTestPanel(t).handler("test-key")
	if got := panelRequest(t, h, "/inference/library", map[string]string{}); got.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /inference/library: %d", got.Code)
	}
}

// Review of 1a98de54a, B2: Remove from OA records the removal in Setup's
// step journal (in the test's stand-in person folder) before it unhosts,
// only for the object that holds the step's model: the downloaded file in
// Setup's model folder, or the adopted copy. Another object leaves the
// journal as it was. A record that cannot be written removes nothing.
func TestModelsRemoveFromOARecordsTheStepsModelFirst(t *testing.T) {
	journal, err := setup.DefaultStepJournal()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(journal) })
	plan := setup.CodingModelPlan()
	digest := "sha256:" + plan.Assets[0].Download.SHA256
	ready := setup.StepState{Progress: "ready", Use: setup.UseCode, Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", ModelPlanID: plan.ID, Hosted: true, Selected: true}
	write := func(state setup.StepState) []byte {
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(journal, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	read := func() setup.StepState {
		state, err := (setup.SetupStep{JournalPath: journal}).Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	p := newTestPanel(t)
	p.codingController = &setup.ModelController{Plan: &plan}
	p.stepModelFolder = func(context.Context) (string, []modelHostChatModel, error) {
		return "oa-chat-1", []modelHostChatModel{{ID: "object-coder", Store: "oa-chat-1", Digest: digest}, {ID: "object-other", Store: "oa-chat-1", Digest: "sha256:" + strings.Repeat("b", 64)}}, nil
	}
	var commands [][]string
	p.modelHostOA = func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, args)
		if len(args) > 1 && args[1] == "unhost" && !read().RemovedFromOA && args[2] == "oa-chat-1/object-coder" {
			t.Error("the model host unhosted the step's model before the removal was recorded")
		}
		return []byte(`[]`), nil
	}
	h := p.handler("test-key")
	unhost := func(store, id string) int {
		return panelRequest(t, h, "/setup/modelhost/unhost", modelHostUnhostRequest{Store: store, ID: id, Confirm: true}).Code
	}

	// Another object leaves the journal byte for byte.
	before := write(ready)
	if code := unhost("oa-chat-1", "object-other"); code != http.StatusOK {
		t.Fatalf("unhost another object: %d", code)
	}
	if after, _ := os.ReadFile(journal); !bytes.Equal(after, before) {
		t.Fatalf("another object changed the journal: %s", after)
	}
	// The step's downloaded file is recorded, also while pictures still run.
	for _, state := range []setup.StepState{ready, {Progress: "approved", Use: setup.UseCode, Title: ready.Title, Family: ready.Family, ModelPlanID: plan.ID, Hosted: true, Pictures: true}} {
		write(state)
		if code := unhost("oa-chat-1", "object-coder"); code != http.StatusOK || !read().RemovedFromOA {
			t.Fatalf("unhost the step's file from %q: %d %+v", state.Progress, code, read())
		}
	}
	// An adopted copy is the step's model wherever it lies.
	adopted := ready
	adopted.Adopted = &setup.StepLocal{Store: "lm-studio", Object: "object-adopted"}
	write(adopted)
	if code := unhost("lm-studio", "object-adopted"); code != http.StatusOK || !read().RemovedFromOA {
		t.Fatalf("unhost the adopted copy: %d %+v", code, read())
	}
	// A record that cannot be written removes nothing and says so.
	write(ready)
	commands = nil
	p.stepModelFolder = func(context.Context) (string, []modelHostChatModel, error) {
		return "", nil, errors.New("model folder unreadable")
	}
	if code := unhost("oa-chat-1", "object-coder"); code != http.StatusServiceUnavailable || len(commands) != 0 {
		t.Fatalf("unrecorded removal answered %d and ran %v", code, commands)
	}
}

// A Panel whose Setup keeps no state for its runtime (an explicit runtime
// without -runtime-state-dir) has no step to record for: Remove from OA
// unhosts. A step journal that exists but cannot be read still refuses.
func TestModelsRemoveFromOAWithoutSetupState(t *testing.T) {
	var commands [][]string
	p := newTestPanel(t)
	p.modelHostOA = func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, args)
		return []byte(`[]`), nil
	}
	h := p.handler("test-key")
	unhost := func() int {
		return panelRequest(t, h, "/setup/modelhost/unhost", modelHostUnhostRequest{Store: "oa-chat-1", ID: "object-coder", Confirm: true}).Code
	}
	setup.UseNoState(errSetupNeedsStateDir)
	t.Cleanup(setup.UseUserState)
	if code := unhost(); code != http.StatusOK || len(commands) == 0 || commands[0][1] != "unhost" {
		t.Fatalf("Remove from OA without Setup state answered %d and ran %v", code, commands)
	}
	setup.UseUserState()
	journal, err := setup.DefaultStepJournal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(journal) })
	if err := os.WriteFile(journal, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands = nil
	if code := unhost(); code != http.StatusServiceUnavailable || len(commands) != 0 {
		t.Fatalf("an unreadable step journal answered %d and ran %v", code, commands)
	}
}

// Every openabstractions command the Panel runs names the explicit runtime
// it serves; without --endpoint the command dials ABSTRACTION_RUNTIME_ENDPOINT
// or the installed runtime. The installed Panel's commands stay as they are.
func TestModelHostCommandsNameTheServedRuntime(t *testing.T) {
	var commands [][]string
	p := newTestPanel(t)
	p.modelHostOA = func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, args)
		return []byte(`[]`), nil
	}
	h := p.handler("test-key")
	before := panelExplicit
	t.Cleanup(func() { panelExplicit = before })
	panelExplicit = &explicitRuntime{Endpoint: "oa-fixture-runtime", Program: `C:\fixture\openabstractions.exe`}
	if got := panelRequest(t, h, "/setup/modelhost/unhost", modelHostUnhostRequest{Store: "oa-chat-1", ID: "object-coder", Confirm: true}); got.Code != http.StatusOK {
		t.Fatalf("unhost: %d %s", got.Code, got.Body.String())
	}
	if _, err := p.listModelHostProvider(context.Background(), setup.ModelHostChatRoot{}, `C:\fixture\inventoryd.exe`); err == nil {
		t.Fatal("an empty provider list matched a declaration")
	}
	if _, err := p.modelHostOpenAbstractions(context.Background(), modelHostHostArgs("oa-chat-1", "object-coder", `C:\fixture\engine\llama-server.exe`, `C:\fixture\modelhostd.exe`, []string{"--ctx-size", "32768"}, "")...); err != nil {
		t.Fatal(err)
	}
	tail := []string{"--endpoint", "oa-fixture-runtime", "--runtime-program", `C:\fixture\openabstractions.exe`}
	if len(commands) != 4 || commands[3][1] != "host" {
		t.Fatalf("commands %v", commands)
	}
	for _, command := range commands {
		if len(command) < len(tail) || !slices.Equal(command[len(command)-len(tail):], tail) {
			t.Fatalf("a command does not name the served runtime: %v", command)
		}
	}
	panelExplicit, commands = nil, nil
	if got := panelRequest(t, h, "/setup/modelhost/unhost", modelHostUnhostRequest{Store: "oa-chat-1", ID: "object-coder", Confirm: true}); got.Code != http.StatusOK {
		t.Fatalf("installed unhost: %d", got.Code)
	}
	for _, command := range commands {
		if slices.Contains(command, "--endpoint") {
			t.Fatalf("the installed Panel named an endpoint: %v", command)
		}
	}
}

// Remove from OA stops hosting one model through the model host's own
// command, verifies it, and changes no file; it needs an explicit confirm
// and an inventory object the command line cannot misread.
func TestModelsRemoveFromOAUnhostsAndKeepsTheFile(t *testing.T) {
	p := newTestPanel(t)
	hosted := `[{"Name":"qwen3-8b","Store":"oa-chat-1","Object":"object-qwen"}]`
	var commands [][]string
	p.modelHostOA = func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, args)
		if len(args) > 1 && args[1] == "unhost" {
			hosted = `[]`
			return []byte(`{}`), nil
		}
		return []byte(hosted), nil
	}
	h := p.handler("test-key")
	for _, bad := range []modelHostUnhostRequest{{Store: "oa-chat-1", ID: "object-qwen"}, {Store: "oa-chat-1", ID: "a/b", Confirm: true}, {Store: "-x", ID: "object-qwen", Confirm: true}} {
		if got := panelRequest(t, h, "/setup/modelhost/unhost", bad); got.Code != http.StatusBadRequest {
			t.Fatalf("%+v answered %d", bad, got.Code)
		}
	}
	if len(commands) != 0 {
		t.Fatalf("a refused request ran %v", commands)
	}
	got := panelRequest(t, h, "/setup/modelhost/unhost", modelHostUnhostRequest{Store: "oa-chat-1", ID: "object-qwen", Confirm: true})
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"unhosted":true`) {
		t.Fatalf("unhost: %d %s", got.Code, got.Body.String())
	}
	want := [][]string{{"models", "unhost", "oa-chat-1/object-qwen", "--json", "--timeout", "30s"}, {"models", "hosts", "--json", "--timeout", "30s"}}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands %v", commands)
	}
	// A runtime that still hosts the model is not reported as removed.
	hosted = `[{"Name":"qwen3-8b","Store":"oa-chat-1","Object":"object-qwen"}]`
	p.modelHostOA = func(_ context.Context, args ...string) ([]byte, error) { return []byte(hosted), nil }
	if got := panelRequest(t, h, "/setup/modelhost/unhost", modelHostUnhostRequest{Store: "oa-chat-1", ID: "object-qwen", Confirm: true}); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("a kept model answered %d %s", got.Code, got.Body.String())
	}
}
