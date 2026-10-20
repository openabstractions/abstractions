package setup

import "slices"

// modelDigests is the SHA-256 of every model file Setup's reviewed catalog
// pins, once each, in order: the coding model's weights and the model files
// of the starter and offered picture models, read from their plans
// (model_plan.go owns each pin). These are the files Setup may find already
// on this computer before it asks the person. A picture plan's model files
// are the assets assetRoles names, as stagedDigests reads them; the engine
// archive is none. The PyTorch plan's files are pinned by its provider's set.
func modelDigests() []string {
	var out []string
	for _, asset := range CodingModelPlan().Assets {
		out = append(out, asset.Download.SHA256)
	}
	for _, plan := range append([]ModelPlan{ApprovedBF16Plan()}, PictureModelChoices()...) {
		for _, asset := range plan.Assets {
			if assetRoles[asset.ID] != "" {
				out = append(out, asset.Download.SHA256)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
