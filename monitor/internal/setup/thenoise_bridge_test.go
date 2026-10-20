package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	thenoise "github.com/openabstractions/abstraction-provider-thenoise/go"
)

func shortTheNoiseHome(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "tn-")
	if err != nil {
		t.Fatal(err)
	}
	previous := chosenHome.Load()
	chosenHome.Store(&homeChoice{home: Home{Config: filepath.Join(root, "c"), Data: root}})
	t.Cleanup(func() { chosenHome.Store(previous); _ = os.RemoveAll(root) })
	return root
}

func TestTheNoiseArchiveReceiptIsOutsideExactEngineChild(t *testing.T) {
	root := shortTheNoiseHome(t)
	zipPath, pin := archiveFixture(t, archiveEntry{name: "./"}, archiveEntry{name: "./engine.bat", data: "fixture"}, archiveEntry{name: "./bin/library.dll", data: "fixture"})
	pin.InstallIdentity = "tn"
	destination := filepath.Join(root, "r")
	if err := InstallArchive(context.Background(), pin, zipPath, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, archiveMarker)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "e", archiveMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker reached engine: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "e", "engine.bat")); err != nil {
		t.Fatal(err)
	}
	if ok, err := ArchiveInstalled(pin, destination); err != nil || !ok {
		t.Fatalf("outer receipt: %t %v", ok, err)
	}
	if err := InstallArchive(context.Background(), pin, zipPath, destination); err == nil {
		t.Fatal("existing tree overwritten")
	}
}

func TestTheNoiseArchiveChecksStageAndFinalRootLimit(t *testing.T) {
	root := shortTheNoiseHome(t)
	zipPath, pin := archiveFixture(t, archiveEntry{name: "./engine.bat", data: "fixture"})
	pin.InstallIdentity = "tn"
	// The final name fits, but the unique same-parent staging name does not.
	parent := filepath.Join(root, strings.Repeat("p", 78-len(root)-1))
	destination := filepath.Join(parent, "r")
	if err := theNoiseEnginePath(filepath.Join(destination, "e")); err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(context.Background(), pin, zipPath, destination); err == nil {
		t.Fatal("overlong staged engine accepted")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published after stage refusal: %v", err)
	}
	if err := theNoiseEnginePath(filepath.Join(root, strings.Repeat("p", 88))); err == nil {
		t.Fatal("overlong final engine accepted")
	}
}

func TestTheNoiseModelApprovalAndGuardAreSeparate(t *testing.T) {
	plan := TheNoiseModelPlan()
	if plan.Engine != TheNoise || plan.ID == ApprovedBF16Plan().ID || len(plan.Assets) != 3 {
		t.Fatalf("unbound model plan: %+v", plan)
	}
	transfer := &choiceTransferFake{}
	c := choiceController(t, plan, nil, transfer)
	c.StageModel = func(context.Context, string, string, map[string]string, func(int64, int64) error) error {
		t.Fatal("unapproved configuration ran")
		return nil
	}
	if _, err := c.Configure(context.Background(), false, nil); err == nil {
		t.Fatal("package approval substituted for model consent")
	}
	if _, err := c.Start(context.Background(), approve(ApprovedBF16Plan())); err == nil {
		t.Fatal("Lemonade approval accepted for TheNoise")
	}
	if _, err := c.Start(context.Background(), approve(plan)); err != nil {
		t.Fatal(err)
	}
	c.Guard = func(context.Context) error { return errors.New("owned engine no longer approved") }
	for _, operation := range []func() error{
		func() error { _, err := c.Advance(context.Background(), "main"); return err },
		func() error { _, err := c.Retry(context.Background(), "main"); return err },
		func() error { _, err := c.Configure(context.Background(), false, nil); return err },
	} {
		if err := operation(); err == nil {
			t.Fatal("changed engine approval accepted")
		}
	}
	if len(transfer.reserved) != 0 {
		t.Fatal("denied action reserved a transfer")
	}
}

type theNoiseRegistryFake struct {
	declaration     *fwire.DeclarationWithExtensions
	readiness       fwire.DeclarationReadiness
	withdrawOutcome fwire.DeclarationEditOutcome
	declareOutcome  fwire.DeclarationEditOutcome
	declareError    error
	declares        int
	events          *[]string
}

