package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
	"github.com/openabstractions/abstraction-resource/go/wake"
	localsource "github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

const pyTorchProviderID = product.Diffusers

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
		pyTorchModelStage(home), err
}

// pyTorchModelStage is the folder Setup stages the PyTorch starter model in
// and the provider records it where it lies: split_files/<category>/<file>.
func pyTorchModelStage(home Home) string {
	return filepath.Join(home.Data, "OA", "m", "pt", torch.Flux2Klein4BTorch.Name)
}

// Only the installed adapter receives fixed arguments from Setup. Ordinary
// source fixtures replace this seam; browser callers supply no command/path.
var pyTorchCLI = func(ctx context.Context, args ...string) error {
	if err := providerHomeAllowed(); err != nil {
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
	command := childproc.Hidden(exec.CommandContext(ctx, filepath.Join(filepath.Dir(panel), "pytorchd.exe"), args...))
	var failure cappedReply
	command.Stdout, command.Stderr = &failure, &failure
	if err := command.Run(); err != nil {
		return providerFailure("PyTorch", failure.String(), err)
	}
	return nil
}

// planHasPyTorch reports whether the plan carries the PyTorch component,
// directly or as a package dependency.
func planHasPyTorch(plan Plan) bool {
	_, ok := planComponent(plan, "pytorch-engine")
	return ok
}

// plannedPyTorchSite resolves the approved component's site-packages and
// returns "" when the plan contains no PyTorch component.
func plannedPyTorchSite(j Journal, state State) (string, error) {
	if !planHasPyTorch(state.Plan) {
		return "", nil
	}
	a, err := j.ComponentArtifact(state, "pytorch-engine")
	if err != nil {
		return "", err
	}
	_, component, _, _, err := pyTorchFolders(a)
	if err != nil {
		return "", err
	}
	return pyTorchSitePackages(component), nil
}

// pyTorchSitePackages is the site-packages of the component at root.
func pyTorchSitePackages(root string) string {
	return torch.Component{Root: root}.SitePackages()
}

// The runtime input package (installer/pytorch_engine.py's ZIP of CPython,
// the lean lock's wheels and the Hummingbird source) is its own release
// component: the runner downloads it through the public job service like any
// archive package, and only when the approved plan selects this engine. It
// never searches the person's Python or wheel installations.

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
	if err != nil || current != a || state.ApprovedPlanID != state.Plan.ID || !planHasPyTorch(state.Plan) || state.Effects["pytorch-engine"].Phase != Staged || state.Effects["pytorch-engine"].Removing {
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
	if err != nil || state.ApprovedPlanID != state.Plan.ID || !planHasPyTorch(state.Plan) || !slices.Contains([]Phase{Staged, Checked, Installed}, state.Effects["pytorch-engine"].Phase) {
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

// installedPyTorch is the PyTorch installation Setup's registration follows
// after an upgrade, when this release's catalog may pin another engine
// package: the approved plan's installed package by a pin this release
// knows (knownInstalledArtifact). While Setup's registration of it is
// recorded, an earlier pin the journal holds is that installation too,
// when Setup's own transfer installed it (setupInstalledPin). The
// installed tree must still match its receipt, its root and its Ready
// manifest. It authorizes no install: the reconcile changes only which
// pinned picture sets the recorded registration serves.
func installedPyTorch(ctx context.Context, journal Journal) (State, Artifact, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return state, Artifact{}, err
	}
	effect := state.Effects["pytorch-engine"]
	if effect.Phase != Installed || effect.Removing {
		return state, Artifact{}, errors.New("the owned PyTorch component has not completed installation")
	}
	a, err := journal.knownInstalledArtifact(state, "pytorch-engine")
	if _, recorded := state.EngineDeclarations[PyTorch]; recorded && errors.Is(err, errUnknownInstalledPin) {
		if a, err = activeArtifact(state, "pytorch-engine"); err == nil {
			err = setupInstalledPin(effect, "pytorch-engine", a)
		}
	}
	if err != nil {
		return state, a, fmt.Errorf("Setup's PyTorch installation: %w", err)
	}
	inputs, root, _, _, err := pyTorchFolders(a)
	if err != nil {
		return state, a, err
	}
	if ok, err := archiveInstalled(a, inputs); err != nil || !ok {
		return state, a, errors.New("the journal-owned PyTorch input package changed")
	}
	ready, err := pyTorchComponentReady(effect, root)
	if err != nil || !ready {
		return state, a, errors.New("the journal-owned PyTorch component is not Ready at its original identity")
	}
	return state, a, nil
}

// errUnrecordedPin refuses an earlier pin Setup's own transfer did not record.
var errUnrecordedPin = errors.New("the installed package pin is not one Setup installed from an OpenAbstractions catalog or preview package")

// setupInstalledPin checks that an earlier pin the journal holds is one
// Setup's runner installed from an OpenAbstractions release asset: the pin
// is complete, names an OpenAbstractions release asset, and the installed
// effect's transfer carries the work key Setup derives from the pin's bytes
// (workKey). A preview package's transfer names that same asset in the
// build's package folder. A pin placed in the journal by hand has no such
// transfer.
func setupInstalledPin(effect Effect, componentID string, a Artifact) error {
	name, ok := releaseAssetName(a.URL)
	if !ok || a.validate() != nil || effect.Work == nil || effect.Work.Key != workKey(componentID, a) {
		return errUnrecordedPin
	}
	if isLocalPackageWork(*effect.Work) && filepath.Base(effect.Work.OperationsBinding.Endpoint) != name {
		return errUnrecordedPin
	}
	return nil
}

// pyTorchDeclaration serves sets, the pair of installed image sets one
// engine process holds (pyTorchPair), in that order.
func pyTorchDeclaration(inputs, root, state, program, runtimeProgram string, sets []string) fwire.DeclarationWithExtensions {
	arguments := []string{"serve", "--endpoint", "{endpoint}", "--runtime-program", runtimeProgram,
		"--root", root, "--source", filepath.Join(inputs, "source", "infer"), "--state", state}
	var models []string
	for _, set := range sets {
		arguments = append(arguments, "--image-model", set)
		models = append(models, "image:"+set)
	}
	arguments = append(arguments, "--resource", torch.DefaultResource)
	return fwire.DeclarationWithExtensions{Declaration: fwire.Declaration{Name: pyTorchProviderID, Program: program,
		Arguments: arguments,
		Endpoint:  pyTorchProviderID, Transport: fwire.DeclarationTransportNative,
		Contracts: []string{"abstraction.inference/image@1", "abstraction.storage/content-reader@1", "abstraction.storage/content-writer@1"},
		Resources: []string{torch.DefaultResource, "profile:image"}, Models: models,
		Activation: fwire.ActivationOnDemand, Role: fwire.DeclarationRoleProvider}, SupportedExtensions: []fwire.DeclarationExtension{}}
}

// pyTorchPictureSets are the Klein sets whose model journals the engine's
// removal marks unprepared.
var pyTorchPictureSets = []torch.TorchModelSet{torch.Flux2Klein4BTorch, torch.Flux2Klein4BCompactTorch}

// pyTorchPairSize is how many image sets one engine process serves
// (pytorch CONTRACT.md PYTORCH-T10).
const pyTorchPairSize = 2

// pyTorchPair is the image sets Setup's registration serves: of the
// installed sets, the person's default picture model first, then the
// engine's pinned table order, two at most. An installed set beyond them is
// held and served by nothing (router ROUTE-H2).
func pyTorchPair(installed []string, preferred string) []string {
	var order []string
	for _, set := range torch.TorchModelSets {
		if set.Profile == "image" && slices.Contains(installed, set.Name) {
			order = append(order, set.Name)
		}
	}
	if i := slices.Index(order, preferred); i > 0 {
		order = append([]string{preferred}, slices.Delete(order, i, i+1)...)
	}
	if len(order) > pyTorchPairSize {
		order = order[:pyTorchPairSize]
	}
	return order
}

// pictureSetsInstalled is the image sets every file of which lies in OA's
// picture folder at its pinned size, by the store's own rule
// (localsource.Pinned.Complete). Tests replace it.
var pictureSetsInstalled = func(home Home) []string {
	files, models := pictureStoreCatalogue()
	return localsource.NewPinned(files, models).CompleteAt(pictureStoreRoot(home))
}

// pictureDefaultModel is the first candidate of the person's picture
// default (oa-image-default), or "" when none is set. A default that could
// not be read is an error, so a failed read never reorders the pair or
// drops the default from it. Tests replace it.
var pictureDefaultModel = func(ctx context.Context) (string, error) {
	operator, err := discoverRuntime().ResolveInferenceSelectionOperatorV2(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return "", err
	}
	read, err := operator.ReadAccountDefault(ctx, oaArchiveLayout.ImagePolicy)
	if err != nil {
		return "", err
	}
	switch read.Outcome {
	case iwire.SelectionReadOutcomeUnknown:
		return "", nil
	case iwire.SelectionReadOutcomeFound:
		if read.Policy == nil || len(read.Policy.Candidates) == 0 {
			return "", nil
		}
		return read.Policy.Candidates[0].Model, nil
	}
	return "", errors.New("the picture default could not be read: " + read.Outcome.String())
}

// pictureStoreRead reports whether the storage inventory has read OA's
// picture store: listed, present and without error. Tests replace it.
var pictureStoreRead = func(ctx context.Context) (bool, error) {
	inventory, err := discoverRuntime().ResolveStorageInventory(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return false, err
	}
	page, err := inventory.ListStore(ctx, localsource.SetupPictureStore, "", 1)
	if err != nil {
		return false, err
	}
	if page.Outcome != content.InventoryOutcomePage {
		return false, nil
	}
	for _, s := range page.Stores {
		if s.Name == localsource.SetupPictureStore && s.Present && len(s.Errors) == 0 {
			return true, nil
		}
	}
	return false, nil
}

// pictureWake holds the machine awake while Setup proves picture model
// files, naming why; the release ends it. Tests replace it.
var pictureWake = func(why string) (func(), error) {
	lease, err := (wake.Holder{}).Hold("OpenAbstractions Setup", why)
	if err != nil {
		return nil, err
	}
	return func() {
		//unchecked: the proof is over; a release that fails leaves the lease to lapse unrenewed
		lease.Release()
	}, nil
}

// errPictureStoreUnread keeps Setup's earlier registration while the
// storage inventory has not read OA's picture store.
var errPictureStoreUnread = errors.New("OpenAbstractions' picture model folder has not been read yet; the earlier picture engine registration stays")

// pyTorchDeclarationSets are the image sets a registered declaration
// serves, when it is Setup's own declaration for a, or nil.
func pyTorchDeclarationSets(declaration fwire.DeclarationWithExtensions, a Artifact) ([]string, error) {
	var sets []string
	args := declaration.Declaration.Arguments
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--image-model" {
			sets = append(sets, args[i+1])
		}
	}
	if len(sets) == 0 || len(sets) > pyTorchPairSize {
		return nil, nil
	}
	for _, name := range sets {
		if set, ok := torch.TorchModelSetNamed(name); !ok || set.Profile != "image" {
			return nil, nil
		}
	}
	expected, err := expectedPyTorchDeclaration(a, sets)
	if err != nil {
		return nil, err
	}
	if !sameEngineDeclaration(declaration, expected) {
		return nil, nil
	}
	return sets, nil
}

