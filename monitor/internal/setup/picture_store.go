package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
	localsource "github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// OA's picture store is OA's own picture model folder, <LocalAppData>\OA\m\pt,
// read as the reserved inventory store oa-picture by one inventoryd provider
// Setup registers (local-stores setup_root.go). Its configuration names that
// folder alone and the catalogue digest of every picture model file Setup
// offers, so the storage inventory reports each staged file by the digest
// Setup verified, without hashing it again. The router lists an engine's
// picture model while the store holds every file of it (router ROUTE-E1).
const (
	pictureStoreProvider = "oa-setup-picture"
	pictureStoreEndpoint = "oa_setup_picture"
	pictureStoreContract = "abstraction.storage/inventory-source@1"
)

// pictureStoreRecord is Setup's registration of the picture store in its
// journal: the exact declaration, and whether the registry confirmed it. A
// confirmed registration the registry no longer lists is the person's
// removal and is never declared again.
type pictureStoreRecord struct {
	Declaration fwire.DeclarationWithExtensions `json:"declaration"`
	Confirmed   bool                            `json:"confirmed,omitempty"`
}

// pictureStoreRoot is OA's picture model folder, the parent of every set
// folder Setup stages picture models in (pyTorchModelStage).
func pictureStoreRoot(home Home) string { return filepath.Join(home.Data, "OA", "m", "pt") }

// pictureStoreConfig is the inventoryd configuration Setup writes for it.
func pictureStoreConfig(home Home) string {
	return filepath.Join(home.Config, "picture-store", "inventoryd.json")
}

// pictureStoreCatalogue is the picture store's catalogue: every file of
// every PyTorch image set Setup offers, at the path it is staged at below
// the store root, with the engine table's size, SHA-256 and role, and each
// set as a model made of its files.
func pictureStoreCatalogue() ([]localsource.PinnedFile, []localsource.PinnedModel) {
	stage := filepath.Base(pyTorchModelStage(Home{}))
	var files []localsource.PinnedFile
	var models []localsource.PinnedModel
	seen := map[string]bool{}
	for _, offer := range PictureModelOffers(PyTorch) {
		set, ok := torch.TorchModelSetNamed(offer.Plan.Model)
		if !ok || set.Profile != "image" {
			continue
		}
		model := localsource.PinnedModel{Name: set.Name}
		for _, f := range set.Files {
			p := path.Join(stage, f.Locators[len(f.Locators)-1])
			model.Files = append(model.Files, p)
			if seen[p] {
				continue
			}
			seen[p] = true
			files = append(files, localsource.PinnedFile{Path: p, Size: f.Size, SHA256: f.SHA256, Role: f.Role})
		}
		models = append(models, model)
	}
	return files, models
}

// pictureStoreDeclaration is the inventoryd provider reading the store's
// configuration and nothing else.
func pictureStoreDeclaration(program, config string) fwire.DeclarationWithExtensions {
	return fwire.DeclarationWithExtensions{Declaration: fwire.Declaration{Name: pictureStoreProvider, Program: program,
		Arguments: []string{"serve", "--endpoint", "{endpoint}", "--config", config, "--config-roots-only"},
		Endpoint:  pictureStoreEndpoint, Transport: fwire.DeclarationTransportNative,
		Contracts: []string{pictureStoreContract}, Resources: []string{"store:" + localsource.SetupPictureStore},
		Activation: fwire.ActivationOnDemand, Role: fwire.DeclarationRoleProvider}, SupportedExtensions: []fwire.DeclarationExtension{}}
}

// registerPictureStore registers OA's picture store with the installed
// inventoryd beside this Panel. It does nothing for an isolated runtime or
// without an installed inventoryd.
func registerPictureStore(ctx context.Context, journal Journal) error {
	home, err := CurrentHome()
	if err != nil || home.Isolated {
		return nil
	}
	panel, err := os.Executable()
	if err != nil {
		return err
	}
	program := filepath.Join(filepath.Dir(panel), "inventoryd.exe")
	if info, err := os.Lstat(program); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	return registerPictureStoreAt(ctx, journal, home, program)
}