func (r *theNoiseRegistryFake) Declarations(context.Context) (fwire.DeclarationListV2, error) {
	page := fwire.DeclarationListV2{Outcome: fwire.DeclarationListOutcomePage, Revision: "revision"}
	if r.declaration != nil {
		page.Declarations = []fwire.DeclarationStateV2{{State: fwire.DeclarationState{Declaration: r.declaration.Declaration, Readiness: r.readiness}, SupportedExtensions: r.declaration.SupportedExtensions}}
	}
	return page, nil
}
func (r *theNoiseRegistryFake) Declare(_ context.Context, _ string, d fwire.DeclarationWithExtensions) (fwire.DeclarationChange, error) {
	r.declares++
	r.declaration = &d
	return fwire.DeclarationChange{Outcome: r.declareOutcome}, r.declareError
}

func TestTheNoiseReadinessTracksCurrentRegistryWithoutEffects(t *testing.T) {
	j, _, d := ownedTheNoiseFixture(t)
	c, err := DefaultTheNoiseController(j)
	if err != nil {
		t.Fatal(err)
	}
	model := ModelState{PlanID: c.plan().ID, Approved: true, ProviderConfigured: true, Effects: map[string]ModelEffect{}}
	for _, a := range c.plan().Assets {
		model.Effects[a.ID] = ModelEffect{Phase: Verified}
	}
	raw, _ := json.Marshal(model)
	if err := os.WriteFile(c.JournalPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	r := &theNoiseRegistryFake{declaration: &d, readiness: fwire.DeclarationReadinessIdle, events: &events}
	oldRegistry, oldCLI := setupEngineRegistry, theNoiseCLI
	setupEngineRegistry = func(context.Context) (engineRegistry, error) { return r, nil }
	theNoiseCLI = func(context.Context, ...string) error { t.Fatal("readiness/configuration ran CLI"); return nil }
	t.Cleanup(func() { setupEngineRegistry, theNoiseCLI = oldRegistry, oldCLI })
	if status, err := c.Status(context.Background()); err != nil || !status.Ready {
		t.Fatalf("owned idle registration: %+v %v", status, err)
	}
	for _, kind := range []string{"absent", "disabled", "foreign"} {
		copy := d
		copy.Declaration.Arguments = slices.Clone(d.Declaration.Arguments)
		r.declaration = &copy
		r.readiness = fwire.DeclarationReadinessIdle
		switch kind {
		case "absent":
			r.declaration = nil
		case "disabled":
			r.readiness = fwire.DeclarationReadinessDisabled
		case "foreign":
			copy.Declaration.Arguments = append(copy.Declaration.Arguments, "foreign")
		}
		if status, err := c.Status(context.Background()); err != nil || status.Ready {
			t.Fatalf("%s stayed ready: %+v %v", kind, status, err)
		}
		if _, err := c.Configure(context.Background(), false, nil); err == nil {
			t.Fatalf("%s configuration silently accepted", kind)
		}
	}
	if r.declares != 0 || len(events) != 0 {
		t.Fatal("registry readiness changed registration")
	}
}

func TestTheNoiseConfirmedRegistrationRefusalCannotClaimIdenticalForeignRow(t *testing.T) {
	for _, reply := range []string{"confirmed-conflict", "lost-reply", "unavailable-after-effect"} {
		t.Run(reply, func(t *testing.T) {
			lostReply := reply != "confirmed-conflict"
			j, _, d := ownedTheNoiseFixture(t)
			state, _ := j.Load(context.Background())
			delete(state.EngineDeclarations, TheNoise)
			raw, _ := json.Marshal(state)
			if err := os.WriteFile(j.Path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			events := []string{}
			r := &theNoiseRegistryFake{declareOutcome: fwire.DeclarationEditOutcomeConflict, events: &events}
			if reply == "lost-reply" {
				r.declareError = errors.New("reply lost after effect")
			}
			if reply == "unavailable-after-effect" {
				r.declareOutcome = fwire.DeclarationEditOutcomeUnavailable
			}
			oldRegistry, oldCLI := setupEngineRegistry, theNoiseCLI
			setupEngineRegistry = func(context.Context) (engineRegistry, error) { return r, nil }
			theNoiseCLI = func(context.Context, ...string) error { t.Fatal("foreign registration permitted unlock"); return nil }
			t.Cleanup(func() { setupEngineRegistry, theNoiseCLI = oldRegistry, oldCLI })
			if err := registerTheNoise(context.Background(), j, state, d); err == nil {
				t.Fatal("refused registration accepted")
			}
			state, _ = j.Load(context.Background())
			_, intent := state.EngineDeclarations[TheNoise]
			if intent != lostReply {
				t.Fatalf("declaration intent after reply: %t lost=%t", intent, lostReply)
			}
			err := registerTheNoise(context.Background(), j, state, d)
			if lostReply {
				if err != nil {
					t.Fatalf("exact lost-reply recovery: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("confirmed conflict's foreign exact row claimed on retry")
			}
			if err := NewRunner(j, nil).RemoveTheNoise(context.Background()); err == nil {
				t.Fatal("foreign exact row permitted engine removal")
			}
			if len(events) != 0 {
				t.Fatalf("foreign row withdrawn: %v", events)
			}
		})
	}
}
func (r *theNoiseRegistryFake) Withdraw(context.Context, string, string) (fwire.DeclarationChange, error) {
	*r.events = append(*r.events, "withdraw/halt")
	if r.withdrawOutcome == fwire.DeclarationEditOutcomeApplied {
		r.declaration = nil
	}
	return fwire.DeclarationChange{Outcome: r.withdrawOutcome}, nil
}

// The fixture synthesizes only a tiny receipt/tree and journal; it invokes no
// installed adapter, native engine, MSI or security probe.
func ownedTheNoiseFixture(t *testing.T) (Journal, string, fwire.DeclarationWithExtensions) {
	shortTheNoiseHome(t)
	ctx := context.Background()
	j, err := DefaultJournal()
	if err != nil {
		t.Fatal(err)
	}
	// The catalogue pins TheNoise for Windows x64 only; the ownership and
	// readiness rules these tests check read the journal's catalogue and are
	// the same on every platform.
	windows := catalogForTarget("windows", "amd64")
	j.catalogOverride = &windows
	plan, err := j.Catalog().Plan([]AppID{TheNoise})
	if err != nil {
		t.Fatal(err)
	}
	state, err := j.SavePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	root, providerState, models, err := theNoiseFolders()
	if err != nil {
		t.Fatal(err)
	}
	zipPath, pin := archiveFixture(t, archiveEntry{name: "./engine.bat", data: "fixture"})
	pin.InstallIdentity = "tn"
	if err := InstallArchive(ctx, pin, zipPath, root); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, archiveMarker))
	if err != nil {
		t.Fatal(err)
	}
	var receipt archiveReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	a := theNoiseArtifact()
	receipt.ArtifactSHA256, receipt.Version = a.SHA256, a.Version
	raw, _ = json.Marshal(receipt)
	if err := os.WriteFile(filepath.Join(root, archiveMarker), raw, 0600); err != nil {
		t.Fatal(err)
	}
	d, err := expectedTheNoiseDeclaration(root)
	if err != nil {
		t.Fatal(err)
	}
	state.Effects["thenoise-engine"] = Effect{Phase: Installed, Revision: 1}
	state.EngineDeclarations = map[AppID]fwire.DeclarationWithExtensions{TheNoise: d}
	raw, _ = json.Marshal(state)
	if err := os.WriteFile(j.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(providerState, "audit.jsonl"), filepath.Join(providerState, "content", "personal.png"), filepath.Join(models, "personal.safetensors"), filepath.Join(providerState, thenoise.EngineFolderName+".manifest"), filepath.Join(providerState, thenoise.EngineFolderName+".files")} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("personal"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return j, root, d
}

func TestTheNoiseRemovalWithdrawsBeforeUnlockAndPreservesPersonalData(t *testing.T) {
	j, root, d := ownedTheNoiseFixture(t)
	events := []string{}
	r := &theNoiseRegistryFake{declaration: &d, withdrawOutcome: fwire.DeclarationEditOutcomeApplied, events: &events}
	oldRegistry, oldCLI := setupEngineRegistry, theNoiseCLI
	setupEngineRegistry = func(context.Context) (engineRegistry, error) { return r, nil }
	theNoiseCLI = func(_ context.Context, args ...string) error {
		events = append(events, "unlock")
		if len(args) != 6 || args[1] != "unlock" || args[3] != filepath.Join(root, "e") {
			t.Fatalf("unfixed unlock: %v", args)
		}
		if r.declaration != nil {
			t.Fatal("unlock before synchronous halt")
		}
		return nil
	}
	t.Cleanup(func() { setupEngineRegistry, theNoiseCLI = oldRegistry, oldCLI })
	if err := NewRunner(j, nil).RemoveTheNoise(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(events, []string{"withdraw/halt", "unlock"}) {
		t.Fatalf("order: %v", events)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned tree remained: %v", err)
	}
	_, state, models, _ := theNoiseFolders()
	for _, path := range []string{filepath.Join(state, "audit.jsonl"), filepath.Join(state, "content", "personal.png"), filepath.Join(models, "personal.safetensors"), filepath.Join(state, thenoise.EngineFolderName+".manifest"), filepath.Join(state, thenoise.EngineFolderName+".files")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("personal data removed: %v", err)
		}
	}
	updated, err := j.Load(context.Background())
	if err != nil || updated.Effects["thenoise-engine"].Phase != Pending || updated.Configuration["thenoise-model"] || len(updated.EngineDeclarations) != 0 {
		t.Fatalf("removal journal: %+v %v", updated, err)
	}
}

func TestTheNoiseRemovalPreservesUnownedOrRefusedTrees(t *testing.T) {
	for _, reason := range []string{"unowned", "foreign", "disabled", "withdraw-refused"} {
		t.Run(reason, func(t *testing.T) {
			j, root, d := ownedTheNoiseFixture(t)
			events := []string{}
			r := &theNoiseRegistryFake{declaration: &d, withdrawOutcome: fwire.DeclarationEditOutcomeApplied, events: &events}
			switch reason {
			case "unowned":
				state, _ := j.Load(context.Background())
				state.Effects["thenoise-engine"] = Effect{Phase: Pending}
				raw, _ := json.Marshal(state)
				if err := os.WriteFile(j.Path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				d.Declaration.Arguments = append(slices.Clone(d.Declaration.Arguments), "foreign")
			case "disabled":
				r.readiness = fwire.DeclarationReadinessDisabled
			case "withdraw-refused":
				r.withdrawOutcome = fwire.DeclarationEditOutcomeConflict
			}
			oldRegistry, oldCLI := setupEngineRegistry, theNoiseCLI
			setupEngineRegistry = func(context.Context) (engineRegistry, error) { return r, nil }
			theNoiseCLI = func(context.Context, ...string) error { t.Fatal("refused removal unlocked engine"); return nil }
			t.Cleanup(func() { setupEngineRegistry, theNoiseCLI = oldRegistry, oldCLI })
			if err := NewRunner(j, nil).RemoveTheNoise(context.Background()); err == nil {
				t.Fatal("unowned/refused removal accepted")
			}
			if _, err := os.Stat(filepath.Join(root, "e", "engine.bat")); err != nil {
				t.Fatal("refused tree removed")
			}
		})
	}
}

func TestTheNoiseDeclarationIdentityPreservesArgumentsAndSamePlanOwnership(t *testing.T) {
	j, root, d := ownedTheNoiseFixture(t)
	actual := d
	actual.Declaration.Arguments = slices.Clone(d.Declaration.Arguments)
	// A harmless existing-file hard-link alias resolves through identity.SameFile.
	program := filepath.Join(filepath.Dir(root), "adapter.fixture")
	alias := program + ".alias"
	if err := os.WriteFile(program, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(program, alias); err != nil {
		t.Fatal(err)
	}
	d.Declaration.Program, actual.Declaration.Program = program, alias
	before := slices.Clone(actual.Declaration.Arguments)
	if !sameEngineDeclaration(actual, d) {
		t.Fatal("same-file alias refused")
	}
	if !reflect.DeepEqual(before, actual.Declaration.Arguments) {
		t.Fatal("comparison modified registry arguments")
	}
	state, _ := j.Load(context.Background())
	state, err := j.SavePlan(context.Background(), state.Plan)
	if err != nil || len(state.EngineDeclarations) != 1 {
		t.Fatalf("same plan lost declaration ownership: %+v %v", state, err)
	}
}
