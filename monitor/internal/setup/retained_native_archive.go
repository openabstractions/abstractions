package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/openabstractions/abstraction-cas/go"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// KeptPackage is an installed package a later plan no longer carries: its
// pin and its effect.
type KeptPackage struct {
	Artifact Artifact `json:"artifact"`
	Effect   Effect   `json:"effect"`
}

// retainedNativeArchive binds the removal lifecycle to the component and
// journal slot selected by a compiled adapter.
type retainedNativeArchive struct {
	component     string
	registration  string
	title         string
	label         string
	slot          func(*State) **KeptPackage
	installPolicy nativeArchiveInstallPolicy
}

// RetainedArchive is the declared Setup archive and its historical journal
// binding. The package is selected in source by its Catalog application ID.
type RetainedArchive struct {
	archive *retainedNativeArchive
}

func (selected RetainedArchive) Component() string {
	if selected.archive == nil {
		return ""
	}
	return selected.archive.component
}

func (selected RetainedArchive) Registration() string {
	if selected.archive == nil {
		return ""
	}
	return selected.archive.registration
}

func (selected RetainedArchive) Installed(state State) (Artifact, Effect, bool) {
	if selected.archive == nil {
		return Artifact{}, Effect{}, false
	}
	artifact, effect, _, ok := selected.archive.installed(&state)
	return artifact, effect, ok
}

var errNoRetainedArchive = errors.New("no Setup retained archive was selected")

func (selected RetainedArchive) UsesNativeResources(ctx context.Context, journal Journal) (bool, error) {
	if selected.archive == nil {
		return false, errNoRetainedArchive
	}
	return selected.archive.usesNativeResources(ctx, journal)
}

func (selected RetainedArchive) RegisterSelf(ctx context.Context, journal Journal) error {
	if selected.archive == nil {
		return errNoRetainedArchive
	}
	return selected.archive.registerSelf(ctx, journal)
}

func (selected RetainedArchive) HoldProgram(ctx context.Context, journal Journal) (HeldProgram, error) {
	if selected.archive == nil {
		return HeldProgram{}, errNoRetainedArchive
	}
	return selected.archive.holdProgram(ctx, journal)
}

func (selected RetainedArchive) Remove(ctx context.Context, journal Journal, unregister func(context.Context, Artifact, string) error) error {
	if selected.archive == nil {
		return errNoRetainedArchive
	}
	return selected.archive.remove(ctx, journal, unregister)
}

// policyFor supplies the reviewed native runtime and readback hooks for this
// component alone. Other archives use the ordinary installation path.
func (archive retainedNativeArchive) policyFor(component string) nativeArchiveInstallPolicy {
	if component == "" || component != archive.component {
		return nativeArchiveInstallPolicy{}
	}
	return archive.installPolicy
}

func (archive retainedNativeArchive) installed(state *State) (artifact Artifact, effect Effect, inPlan, ok bool) {
	if component, ok := planComponent(state.Plan, archive.component); ok && state.Plan.ID != "" &&
		state.ApprovedPlanID == state.Plan.ID && component.Artifact != nil && component.Artifact.NativeResources != nil {
		if effect := state.Effects[archive.component]; effect.Phase == Installed {
			return *component.Artifact, effect, true, true
		}
	}
	if kept := archive.displacedInstallation(state); kept != nil {
		return kept.Artifact, kept.Effect, false, true
	}
	return Artifact{}, Effect{}, false, false
}

func (archive retainedNativeArchive) displacedInstallation(state *State) *KeptPackage {
	kept := *archive.slot(state)
	if kept == nil || kept.Artifact.NativeResources == nil || kept.Effect.Phase != Installed {
		return nil
	}
	return kept
}

func (archive retainedNativeArchive) currentHoldsRuntime(state *State) bool {
	switch state.Effects[archive.component].Phase {
	case "", Pending, Removed:
		return false
	}
	return true
}

// resealCurrentRuntime selects the reviewed package of the current installed
// effect. A displaced package or an effect being removed stays untouched.
func (archive retainedNativeArchive) resealCurrentRuntime(ctx context.Context, journal Journal, state State, site string, seal func(context.Context, Artifact, string) error) error {
	effect := state.Effects[archive.component]
	if effect.Phase != Installed || effect.Removing {
		return nil
	}
	artifact, err := journal.ComponentArtifact(state, archive.component)
	if err != nil {
		return err
	}
	return seal(ctx, artifact, site)
}