// pyTorchDeclarationSet is the set a registered declaration serves first,
// when it is Setup's own declaration for a, or "".
func pyTorchDeclarationSet(declaration fwire.DeclarationWithExtensions, a Artifact) (string, error) {
	sets, err := pyTorchDeclarationSets(declaration, a)
	if err != nil || len(sets) == 0 {
		return "", err
	}
	return sets[0], nil
}

// PyTorchServedModel is the picture model Setup's PyTorch registration
// serves first, from the journal's recorded declaration, or "" when none is
// recorded.
func PyTorchServedModel(ctx context.Context, journal Journal) (string, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return "", err
	}
	declaration, ok := state.EngineDeclarations[PyTorch]
	if !ok {
		return "", nil
	}
	a, err := journal.ComponentArtifact(state, "pytorch-engine")
	if err != nil {
		return "", err
	}
	return pyTorchDeclarationSet(declaration, a)
}

func expectedPyTorchDeclaration(a Artifact, sets []string) (fwire.DeclarationWithExtensions, error) {
	inputs, root, state, _, err := pyTorchFolders(a)
	if err != nil {
		return fwire.DeclarationWithExtensions{}, err
	}
	panel, err := os.Executable()
	if err != nil {
		return fwire.DeclarationWithExtensions{}, err
	}
	return pyTorchDeclaration(inputs, root, state, filepath.Join(filepath.Dir(panel), "pytorchd.exe"), filepath.Join(filepath.Dir(panel), "openabstractions.exe"), sets), nil
}

