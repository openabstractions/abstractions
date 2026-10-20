package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	"os"
	"os/exec"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	thenoise "github.com/openabstractions/abstraction-provider-thenoise/go"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

const theNoiseProviderID = product.TheNoise

func theNoiseEnginePath(path string) error {
	if err := identity.LocalPath(path); err != nil {
		return err
	}
	if supervision.UTF16Len(filepath.Clean(path)) > thenoise.MaxEngineDirLength {
		return fmt.Errorf("TheNoise needs an engine folder of at most %d characters; this account's Setup path is too long", thenoise.MaxEngineDirLength)
	}
	if strings.ContainsAny(path, `"%!&|<>^`) {
		return errors.New("TheNoise's Setup path contains unsupported batch characters")
	}
	return nil
}

func theNoiseFolders() (root, state, models string, err error) {
	home, err := CurrentHome()
	if err != nil {
		return "", "", "", err
	}
	if home.Isolated {
		return "", "", "", errIsolatedProvider
	}
	root, err = installPath(theNoiseArtifact())
	return root, filepath.Join(home.Data, "OA", "p", "tn"), filepath.Join(home.Data, "OA", "m", "tn"), err
}

// The installed adapter is the sole CLI target. Browser requests never supply
// a program or arguments. This seam lets ordinary fixtures prove ordering.
var theNoiseCLI = func(ctx context.Context, args ...string) error {
	if err := providerHomeAllowed(); err != nil {
		return err
	}
	status, err := EngineFeatureStatus(TheNoise)
	if err != nil || !status.Installed {
		return errors.New("add the installed TheNoise Image feature first")
	}
	panel, err := os.Executable()
	if err != nil {
		return err
	}
	command := childproc.Hidden(exec.CommandContext(ctx, filepath.Join(filepath.Dir(panel), "thenoise-provider.exe"), args...))
	var failure cappedReply
	command.Stderr, command.Stdout = &failure, &failure
	if err := command.Run(); err != nil {
		return providerFailure("TheNoise", failure.String(), err)
	}
	return nil
}

func (r Runner) installTheNoiseFiles(ctx context.Context, a Artifact, root, archive string) error {
	if a != theNoiseArtifact() {
		return errors.New("TheNoise package differs from its reviewed pin")
	}
	if ok, err := ArchiveInstalled(a, root); err != nil || !ok {
		return errors.New("TheNoise's staged managed package is not verified")
	}
	_, state, _, err := theNoiseFolders()
	if err != nil {
		return err
	}
	if err := verifyFile(archive, a); err != nil {
		return err
	}
	return theNoiseCLI(ctx, "engine", "install", "-engine-dir", filepath.Join(root, "e"), "-state", state, "-engine-zip", archive)
}

// ownedTheNoise requires the current approved plan and its installed effect.
// A matching personal tree or a marker alone establishes no Setup ownership.
func ownedTheNoise(ctx context.Context, journal Journal) (State, string, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return State{}, "", err
	}
	if state.Plan.ID == "" || state.ApprovedPlanID != state.Plan.ID || !slices.Contains(state.Plan.Selected, TheNoise) {
		return State{}, "", errors.New("approve the current TheNoise package plan first")
	}
	a, err := journal.ComponentArtifact(state, "thenoise-engine")
	if err != nil || a != theNoiseArtifact() || state.Effects["thenoise-engine"].Phase != Installed || state.Effects["thenoise-engine"].Removing {
		return State{}, "", errors.New("Setup has no current journal-owned TheNoise installation")
	}
	root, err := installPath(a)
	if err != nil {
		return State{}, "", err
	}
	if ok, err := archiveInstalled(a, root); err != nil || !ok {
		return State{}, "", errors.New("TheNoise's journal-owned package changed")
	}
	return state, root, nil
}

type engineRegistry interface {
	Declarations(context.Context) (fwire.DeclarationListV2, error)
	Declare(context.Context, string, fwire.DeclarationWithExtensions) (fwire.DeclarationChange, error)
	Withdraw(context.Context, string, string) (fwire.DeclarationChange, error)
	ReplacePinned(context.Context, string, fwire.PinnedDeclaration) (fwire.DeclarationChange, error)
}

