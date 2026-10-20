package setup

import "context"

// RecordedProgram is the program Setup's own install record gives app: the
// receipt-verified image of the native archive Setup installed for one of
// the app's catalog components, and "" when Setup installed none or the
// installed tree no longer matches its receipt. The setup step uses it to
// trust an installed application manifest that names this same program.
func (j Journal) RecordedProgram(ctx context.Context, app AppID) string {
	state, err := j.Load(ctx)
	if err != nil {
		return ""
	}
	for _, choice := range j.catalog().Choices {
		if choice.ID != app {
			continue
		}
		for _, component := range choice.Components {
			artifact, err := j.knownInstalledArtifact(state, component)
			if err != nil || artifact.ApplicationProgram == "" {
				continue
			}
			root, err := installPath(artifact)
			if err != nil {
				continue
			}
			if program, err := NativeArchiveProgram(ctx, artifact, root); err == nil {
				return program
			}
		}
	}
	return ""
}