// pyTorchModelsInstallArgs records the staged set in the provider. The
// job service hard-links each verified download into the stage (JOB-A17),
// so the staged file's other name lies in the runtime's job store; the
// provider admits that store among OA's own roots (modelplace.OwnedRoots)
// when it protects the set folder (identity ID-D3, KIT-M3).
func pyTorchModelsInstallArgs(model, models, state string) []string {
	return []string{"models", "install", "--set", model, "--from", models, "--models-root", filepath.Dir(models),
		"--state", state}
}

func configurePyTorch(ctx context.Context, journal Journal, plan ModelPlan, files map[string]string, progress func(int64, int64) error) error {
	_, a, _, err := ownedPyTorch(ctx, journal)
	if err != nil {
		return err
	}
	_, _, _, models, err := pyTorchFolders(a)
	if err != nil {
		return err
	}
	for _, asset := range plan.Assets {
		path := files[asset.Role]
		if !samePrivatePath(path, asset.reviewedStagePath(models)) {
			return errors.New("PyTorch model input differs from its fixed reviewed stage")
		}
		if err := checkModelStage(ctx, path, asset.Download); err != nil {
			return err
		}
	}
	// The placed files join OA's picture store, which the router reads.
	if err := registerPictureStore(ctx, journal); err != nil {
		return err
	}
	// The registration follows what is installed, never this click: a set
	// whose files are all placed joins it, the person's default first.
	return reconcilePyTorchRegistration(ctx, journal, pictureStoreWait)
}