var setupEngineRegistry = func(ctx context.Context) (engineRegistry, error) {
	return discoverRuntime().ResolveRegistryV2(ctx, client.Requirements{Scope: client.ScopeLocal})
}

func theNoiseDeclaration(root, state, models, program, runtimeProgram string) fwire.DeclarationWithExtensions {
	plan := TheNoiseModelPlan()
	paths := map[string]string{}
	for _, asset := range plan.Assets {
		paths[asset.ID] = asset.digestStagePath(models)
	}
	declaration := fwire.Declaration{Name: theNoiseProviderID, Program: program,
		Arguments: []string{"-endpoint", "{endpoint}", "-runtime", runtimeProgram,
			"-engine-dir", filepath.Join(root, "e"), "-state", state,
			"-store", filepath.Join(state, "content"), "-records", filepath.Join(state, "content.json"),
			"-audit", filepath.Join(state, "audit.jsonl"), "-dit", paths["main"], "-vae", paths["vae"],
			"-text-encoder", paths["encoder"], "-model", plan.Model, "-device", "cpu", "-resource", "memory"},
		Endpoint: theNoiseProviderID, Transport: fwire.DeclarationTransportNative,
		Contracts: []string{"abstraction.inference/image@1", "abstraction.storage/content-reader@1", "abstraction.storage/content-writer@1"},
		Resources: []string{"memory", "profile:image"}, Models: []string{"image:" + plan.Model},
		Activation: fwire.ActivationOnDemand, Role: fwire.DeclarationRoleProvider}
	extensions := []fwire.DeclarationExtension{}
	for _, key := range thenoise.SupportedExtensions() {
		extensions = append(extensions, fwire.DeclarationExtension{Profile: "image", Key: key})
	}
	return fwire.DeclarationWithExtensions{Declaration: declaration, SupportedExtensions: extensions}
}

func (j Journal) saveEngineDeclaration(ctx context.Context, planID string, engine AppID, declaration fwire.DeclarationWithExtensions) error {
	return cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		component := string(engine) + "-engine"
		if engine != TheNoise && engine != PyTorch || state.Plan.ID != planID || state.ApprovedPlanID != planID || state.Effects[component].Phase != Installed || state.Effects[component].Removing {
			return nil, errors.New("approved engine setup changed")
		}
		if err := j.Authorize(state.Plan); err != nil {
			return nil, err
		}
		if state.EngineDeclarations == nil {
			state.EngineDeclarations = map[AppID]fwire.DeclarationWithExtensions{}
		}
		if prior, ok := state.EngineDeclarations[engine]; ok && !reflect.DeepEqual(prior, declaration) {
			return nil, errors.New("existing engine declaration needs explicit recovery")
		}
		state.EngineDeclarations[engine] = declaration
		return json.Marshal(state)
	})
}

// registryProgramMissing is the reason the runtime gives a declaration whose
// program is gone; the Panel shows it as "Program not found".
const registryProgramMissing = "program:missing"

// errRegistryProgramMissing marks a disabled row that is a leftover of a
// deleted program. The runtime disabled it, so it is no person's choice.
var errRegistryProgramMissing = errors.New("the provider's program is missing")

var errForeignDeclaration = errors.New("a different provider declaration owns this name; it was preserved")

func registryDeclaration(ctx context.Context, registry engineRegistry, expected fwire.DeclarationWithExtensions) (fwire.DeclarationListV2, bool, error) {
	page, err := registry.Declarations(ctx)
	if err != nil {
		return page, false, err
	}
	if page.Outcome != fwire.DeclarationListOutcomePage || page.Revision == "" {
		return page, false, errors.New("the provider registry refused its current declarations")
	}
	for _, row := range page.Declarations {
		if row.State.Declaration.Name != expected.Declaration.Name {
			continue
		}
		if row.State.Readiness == fwire.DeclarationReadinessDisabled {
			if row.State.Why == registryProgramMissing {
				// The installation's and a product's declarations are theirs. The
				// runtime lists them disabled with why operator, never program:missing
				// (facade FAC-R7; serve providerFile.disabledReason), so this check
				// is a second guard.
				if row.State.DeclaredBy == "installation" || row.State.DeclaredBy == "product" {
					return page, false, errForeignDeclaration
				}
				return page, false, errRegistryProgramMissing
			}
			return page, false, errors.New("the provider was disabled; Setup preserves that choice")
		}
		actual := fwire.DeclarationWithExtensions{Declaration: row.State.Declaration, SupportedExtensions: row.SupportedExtensions}
		if !sameEngineDeclaration(actual, expected) {
			return page, false, errForeignDeclaration
		}
		return page, true, nil
	}
	return page, false, nil
}

