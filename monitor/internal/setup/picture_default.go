package setup

import "context"

// InstalledPictureModel is the model family of a prepared picture model the
// account's picture default can name: the recommended one of the first
// picture engine that has one prepared, else that engine's first prepared
// offer. Plans that only stage files are skipped. For the PyTorch
// engine the family is the Klein set its registration serves, when one is
// recorded. ok is false when no picture model is prepared.
func InstalledPictureModel(ctx context.Context, journal Journal) (model string, ok bool, err error) {
	for _, recipe := range defaultPictureRecipes {
		var first, recommended string
		for _, offer := range PictureModelOffers(recipe.engine) {
			if offer.Plan.StagesPictureFilesOnly() {
				continue
			}
			prepared, err := PictureModelConfigured(ctx, journal, offer.Plan)
			if err != nil {
				return "", false, err
			}
			if !prepared {
				continue
			}
			if first == "" {
				first = offer.Plan.Model
			}
			if offer.Recommended {
				recommended = offer.Plan.Model
			}
		}
		chosen := recommended
		if chosen == "" {
			chosen = first
		}
		if chosen == "" {
			continue
		}
		if recipe.servedModel != nil {
			if served, err := recipe.servedModel(ctx, journal); err == nil && served != "" {
				chosen = served
			}
		}
		return chosen, true, nil
	}
	return "", false, nil
}