// pictureStoreWait bounds how long a preparation waits for the storage
// inventory to read OA's picture store before replacing a registration.
var pictureStoreWait = 2 * time.Minute

// reconcilePyTorchRegistration brings Setup's PyTorch registration to the
// pair of installed sets (pyTorchPair). A first registration is declared.
// A registration Setup recorded and the registry still lists is replaced
// in one edit, and only once the storage inventory has read OA's picture
// store, waiting up to wait for it: the router never hides a model the
// registration listed. Until then the registration stays. A registration
// the person removed or disabled stays so. Each set of the new pair is
// proved in the provider once per engine package (pyTorchProveSets).
func reconcilePyTorchRegistration(ctx context.Context, journal Journal, wait time.Duration) error {
	owned, a, err := installedPyTorch(ctx, journal)
	if err != nil {
		return err
	}
	home, err := CurrentHome()
	if err != nil {
		return err
	}
	preferred, err := pictureDefaultModel(ctx)
	if err != nil {
		return err
	}
	wanted := pyTorchPair(pictureSetsInstalled(home), preferred)
	if len(wanted) == 0 {
		return nil
	}
	next, err := expectedPyTorchDeclaration(a, wanted)
	if err != nil {
		return err
	}
	prior, recorded := owned.EngineDeclarations[PyTorch]
	if !recorded {
		if err := pyTorchProveSets(ctx, journal, a, wanted); err != nil {
			return err
		}
		return registerSetupEngine(ctx, journal, owned, PyTorch, next)
	}
	served, err := pyTorchDeclarationSets(prior, a)
	if err != nil || len(served) == 0 || slices.Equal(served, wanted) {
		return err
	}
	registry, err := setupEngineRegistry(ctx)
	if err != nil {
		return err
	}
	if _, found, err := registryDeclaration(ctx, registry, prior); errors.Is(err, errForeignDeclaration) {
		// A replacement whose reply was lost is listed already.
		if _, current, nextErr := registryDeclaration(ctx, registry, next); nextErr == nil && current {
			return journal.replaceEngineDeclaration(ctx, owned.Plan.ID, PyTorch, prior, next)
		}
		return err
	} else if err != nil || !found {
		return err
	}
	if err := waitPictureStoreRead(ctx, wait); err != nil {
		return err
	}
	if err := pyTorchProveSets(ctx, journal, a, wanted); err != nil {
		return err
	}
	page, found, err := registryDeclaration(ctx, registry, prior)
	if err != nil || !found {
		return err
	}
	change, err := registry.ReplacePinned(ctx, page.Revision, fwire.PinnedDeclaration{Declaration: next.Declaration, SupportedExtensions: next.SupportedExtensions})
	if err != nil {
		return err
	}
	if change.Outcome != fwire.DeclarationEditOutcomeApplied {
		return errors.New("the registry did not replace the picture engine registration: " + change.Outcome.String() + " " + change.Reason)
	}
	return journal.replaceEngineDeclaration(ctx, owned.Plan.ID, PyTorch, prior, next)
}

