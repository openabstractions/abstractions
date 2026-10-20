package setup

import (
	"context"
	"errors"
	"slices"
)

// FeatureStatus is the installed core MSI's native provider feature state.
// Provider binaries and their engines and models have separate states.
type FeatureStatus struct {
	Available bool   `json:"available"`
	Installed bool   `json:"installed"`
	Reason    string `json:"reason,omitempty"`
}

// LemonadeFeatureStatus inspects the core MSI feature for this Panel install.
// It never changes Windows Installer state or starts a provider.
func LemonadeFeatureStatus() (FeatureStatus, error) {
	status, _, err := queryLemonadeFeature()
	return status, err
}

// EngineFeatureStatus accepts only the release's closed engine identities.
func EngineFeatureStatus(engine AppID) (FeatureStatus, error) {
	status, _, err := queryEngineFeature(engine)
	return status, err
}

func (r Runner) AddEngineFeature(ctx context.Context, engine AppID) (FeatureStatus, error) {
	if engine == Lemonade {
		return r.AddLemonadeFeature(ctx)
	}
	if engine != TheNoise && engine != PyTorch {
		return FeatureStatus{}, errors.New("unknown installed engine feature")
	}
	if err := lemonadeHomeAllowed(); err != nil {
		return FeatureStatus{}, err
	}
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return FeatureStatus{}, err
	}
	if state.Plan.ID == "" || state.ApprovedPlanID != state.Plan.ID || !slices.Contains(state.Plan.Selected, engine) {
		return FeatureStatus{}, errors.New("approve the current engine package plan first")
	}
	if err := r.Journal.Authorize(state.Plan); err != nil {
		return FeatureStatus{}, err
	}
	return addEngineFeature(ctx, engine)
}

// AddLemonadeFeature requests only the fixed OA-owned provider feature through
// normal Windows Installer maintenance. The approved setup plan must include
// local image use. A failed/interrupted request is reconciled by querying MSI.
func (r Runner) AddLemonadeFeature(ctx context.Context) (FeatureStatus, error) {
	if err := ctx.Err(); err != nil {
		return FeatureStatus{}, err
	}
	// The core MSI is the person's own installation, whichever runtime the
	// Panel serves.
	if err := lemonadeHomeAllowed(); err != nil {
		return FeatureStatus{}, err
	}
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return FeatureStatus{}, err
	}
	if state.ApprovedPlanID == "" || state.ApprovedPlanID != state.Plan.ID {
		return FeatureStatus{}, errors.New("setup plan has not been approved")
	}
	if err := r.Journal.Authorize(state.Plan); err != nil {
		return FeatureStatus{}, err
	}
	if !slices.Contains(state.Plan.Selected, Lemonade) && !slices.Contains(state.Plan.Selected, ComfyUI) {
		return FeatureStatus{}, errors.New("approved setup plan has no local image provider")
	}
	return addLemonadeFeature(ctx)
}

// ModelHostFeatureStatus inspects the core MSI's ModelHost feature. It never
// changes Windows Installer state or starts a provider.
func ModelHostFeatureStatus() (FeatureStatus, error) {
	status, _, err := queryModelHostFeature()
	return status, err
}

// AddModelHostFeature requests the fixed OA-owned model-host feature through
// normal Windows Installer maintenance. It is available only from an approved
// setup plan; engine and model choices remain separate approvals.
func (r Runner) AddModelHostFeature(ctx context.Context) (FeatureStatus, error) {
	if err := ctx.Err(); err != nil {
		return FeatureStatus{}, err
	}
	homeAllowed := r.modelHostHomeAllowed
	if homeAllowed == nil {
		homeAllowed = lemonadeHomeAllowed
	}
	if err := homeAllowed(); err != nil {
		return FeatureStatus{}, err
	}
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return FeatureStatus{}, err
	}
	if state.ApprovedPlanID == "" || state.ApprovedPlanID != state.Plan.ID {
		return FeatureStatus{}, errors.New("setup plan has not been approved")
	}
	if err := r.Journal.Authorize(state.Plan); err != nil {
		return FeatureStatus{}, err
	}
	if r.modelHostFeatureAdd != nil {
		return r.modelHostFeatureAdd(ctx)
	}
	return addModelHostFeature(ctx)
}

func (r Runner) requireLemonadeFeature(a Artifact) error {
	engine := Lemonade
	if a.InstallIdentity == "tn" {
		engine = TheNoise
	} else if a.InstallIdentity == "pt" {
		engine = PyTorch
	} else if a.InstallIdentity != "lemonade" {
		return nil // fixture packages and other applications use their own prerequisites.
	}
	check := r.featureCheck
	if check == nil {
		check = func() (FeatureStatus, error) { return EngineFeatureStatus(engine) }
	}
	status, err := check()
	if err != nil {
		return err
	}
	if !status.Installed {
		if status.Reason != "" {
			return errors.New(status.Reason)
		}
		return errors.New("install the optional OpenAbstractions engine feature first")
	}
	return nil
}
