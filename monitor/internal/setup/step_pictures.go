package setup

import (
	"context"
	"errors"
	"fmt"
	asks "github.com/openabstractions/abstraction-asks/go"
	"slices"
)

// The picture part's engine owner (research/first-run/DECISION.md §4, slice
// 2): One Install's package and feature steps, driven by Setup's runner
// instead of the Setup page's script. The step calls StepEngineOffer before
// the question and StepEngine after the answer.

// stepEngineChoices is the package plan the picture part approves: the
// journal's current choices with the app's picture engine added, so the
// packages of an earlier One Install plan stay in it and keep their effects.
func (r Runner) stepEngineChoices(state State, engine AppID) []AppID {
	selected := []AppID{engine}
	if state.Plan.ID != "" && r.Journal.Authorize(state.Plan) == nil {
		for _, id := range state.Plan.Selected {
			if !slices.Contains(selected, id) {
				selected = append(selected, id)
			}
		}
	}
	slices.Sort(selected)
	return selected
}

// The sentences Show details gives when this computer's graphics card fits
// a faster picture engine that the step cannot use yet.
const (
	stepNoEnginePackage = "the picture engine has no reviewed package for this computer"
	fallbackNoPackage   = asks.SetupFallbackNoPackage
	fallbackNoFeature   = asks.SetupFallbackNoFeature
)

// StepPictureEngine is the picture engine the setup step uses on this
// computer, from a picture app's ranked engine choices (Choice.Engines): the
// first whose packages fit this computer's graphics card, whose package plan
// is preparable here and whose provider feature this core has or can add;
// the last choice, the fallback, otherwise. feature reads an engine's core
// feature (EngineFeatureStatus). An engine that cannot be read is passed
// over for the next. Fallback is one sentence for Show details when the card
// fits an engine the step passed over, and "" when none fits it.
func (r Runner) StepPictureEngine(ctx context.Context, engines []AppID, feature func(AppID) (FeatureStatus, error)) (engine AppID, fallback string, err error) {
	if len(engines) == 0 {
		return "", "", errors.New("the picture app names no picture engine")
	}
	hardware, err := machineGraphics()
	if err != nil {
		hardware = nil // an unreadable card fits no graphics-specific package
	}
	families := graphicsOf(hardware)
	catalog := r.Journal.catalog()
	for _, candidate := range engines[:len(engines)-1] {
		if !catalog.engineFits(candidate, families) {
			continue
		}
		status, err := feature(candidate)
		if err != nil {
			fallback = firstSentence(fallback, fallbackNoFeature)
			continue
		}
		offer, err := r.StepEngineOffer(ctx, status, candidate)
		switch {
		case err == nil && offer.Reason == "":
			return candidate, "", nil
		case err == nil && offer.Reason == stepNoEnginePackage:
			fallback = firstSentence(fallback, fallbackNoPackage)
		default:
			fallback = firstSentence(fallback, fallbackNoFeature)
		}
	}
	return engines[len(engines)-1], fallback, nil
}

// firstSentence keeps the reason of the strongest engine passed over.
func firstSentence(kept, next string) string {
	if kept != "" {
		return kept
	}
	return next
}

// PictureHost is the inference host a picture engine choice's provider
// registers (Choice.Host): the host a picture app's rules name. It is ""
// for a choice that is no engine.
func PictureHost(engine AppID) string {
	for _, choice := range NewCatalog().Choices {
		if choice.ID == engine {
			return choice.Host
		}
	}
	return ""
}

// PictureHosts are the hosts of every picture engine choice.
func PictureHosts() []string {
	var hosts []string
	for _, choice := range NewCatalog().Choices {
		if choice.Host != "" {
			hosts = append(hosts, choice.Host)
		}
	}
	return hosts
}

// StepEngineOffer reads the engine part of the picture offer for engine, the
// picture engine choice the app's catalog entry names: the package plan,
// what it still downloads and whether the packages and the provider feature
// are installed. Reason is set when this Setup cannot prepare the engine
// here, such as a package with no reviewed pin for this computer.
func (r Runner) StepEngineOffer(ctx context.Context, feature FeatureStatus, engine AppID) (StepPictureOffer, error) {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return StepPictureOffer{}, err
	}
	selected := r.stepEngineChoices(state, engine)
	plan, err := r.Journal.catalog().Plan(selected)
	if err != nil {
		return StepPictureOffer{}, err
	}
	offer := StepPictureOffer{Engine: engine, PlanID: plan.ID, Packages: selected, PackageLicenses: slices.Clone(plan.Licenses)}
	switch {
	case !plan.Preparable:
		offer.Reason = stepNoEnginePackage
		return offer, nil
	case !feature.Installed && !feature.Available:
		offer.Reason = "the local picture engine feature cannot be added here"
		if feature.Reason != "" {
			offer.Reason = feature.Reason
		}
		return offer, nil
	}
	installed := true
	for _, component := range plan.Components {
		effect := state.Effects[component.ID]
		prior, had := planComponent(state.Plan, component.ID)
		if !had || !sameArtifact(prior.Artifact, component.Artifact) || effect.Phase != Installed || r.installedTreeMissing(state, component.ID) != "" {
			installed = false
		}
	}
	if !installed {
		// The plan's totals: a partly installed plan states the whole need.
		offer.PackageBytes, offer.PackagePeakBytes = plan.DownloadBytes, plan.PeakBytes
	}
	offer.EngineReady = installed && feature.Installed
	return offer, nil
}