// sameEngineDeclaration is whether a registered declaration is the one
// Setup expects, up to private path spelling and to the wire's empty lists:
// the registry codec omits an empty extension, guarantee, resource or model
// list, so a listed row reads it back as nil where Setup's journal holds [].
func sameEngineDeclaration(actual, expected fwire.DeclarationWithExtensions) bool {
	actual, expected = wireEmptyLists(actual), wireEmptyLists(expected)
	if !samePrivatePath(actual.Declaration.Program, expected.Declaration.Program) {
		return false
	}
	actual.Declaration.Program = expected.Declaration.Program
	actual.Declaration.Arguments = slices.Clone(actual.Declaration.Arguments)
	if len(actual.Declaration.Arguments) != len(expected.Declaration.Arguments) {
		return false
	}
	for i := 1; i < len(actual.Declaration.Arguments); i++ {
		switch expected.Declaration.Arguments[i-1] {
		case "-runtime", "-engine-dir", "-state", "-store", "-records", "-audit", "-dit", "-vae", "-text-encoder", "--runtime-program", "--root", "--source", "--state":
			if !samePrivatePath(actual.Declaration.Arguments[i], expected.Declaration.Arguments[i]) {
				return false
			}
			actual.Declaration.Arguments[i] = expected.Declaration.Arguments[i]
		}
	}
	return reflect.DeepEqual(actual, expected)
}

// wireEmptyLists spells every empty list of d as nil, as a registry row
// decoded from the wire spells it.
func wireEmptyLists(d fwire.DeclarationWithExtensions) fwire.DeclarationWithExtensions {
	empty := func(list []string) []string {
		if len(list) == 0 {
			return nil
		}
		return list
	}
	d.Declaration.Arguments = empty(d.Declaration.Arguments)
	d.Declaration.Contracts = empty(d.Declaration.Contracts)
	d.Declaration.Guarantees = empty(d.Declaration.Guarantees)
	d.Declaration.Resources = empty(d.Declaration.Resources)
	d.Declaration.Models = empty(d.Declaration.Models)
	if len(d.SupportedExtensions) == 0 {
		d.SupportedExtensions = nil
	}
	return d
}

func expectedTheNoiseDeclaration(root string) (fwire.DeclarationWithExtensions, error) {
	_, state, models, err := theNoiseFolders()
	if err != nil {
		return fwire.DeclarationWithExtensions{}, err
	}
	panel, err := os.Executable()
	if err != nil {
		return fwire.DeclarationWithExtensions{}, err
	}
	return theNoiseDeclaration(root, state, models, filepath.Join(filepath.Dir(panel), "thenoise-provider.exe"), filepath.Join(filepath.Dir(panel), "openabstractions.exe")), nil
}

func configureTheNoise(ctx context.Context, journal Journal, assets []ModelAsset, files map[string]string) error {
	owned, root, err := ownedTheNoise(ctx, journal)
	if err != nil {
		return err
	}
	_, _, models, err := theNoiseFolders()
	if err != nil {
		return err
	}
	for _, asset := range assets {
		path := files[assetRoles[asset.ID]]
		expected := asset.digestStagePath(models)
		if !samePrivatePath(path, expected) {
			return errors.New("TheNoise model input differs from its fixed verified stage")
		}
		if err := checkModelStage(ctx, path, asset.Download); err != nil {
			return err
		}
	}
	declaration, err := expectedTheNoiseDeclaration(root)
	if err != nil {
		return err
	}
	return registerTheNoise(ctx, journal, owned, declaration)
}

