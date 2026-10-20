package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"

	cas "github.com/openabstractions/abstraction-cas/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
)

const pyTorchProviderID = "pytorch-image"
const pyTorchInputBundle = "pytorch-2.11.0-rocm7.13.0-cpython3.12.10-gfx1151-inputs.zip"

func pyTorchFolders(a Artifact) (inputs, component, state, models string, err error) {
	home, err := CurrentHome()
	if err != nil {
		return "", "", "", "", err
	}
	if home.Isolated {
		return "", "", "", "", errIsolatedProvider
	}
	if a.InstallIdentity != "pt" {
		return "", "", "", "", errors.New("unknown PyTorch input identity")
	}
	inputs, err = installPath(a)
	return inputs, filepath.Join(filepath.Dir(inputs), "c"), filepath.Join(home.Data, "OA", "p", "pt"),
		filepath.Join(home.Data, "OA", "m", "pt", torch.Flux2Klein4BTorch.Name), err
}

// Only the installed adapter receives fixed arguments from Setup. Ordinary
// source fixtures replace this seam; browser callers supply no command/path.
var pyTorchCLI = func(ctx context.Context, args ...string) error {
	if err := lemonadeHomeAllowed(); err != nil {
		return err
	}
	status, err := EngineFeatureStatus(PyTorch)
	if err != nil || !status.Installed {
		return errors.New("add the installed PyTorch Image feature first")
	}
	panel, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, filepath.Join(filepath.Dir(panel), "pytorchd.exe"), args...)
	var failure cappedReply
	command.Stdout, command.Stderr = &failure, &failure
	if err := command.Run(); err != nil {
		return lemonadeFailure(failure.String(), err)
	}
	return nil
}

var installedPyTorchBundle = func() (string, error) {
	panel, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(filepath.Dir(panel)), "engines", "pytorch", pyTorchInputBundle), nil
}

