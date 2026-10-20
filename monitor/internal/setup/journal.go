package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"

	cas "github.com/openabstractions/abstraction-cas/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
)

const maxJournalBytes = 1 << 20

type Phase string

const (
	Pending    Phase = "pending"
	Requested  Phase = "requested"  // public job identity is recorded
	Verified   Phase = "verified"   // exact staged bytes were checked
	Installing Phase = "installing" // effect may have happened; never auto-retry
	Installed  Phase = "installed"  // install identity was observed
	Uncertain  Phase = "uncertain"  // inspect identity and ask before relaunch
	Failed     Phase = "failed"     // a known no-effect refusal or failure
	// An update of an installed package passes two more steps before it is
	// Installed. The retained earlier version stays the active one until then.
	Staged  Phase = "staged"  // new version published beside the installed one; receipt verified
	Checked Phase = "checked" // new version passed its package health check
)

type WorkIdentity struct {
	Key               string             `json:"key"`
	HistoryEpoch      string             `json:"history_epoch"`
	Attempt           int64              `json:"attempt"`
	Binding           client.JobsBinding `json:"binding"`
	OperationsBinding client.JobsBinding `json:"operations_binding"`
}

type Effect struct {
	Revision uint64        `json:"revision"`
	Phase    Phase         `json:"phase"`
	Work     *WorkIdentity `json:"work,omitempty"`
	// A staged path is a local output name, never a service store path. Its
	// bytes are rehashed before any installer handler uses them.
	StagedPath string `json:"staged_path,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Retained is the exact installed package an update replaces. It stays on
	// disk and remains the active version until the update is activated.
	Retained *Artifact `json:"retained,omitempty"`
	// Retired lists versions Setup removes next: a version an activated update
	// replaced, or a staged version that failed. Removal deletes only files
	// that still match the version's receipt.
	Retired []Artifact `json:"retired,omitempty"`
	// Removing records the explicit removal's completed withdrawal boundary.
	// TheNoise also completed unlock; PyTorch resumes its CLI-owned removal.
	Removing bool `json:"removing,omitempty"`
	// ComponentRootKey is the filesystem identity of the generated PyTorch
	// root Setup created. Input receipts and profile names prove no creator.
	ComponentRootKey string `json:"component_root_key,omitempty"`
}

type State struct {
	Plan           Plan              `json:"plan"`
	ApprovedPlanID string            `json:"approved_plan_id"`
	Effects        map[string]Effect `json:"effects"`
	Configuration  map[string]bool   `json:"configuration"`
	// EngineDeclarations retain the exact arguments Setup approved and
	// supplied to the existing facade registry, including lost-reply recovery.
	EngineDeclarations map[AppID]fwire.DeclarationWithExtensions `json:"engine_declarations,omitempty"`
}

type Journal struct {
	Path string
	// Tests in this package can inject a complete fixture catalog. Product
	// callers always use the compiled release-owned catalog.
	catalogOverride *Catalog
}

func (j Journal) catalog() Catalog {
	if j.catalogOverride != nil {
		return *j.catalogOverride
	}
	return NewCatalog()
}

// Catalog is the release catalog this journal reviews, records and installs
// plans against. The Panel's review and approval read it, so the plan a
// person reviews is the plan the runner records and authorizes.
func (j Journal) Catalog() Catalog { return j.catalog() }

// WithAdopterAssets returns the journal with a pins document in
// adopter_assets.json's schema projected over its catalog's app pins, as the
// release projects the embedded document on this platform. Panel tests
// review and install stand-in builds through it; the Panel itself always
// uses the embedded document.
func (j Journal) WithAdopterAssets(data []byte) (Journal, error) {
	c := j.catalog()
	c.Components = slices.Clone(c.Components)
	if err := projectAdopterAssets(&c, runtime.GOOS, runtime.GOARCH, data); err != nil {
		return Journal{}, err
	}
	if err := c.Validate(); err != nil {
		return Journal{}, err
	}
	j.catalogOverride = &c
	return j, nil
}

// DefaultJournal is the plan journal in the Home this process selected
// (CurrentHome): the served runtime's state folder, or the person's own.
func DefaultJournal() (Journal, error) {
	home, err := CurrentHome()
	if err != nil {
		return Journal{}, err
	}
	return Journal{Path: filepath.Join(home.Config, "plan.json")}, nil
}

func (j Journal) Load(ctx context.Context) (State, error) {
	if j.Path == "" {
		return State{}, errors.New("setup journal path is empty")
	}
	raw, err := cas.ReadLimitContext(ctx, j.Path, maxJournalBytes)
	if err != nil || raw == nil {
		return State{}, err
	}
	return decodeState(raw)
}

// SavePlan records the person's one approval of this exact catalog-backed
// plan. The Panel calls it only after displaying package scope and licenses.
// It retains completed effects for unchanged exact artifacts. An installing or
// uncertain effect blocks plan replacement until reconciled.
func (j Journal) SavePlan(ctx context.Context, plan Plan) (State, error) {
	if err := j.Authorize(plan); err != nil {
		return State{}, err
	}
	if j.Path == "" {
		return State{}, errors.New("setup journal path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(j.Path), 0700); err != nil {
		return State{}, err
	}
	var saved State
	err := cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		previous, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if previous.Effects["pytorch-engine"].ComponentRootKey != "" {
			old, had := planComponent(previous.Plan, "pytorch-engine")
			next, has := planComponent(plan, "pytorch-engine")
			if !had || !has || old.Artifact == nil || next.Artifact == nil || *old.Artifact != *next.Artifact {
				return nil, errors.New("remove the journal-owned PyTorch component explicitly before changing or dropping its fixed package; its ownership evidence is preserved")
			}
		}
		if previous.Plan.ID != "" && previous.Plan.ID != plan.ID {
			for _, effect := range previous.Effects {
				if effect.Phase == Requested || effect.Phase == Installing || effect.Phase == Uncertain || effect.Removing {
					return nil, errors.New("finish or explicitly cancel active work, and reconcile uncertain installers, before replacing the setup plan")
				}
			}
		}
		saved = State{Plan: plan, ApprovedPlanID: plan.ID, Effects: make(map[string]Effect, len(plan.Components)), Configuration: make(map[string]bool)}
		for _, engine := range []AppID{TheNoise, PyTorch} {
			if slices.Contains(plan.Selected, engine) {
				old, had := planComponent(previous.Plan, string(engine)+"-engine")
				next, has := planComponent(plan, string(engine)+"-engine")
				if had && has && sameArtifact(old.Artifact, next.Artifact) {
					if declaration, ok := previous.EngineDeclarations[engine]; ok {
						if saved.EngineDeclarations == nil {
							saved.EngineDeclarations = map[AppID]fwire.DeclarationWithExtensions{}
						}
						saved.EngineDeclarations[engine] = declaration
					}
				}
			}
		}
		for _, requirement := range plan.Configuration {
			saved.Configuration[requirement.ID] = previous.Plan.ID == plan.ID && previous.Configuration[requirement.ID]
		}
		for _, component := range plan.Components {
			effect := Effect{Phase: Pending}
			previousComponent, had := planComponent(previous.Plan, component.ID)
			prior := previous.Effects[component.ID]
			if had && sameArtifact(previousComponent.Artifact, component.Artifact) {
				effect = prior
				if effect.Phase == "" {
					effect.Phase = Pending
				}
			} else if had {
				var err error
				if effect, err = changedPackageEffect(component.ID, previousComponent, prior, component.Artifact); err != nil {
					return nil, err
				}
			}
			saved.Effects[component.ID] = effect
		}
		return json.Marshal(saved)
	})
	return saved, err
}

// changedPackageEffect starts the effect of a component whose package pin the
// new plan changes. An installed earlier version becomes Retained, the active
// version until the update activates; an update still in progress keeps its
// retained version, and versions awaiting removal stay listed.
func changedPackageEffect(componentID string, previous PlannedComponent, prior Effect, next *Artifact) (Effect, error) {
	if prior.Phase == Staged || prior.Phase == Checked {
		return Effect{}, fmt.Errorf("an update of %s is staged but not activated; reconcile it before replacing the setup plan", componentID)
	}
	effect := Effect{Phase: Pending, Retired: slices.Clone(prior.Retired)}
	switch {
	case prior.Phase == Installed && previous.Artifact != nil:
		retained := *previous.Artifact
		effect.Retained = &retained
	case prior.Retained != nil:
		retained := *prior.Retained
		effect.Retained = &retained
	}
	if effect.Retained != nil && next != nil && sameVersionFolder(*next, *effect.Retained) {
		return Effect{}, fmt.Errorf("the new %s package reuses the installed version's folder %s; a changed package needs a new version", componentID, next.Version)
	}
	return effect, nil
}

// Update performs one compare-and-replace transition. A new process can read
// the journal and resume the exact step; competing windows cannot overwrite
// each other's effects.
func (j Journal) Update(ctx context.Context, planID, componentID string, expected Effect, next Effect) (State, error) {
	if j.Path == "" {
		return State{}, errors.New("setup journal path is empty")
	}
	var saved State
	err := cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID == "" || state.Plan.ID != planID || state.ApprovedPlanID != planID {
			return nil, errors.New("setup plan changed; reload before continuing")
		}
		if err := j.Authorize(state.Plan); err != nil {
			return nil, err
		}
		current, ok := state.Effects[componentID]
		if !ok || !reflect.DeepEqual(current, expected) {
			return nil, fmt.Errorf("setup component %q changed; reload before continuing", componentID)
		}
		if componentID == "pytorch-engine" && current.Removing {
			return nil, errors.New("PyTorch removal is in progress; finish explicit removal before another transition")
		}
		if !validTransition(expected.Phase, next.Phase) {
			return nil, fmt.Errorf("invalid setup transition %s -> %s", expected.Phase, next.Phase)
		}
		if next.Phase == Requested && !validWorkIdentity(next.Work) {
			return nil, errors.New("requested work needs its public job identity")
		}
		if next.Phase == Verified && next.StagedPath == "" {
			return nil, errors.New("verified artifact needs its staged output path")
		}
		if next.Phase == Installing && current.StagedPath == "" {
			return nil, errors.New("an installer needs a verified staged artifact")
		}
		if err := carryUpdate(state.Plan, componentID, expected, &next); err != nil {
			return nil, err
		}
		if next.Work == nil {
			next.Work = current.Work
		}
		if next.StagedPath == "" {
			next.StagedPath = current.StagedPath
		}
		if componentID == "pytorch-engine" {
			if next.ComponentRootKey != "" && next.ComponentRootKey != current.ComponentRootKey {
				return nil, errors.New("component root ownership changes only at its allocation boundary")
			}
			next.ComponentRootKey = current.ComponentRootKey
		}
		next.Revision = current.Revision + 1
		state.Effects[componentID] = next
		saved = state
		return json.Marshal(state)
	})
	return saved, err
}

// carryUpdate owns the update fields of an effect. Callers never set them: the
// retained version is carried until activation moves it to Retired, and a
// staged version that fails is retired so its files are removed.
func carryUpdate(plan Plan, componentID string, current Effect, next *Effect) error {
	next.Retained, next.Retired = current.Retained, slices.Clone(current.Retired)
	update := current.Retained != nil
	staged := update || firstInstallChecked(componentID)
	if (next.Phase == Staged || next.Phase == Checked) && !staged {
		return errors.New("only an update or a package with a first-install health check is staged and checked")
	}
	if !staged {
		return nil
	}
	component, _ := planComponent(plan, componentID)
	if component.Artifact == nil {
		return errors.New("the package has no reviewed release artifact")
	}
	switch {
	case next.Phase == Installed && update && current.Phase != Checked:
		return errors.New("an update activates only after its staged version passed its health check")
	case next.Phase == Installed && !update:
	case next.Phase == Installed:
		next.Retired = append(next.Retired, *current.Retained)
		next.Retained = nil
	case next.Phase == Failed && (current.Phase == Staged || current.Phase == Checked):
		next.Retired = append(next.Retired, *component.Artifact)
	case next.Phase == Requested || next.Phase == Verified:
		for _, retired := range current.Retired {
			if sameArtifact(&retired, component.Artifact) {
				return errors.New("the failed copy of this version is still being removed; advance setup first")
			}
		}
	}
	return nil
}

// ForgetRetired records that one retired version's package files are gone.
// Removal is no new installer effect, so a review-only earlier plan may also
// record it.
func (j Journal) ForgetRetired(ctx context.Context, planID, componentID string, expected Effect, retired Artifact, reason string) (State, error) {
	var saved State
	err := cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID == "" || state.Plan.ID != planID || state.ApprovedPlanID != planID {
			return nil, errors.New("setup plan changed; reload before continuing")
		}
		if err := j.Authorize(state.Plan); err != nil {
			if priorErr := j.recognizePriorPlan(state.Plan); priorErr != nil {
				return nil, err
			}
		}
		current, ok := state.Effects[componentID]
		if !ok || !reflect.DeepEqual(current, expected) {
			return nil, fmt.Errorf("setup component %q changed; reload before continuing", componentID)
		}
		index := slices.IndexFunc(current.Retired, func(a Artifact) bool { return a == retired })
		if index < 0 {
			return nil, errors.New("this version is not awaiting removal")
		}
		current.Retired = slices.Delete(slices.Clone(current.Retired), index, index+1)
		if len(current.Retired) == 0 {
			current.Retired = nil
		}
		if reason != "" {
			current.Reason = reason
		}
		current.Revision++
		state.Effects[componentID] = current
		saved = state
		return json.Marshal(state)
	})
	return saved, err
}

// Authorize rejects a journal or browser-supplied plan that differs from the
// compiled release catalog. Install handlers must call this again before use.
func (j Journal) Authorize(plan Plan) error {
	want, err := j.catalog().Plan(plan.Selected)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, plan) {
		return errors.New("setup plan differs from the trusted release catalog")
	}
	return nil
}

// ComponentArtifact returns the release-owned pin for a prepared component.
// A stored URL, action or staged path is never authority to run a package.
func (j Journal) ComponentArtifact(state State, componentID string) (Artifact, error) {
	if err := j.Authorize(state.Plan); err != nil {
		return Artifact{}, err
	}
	component, ok := planComponent(state.Plan, componentID)
	if !ok || component.Artifact == nil {
		return Artifact{}, errors.New("component has no reviewed release artifact")
	}
	return *component.Artifact, nil
}

// knownInstalledArtifact resolves an old pin from an approved installed effect.
// Callers must also verify the installed receipt and files before trusting it.
// Only the current catalog can authorize new installer effects.
func (j Journal) knownInstalledArtifact(state State, componentID string) (Artifact, error) {
	if state.Plan.ID == "" || state.ApprovedPlanID != state.Plan.ID {
		return Artifact{}, errors.New("setup plan is not approved")
	}
	if err := j.recognizePriorPlan(state.Plan); err != nil {
		return Artifact{}, err
	}
	artifact, err := activeArtifact(state, componentID)
	if err != nil {
		return Artifact{}, err
	}
	if !j.catalog().knownInstallArtifact(componentID, artifact) {
		return Artifact{}, errors.New("installed package pin is not known to this release")
	}
	return artifact, nil
}

// InstalledVersions returns the version of each component that is in use,
// for display. It confers no authority to launch or install.
func (j Journal) InstalledVersions(ctx context.Context) (map[string]Artifact, error) {
	state, err := j.Load(ctx)
	if err != nil {
		return nil, err
	}
	installed := map[string]Artifact{}
	for _, component := range state.Plan.Components {
		if a, err := activeArtifact(state, component.ID); err == nil {
			installed[component.ID] = a
		}
	}
	return installed, nil
}

// activeArtifact names the version that is in use: the plan's package once it
// is Installed, and the retained earlier version while an update proceeds.
func activeArtifact(state State, componentID string) (Artifact, error) {
	component, ok := planComponent(state.Plan, componentID)
	effect := state.Effects[componentID]
	switch {
	case ok && component.Artifact != nil && effect.Phase == Installed:
		return *component.Artifact, nil
	case ok && effect.Retained != nil:
		return *effect.Retained, nil
	}
	return Artifact{}, errors.New("component has no installed package")
}

// recognizePriorPlan checks only fixed release-owned app identities. The
// journal's old URL, action and work identity never authorize fresh effects.
func (j Journal) recognizePriorPlan(plan Plan) error {
	catalog := j.catalog()
	if err := catalog.Validate(); err != nil {
		return err
	}
	if len(plan.CatalogSHA256) != 64 || len(plan.ID) != 64 {
		return errors.New("prior setup plan has invalid identity")
	}
	if _, err := hex.DecodeString(plan.CatalogSHA256); err != nil {
		return errors.New("prior setup plan has invalid catalog hash")
	}
	selected := make([]AppID, len(plan.Selected))
	copy(selected, plan.Selected)
	slices.Sort(selected)
	if len(selected) == 0 || !slices.Equal(selected, plan.Selected) {
		return errors.New("prior setup plan has invalid choices")
	}
	choices := map[AppID]bool{}
	for _, choice := range catalog.Choices {
		choices[choice.ID] = true
	}
	for i, id := range selected {
		if !choices[id] || i > 0 && selected[i-1] == id {
			return errors.New("prior setup plan has unknown or duplicate choice")
		}
	}
	key, _ := json.Marshal(struct {
		Catalog  string  `json:"catalog"`
		Selected []AppID `json:"selected"`
	}{plan.CatalogSHA256, selected})
	want := sha256.Sum256(key)
	if plan.ID != hex.EncodeToString(want[:]) {
		return errors.New("prior setup plan identity changed")
	}
	componentIDs := map[string]bool{}
	for _, component := range catalog.Components {
		componentIDs[component.ID] = true
	}
	seen := map[string]bool{}
	for _, component := range plan.Components {
		if !componentIDs[component.ID] || seen[component.ID] {
			return errors.New("prior setup plan has unknown or duplicate component")
		}
		seen[component.ID] = true
		if component.Artifact != nil && !catalog.knownInstallArtifact(component.ID, *component.Artifact) {
			return fmt.Errorf("prior setup package %q is not known to this release", component.ID)
		}
	}
	return nil
}

func (s State) Ready() bool {
	if s.ApprovedPlanID != s.Plan.ID || !s.Plan.Preparable || len(s.Plan.Components) == 0 {
		return false
	}
	for _, component := range s.Plan.Components {
		if s.Effects[component.ID].Phase != Installed {
			return false
		}
	}
	for _, requirement := range s.Plan.Configuration {
		if !s.Configuration[requirement.ID] {
			return false
		}
	}
	return true
}

// ConfirmConfiguration is called only after the owner of that requirement
// confirms its public-IPC and app-owned configuration writes succeeded.
func (j Journal) ConfirmConfiguration(ctx context.Context, planID, requirementID string) (State, error) {
	var saved State
	err := cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID != planID {
			return nil, errors.New("setup plan changed; reload before continuing")
		}
		if err := j.Authorize(state.Plan); err != nil {
			return nil, err
		}
		if _, ok := state.Configuration[requirementID]; !ok {
			return nil, errors.New("unknown setup configuration requirement")
		}
		state.Configuration[requirementID] = true
		saved = state
		return json.Marshal(state)
	})
	return saved, err
}

func validTransition(before, after Phase) bool {
	switch before {
	case Pending:
		return slices.Contains([]Phase{Requested, Verified, Installed, Staged, Failed}, after)
	case Requested:
		return slices.Contains([]Phase{Verified, Failed}, after)
	case Verified:
		return slices.Contains([]Phase{Installing, Installed, Failed}, after)
	case Installing:
		return slices.Contains([]Phase{Installed, Staged, Uncertain}, after)
	case Uncertain:
		return slices.Contains([]Phase{Installed, Staged, Failed, Verified}, after)
	case Failed:
		return slices.Contains([]Phase{Requested, Verified, Installed}, after)
	case Staged:
		return slices.Contains([]Phase{Checked, Failed}, after)
	case Checked:
		return slices.Contains([]Phase{Installed, Failed}, after)
	}
	return false
}

func validWorkIdentity(work *WorkIdentity) bool {
	return work != nil && work.Key != "" && work.HistoryEpoch != "" && work.Attempt >= 0 &&
		work.Binding.LogicalOwner != "" && work.Binding.Endpoint != "" &&
		work.OperationsBinding.LogicalOwner != "" && work.OperationsBinding.Endpoint != ""
}

func decodeState(raw []byte) (State, error) {
	if raw == nil {
		return State{}, nil
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("setup journal is invalid: %w", err)
	}
	if state.Plan.ID == "" || state.Effects == nil || state.Configuration == nil {
		return State{}, errors.New("setup journal has no plan or effects")
	}
	return state, nil
}

func planComponent(plan Plan, id string) (PlannedComponent, bool) {
	for _, component := range plan.Components {
		if component.ID == id {
			return component, true
		}
	}
	return PlannedComponent{}, false
}

func sameArtifact(a, b *Artifact) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.SHA256 == b.SHA256 && a.InstallIdentity == b.InstallIdentity && a.Action == b.Action
}