// waitPictureStoreRead returns once the storage inventory has read OA's
// picture store, asking every few seconds for at most wait.
func waitPictureStoreRead(ctx context.Context, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		read, err := pictureStoreRead(ctx)
		if err != nil {
			return errors.Join(errPictureStoreUnread, err)
		}
		if read {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errPictureStoreUnread
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// pyTorchProveSets records each set in the provider where its files lie
// (pytorchd models install), which reads every file once: about 24 GB for
// Z-Image-Turbo beside a Klein set. A set proved for the current engine
// package and still proved in the provider is not read again. The proof
// runs under a wake lease naming why.
func pyTorchProveSets(ctx context.Context, journal Journal, a Artifact, sets []string) error {
	_, _, state, models, err := pyTorchFolders(a)
	if err != nil {
		return err
	}
	current, err := journal.Load(ctx)
	if err != nil {
		return err
	}
	var todo []string
	for _, name := range sets {
		set, ok := torch.TorchModelSetNamed(name)
		if !ok {
			return errors.New("no reviewed PyTorch set is named " + name)
		}
		if current.PyTorchProved[name] != a.SHA256 || !torch.TorchModelsProven(state, set) {
			todo = append(todo, name)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	if release, err := pictureWake("proving the picture model files for the PyTorch engine"); err == nil {
		defer release()
	}
	for _, name := range todo {
		if err := pyTorchCLI(ctx, pyTorchModelsInstallArgs(name, models, state)...); err != nil {
			return err
		}
		if err := journal.recordPyTorchProved(ctx, name, a.SHA256); err != nil {
			return err
		}
	}
	return nil
}

// recordPyTorchProved records set as proved for the engine package pkg.
func (j Journal) recordPyTorchProved(ctx context.Context, set, pkg string) error {
	return cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.PyTorchProved == nil {
			state.PyTorchProved = map[string]string{}
		}
		state.PyTorchProved[set] = pkg
		return json.Marshal(state)
	})
}

// replaceEngineDeclaration records next as engine's declaration in place of
// prior, which the registry replaced in one edit.
func (j Journal) replaceEngineDeclaration(ctx context.Context, planID string, engine AppID, prior, next fwire.DeclarationWithExtensions) error {
	return cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID != planID || !reflect.DeepEqual(state.EngineDeclarations[engine], prior) {
			return nil, errors.New("the recorded engine declaration changed during its replacement")
		}
		state.EngineDeclarations[engine] = next
		return json.Marshal(state)
	})
}