// The beginner package receives the exact runtime inputs from its optional
// MSI feature. It never searches the person's Python or wheel installations.
func (r Runner) stageInstalledPyTorchInputs(ctx context.Context, a Artifact) (string, error) {
	source, err := installedPyTorchBundle()
	if err != nil {
		return "", err
	}
	if err := verifyFile(source, a); err != nil {
		return "", fmt.Errorf("the installed PyTorch input bundle differs from its reviewed package: %w", err)
	}
	dir := filepath.Join(filepath.Dir(r.Journal.Path), "downloads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	target := filepath.Join(dir, a.SHA256+".zip")
	if err := verifyFile(target, a); err == nil {
		return target, nil
	}
	if err := absent(target); err != nil {
		return "", err
	}
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".pytorch-inputs-")
	if err != nil {
		return "", err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	n, err := io.Copy(&boundedWriter{writer: out, remaining: a.Bytes}, in)
	if err != nil || n != a.Bytes {
		return "", errors.New("the installed PyTorch input copy did not finish")
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := verifyFile(out.Name(), a); err != nil {
		return "", err
	}
	if err := os.Link(out.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}

func pyTorchRootMatches(effect Effect, root string) error {
	if effect.ComponentRootKey == "" {
		return errors.New("Setup has no durable ownership of this PyTorch component; the folder was preserved")
	}
	key, err := identity.FileKey(root)
	if err != nil || key != effect.ComponentRootKey {
		return errors.New("the PyTorch component root was replaced or is missing; Setup preserved it")
	}
	return nil
}

// The sandbox owner reads its own manifest. Ready is reconciliation proof,
// not a claim that native execution or security qualification has passed.
func pyTorchComponentReady(effect Effect, root string) (bool, error) {
	if err := pyTorchRootMatches(effect, root); err != nil {
		return false, err
	}
	component := torch.Component{Root: root}
	profile, err := component.Profile()
	if err != nil {
		return false, err
	}
	manifest, err := sandbox.ReadManifest(component.Manifest())
	if errors.Is(err, sandbox.ErrNotInstalled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return manifest.Profile == profile && manifest.Phase == sandbox.PhaseReady && manifest.SID != "", nil
}

func (j Journal) allocatePyTorchRoot(ctx context.Context, expected State, root string) (State, bool, error) {
	effect := expected.Effects["pytorch-engine"]
	if effect.ComponentRootKey != "" {
		return expected, false, pyTorchRootMatches(effect, root)
	}
	if err := identity.LocalPath(root); err != nil {
		return expected, false, err
	}
	// Mkdir is the atomic absence check; an existing personal root is kept.
	if err := os.Mkdir(root, 0700); err != nil {
		return expected, false, fmt.Errorf("existing PyTorch component data is preserved: %w", err)
	}
	key, err := identity.FileKey(root)
	if err != nil {
		return expected, false, err
	}
	var saved State
	err = cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID != expected.Plan.ID || state.ApprovedPlanID != state.Plan.ID || !reflect.DeepEqual(state.Effects["pytorch-engine"], effect) || effect.Phase != Staged {
			return nil, errors.New("PyTorch allocation approval changed; preserve the unrecorded root for inspection")
		}
		if err := j.Authorize(state.Plan); err != nil {
			return nil, err
		}
		current, err := identity.FileKey(root)
		if err != nil || current != key {
			return nil, errors.New("the allocated PyTorch root changed before its journal receipt")
		}
		effect.ComponentRootKey, effect.Revision = key, effect.Revision+1
		state.Effects["pytorch-engine"] = effect
		saved = state
		return json.Marshal(state)
	})
	// A failed CAS leaves the folder untouched and never invokes the CLI.
	if err != nil {
		return expected, false, err
	}
	return saved, true, nil
}

func (r Runner) preparePyTorchComponent(ctx context.Context, state State, a Artifact, inputs string) (State, error) {
	return r.installPyTorchComponent(ctx, state, a, inputs, false)
}

func (r Runner) installPyTorchComponent(ctx context.Context, state State, a Artifact, inputs string, explicitRetry bool) (State, error) {
	controller, err := DefaultPyTorchController(r.Journal)
	if err != nil {
		return state, err
	}
	if r.crashAfter != nil {
		if err := r.crashAfter("before-pytorch-provider-operation"); err != nil {
			return state, err
		}
	}
	result := state
	err = controller.withProviderOperation(ctx, func() error {
		current, err := r.Journal.Load(ctx)
		if err != nil {
			return err
		}
		result = current
		if current.Plan.ID != state.Plan.ID || !reflect.DeepEqual(current.Effects["pytorch-engine"], state.Effects["pytorch-engine"]) {
			return errors.New("PyTorch component approval changed while waiting; reload before retry")
		}
		result, err = r.installPyTorchComponentLocked(ctx, current, a, inputs, explicitRetry)
		return err
	})
	return result, err
}

func (r Runner) installPyTorchComponentLocked(ctx context.Context, state State, a Artifact, inputs string, explicitRetry bool) (State, error) {
	current, err := r.Journal.ComponentArtifact(state, "pytorch-engine")
	if err != nil || current != a || state.ApprovedPlanID != state.Plan.ID || !slices.Contains(state.Plan.Selected, PyTorch) || state.Effects["pytorch-engine"].Phase != Staged || state.Effects["pytorch-engine"].Removing {
		return state, errors.New("approve the current fixed PyTorch package plan first")
	}
	if ok, err := ArchiveInstalled(a, inputs); err != nil || !ok {
		return state, errors.New("the approved PyTorch input tree changed")
	}
	want, component, providerState, _, err := pyTorchFolders(a)
	if err != nil || !samePrivatePath(inputs, want) {
		return state, errors.New("PyTorch inputs differ from the fixed approved package path")
	}
	state, created, err := r.Journal.allocatePyTorchRoot(ctx, state, component)
	if err != nil {
		return state, err
	}
	ready, err := pyTorchComponentReady(state.Effects["pytorch-engine"], component)
	if err != nil {
		return state, err
	}
	if ready {
		return state, nil
	}
	if !created && !explicitRetry {
		return state, errors.New("PyTorch installation was interrupted; use the explicit same-root retry after inspection")
	}
	if err := pyTorchCLI(ctx, "setup", "install", "--root", component, "--python-home", filepath.Join(inputs, "python"),
		"--wheels", filepath.Join(inputs, "wheels"), "--source", filepath.Join(inputs, "source", "infer"), "--state", providerState); err != nil {
		return state, err
	}
	ready, err = pyTorchComponentReady(state.Effects["pytorch-engine"], component)
	if err != nil || !ready {
		return state, errors.New("PyTorch's CLI did not leave a same-root Ready component; inspect before retry")
	}
	return state, nil
}

func ownedPyTorchRoot(ctx context.Context, journal Journal) (State, Artifact, string, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return state, Artifact{}, "", err
	}
	a, err := journal.ComponentArtifact(state, "pytorch-engine")
	if err != nil || state.ApprovedPlanID != state.Plan.ID || !slices.Contains(state.Plan.Selected, PyTorch) || !slices.Contains([]Phase{Staged, Checked, Installed}, state.Effects["pytorch-engine"].Phase) {
		return state, a, "", errors.New("Setup has no current approved PyTorch installation")
	}
	inputs, root, _, _, err := pyTorchFolders(a)
	if err != nil {
		return state, a, "", err
	}
	if ok, err := archiveInstalled(a, inputs); err != nil || !ok {
		return state, a, "", errors.New("the journal-owned PyTorch input package changed")
	}
	if err := pyTorchRootMatches(state.Effects["pytorch-engine"], root); err != nil {
		return state, a, "", err
	}
	return state, a, root, nil
}

func ownedPyTorch(ctx context.Context, journal Journal) (State, Artifact, string, error) {
	state, a, root, err := ownedPyTorchRoot(ctx, journal)
	if err != nil {
		return state, a, root, err
	}
	if state.Effects["pytorch-engine"].Phase != Installed || state.Effects["pytorch-engine"].Removing {
		return state, a, root, errors.New("the owned PyTorch component has not completed installation")
	}
	ready, err := pyTorchComponentReady(state.Effects["pytorch-engine"], root)
	if err != nil || !ready {
		return state, a, "", errors.New("the journal-owned PyTorch component is not Ready at its original identity")
	}
	return state, a, root, nil
}

func pyTorchDeclaration(inputs, root, state, program, runtimeProgram string) fwire.DeclarationWithExtensions {
	return fwire.DeclarationWithExtensions{Declaration: fwire.Declaration{Name: pyTorchProviderID, Program: program,
		Arguments: []string{"serve", "--endpoint", "{endpoint}", "--runtime-program", runtimeProgram,
			"--root", root, "--source", filepath.Join(inputs, "source", "infer"), "--state", state,
			"--image-model", torch.Flux2Klein4BTorch.Name, "--resource", torch.DefaultResource},
		Endpoint: pyTorchProviderID, Transport: fwire.DeclarationTransportNative,
		Contracts: []string{"abstraction.inference/image@1", "abstraction.storage/content-reader@1", "abstraction.storage/content-writer@1"},
		Resources: []string{torch.DefaultResource, "profile:image"}, Models: []string{"image:" + torch.Flux2Klein4BTorch.Name},
		Activation: fwire.ActivationOnDemand, Role: fwire.DeclarationRoleProvider}, SupportedExtensions: []fwire.DeclarationExtension{}}
}

func expectedPyTorchDeclaration(a Artifact) (fwire.DeclarationWithExtensions, error) {
	inputs, root, state, _, err := pyTorchFolders(a)
	if err != nil {
		return fwire.DeclarationWithExtensions{}, err
	}
	panel, err := os.Executable()
	if err != nil {
		return fwire.DeclarationWithExtensions{}, err
	}
	return pyTorchDeclaration(inputs, root, state, filepath.Join(filepath.Dir(panel), "pytorchd.exe"), filepath.Join(filepath.Dir(panel), "openabstractions.exe")), nil
}

func configurePyTorch(ctx context.Context, journal Journal, files map[string]string, progress func(int64, int64) error) error {
	owned, a, _, err := ownedPyTorch(ctx, journal)
	if err != nil {
		return err
	}
	_, _, state, models, err := pyTorchFolders(a)
	if err != nil {
		return err
	}
	plan := PyTorchModelPlan()
	for _, asset := range plan.Assets {
		path := files[asset.Role]
		if !samePrivatePath(path, filepath.Join(models, filepath.FromSlash(asset.Path))) {
			return errors.New("PyTorch model input differs from its fixed reviewed stage")
		}
		if err := checkModelStage(ctx, path, asset.Download); err != nil {
			return err
		}
	}
	if err := pyTorchCLI(ctx, "models", "install", "--set", plan.Model, "--from", models, "--models-root", filepath.Dir(models), "--job-store", "", "--state", state); err != nil {
		return err
	}
	declaration, err := expectedPyTorchDeclaration(a)
	if err != nil {
		return err
	}
	return registerSetupEngine(ctx, journal, owned, PyTorch, declaration)
}

func DefaultPyTorchController(journal Journal) (ModelController, error) {
	home, err := CurrentHome()
	if err != nil {
		return ModelController{}, err
	}
	if home.Isolated {
		return ModelController{}, errIsolatedProvider
	}
	plan := PyTorchModelPlan()
	return ModelController{Plan: &plan, JournalPath: filepath.Join(filepath.Dir(journal.Path), "model-pytorch.json"),
		StageDir: filepath.Join(home.Data, "OA", "m", "pt", plan.Model), ProviderRoot: filepath.Join(home.Data, "OA", "p", "pt"), Transfer: PublicTransfer{},
		Guard: func(ctx context.Context) error { _, _, _, err := ownedPyTorch(ctx, journal); return err },
		StageModel: func(ctx context.Context, _, _ string, files map[string]string, progress func(int64, int64) error) error {
			return configurePyTorch(ctx, journal, files, progress)
		},
		Probe:    func(ctx context.Context, _ string) (bool, error) { return pyTorchReady(ctx, journal, true) },
		Selected: func(ctx context.Context) (bool, error) { return pyTorchReady(ctx, journal, false) }}, nil
}

func pyTorchReady(ctx context.Context, journal Journal, verify bool) (bool, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return false, err
	}
	a, err := journal.ComponentArtifact(state, "pytorch-engine")
	if err != nil || state.ApprovedPlanID != state.Plan.ID || !slices.Contains(state.Plan.Selected, PyTorch) || state.Effects["pytorch-engine"].Phase != Installed || state.Effects["pytorch-engine"].Removing {
		return false, nil
	}
	_, root, providerState, _, err := pyTorchFolders(a)
	if err != nil {
		return false, err
	}
	if err := pyTorchRootMatches(state.Effects["pytorch-engine"], root); err != nil {
		return false, nil
	}
	expected, err := expectedPyTorchDeclaration(a)
	if err != nil {
		return false, err
	}
	declaration, ok := state.EngineDeclarations[PyTorch]
	if !ok || !sameEngineDeclaration(declaration, expected) {
		return false, nil
	}
	if verify {
		if _, _, _, err := ownedPyTorch(ctx, journal); err != nil {
			return false, err
		}
		if err := pyTorchCLI(ctx, "engine", "check", "--set", PyTorchModelPlan().Model, "--root", root, "--state", providerState); err != nil {
			return false, err
		}
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
		if row.State.Declaration.Name == expected.Declaration.Name {
			actual := fwire.DeclarationWithExtensions{Declaration: row.State.Declaration, SupportedExtensions: row.SupportedExtensions}
			return sameEngineDeclaration(actual, expected) && (row.State.Readiness == fwire.DeclarationReadinessIdle || row.State.Readiness == fwire.DeclarationReadinessReady), nil
		}
	}
	return false, nil
}