// Registration follows the fixed model checks above. A durable exact intent
// permits lost-reply reconciliation; a confirmed no-effect reply clears it.
func registerTheNoise(ctx context.Context, journal Journal, owned State, declaration fwire.DeclarationWithExtensions) error {
	return registerSetupEngine(ctx, journal, owned, TheNoise, declaration)
}

func registerSetupEngine(ctx context.Context, journal Journal, owned State, engine AppID, declaration fwire.DeclarationWithExtensions) error {
	registry, err := setupEngineRegistry(ctx)
	if err != nil {
		return err
	}
	if prior, ok := owned.EngineDeclarations[engine]; ok && !sameEngineDeclaration(prior, declaration) {
		// Setup's own registration serves other models than it now should:
		// it is withdrawn and declared afresh.
		if err := withdrawSetupEngine(ctx, journal, registry, owned.Plan.ID, engine, prior); err != nil {
			return err
		}
	}
	page, found, err := registryDeclaration(ctx, registry, declaration)
	if errors.Is(err, errRegistryProgramMissing) {
		// Setup reinstalls the program, so the leftover declaration of the
		// deleted one is withdrawn and declared afresh.
		change, werr := registry.Withdraw(ctx, page.Revision, declaration.Declaration.Name)
		if werr != nil {
			return werr
		}
		// applied with reason disabled kept the declaration: it was never Setup's.
		if change.Outcome != fwire.DeclarationEditOutcomeApplied || change.Reason == "disabled" {
			return errors.New("the registry did not withdraw the provider whose program is missing")
		}
		page, found, err = registryDeclaration(ctx, registry, declaration)
	}
	if err != nil {
		return err
	}
	if found {
		prior, ok := owned.EngineDeclarations[engine]
		if !ok || !sameEngineDeclaration(prior, declaration) {
			return errors.New("the existing provider declaration has no Setup journal ownership; it was preserved")
		}
		return nil
	}
	if err := journal.saveEngineDeclaration(ctx, owned.Plan.ID, engine, declaration); err != nil {
		return err
	}
	change, err := registry.Declare(ctx, page.Revision, declaration)
	if err != nil {
		return err
	}
	if change.Outcome == fwire.DeclarationEditOutcomeConflict || change.Outcome == fwire.DeclarationEditOutcomeInvalid || change.Outcome == fwire.DeclarationEditOutcomeForbidden {
		if err := cas.ChangeLimitContext(ctx, journal.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
			state, err := decodeState(raw)
			if err != nil {
				return nil, err
			}
			if state.Plan.ID != owned.Plan.ID || !reflect.DeepEqual(state.EngineDeclarations[engine], declaration) {
				return nil, errors.New("declaration intent changed during refusal recovery")
			}
			delete(state.EngineDeclarations, engine)
			return json.Marshal(state)
		}); err != nil {
			return err
		}
	}
	if change.Outcome != fwire.DeclarationEditOutcomeApplied {
		return errors.New("the registry did not confirm the fixed engine declaration; inspect and reconcile before retry")
	}
	return nil
}

// withdrawSetupEngine withdraws Setup's own recorded declaration prior of
// engine and drops it from the journal, so a changed declaration can be
// declared in its place. A declaration the registry lists differently is
// another owner's and stays.
func withdrawSetupEngine(ctx context.Context, journal Journal, registry engineRegistry, planID string, engine AppID, prior fwire.DeclarationWithExtensions) error {
	page, found, err := registryDeclaration(ctx, registry, prior)
	if errors.Is(err, errRegistryProgramMissing) {
		found, err = true, nil
	}
	if err != nil {
		return err
	}
	if found {
		change, err := registry.Withdraw(ctx, page.Revision, prior.Declaration.Name)
		if err != nil {
			return err
		}
		if change.Outcome != fwire.DeclarationEditOutcomeApplied || change.Reason == "disabled" {
			return errors.New("the registry did not withdraw Setup's earlier engine declaration; it was preserved")
		}
	}
	return cas.ChangeLimitContext(ctx, journal.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID != planID || !reflect.DeepEqual(state.EngineDeclarations[engine], prior) {
			return nil, errors.New("declaration intent changed while its registration was replaced")
		}
		delete(state.EngineDeclarations, engine)
		return json.Marshal(state)
	})
}