func sharedPictureRuntime() starterPictureRecipe {
	return starterPictureRecipe{
		engine: PyTorch, plan: PyTorchModelPlan, fixedController: livePictureController,
		fixedFactory: func(journal Journal) (ModelController, error) {
			return PyTorchServedController(context.Background(), journal)
		},
		servedModel: PyTorchServedModel, starterJournal: "model-pytorch-compact.json", starterOfferJournal: true,
		removePackage: Runner.RemovePyTorch, reviewedLayout: true,
		extraOffers: []pictureOfferRecipe{
			{plan: PyTorchFullModelPlan, journal: "model-pytorch.json", caption: "Full precision, larger"},
			retainedArchiveModelOffer,
		},
	}
}

// DefaultPyTorchController prepares the PyTorch engine's starter picture
// model, the compact FLUX.2 Klein.
func DefaultPyTorchController(journal Journal) (ModelController, error) {
	return DefaultPyTorchChoiceController(journal, PyTorchModelPlan())
}

// PyTorchServedController is the controller of the Klein plan the PyTorch
// engine serves: the full-precision one once prepared, else the compact
// starter. A prepared full-precision Klein makes the engine's picture model
// prepared, so the compact files are never fetched beside it unasked.
func PyTorchServedController(ctx context.Context, journal Journal) (ModelController, error) {
	full := PyTorchFullModelPlan()
	prepared, err := PictureModelConfigured(ctx, journal, full)
	if err != nil {
		return ModelController{}, err
	}
	if prepared {
		return DefaultPyTorchChoiceController(journal, full)
	}
	return DefaultPyTorchController(journal)
}

// DefaultPyTorchChoiceController prepares one picture model Setup offers
// with the PyTorch engine (PictureModelOffers). Each is staged in the one
// shared folder the host's model-paths file names, under one provider
// operation lock. A Klein plan is installed into the provider; a
// host-only plan is ready once its files are staged.
func DefaultPyTorchChoiceController(journal Journal, plan ModelPlan) (ModelController, error) {
	if _, ok := PictureOfferNamed(PyTorch, plan.ID); !ok {
		return ModelController{}, errors.New("unknown PyTorch picture model plan")
	}
	home, err := CurrentHome()
	if err != nil {
		return ModelController{}, err
	}
	if home.Isolated {
		return ModelController{}, errIsolatedProvider
	}
	c := ModelController{Plan: &plan, JournalPath: PictureModelJournal(journal, plan),
		OperationLock: pyTorchOperationLock(journal),
		StageDir:      pyTorchModelStage(home), ProviderRoot: filepath.Join(home.Data, "OA", "p", "pt"), Transfer: PublicTransfer{},
		Guard: func(ctx context.Context) error { _, _, _, err := ownedPyTorch(ctx, journal); return err },
		StageModel: func(ctx context.Context, _, _ string, files map[string]string, progress func(int64, int64) error) error {
			return configurePyTorch(ctx, journal, plan, files, progress)
		},
		Probe:    func(ctx context.Context, _ string) (bool, error) { return pyTorchReady(ctx, journal, true) },
		Selected: func(ctx context.Context) (bool, error) { return pyTorchReady(ctx, journal, false) },
		Refresh:  func(ctx context.Context) error { return reconcilePyTorchRegistration(ctx, journal, 0) }}
	if plan.StagesPictureFilesOnly() {
		c.Probe = func(context.Context, string) (bool, error) { return true, nil }
		c.Selected = nil
	}
	return c, nil
}

// pyTorchOperationLock is the one provider operation lock of the PyTorch
// engine's picture models: the record beside the full-precision Klein's
// journal, model-pytorch.json, which held it before the offers.
func pyTorchOperationLock(journal Journal) string {
	return filepath.Join(filepath.Dir(journal.Path), "model-pytorch.json.provider-op")
}