// displacedHoldsRuntimeSite compares existing directories by identity.
// Missing directories fall back to the same checked path spelling.
func (archive retainedNativeArchive) displacedHoldsRuntimeSite(ctx context.Context, state *State, site string) (bool, error) {
	kept := archive.displacedInstallation(state)
	if kept == nil {
		return false, nil
	}
	root, err := installPath(kept.Artifact)
	if err != nil {
		return false, err
	}
	used, err := installedPyTorchSite(ctx, root, kept.Artifact)
	if err != nil || used == "" {
		return false, err
	}
	dependency, err := fileid.Resolve(ctx, fileid.Self, used, fileid.Options{})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	engine, err := fileid.Resolve(ctx, fileid.Self, site, fileid.Options{})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return dependency.Equal(engine) || (dependency.IsZero() || engine.IsZero()) && fileid.SamePathName(used, site), nil
}

func (archive retainedNativeArchive) displaced(previous *State, plan Plan) *KeptPackage {
	artifact, effect, _, ok := archive.installed(previous)
	if !ok {
		return nil
	}
	if next, found := planComponent(plan, archive.component); found && sameArtifact(next.Artifact, &artifact) {
		return nil
	}
	return &KeptPackage{Artifact: artifact, Effect: effect}
}

func (archive retainedNativeArchive) remove(ctx context.Context, journal Journal, unregister func(context.Context, Artifact, string) error) error {
	state, err := journal.Load(ctx)
	if err != nil {
		return err
	}
	artifact, effect, _, ok := archive.installed(&state)
	if !ok || unregister == nil {
		return errors.New("no " + archive.label + " is installed")
	}
	root, err := installPath(artifact)
	if err != nil {
		return err
	}
	if !effect.Removing {
		if _, err := matchingReceiptContext(ctx, artifact, root); err != nil {
			return err
		}
		intent := nativeRemovalEffect(effect, false, 0)
		if err := archive.recordRemoval(ctx, journal, artifact, effect, intent); err != nil {
			return err
		}
		effect = intent
	}
	if err := unregister(ctx, artifact, root); err != nil {
		return err
	}
	kept, err := removePackageTreeContext(ctx, artifact, root)
	if err != nil {
		return err
	}
	return archive.recordRemoval(ctx, journal, artifact, effect, nativeRemovalEffect(effect, true, kept))
}

// recordRemoval compares the full pin and effect. A later plan can carry the
// same record forward without changing the pending removal's identity.
func (archive retainedNativeArchive) recordRemoval(ctx context.Context, journal Journal, expected Artifact, effect, next Effect) error {
	return cas.ChangeLimitContext(ctx, journal.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		artifact, current, inPlan, ok := archive.installed(&state)
		if !ok || !reflect.DeepEqual(artifact, expected) || !reflect.DeepEqual(current, effect) {
			return nil, errors.New("the " + archive.label + " changed in the setup journal")
		}
		if inPlan {
			state.Effects[archive.component] = next
		} else if next.Phase == Removed {
			*archive.slot(&state) = nil
		} else {
			(*archive.slot(&state)).Effect = next
		}
		return json.Marshal(state)
	})
}

// archiveRootsHold checks the same account root boundary for each approved
// relative installation root. A root itself is never an installed program.
func archiveRootsHold(program string, roots ...string) bool {
	home, err := CurrentHome()
	if err != nil || home.Data == "" || !filepath.IsAbs(program) {
		return false
	}
	program = filepath.Clean(program)
	for _, relative := range roots {
		root := filepath.Join(home.Data, filepath.FromSlash(relative))
		if program != root && withinPath(root, program) {
			return true
		}
	}
	return false
}

// archiveProgramPaths derives the locators for a reviewed artifact. Callers
// still need receipt verification to launch one of these programs.
func archiveProgramPaths(artifact Artifact, portableCaller, title string) (root, caller, native string, err error) {
	if artifact.Platform != "windows/amd64" || artifact.Action != "archive_per_user" {
		return "", "", "", errors.New(title + " app is not a Windows x64 portable archive")
	}
	root, err = installPath(artifact)
	if err != nil {
		return "", "", "", err
	}
	// Expand an existing 8.3 ancestor for the root and the derived caller.
	root = identity.CanonicalProgramPath(root)
	member := portableCaller
	if artifact.ApplicationProgram != "" {
		if !safeMemberPath(artifact.ApplicationProgram) || !safeMemberPath(artifact.ServiceProgram) {
			return "", "", "", errors.New("invalid installed program member")
		}
		member = artifact.ServiceProgram
		native = filepath.Join(root, filepath.FromSlash(artifact.ApplicationProgram))
	}
	caller = identity.CanonicalProgramPath(filepath.Clean(filepath.Join(root, filepath.FromSlash(member))))
	return root, caller, native, nil
}

func prepareArchiveDataDirectories(directories []string, title string) error {
	for _, path := range directories {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s data path is not a regular directory: %s", title, path)
		}
	}
	return nil
}
