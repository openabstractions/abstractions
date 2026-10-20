package setup

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultBF16Controller binds the reviewed model plan to the application's
// private intent/staging paths, the public OA transfer, and the fixed installed
// Lemonade configuration bridge. selected must re-read the exact public
// program-scoped image policy; it is never taken from a browser request.
func DefaultBF16Controller(selected func(context.Context) (bool, error)) (ModelController, error) {
	if selected == nil {
		return ModelController{}, errors.New("model selection reader is required")
	}
	journal, err := DefaultJournal()
	if err != nil {
		return ModelController{}, err
	}
	// Downloads go into the provider's own download folder, on its models
	// folder's volume, and the provider publishes each by rename: the disk
	// holds one file per download (VISION 2026-10-01).
	root, downloads, err := defaultLemonadeFolders()
	if err != nil {
		return ModelController{}, err
	}
	return ModelController{
		JournalPath:  filepath.Join(filepath.Dir(journal.Path), "model.json"),
		StageDir:     downloads,
		ProviderRoot: root,
		Transfer:     PublicTransfer{},
		Stage: func(ctx context.Context, root string, files BF16StageFiles, progress func(int64, int64) error, replace bool) error {
			return ConfigureLemonade(ctx, root, files.Main, files.Encoder, files.VAE, files.BackendArchive, progress, replace)
		},
		Probe:    ProbeLemonade,
		Selected: selected,
	}, nil
}

// DefaultChoiceController binds one offered picture model plan
// (PictureModelChoices) to its own journal beside the starter plan's, the
// same staging folder and transfer, and the installed provider's model verb.
// Its probe requires the provider to verify and to serve the plan's model.
func DefaultChoiceController(plan ModelPlan) (ModelController, error) {
	known := slices.IndexFunc(PictureModelChoices(), func(p ModelPlan) bool { return p.ID == plan.ID })
	if known < 0 {
		return ModelController{}, errors.New("unknown picture model plan")
	}
	starter, err := DefaultBF16Controller(func(context.Context) (bool, error) { return false, nil })
	if err != nil {
		return ModelController{}, err
	}
	model := plan.Model
	return ModelController{
		Plan:         &plan,
		JournalPath:  filepath.Join(filepath.Dir(starter.JournalPath), "model-"+strings.TrimPrefix(model, "user.")+".json"),
		StageDir:     starter.StageDir,
		ProviderRoot: starter.ProviderRoot,
		Transfer:     PublicTransfer{},
		StagedModels: LemonadeStagedModels,
		StageModel:   StageLemonadeModel,
		Probe: func(ctx context.Context, root string) (bool, error) {
			verified, err := ProbeLemonade(ctx, root)
			if err != nil || !verified {
				return false, err
			}
			staged, err := LemonadeStagedModels(ctx)
			return slices.Contains(staged, model), err
		},
	}, nil
}