// PyTorchEngineChecked is the PyTorch engine's live check for the setup
// step's Ready: the engine's own `engine check` of the set its registration
// serves, then the registry's listing of that registration.
func PyTorchEngineChecked(ctx context.Context, journal Journal) (bool, error) {
	return pyTorchReady(ctx, journal, true)
}

// PyTorchEngineRegistered is the cheap reading of the PyTorch engine for an
// offer: the journal's installed engine and the registry's listing of its
// registration, without running the engine's own check.
func PyTorchEngineRegistered(ctx context.Context, journal Journal) (bool, error) {
	return pyTorchReady(ctx, journal, false)
}

func pyTorchReady(ctx context.Context, journal Journal, verify bool) (bool, error) {
	state, err := journal.Load(ctx)
	if err != nil {
		return false, err
	}
	a, err := journal.ComponentArtifact(state, "pytorch-engine")
	if err != nil || state.ApprovedPlanID != state.Plan.ID || !planHasPyTorch(state.Plan) || state.Effects["pytorch-engine"].Phase != Installed || state.Effects["pytorch-engine"].Removing {
		return false, nil
	}
	_, root, providerState, _, err := pyTorchFolders(a)
	if err != nil {
		return false, err
	}
	if err := pyTorchRootMatches(state.Effects["pytorch-engine"], root); err != nil {
		return false, nil
	}
	declaration, ok := state.EngineDeclarations[PyTorch]
	if !ok {
		return false, nil
	}
	served, err := pyTorchDeclarationSets(declaration, a)
	if err != nil || len(served) == 0 {
		return false, err
	}
	expected, err := expectedPyTorchDeclaration(a, served)
	if err != nil {
		return false, err
	}
	if verify {
		if _, _, _, err := ownedPyTorch(ctx, journal); err != nil {
			return false, err
		}
		for _, set := range served {
			if err := pyTorchCLI(ctx, "engine", "check", "--set", set, "--root", root, "--state", providerState); err != nil {
				return false, err
			}
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
	if err := guardCurrentArchiveEngine(state); err != nil {
		return err
	}
	a, err := r.Journal.ComponentArtifact(state, "pytorch-engine")
	if err != nil || state.Plan.ID == "" || state.ApprovedPlanID != state.Plan.ID || !planHasPyTorch(state.Plan) || !slices.Contains([]Phase{Staged, Checked, Installed}, state.Effects["pytorch-engine"].Phase) || state.Effects["pytorch-engine"].ComponentRootKey == "" {
		return errors.New("Setup has no current journal-owned PyTorch installation")
	}
	inputs, root, _, _, err := pyTorchFolders(a)
	if err != nil {
		return err
	}
	if err := guardRetainedArchiveEngine(ctx, state, pyTorchSitePackages(root)); err != nil {
		return err
	}
	if !state.Effects["pytorch-engine"].Removing {
		if _, _, _, err := ownedPyTorchRoot(ctx, r.Journal); err != nil {
			return err
		}
	}
	served := []string{torch.Flux2Klein4BCompactTorch.Name}
	declaration, configured := state.EngineDeclarations[PyTorch]
	if configured {
		if served, err = pyTorchDeclarationSets(declaration, a); err != nil {
			return err
		}
		if len(served) == 0 {
			return errors.New("the journal's PyTorch declaration differs from its fixed owned paths")
		}
	}
	expected, err := expectedPyTorchDeclaration(a, served)
	if err != nil {
		return err
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
	// Each Klein model the engine served is unprepared; its files stay.
	for _, set := range pyTorchPictureSets {
		plan := PyTorchModelPlan()
		if set.Name == torch.Flux2Klein4BTorch.Name {
			plan = PyTorchFullModelPlan()
		}
		controller := ModelController{Plan: &plan, JournalPath: PictureModelJournal(r.Journal, plan)}
		raw, err := cas.ReadLimitContext(ctx, controller.JournalPath, maxJournalBytes)
		if err != nil {
			return err
		}
		if raw == nil {
			continue
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