// RemoveTheNoise removes only the current Setup-owned engine. The registry's
// Applied withdrawal returns after its supervisor has halted the owned tree.
// Personal engines, model files, content, results and audit stay in place.
func (r Runner) RemoveTheNoise(ctx context.Context) error {
	controller, err := DefaultTheNoiseController(r.Journal)
	if err != nil {
		return err
	}
	return controller.withProviderOperation(ctx, func() error { return r.removeTheNoiseLocked(ctx, controller) })
}

func (r Runner) removeTheNoiseLocked(ctx context.Context, controller ModelController) error {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return err
	}
	a, err := r.Journal.ComponentArtifact(state, "thenoise-engine")
	if err != nil || state.Plan.ID == "" || state.ApprovedPlanID != state.Plan.ID || a != theNoiseArtifact() || state.Effects["thenoise-engine"].Phase != Installed {
		return errors.New("Setup has no current journal-owned TheNoise installation")
	}
	root, err := installPath(a)
	if err != nil {
		return err
	}
	if !state.Effects["thenoise-engine"].Removing {
		if _, _, err := ownedTheNoise(ctx, r.Journal); err != nil {
			return err
		}
	}
	expected, err := expectedTheNoiseDeclaration(root)
	if err != nil {
		return err
	}
	declaration, configured := state.EngineDeclarations[TheNoise]
	if configured && !sameEngineDeclaration(declaration, expected) {
		return errors.New("the journal's TheNoise declaration differs from its fixed owned paths")
	}
	registry, err := setupEngineRegistry(ctx)
	if err != nil {
		return err
	}
	page, found, err := registryDeclaration(ctx, registry, expected)
	if errors.Is(err, errRegistryProgramMissing) {
		found, err = true, nil
	}
	if err != nil {
		return err
	}
	if found {
		if !configured {
			return errors.New("the TheNoise declaration has no Setup ownership; it was preserved")
		}
		change, err := registry.Withdraw(ctx, page.Revision, declaration.Declaration.Name)
		if err != nil {
			return err
		}
		if change.Outcome != fwire.DeclarationEditOutcomeApplied {
			return errors.New("TheNoise withdrawal did not confirm its owned processes stopped; engine preserved")
		}
	}
	_, providerState, _, err := theNoiseFolders()
	if err != nil {
		return err
	}
	if !state.Effects["thenoise-engine"].Removing {
		if err := theNoiseCLI(ctx, "engine", "unlock", "-engine-dir", filepath.Join(root, "e"), "-state", providerState); err != nil {
			return err
		}
		if err := r.Journal.recordTheNoiseRemoval(ctx, state, false); err != nil {
			return err
		}
	}
	_, err = removePackageTree(theNoiseArtifact(), root)
	if err != nil {
		return err
	}
	// Manifest/verification caches may have been reused by a personal CLI
	// installation. Their independent identity is unproven, so preserve them.
	state, err = r.Journal.Load(ctx)
	if err != nil {
		return err
	}
	if err := cas.ChangeLimitContext(ctx, controller.JournalPath, maxJournalBytes, func(raw []byte) ([]byte, error) {
		model, err := controller.decode(raw)
		if err != nil {
			return nil, err
		}
		model.ProviderConfigured, model.ProviderConfiguring = false, false
		model.ProviderFailure = "The owned engine was removed; reinstall it to configure these preserved model files"
		return json.Marshal(model)
	}); err != nil {
		return err
	}
	return r.Journal.recordTheNoiseRemoval(ctx, state, true)
}

func (j Journal) recordTheNoiseRemoval(ctx context.Context, expected State, finished bool) error {
	return cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID != expected.Plan.ID || state.ApprovedPlanID != state.Plan.ID || !reflect.DeepEqual(state.Effects["thenoise-engine"], expected.Effects["thenoise-engine"]) {
			return nil, errors.New("engine removal journal changed")
		}
		if err := j.Authorize(state.Plan); err != nil {
			return nil, err
		}
		effect := state.Effects["thenoise-engine"]
		effect.Removing, effect.Revision = !finished, effect.Revision+1
		if finished {
			effect.Phase, effect.Reason = Pending, "The owned engine was removed; model files, results and audit were preserved"
			state.Configuration["thenoise-model"] = false
			delete(state.EngineDeclarations, TheNoise)
		}
		state.Effects["thenoise-engine"] = effect
		return json.Marshal(state)
	})
}

