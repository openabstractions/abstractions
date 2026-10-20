package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/placetest"
	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
)

func pyTorchFixture(t *testing.T) (Journal, State, Artifact, string, string) {
	t.Helper()
	shortTheNoiseHome(t)
	zip, a := archiveFixture(t, archiveEntry{name: "python/python.exe", data: "fixture input"}, archiveEntry{name: "source/infer/fixture.py", data: "fixture source"}, archiveEntry{name: "wheels/fixture.whl", data: "fixture wheel"})
	a.InstallIdentity = "pt"
	fixturePackagePins("pytorch-engine", &a)
	// Setup installs PyTorch from an OpenAbstractions release asset.
	a.URL = "https://github.com/openabstractions/redist/releases/download/v0.8.0/pytorch-inputs.zip"
	a.SourceArchiveURL = a.URL[:strings.LastIndex(a.URL, "/")+1] + "pytorch-source.zip"
	c := fixtureCatalog()
	for i := range c.Components {
		if c.Components[i].ID == "pytorch-engine" {
			c.Components[i].Artifact = &a
		}
	}
	j, err := DefaultJournal()
	if err != nil {
		t.Fatal(err)
	}
	j.catalogOverride = &c
	plan, err := c.Plan([]AppID{PyTorch})
	if err != nil {
		t.Fatal(err)
	}
	state, err := j.SavePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	inputs, component, _, _, err := pyTorchFolders(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(context.Background(), a, zip, inputs); err != nil {
		t.Fatal(err)
	}
	state.Effects["pytorch-engine"] = Effect{Phase: Staged, StagedPath: zip, Revision: 1}
	writePyTorchState(t, j, state)
	return j, state, a, inputs, component
}

func writePyTorchState(t *testing.T, j Journal, state State) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func writePyTorchReady(t *testing.T, root string) {
	t.Helper()
	c := torch.Component{Root: root}
	profile, err := c.Profile()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sandbox.Manifest{Format: 2, Profile: profile, Phase: sandbox.PhaseReady, SID: "fixture-only-sid"})
	if err := os.WriteFile(c.Manifest(), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPyTorchOwnershipAllocationRetryAndLostReply(t *testing.T) {
	for _, kind := range []string{"foreign", "fresh", "interrupted", "ready-lost-reply", "replaced", "cas-refused"} {
		t.Run(kind, func(t *testing.T) {
			j, state, a, inputs, root := pyTorchFixture(t)
			r := NewRunner(j, nil)
			calls := 0
			old := pyTorchCLI
			pyTorchCLI = func(_ context.Context, args ...string) error {
				calls++
				if !slices.Equal(args, []string{"setup", "install", "--root", root, "--python-home", filepath.Join(inputs, "python"), "--wheels", filepath.Join(inputs, "wheels"), "--source", filepath.Join(inputs, "source", "infer"), "--state", filepath.Join(chosenHome.Load().home.Data, "OA", "p", "pt")}) {
					t.Fatalf("mutable command: %v", args)
				}
				current, _ := j.Load(context.Background())
				if err := pyTorchRootMatches(current.Effects["pytorch-engine"], root); err != nil {
					t.Fatal("CLI preceded durable root ownership", err)
				}
				writePyTorchReady(t, root)
				return nil
			}
			t.Cleanup(func() { pyTorchCLI = old })
			if kind != "fresh" && kind != "cas-refused" {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				if kind != "foreign" {
					key, err := identity.FileKey(root)
					if err != nil {
						t.Fatal(err)
					}
					e := state.Effects["pytorch-engine"]
					e.ComponentRootKey = key
					state.Effects["pytorch-engine"] = e
					writePyTorchState(t, j, state)
				}
				if kind == "ready-lost-reply" {
					writePyTorchReady(t, root)
				}
				if kind == "replaced" {
					if err := os.Rename(root, root+"-original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "cas-refused" {
				changed := state
				changed.ApprovedPlanID = "changed"
				writePyTorchState(t, j, changed)
			}
			got, err := r.preparePyTorchComponent(context.Background(), state, a, inputs)
			success := kind == "fresh" || kind == "ready-lost-reply"
			if (err == nil) != success {
				t.Fatalf("%s outcome: %v", kind, err)
			}
			wantCalls := 0
			if kind == "fresh" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("%s CLI calls %d", kind, calls)
			}
			if success && got.Effects["pytorch-engine"].ComponentRootKey == "" {
				t.Fatal("ownership was lost")
			}
			if kind == "interrupted" {
				if _, err := r.installPyTorchComponent(context.Background(), state, a, inputs, true); err != nil {
					t.Fatal("explicit same-key retry", err)
				}
				if calls != 1 {
					t.Fatal("retry count", calls)
				}
			}
			if kind == "cas-refused" {
				if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("changed approval allocated root", err)
				}
			} else if _, err := os.Stat(root); err != nil {
				t.Fatal("unconfirmed root was deleted", err)
			}
		})
	}
}

func TestPyTorchAllocationCASRefusalPreservesUnrecordedRoot(t *testing.T) {
	j, state, _, _, root := pyTorchFixture(t)
	changed := state
	changed.ApprovedPlanID = "changed"
	writePyTorchState(t, j, changed)
	if _, _, err := j.allocatePyTorchRoot(context.Background(), state, root); err == nil {
		t.Fatal("allocation accepted changed approval")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("unrecorded root was deleted", err)
	}
	current, err := j.Load(context.Background())
	if err != nil || current.Effects["pytorch-engine"].ComponentRootKey != "" {
		t.Fatal("failed CAS adopted root", err)
	}
}

func TestPyTorchRetryCannotRaceOwnedRemoval(t *testing.T) {
	for _, kind := range []string{"removing", "blocked-behind-removal"} {
		t.Run(kind, func(t *testing.T) {
			j, state, _, _, root := pyTorchFixture(t)
			state, _, err := j.allocatePyTorchRoot(context.Background(), state, root)
			if err != nil {
				t.Fatal(err)
			}
			r := NewRunner(j, nil)
			r.featureCheck = func() (FeatureStatus, error) { return FeatureStatus{Installed: true}, nil }
			oldCLI, oldRegistry := pyTorchCLI, setupEngineRegistry
			installCalls, removeCalls := 0, 0
			pyTorchCLI = func(_ context.Context, args ...string) error {
				if len(args) < 2 || args[0] != "setup" {
					t.Fatal("unexpected CLI", args)
				}
				if args[1] == "install" {
					installCalls++
					return errors.New("unexpected fixture install")
				}
				removeCalls++
				return os.RemoveAll(root) // this fixture's tiny generated root
			}
			events := []string{}
			setupEngineRegistry = func(context.Context) (engineRegistry, error) { return &theNoiseRegistryFake{events: &events}, nil }
			t.Cleanup(func() { pyTorchCLI, setupEngineRegistry = oldCLI, oldRegistry })
			if kind == "removing" {
				state, err = j.recordPyTorchRemoval(context.Background(), state, false)
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(j.Path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.Retry(context.Background(), "pytorch-engine"); err == nil {
					t.Fatal("Removing installation retried")
				}
				after, err := os.ReadFile(j.Path)
				if err != nil || string(before) != string(after) {
					t.Fatal("refused retry changed ownership", err)
				}
			} else {
				controller, err := DefaultPyTorchController(j)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				queued, done := make(chan struct{}), make(chan error, 1)
				r.crashAfter = func(step string) error {
					if step == "before-pytorch-provider-operation" {
						close(queued)
					}
					return nil
				}
				err = controller.withProviderOperation(ctx, func() error {
					go func() { _, err := r.Retry(ctx, "pytorch-engine"); done <- err }()
					select {
					case <-queued:
					case <-ctx.Done():
						return ctx.Err()
					}
					// The retry has read Staged+key and reaches the same operation
					// already held by removal. Complete removal before releasing it.
					return r.removePyTorchLocked(ctx)
				})
				if err != nil {
					t.Fatal("fixture removal", err)
				}
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("stale retry accepted removed root")
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				current, err := j.Load(ctx)
				if err != nil || current.Effects["pytorch-engine"].Phase != Pending || current.Effects["pytorch-engine"].ComponentRootKey != "" {
					t.Fatal("retry recreated removed ownership", err)
				}
				if removeCalls != 1 {
					t.Fatal("removal dispatch count", removeCalls)
				}
				if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("stale retry recreated root", err)
				}
			}
			if installCalls != 0 {
				t.Fatal("stale/removing retry installed component", installCalls)
			}
		})
	}
}

func TestPyTorchRemovingIntentRefusesCheckedActivation(t *testing.T) {
	j, state, _, _, root := pyTorchFixture(t)
	state, _, err := j.allocatePyTorchRoot(context.Background(), state, root)
	if err != nil {
		t.Fatal(err)
	}
	writePyTorchReady(t, root)
	effect := state.Effects["pytorch-engine"]
	effect.Phase, effect.Removing = Checked, true
	state.Effects["pytorch-engine"] = effect
	writePyTorchState(t, j, state)
	before, err := os.ReadFile(j.Path)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(j, nil)
	r.featureCheck = func() (FeatureStatus, error) { return FeatureStatus{Installed: true}, nil }
	old := pyTorchCLI
	pyTorchCLI = func(context.Context, ...string) error { t.Fatal("removing activation ran CLI"); return nil }
	t.Cleanup(func() { pyTorchCLI = old })
	if _, err := r.Advance(context.Background(), "pytorch-engine"); err == nil {
		t.Fatal("Checked activation discarded removal intent")
	}
	if _, err := j.Update(context.Background(), state.Plan.ID, "pytorch-engine", effect, Effect{Phase: Installed}); err == nil {
		t.Fatal("journal allowed removal-intent loss")
	}
	after, err := os.ReadFile(j.Path)
	if err != nil || string(before) != string(after) {
		t.Fatal("refused activation changed ownership", err)
	}
	if err := pyTorchRootMatches(effect, root); err != nil {
		t.Fatal("refused activation changed root", err)
	}
}

func TestPyTorchPackageModelApprovalAndOwnerRoster(t *testing.T) {
	plan := PyTorchModelPlan()
	if plan.Engine != PyTorch || plan.ID == TheNoiseModelPlan().ID || len(plan.Assets) != len(torch.Flux2Klein4BCompactTorch.Files) {
		t.Fatal("model binding/tokenizer roster")
	}
	for i, asset := range plan.Assets {
		pin := torch.Flux2Klein4BCompactTorch.Files[i]
		if asset.Role != pin.Role || asset.Download.SHA256 != pin.SHA256 || asset.Download.Bytes != pin.Size || !strings.Contains(asset.Download.URL, "/resolve/"+pin.Revision+"/") {
			t.Fatal("model pin drift", asset)
		}
	}
	c := choiceController(t, plan, nil, &choiceTransferFake{})
	c.StageModel = func(context.Context, string, string, map[string]string, func(int64, int64) error) error {
		t.Fatal("unapproved model effect")
		return nil
	}
	if _, err := c.Configure(context.Background(), false, nil); err == nil {
		t.Fatal("package replaced model approval")
	}
	if _, err := c.Start(context.Background(), approve(TheNoiseModelPlan())); err == nil {
		t.Fatal("wrong engine model consent")
	}
	if _, err := c.Start(context.Background(), approve(plan)); err != nil {
		t.Fatal(err)
	}
	c.Guard = func(context.Context) error { return errors.New("package changed") }
	if _, err := c.Configure(context.Background(), false, nil); err == nil {
		t.Fatal("stale package allowed model effect")
	}
	for _, target := range []struct{ os, arch string }{{"windows", "arm64"}, {"linux", "amd64"}, {"windows", "amd64"}} {
		catalog := catalogForTarget(target.os, target.arch)
		p, err := catalog.Plan([]AppID{PyTorch})
		if err != nil || p.Preparable || len(p.Blockers) == 0 {
			t.Fatalf("unbuilt native payload claimed: %+v %v", p, err)
		}
	}
}

func TestPyTorchReadinessAndOwnedRemovalPreservePersonalState(t *testing.T) {
	for _, kind := range []string{"applied", "refused", "foreign", "disabled", "unowned", "staged", "checked", "resume", "lost-remove-reply", "missing", "missing-unowned", "retained-dependent", "retained-dependent-alias", "retained-other-engine"} {
		t.Run(kind, func(t *testing.T) {
			j, state, a, inputs, root := pyTorchFixture(t)
			state, _, err := j.allocatePyTorchRoot(context.Background(), state, root)
			if err != nil {
				t.Fatal(err)
			}
			writePyTorchReady(t, root)
			e := state.Effects["pytorch-engine"]
			e.Phase = Installed
			if kind == "staged" {
				e.Phase = Staged
				if err := os.Remove((torch.Component{Root: root}).Manifest()); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "checked" {
				e.Phase = Checked
			}
			e.Removing = kind == "resume"
			state.Effects["pytorch-engine"] = e
			d, err := expectedPyTorchDeclaration(a, []string{torch.Flux2Klein4BCompactTorch.Name})
			if err != nil {
				t.Fatal(err)
			}
			state.EngineDeclarations = map[AppID]fwire.DeclarationWithExtensions{PyTorch: d}
			if kind == "unowned" || kind == "missing-unowned" || kind == "staged" || kind == "checked" {
				delete(state.EngineDeclarations, PyTorch)
			}
			if strings.HasPrefix(kind, "retained-") {
				source, archive, _, _ := nativeArchiveFixture(t)
				archiveRoot, err := installPath(archive)
				if err != nil {
					t.Fatal(err)
				}
				if err := InstallArchive(context.Background(), archive, source, archiveRoot); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(archiveRoot, nativeResourceDirectory(archive), "standalone-env", "Lib", "site-packages", "oa-pytorch.pth")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				site := pyTorchSitePackages(root)
				if kind == "retained-other-engine" {
					site = pyTorchSitePackages(filepath.Join(filepath.Dir(root), "other"))
				}
				if kind == "retained-dependent-alias" {
					if err := os.MkdirAll(site, 0700); err != nil {
						t.Fatal(err)
					}
					alias := filepath.Join(t.TempDir(), "alias")
					if runtime.GOOS == "windows" {
						err = placetest.Junction(alias, site)
					} else {
						err = os.Symlink(site, alias)
					}
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := os.Remove(alias); err != nil {
							t.Error(err)
						}
					})
					site = alias
				}
				if err := os.WriteFile(path, []byte("# Setup places the engine site here\n"+site+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				state.RetainedArchive = &KeptPackage{Artifact: archive, Effect: Effect{Phase: Installed, Revision: 1}}
			}
			writePyTorchState(t, j, state)
			if kind == "staged" || kind == "checked" {
				if _, _, _, err := ownedPyTorch(context.Background(), j); err == nil {
					t.Fatal("unfinished phase allowed model configuration")
				}
			}
			_, _, providerState, models, _ := pyTorchFolders(a)
			personal := []string{filepath.Join(models, "personal.safetensors"), filepath.Join(providerState, "content", "picture.png"), filepath.Join(providerState, "audit.jsonl"), filepath.Join(providerState, "personal.places")}
			for _, p := range personal {
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("personal"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			events := []string{}
			registry := &theNoiseRegistryFake{declaration: &d, readiness: fwire.DeclarationReadinessIdle, withdrawOutcome: fwire.DeclarationEditOutcomeApplied, events: &events}
			if kind == "staged" || kind == "checked" {
				registry.declaration = nil
			}
			if kind == "refused" {
				registry.withdrawOutcome = fwire.DeclarationEditOutcomeConflict
			}
			if kind == "foreign" {
				copy := d
				copy.Declaration.Arguments = slices.Clone(d.Declaration.Arguments)
				copy.Declaration.Arguments = append(copy.Declaration.Arguments, "foreign")
				registry.declaration = &copy
			}
			if kind == "disabled" {
				registry.readiness = fwire.DeclarationReadinessDisabled
			}
			// The serve listing reads a deleted program as disabled/program:missing.
			if kind == "missing" || kind == "missing-unowned" {
				registry.readiness, registry.why = fwire.DeclarationReadinessDisabled, "program:missing"
			}
			oldRegistry, oldCLI := setupEngineRegistry, pyTorchCLI
			removeCalls := 0
			setupEngineRegistry = func(context.Context) (engineRegistry, error) { return registry, nil }
			pyTorchCLI = func(_ context.Context, args ...string) error {
				if !slices.Equal(args, []string{"setup", "remove", "--root", root, "--expected-root-key", e.ComponentRootKey}) {
					t.Fatal("unexpected effect", args)
				}
				current, err := j.Load(context.Background())
				if err != nil || !current.Effects["pytorch-engine"].Removing || current.Effects["pytorch-engine"].ComponentRootKey != e.ComponentRootKey {
					t.Fatal("CLI removal preceded durable same-key Removing intent", err)
				}
				removeCalls++
				if kind == "lost-remove-reply" && removeCalls == 1 {
					if err := os.Remove((torch.Component{Root: root}).Manifest()); err != nil {
						return err
					}
					return errors.New("fixture lost reply after profile/manifest removal")
				}
				if kind != "staged" && kind != "checked" && !slices.Contains(events, "withdraw/halt") {
					t.Fatal("removal preceded synchronous halt")
				}
				events = append(events, "CLI grants/profile/engine removal")
				return os.RemoveAll(root) // tiny source-fixture tree only
			}
			t.Cleanup(func() { setupEngineRegistry, pyTorchCLI = oldRegistry, oldCLI })
			ready, err := pyTorchReady(context.Background(), j, false)
			if err != nil {
				t.Fatal(err)
			}
			if ready != (kind == "applied" || kind == "refused" || kind == "lost-remove-reply" || strings.HasPrefix(kind, "retained-")) {
				t.Fatal("readiness adopted changed/foreign state", kind, ready)
			}
			if len(events) != 0 {
				t.Fatal("readiness effected a process", events)
			}
			err = NewRunner(j, nil).RemovePyTorch(context.Background())
			if kind == "lost-remove-reply" {
				if err == nil {
					t.Fatal("lost removal reply claimed completion")
				}
				current, loadErr := j.Load(context.Background())
				if loadErr != nil || !current.Effects["pytorch-engine"].Removing || current.Effects["pytorch-engine"].ComponentRootKey != e.ComponentRootKey {
					t.Fatal("interruption lost ownership", loadErr)
				}
				err = NewRunner(j, nil).RemovePyTorch(context.Background())
			}
			success := kind == "applied" || kind == "staged" || kind == "checked" || kind == "resume" || kind == "lost-remove-reply" || kind == "missing" || kind == "retained-other-engine"
			if (err == nil) != success {
				t.Fatalf("%s remove: %v", kind, err)
			}
			if strings.HasPrefix(kind, "retained-dependent") && (!strings.Contains(err.Error(), "remove the earlier") || len(events) != 0 || removeCalls != 0) {
				t.Fatalf("dependent engine was not refused before mutation: %v, %v, %d", err, events, removeCalls)
			}
			for _, p := range personal {
				raw, err := os.ReadFile(p)
				if err != nil || string(raw) != "personal" {
					t.Fatal("personal data changed", p, err)
				}
			}
			if success {
				wantEvents := []string{"withdraw/halt", "CLI grants/profile/engine removal"}
				if kind == "staged" || kind == "checked" {
					wantEvents = wantEvents[1:]
				}
				if !reflect.DeepEqual(events, wantEvents) {
					t.Fatal("wrong ordering", events)
				}
				if _, err := os.Stat(inputs); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("owned inputs retained", err)
				}
			} else if _, err := os.Stat(root); err != nil {
				t.Fatal("refused removal deleted engine", err)
			}
		})
	}
}

func TestPyTorchSameArtifactRetainsOwnershipAcrossOtherSetupChoices(t *testing.T) {
	j, state, _, _, root := pyTorchFixture(t)
	state, _, err := j.allocatePyTorchRoot(context.Background(), state, root)
	if err != nil {
		t.Fatal(err)
	}
	e := state.Effects["pytorch-engine"]
	e.Phase = Installed
	state.Effects["pytorch-engine"] = e
	writePyTorchState(t, j, state)
	combined, err := j.Catalog().Plan([]AppID{PyTorch, Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	next, err := j.SavePlan(context.Background(), combined)
	if err != nil || next.Effects["pytorch-engine"].ComponentRootKey != e.ComponentRootKey {
		t.Fatal("unrelated app setup lost/blocked ownership", err)
	}
	dropped, _ := j.Catalog().Plan([]AppID{Lemonade})
	if _, err := j.SavePlan(context.Background(), dropped); err == nil {
		t.Fatal("dropping native component lost removal proof")
	}
	changed := j.Catalog()
	for i := range changed.Components {
		if changed.Components[i].ID == "pytorch-engine" {
			copy := *changed.Components[i].Artifact
			copy.Version = "next-stack"
			changed.Components[i].Artifact = &copy
		}
	}
	j.catalogOverride = &changed
	replacement, err := changed.Plan([]AppID{PyTorch, Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.SavePlan(context.Background(), replacement); err == nil {
		t.Fatal("native replacement discarded original component key")
	}
}

func TestPyTorchConfiguredModelRefusesWithdrawnRegistrationWithoutEffects(t *testing.T) {
	j, state, a, _, root := pyTorchFixture(t)
	state, _, err := j.allocatePyTorchRoot(context.Background(), state, root)
	if err != nil {
		t.Fatal(err)
	}
	writePyTorchReady(t, root)
	e := state.Effects["pytorch-engine"]
	e.Phase = Installed
	state.Effects["pytorch-engine"] = e
	d, err := expectedPyTorchDeclaration(a, []string{torch.Flux2Klein4BCompactTorch.Name})
	if err != nil {
		t.Fatal(err)
	}
	state.EngineDeclarations = map[AppID]fwire.DeclarationWithExtensions{PyTorch: d}
	writePyTorchState(t, j, state)
	c, err := DefaultPyTorchController(j)
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
	registry := &theNoiseRegistryFake{readiness: fwire.DeclarationReadinessIdle, events: &events}
	oldRegistry, oldCLI := setupEngineRegistry, pyTorchCLI
	setupEngineRegistry = func(context.Context) (engineRegistry, error) { return registry, nil }
	pyTorchCLI = func(context.Context, ...string) error {
		t.Fatal("withdrawn provider readiness replayed a CLI effect")
		return nil
	}
	t.Cleanup(func() { setupEngineRegistry, pyTorchCLI = oldRegistry, oldCLI })
	if status, err := c.Status(context.Background()); err != nil || status.Ready {
		t.Fatal("withdrawn registration remained Ready", err)
	}
	if _, err := c.Configure(context.Background(), false, nil); err == nil {
		t.Fatal("configured journal recreated withdrawn registration")
	}
	if registry.declares != 0 || len(events) != 0 {
		t.Fatal("status/configure effected registry", events)
	}
}

// The runtime input package is its own release component: once the plan
// that selects the engine is approved, the runner reserves its exact pinned
// download through the public job service, and never before.
func TestPyTorchInputPackageIsDownloadedOnlyAfterApproval(t *testing.T) {
	j, state, a, inputs, _ := pyTorchFixture(t)
	if err := os.RemoveAll(inputs); err != nil {
		t.Fatal(err)
	} // owned tiny fixture only
	state.Effects["pytorch-engine"] = Effect{Phase: Pending}
	state.ApprovedPlanID = ""
	writePyTorchState(t, j, state)
	transfer := &choiceTransferFake{}
	r := NewRunner(j, transfer)
	r.featureCheck = func() (FeatureStatus, error) { return FeatureStatus{Installed: true}, nil }
	if _, err := r.Advance(context.Background(), "pytorch-engine"); err == nil || len(transfer.reserved) != 0 {
		t.Fatal("an unapproved package reserved a download", transfer.reserved)
	}
	state.ApprovedPlanID = state.Plan.ID
	writePyTorchState(t, j, state)
	if _, err := r.Advance(context.Background(), "pytorch-engine"); err != nil {
		t.Fatal(err)
	}
	if len(transfer.reserved) != 1 || transfer.reserved[0].URL != a.URL || transfer.reserved[0].SHA256 != a.SHA256 || transfer.reserved[0].Bytes != a.Bytes {
		t.Fatalf("reserved %+v, want the pinned input package %s", transfer.reserved, a.URL)
	}
	current, err := j.Load(context.Background())
	if err != nil || current.Effects["pytorch-engine"].Phase != Requested || current.Effects["pytorch-engine"].Work == nil {
		t.Fatal("the download was not journaled as requested work", err)
	}
	if _, err := os.Stat(inputs); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("input staging installed before the download finished", err)
	}
}