// StepEngine makes one transition toward the picture engine: it records the
// package plan the answer approved, adds the provider feature through
// addFeature, then advances the next automatic package. It answers true once
// the feature and every package are installed.
func (r Runner) StepEngine(ctx context.Context, offer StepPictureOffer, feature func() (FeatureStatus, error), addFeature func(context.Context) (FeatureStatus, error)) (bool, error) {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return false, err
	}
	if state.Plan.ID != offer.PlanID || state.ApprovedPlanID != state.Plan.ID {
		// The answer approved exactly this plan; a different one is not
		// recorded.
		plan, err := r.Journal.catalog().Plan(offer.Packages)
		if err != nil {
			return false, err
		}
		if plan.ID != offer.PlanID {
			return false, errors.New("the reviewed picture engine packages changed; set up again")
		}
		if _, err := r.Start(ctx, offer.Packages); err != nil {
			return false, err
		}
	}
	status, err := feature()
	if err != nil {
		return false, err
	}
	if !status.Installed {
		if status, err = addFeature(ctx); err != nil {
			return false, err
		}
		if !status.Installed {
			if status.Reason != "" {
				return false, errors.New(status.Reason)
			}
			return false, errors.New("the local picture engine feature was not added")
		}
	}
	return r.AdvanceAutomatic(ctx)
}

// NextAutomatic names the component of the approved plan that advances next
// with no person's action, or "" when none does now. It is the one package
// driver: the setup step advances through AdvanceAutomatic and the Setup
// page through /setup/advance-next. A component advances automatically
// while it is pending, requested with a finished transfer, verified, staged
// or checked, or when an installed or failed version leaves earlier
// versions to remove; a package kind whose earlier version waits for the
// app's updated registration (retiresAfterRegistration) is left to the
// Panel's registration, which removes it. Every dependency must be
// installed first.
func (s Status) NextAutomatic() string {
	if !s.CurrentCatalog || !s.Plan.Preparable {
		return ""
	}
	rows := make(map[string]ComponentStatus, len(s.Components))
	for _, row := range s.Components {
		rows[row.ID] = row
	}
	for _, component := range s.Plan.Components {
		row, ok := rows[component.ID]
		if !ok || row.Effect.Removing {
			continue
		}
		phase := row.Effect.Phase
		if !retiringAutomatically(component.ID, row.Effect) && !slices.Contains([]Phase{Pending, Requested, Verified, Staged, Checked}, phase) {
			continue
		}
		ready := true
		for _, dependency := range component.Dependencies {
			if rows[dependency].Effect.Phase != Installed {
				ready = false
			}
		}
		if !ready {
			continue
		}
		if phase == Requested && (row.Transfer == nil || !slices.Contains([]string{"complete", "failed", "cancelled"}, row.Transfer.State)) {
			continue
		}
		return component.ID
	}
	return ""
}

// retiringAutomatically is an installed or failed version whose earlier
// versions are removed next; an installed version whose kind waits for the
// app's updated registration is not.
func retiringAutomatically(componentID string, effect Effect) bool {
	return (effect.Phase == Installed || effect.Phase == Failed) && len(effect.Retired) > 0 &&
		!(retiresAfterRegistration(componentID) && effect.Phase == Installed)
}

// AdvanceAutomatic makes the next automatic transition of the approved
// plan's packages (NextAutomatic) and answers true once every package is
// installed. A failed or uncertain package stops it with its reason; a
// running transfer or install leaves it waiting.
func (r Runner) AdvanceAutomatic(ctx context.Context) (bool, error) {
	status, err := r.Status(ctx)
	if err != nil {
		return false, err
	}
	if !status.CurrentCatalog || !status.Plan.Preparable {
		return false, errors.New("the approved packages cannot be prepared on this computer")
	}
	if next := status.NextAutomatic(); next != "" {
		_, err := r.Advance(ctx, next)
		return false, err
	}
	rows := make(map[string]ComponentStatus, len(status.Components))
	for _, row := range status.Components {
		rows[row.ID] = row
	}
	done := true
	for _, component := range status.Plan.Components {
		row, ok := rows[component.ID]
		if !ok {
			return false, fmt.Errorf("%s has no setup record", component.Title)
		}
		switch {
		case row.Effect.Phase == Failed || row.Effect.Phase == Uncertain:
			return false, fmt.Errorf("%s stopped: %s", component.Title, row.Effect.Reason)
		case row.Effect.Phase != Installed || row.Effect.Removing || retiringAutomatically(component.ID, row.Effect):
			done = false
		}
	}
	return done, nil
}