func cpuPictureRuntime() starterPictureRecipe {
	return starterPictureRecipe{
		engine: TheNoise, plan: TheNoiseModelPlan, fixedController: cachedPictureController,
		fixedFactory: DefaultTheNoiseController, removePackage: Runner.RemoveTheNoise,
	}
}

func DefaultTheNoiseController(journal Journal) (ModelController, error) {
	_, state, models, err := theNoiseFolders()
	if err != nil {
		return ModelController{}, err
	}
	plan := TheNoiseModelPlan()
	assets := slices.Clone(plan.Assets)
	return ModelController{Plan: &plan, JournalPath: StarterModelJournal(journal, TheNoise),
		StageDir: models, ProviderRoot: state, Transfer: PublicTransfer{},
		Guard: func(ctx context.Context) error { _, _, err := ownedTheNoise(ctx, journal); return err },
		StageModel: func(ctx context.Context, _ string, _ string, files map[string]string, _ func(int64, int64) error) error {
			return configureTheNoise(ctx, journal, assets, files)
		},
		Probe: func(ctx context.Context, _ string) (bool, error) {
			owned, root, err := ownedTheNoise(ctx, journal)
			if err != nil {
				return false, err
			}
			declaration, ok := owned.EngineDeclarations[TheNoise]
			if !ok {
				return false, nil
			}
			expected, err := expectedTheNoiseDeclaration(root)
			if err != nil || !sameEngineDeclaration(declaration, expected) {
				return false, errors.New("the journal's engine binding differs from its reviewed paths")
			}
			for _, asset := range plan.Assets {
				if err := checkModelStage(ctx, asset.digestStagePath(models), asset.Download); err != nil {
					return false, err
				}
			}
			registry, err := setupEngineRegistry(ctx)
			if err != nil {
				return false, err
			}
			_, found, err := registryDeclaration(ctx, registry, declaration)
			if errors.Is(err, errRegistryProgramMissing) {
				return false, nil
			}
			return found, err
		}, Selected: func(ctx context.Context) (bool, error) { return theNoiseReady(ctx, journal) }}, nil
}

// Readiness reads current intent and registry identity, without hashing the
// engine/model bytes or starting a provider on a status poll.
func theNoiseReady(ctx context.Context, journal Journal) (bool, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return false, err
	}
	if state.Plan.ID == "" || state.ApprovedPlanID != state.Plan.ID || !slices.Contains(state.Plan.Selected, TheNoise) {
		return false, nil
	}
	a, err := journal.ComponentArtifact(state, "thenoise-engine")
	if err != nil || a != theNoiseArtifact() || state.Effects["thenoise-engine"].Phase != Installed || state.Effects["thenoise-engine"].Removing {
		return false, nil
	}
	root, err := installPath(a)
	if err != nil {
		return false, err
	}
	expected, err := expectedTheNoiseDeclaration(root)
	if err != nil {
		return false, err
	}
	if owned, ok := state.EngineDeclarations[TheNoise]; !ok || !sameEngineDeclaration(owned, expected) {
		return false, nil
	}
	registry, err := setupEngineRegistry(ctx)
	if err != nil {
		return false, err
	}
	page, err := registry.Declarations(ctx)
	if err != nil {
		return false, err
	}
	if page.Outcome != fwire.DeclarationListOutcomePage || page.Revision == "" {
		return false, nil
	}
	for _, row := range page.Declarations {
		if row.State.Declaration.Name != expected.Declaration.Name {
			continue
		}
		actual := fwire.DeclarationWithExtensions{Declaration: row.State.Declaration, SupportedExtensions: row.SupportedExtensions}
		return sameEngineDeclaration(actual, expected) && (row.State.Readiness == fwire.DeclarationReadinessIdle || row.State.Readiness == fwire.DeclarationReadinessReady), nil
	}
	return false, nil
}
