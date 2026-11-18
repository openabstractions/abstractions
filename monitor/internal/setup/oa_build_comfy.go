package setup

import (
	"context"
	"fmt"
	"log"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// Earlier releases installed an OA build of the picture app (a native
// archive). This release builds none and keeps Remove for one release
// (owner, 2026-11-11): Setup removes the build's registration, its package
// files and its Start menu shortcut. Files the person added or changed stay,
// and so do the app's user folder and its models, which live outside the
// package tree. No catalog pin names the build any more, so its removal
// reads the journal's own record and checks the build's install receipt
// before it removes anything.

// The generated catalogue checker requires this historical component binding.
var retainedArchivePackage, _ = product.RetainedArchiveDeclaration()
var retainedArchiveComponent = retainedArchivePackage.RetainedArchiveComponent
var oaArchiveLayout = retainedArchivePackage.RetainedArchiveLayout

// The adopted host and historical archive share this account model record.
func SyncRetainedArchiveModelPaths(ctx context.Context, journal Journal) error {
	policy := modelPathsPolicy{root: oaArchiveLayout.DataRoot, file: oaArchiveLayout.ModelFile,
		group: oaArchiveLayout.ModelGroup, categories: oaArchiveLayout.ModelCategories[:]}
	return syncModelPaths(ctx, journal, policy, PictureModelOffers(PyTorch), pyTorchModelStage)
}

// The package family and title are fixed by Setup's compiled Catalog.
var nativeArchiveAppTitle = ApplicationName(AppID(retainedArchivePackage.ID))

// RetainedArchiveFor selects a compiled archive with an existing journal slot.
func RetainedArchiveFor(app AppID) RetainedArchive {
	if string(app) == retainedArchivePackage.ID && historicalArchive.component != "" {
		return RetainedArchive{archive: &historicalArchive}
	}
	return RetainedArchive{}
}

// The retained package keeps its historical journal field even when no
// current Catalog offers the original archive.
var historicalArchive = retainedNativeArchive{
	component:    retainedArchiveComponent,
	registration: retainedArchivePackage.Registration,
	title:        nativeArchiveAppTitle,
	label:        "previous version of " + nativeArchiveAppTitle,
	slot:         func(state *State) **KeptPackage { return &state.RetainedArchive },
	installPolicy: nativeArchiveInstallPolicy{
		runtimeSite: plannedPyTorchSite,
		afterReadback: func(ctx context.Context, journal Journal) {
			if err := SyncRetainedArchiveModelPaths(ctx, journal); err != nil {
				log.Printf("setup: %s model folders: %v", nativeArchiveAppTitle, err)
			}
		},
	},
}

func retainDisplacedNativeArchive(previous State, plan Plan) *KeptPackage {
	return historicalArchive.displaced(&previous, plan)
}

func nativeArchiveInstallPolicyFor(component string) nativeArchiveInstallPolicy {
	return historicalArchive.policyFor(component)
}

// guardRetainedArchiveEngine keeps an engine still named by the displaced
// archive. Existing directories are compared by filesystem identity.
func guardRetainedArchiveEngine(ctx context.Context, state State, site string) error {
	held, err := historicalArchive.displacedHoldsRuntimeSite(ctx, &state, site)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("remove the earlier %s build before removing this engine", nativeArchiveAppTitle)
	}
	return nil
}

// guardCurrentArchiveEngine keeps the shared runtime until the current
// associated archive has no pending or installed files.
func guardCurrentArchiveEngine(state State) error {
	if !historicalArchive.currentHoldsRuntime(&state) {
		return nil
	}
	return fmt.Errorf("%s runs its torch from this PyTorch component; remove %s first", nativeArchiveAppTitle, nativeArchiveAppTitle)
}

// resealCurrentArchiveEngine preserves the historical activation failure copy.
func (r Runner) resealCurrentArchiveEngine(ctx context.Context, state State, site, version string) error {
	err := historicalArchive.resealCurrentRuntime(ctx, r.Journal, state, site, resealArchiveRuntime)
	if err != nil {
		return fmt.Errorf("%s could not switch to PyTorch %s; the earlier version stays active: %w", nativeArchiveAppTitle, version, err)
	}
	return nil
}

// Keep the historical effect seam next to the environment that selects it.
var resealArchiveRuntime = resealNativeArchiveRuntimeSite