// registerPictureStoreAt writes the store's configuration for this
// version's catalogue and declares its provider once. A folder Setup never
// staged a model in needs no store. A registration the person removed
// stays removed; one an earlier version declared differently is replaced.
func registerPictureStoreAt(ctx context.Context, journal Journal, home Home, program string) error {
	root := pictureStoreRoot(home)
	if info, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	} else if !info.IsDir() {
		return errors.New("OA's picture model folder is not a folder")
	}
	selected, err := localsource.NewSetupInventoryRoot(root, localsource.SetupPictureStore)
	if err != nil {
		return err
	}
	selected.Files, selected.Models = pictureStoreCatalogue()
	config, err := localsource.SetupInventoryRootConfig(selected)
	if err != nil {
		return err
	}
	configPath := pictureStoreConfig(home)
	if err := writePictureStoreConfig(ctx, configPath, config); err != nil {
		return err
	}
	declaration := pictureStoreDeclaration(program, configPath)
	state, err := journal.Load(ctx)
	if err != nil {
		return err
	}
	registry, err := setupEngineRegistry(ctx)
	if err != nil {
		return err
	}
	if prior := state.PictureStore; prior != nil {
		page, found, err := registryDeclaration(ctx, registry, prior.Declaration)
		if errors.Is(err, errRegistryProgramMissing) {
			found, err = true, nil
		}
		if err != nil {
			return err
		}
		if !found && prior.Confirmed {
			return nil
		}
		if found && sameEngineDeclaration(prior.Declaration, declaration) {
			if !prior.Confirmed {
				return journal.savePictureStore(ctx, &pictureStoreRecord{Declaration: prior.Declaration, Confirmed: true})
			}
			return nil
		}
		if found {
			change, err := registry.Withdraw(ctx, page.Revision, prior.Declaration.Declaration.Name)
			if err != nil {
				return err
			}
			if change.Outcome != fwire.DeclarationEditOutcomeApplied {
				return errors.New("the registry did not withdraw Setup's earlier picture store")
			}
		}
	}
	page, found, err := registryDeclaration(ctx, registry, declaration)
	if err != nil {
		return err
	}
	if found {
		return journal.savePictureStore(ctx, &pictureStoreRecord{Declaration: declaration, Confirmed: true})
	}
	if err := journal.savePictureStore(ctx, &pictureStoreRecord{Declaration: declaration}); err != nil {
		return err
	}
	change, err := registry.Declare(ctx, page.Revision, declaration)
	if err != nil {
		return err
	}
	if change.Outcome != fwire.DeclarationEditOutcomeApplied {
		// A refused declaration is no registration the person could remove.
		if clearErr := journal.savePictureStore(ctx, nil); clearErr != nil {
			return clearErr
		}
		return errors.New("the registry did not confirm OA's picture store: " + change.Outcome.String())
	}
	return journal.savePictureStore(ctx, &pictureStoreRecord{Declaration: declaration, Confirmed: true})
}

// writePictureStoreConfig replaces the configuration when it differs.
func writePictureStoreConfig(ctx context.Context, file string, config []byte) error {
	if current, err := os.ReadFile(file); err == nil && bytes.Equal(current, config) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return cas.ChangeLimitContext(ctx, file, 1<<20, func([]byte) ([]byte, error) { return config, nil })
}

// savePictureStore records Setup's picture store registration.
func (j Journal) savePictureStore(ctx context.Context, record *pictureStoreRecord) error {
	return cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		state.PictureStore = record
		return json.Marshal(state)
	})
}

// ReconcilePictureEngine brings Setup's picture engine to this version, as
// the presenter starts after an install, an upgrade or a sign-in: OA's
// picture store is registered with this version's catalogue, and Setup's own
// PyTorch registration, while the registry still lists it, serves the pair
// of installed picture sets (reconcilePyTorchRegistration). It keeps the
// earlier registration until the storage inventory has read the store and
// says so (errPictureStoreUnread). A registration the person removed stays
// removed. Without a Setup-installed PyTorch engine it does nothing.
func ReconcilePictureEngine(ctx context.Context, journal Journal) error {
	state, err := journal.Load(ctx)
	if err != nil {
		return err
	}
	effect := state.Effects["pytorch-engine"]
	if state.Plan.ID == "" || !planHasPyTorch(state.Plan) || effect.Phase != Installed || effect.Removing {
		return nil
	}
	controller, err := DefaultPyTorchController(journal)
	if err != nil {
		return err
	}
	return controller.withProviderOperation(ctx, func() error {
		return errors.Join(registerPictureStore(ctx, journal), reconcilePyTorchRegistration(ctx, journal, 0))
	})
}