func (r Runner) RemovePyTorch(ctx context.Context) error {
	controller, err := DefaultPyTorchController(r.Journal)
	if err != nil {
		return err
	}
	return controller.withProviderOperation(ctx, func() error { return r.removePyTorchLocked(ctx) })
}

func (r Runner) removePyTorchLocked(ctx context.Context) error {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return err
	}
	a, err := r.Journal.ComponentArtifact(state, "pytorch-engine")
	if err != nil || state.Plan.ID == "" || state.ApprovedPlanID != state.Plan.ID || !slices.Contains(state.Plan.Selected, PyTorch) || !slices.Contains([]Phase{Staged, Checked, Installed}, state.Effects["pytorch-engine"].Phase) || state.Effects["pytorch-engine"].ComponentRootKey == "" {
		return errors.New("Setup has no current journal-owned PyTorch installation")
	}
	inputs, root, _, _, err := pyTorchFolders(a)
	if err != nil {
		return err
	}
	if !state.Effects["pytorch-engine"].Removing {
		if _, _, _, err := ownedPyTorchRoot(ctx, r.Journal); err != nil {
			return err
		}
	}
	expected, err := expectedPyTorchDeclaration(a)
	if err != nil {
		return err
	}
	declaration, configured := state.EngineDeclarations[PyTorch]
	if configured && !sameEngineDeclaration(declaration, expected) {
		return errors.New("the journal's PyTorch declaration differs from its fixed owned paths")
	}
	registry, err := setupEngineRegistry(ctx)
	if err != nil {
		return err
	}
	page, found, err := registryDeclaration(ctx, registry, expected)
	if err != nil {
		return err
	}
	if found {
		if !configured {
			return errors.New("the PyTorch declaration has no Setup ownership; it was preserved")
		}
		change, err := registry.Withdraw(ctx, page.Revision, declaration.Declaration.Name)
		if err != nil {
			return err
		}
		if change.Outcome != fwire.DeclarationEditOutcomeApplied {
			return errors.New("PyTorch withdrawal did not confirm its processes stopped; the engine was preserved")
		}
	}
	if !state.Effects["pytorch-engine"].Removing {
		state, err = r.Journal.recordPyTorchRemoval(ctx, state, false)
		if err != nil {
			return err
		}
	}
	if _, err := os.Lstat(root); err == nil {
		if err := pyTorchRootMatches(state.Effects["pytorch-engine"], root); err != nil {
			return err
		}
		if err := pyTorchCLI(ctx, "setup", "remove", "--root", root, "--expected-root-key", state.Effects["pytorch-engine"].ComponentRootKey); err != nil {
			return err
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			return errors.New("PyTorch component removal is incomplete; its data was preserved")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Only the pinned input receipt is removed by Setup. The CLI owns grants,
	// profile and generated engine removal; model places/content/audit stay.
	if _, err := removePackageTree(a, inputs); err != nil {
		return err
	}
	controller, err := DefaultPyTorchController(r.Journal)
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
	_, err = r.Journal.recordPyTorchRemoval(ctx, state, true)
	return err
}

func (j Journal) recordPyTorchRemoval(ctx context.Context, expected State, finished bool) (State, error) {
	var saved State
	err := cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID != expected.Plan.ID || state.ApprovedPlanID != state.Plan.ID || !reflect.DeepEqual(state.Effects["pytorch-engine"], expected.Effects["pytorch-engine"]) {
			return nil, errors.New("PyTorch removal journal changed")
		}
		if err := j.Authorize(state.Plan); err != nil {
			return nil, err
		}
		effect := state.Effects["pytorch-engine"]
		effect.Removing, effect.Revision = !finished, effect.Revision+1
		if finished {
			effect.Phase, effect.ComponentRootKey, effect.Reason = Pending, "", "The owned engine was removed; models, results and audit were preserved"
			state.Configuration["pytorch-model"] = false
			delete(state.EngineDeclarations, PyTorch)
		}
		state.Effects["pytorch-engine"] = effect
		saved = state
		return json.Marshal(state)
	})
	return saved, err
}
