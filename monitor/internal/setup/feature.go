package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	identity "github.com/openabstractions/abstraction-identity"
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

// AddEngineFeature makes the engine's provider feature present. Every x64
// core installs the Lemonade and PyTorch adapters (Level 1 in
// abstraction.wxs); a core since 2026-11-11 carries no TheNoise adapter.
// Setup only reads the adapter program and never changes
// Windows Installer state; a missing adapter answers with the sentence that
// tells the person how to restore it.
func (r Runner) AddEngineFeature(ctx context.Context, engine AppID) (FeatureStatus, error) {
	if engine != Lemonade && engine != TheNoise && engine != PyTorch {
		return FeatureStatus{}, errors.New("unknown installed engine feature")
	}
	if err := ctx.Err(); err != nil {
		return FeatureStatus{}, err
	}
	check := r.featureCheck
	if check == nil {
		check = func() (FeatureStatus, error) { return EngineFeatureStatus(engine) }
	}
	status, err := check()
	if err != nil || status.Installed {
		return status, err
	}
	if status.Reason == "" {
		status.Reason = adapterRestoreSentence(engineFeatureTitle(engine))
	}
	return status, errors.New(status.Reason)
}

// engineFeatureTitle names an engine's provider feature to the person.
func engineFeatureTitle(engine AppID) string {
	switch engine {
	case TheNoise:
		return "TheNoise Image"
	case PyTorch:
		return "Diffusers Image"
	default:
		return "Lemonade Image"
	}
}

// adapterRestoreSentence tells the person how to bring back an adapter every
// core installs. The current Setup plans each always-installed core feature
// local on every run (installer/apps/bundle-actions/actions.h), and the core
// offers no Repair in Apps (ARPNOREPAIR).
func adapterRestoreSentence(title string) string {
	return "Run the latest OpenAbstractions Setup to add " + title + "."
}

// adapterPresence reads an always-installed adapter beside the Panel program:
// installed when its provider program is a regular file in the Panel's tools
// folder. It reads no Windows Installer state.
func adapterPresence(panel, providerBinary, title string) FeatureStatus {
	if !identity.SamePathName(filepath.Base(panel), "Abstraction Panel.exe") ||
		!identity.SamePathName(filepath.Base(filepath.Dir(panel)), "tools") {
		return FeatureStatus{Reason: "Open the installed OpenAbstractions Panel to use " + title}
	}
	info, err := os.Lstat(filepath.Join(filepath.Dir(panel), providerBinary))
	if err == nil && info.Mode().IsRegular() {
		return FeatureStatus{Available: true, Installed: true}
	}
	return FeatureStatus{Reason: adapterRestoreSentence(title)}
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
		homeAllowed = providerHomeAllowed
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
