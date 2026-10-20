package setup

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// The starter picture model of each picture engine: the reviewed plan Setup
// installs with the engine when it holds no picture model yet, and the model
// journal beside Setup's plan journal that records its preparation.

type fixedPictureControllerKind int

const (
	noFixedPictureController fixedPictureControllerKind = iota
	panelPictureController
	cachedPictureController
	livePictureController
)

type pictureOfferRecipe struct {
	plan       func() ModelPlan
	journal    string
	caption    string
	stagedOnly bool
}

type starterPictureRecipe struct {
	engine          AppID
	plan            func() ModelPlan
	fixedController fixedPictureControllerKind
	fixedFactory    func(Journal) (ModelController, error)
	recordInstalled func(context.Context) error
	removePackage   func(Runner, context.Context) error
	// A zero rank offers a starter without selecting it as the account default.
	defaultRank int
	servedModel func(context.Context, Journal) (string, error)

	starterJournal      string
	starterOfferJournal bool
	installedFallback   bool
	standalone          bool
	reviewedLayout      bool
	stagedOnlyEngine    AppID
	extraOffers         []pictureOfferRecipe
	providerChoices     func() []ModelPlan
}

var starterPictureRecipes, defaultPictureRecipes []starterPictureRecipe

func init() {
	// The live controller consults its own offers, so bind these factories
	// after package variables are initialized.
	runtimeRecipes := map[AppID]starterPictureRecipe{}
	for _, recipe := range []starterPictureRecipe{portablePictureRuntime(), sharedPictureRuntime(), cpuPictureRuntime()} {
		runtimeRecipes[recipe.engine] = recipe
	}

	for _, facts := range product.PictureSetupRecipes() {
		engine := AppID(facts.Engine)
		recipe, ok := runtimeRecipes[engine]
		if !ok {
			panic("missing picture recipe runtime for " + facts.Engine)
		}
		recipe.defaultRank = facts.DefaultRank
		recipe.standalone, recipe.installedFallback = facts.Standalone, facts.InstalledFallback
		for _, offer := range recipe.extraOffers {
			if offer.stagedOnly {
				recipe.stagedOnlyEngine = offer.plan().Engine
			}
		}
		starterPictureRecipes = append(starterPictureRecipes, recipe)
		if recipe.defaultRank > 0 {
			defaultPictureRecipes = append(defaultPictureRecipes, recipe)
		}
	}
	slices.SortStableFunc(defaultPictureRecipes, func(a, b starterPictureRecipe) int {
		return cmp.Compare(a.defaultRank, b.defaultRank)
	})
}

// ReconcileInstalledPictureProviderRecords asks the installed-record owners
// configured by the picture recipes, independently of the engine being offered.
func ReconcileInstalledPictureProviderRecords(ctx context.Context) error {
	var failures []error
	for _, recipe := range starterPictureRecipes {
		if recipe.recordInstalled == nil {
			continue
		}
		if err := recipe.recordInstalled(ctx); err != nil {
			title := string(recipe.engine)
			for _, choice := range compiledCatalog().Choices {
				if choice.ID == recipe.engine {
					title = choice.Name()
					break
				}
			}
			failures = append(failures, fmt.Errorf("%s installed models: %w", title, err))
		}
	}
	return errors.Join(failures...)
}

// PictureEngineRemoval supplies the package removal owned by the engine's
// recipe. Setup uses the same capability for its overview and removal route.
func PictureEngineRemoval(engine AppID) func(Runner, context.Context) error {
	for _, recipe := range starterPictureRecipes {
		if recipe.engine == engine {
			return recipe.removePackage
		}
	}
	return nil
}

func fixedPictureRecipe(engine AppID) (starterPictureRecipe, error) {
	for _, recipe := range starterPictureRecipes {
		if recipe.fixedController == noFixedPictureController {
			continue
		}
		if engine == recipe.engine || engine == "" && recipe.fixedController == panelPictureController {
			return recipe, nil
		}
	}
	return starterPictureRecipe{}, errors.New("unknown fixed picture engine")
}

// FixedPictureEngine resolves the fixed picture setup selection. An empty
// selection uses the engine whose controller the Panel owns.
func FixedPictureEngine(engine AppID) (AppID, error) {
	recipe, err := fixedPictureRecipe(engine)
	return recipe.engine, err
}

// FixedPictureController supplies a fixed selection's reviewed starter and
// controller factory. A nil factory uses the Panel's existing controller.
// A live factory is reopened on each read and supplies its own reviewed plan.
func FixedPictureController(engine AppID) (plan ModelPlan, open func(Journal) (ModelController, error), live bool, err error) {
	recipe, err := fixedPictureRecipe(engine)
	if err != nil {
		return ModelPlan{}, nil, false, err
	}
	return recipe.plan(), recipe.fixedFactory, recipe.fixedController == livePictureController, nil
}

func pictureRecipeFor(engine AppID) (starterPictureRecipe, bool) {
	for _, recipe := range starterPictureRecipes {
		if recipe.engine == engine {
			return recipe, true
		}
	}
	return starterPictureRecipe{}, false
}

// StarterPictureModel is the reviewed starter picture-model plan of engine:
// the FLUX.2 Klein files in the format that engine's controller stages; the
// PyTorch engine's is the compact set.
func StarterPictureModel(engine AppID) (ModelPlan, bool) {
	recipe, ok := pictureRecipeFor(engine)
	if !ok {
		return ModelPlan{}, false
	}
	return recipe.plan(), true
}

// StarterPictureEngine is the engine that offers the plan with id, or "":
// its starter or another offered toggle, so an Install record that names
// the full-precision Klein, the PyTorch starter up to 2026-11-09, keeps its
// engine.
func StarterPictureEngine(id string) AppID {
	for _, recipe := range starterPictureRecipes {
		if _, ok := PictureOfferNamed(recipe.engine, id); ok {
			return recipe.engine
		}
	}
	return ""
}

// PictureEngineOf is the engine plan runs pictures on: the engine of its
// picture app (the first of the app's catalog engines the plan selects, else
// the first whose engine component the plan installs). Without a picture app
// it chooses a selected engine whose recipe allows standalone pictures. The
// app result reports whether a picture app is present in the plan.
func PictureEngineOf(plan Plan) (engine AppID, app bool) {
	if _, engine, ok := NewCatalog().PicturePlanChoice(plan); ok {
		return engine, true
	}
	for _, recipe := range starterPictureRecipes {
		if recipe.standalone && slices.Contains(plan.Selected, recipe.engine) {
			return recipe.engine, false
		}
	}
	return "", false
}

// PicturePlanChoice returns the picture app whose selected or planned
// engine supplies the plan's picture configuration.
func (c Catalog) PicturePlanChoice(plan Plan) (Choice, AppID, bool) {
	for _, choice := range c.Choices {
		if choice.Use != UseImage || len(choice.Engines) == 0 || !slices.Contains(plan.Selected, choice.ID) {
			continue
		}
		for _, engine := range choice.Engines {
			if slices.Contains(plan.Selected, engine) {
				return choice, engine, true
			}
		}
		for _, engine := range choice.Engines {
			if _, ok := planComponent(plan, string(engine)+"-engine"); ok {
				return choice, engine, true
			}
		}
	}
	return Choice{}, "", false
}

// StarterModelJournal is the model journal of engine's starter picture
// model beside Setup's plan journal.
func StarterModelJournal(journal Journal, engine AppID) string {
	name := "model-" + string(engine) + ".json"
	if recipe, ok := pictureRecipeFor(engine); ok && recipe.starterJournal != "" {
		name = recipe.starterJournal
	}
	return filepath.Join(filepath.Dir(journal.Path), name)
}

// PictureOffer is one picture model Setup offers as its own toggle beside a
// picture app: the plan, whether it is the engine's recommended model (the
// small default an app without a model arrives with, ticked), and its
// reviewed caption.
type PictureOffer struct {
	Plan        ModelPlan
	Recommended bool
	caption     string
}

// ReviewCaption gives the reason the reviewed offer is shown on its toggle.
func (offer PictureOffer) ReviewCaption() string {
	if offer.Recommended {
		return "Recommended for this computer"
	}
	if offer.caption != "" {
		return offer.caption
	}
	return "Larger download"
}

// StagesPictureFilesOnly reports whether a reviewed picture plan is ready
// once its files are staged and has no provider configuration to confirm.
func (plan ModelPlan) StagesPictureFilesOnly() bool {
	if plan.Engine != "" {
		for _, recipe := range starterPictureRecipes {
			if plan.Engine == recipe.stagedOnlyEngine {
				return true
			}
		}
	}
	return false
}

// PictureModelOffers are the picture models Setup offers for engine, the
// recommended starter first, followed by the reviewed optional offers.
func PictureModelOffers(engine AppID) []PictureOffer {
	recipe, ok := pictureRecipeFor(engine)
	if !ok {
		return nil
	}
	offers := []PictureOffer{{Plan: recipe.plan(), Recommended: true}}
	for _, extra := range recipe.extraOffers {
		plan := extra.plan()
		caption := extra.caption
		if plan.StagesPictureFilesOnly() && caption != "" {
			caption += ", for " + ApplicationName(plan.Engine)
		}
		offers = append(offers, PictureOffer{Plan: plan, caption: caption})
	}
	return offers
}

// PictureChoicesForEngine lists the optional plans the Setup picture controls
// can offer. The installed fixed plan stays out of the shared-engine toggles.
func PictureChoicesForEngine(engine AppID, fixed string) []ModelPlan {
	recipe, ok := pictureRecipeFor(engine)
	if !ok {
		return nil
	}
	if recipe.providerChoices != nil {
		return recipe.providerChoices()
	}
	if len(recipe.extraOffers) == 0 {
		return nil
	}
	var plans []ModelPlan
	for _, offer := range PictureModelOffers(engine) {
		if offer.Plan.ID != fixed {
			plans = append(plans, offer.Plan)
		}
	}
	return plans
}

// FindPictureChoice resolves the reviewed ID and its controller's engine.
// Recipe offers take precedence over provider choices.
func FindPictureChoice(id string) (ModelPlan, AppID, bool) {
	if id == "" {
		return ModelPlan{}, "", false
	}
	for _, recipe := range starterPictureRecipes {
		if len(recipe.extraOffers) > 0 {
			if offer, ok := PictureOfferNamed(recipe.engine, id); ok {
				return offer.Plan, recipe.engine, true
			}
		}
	}
	for _, recipe := range starterPictureRecipes {
		if recipe.providerChoices != nil {
			for _, plan := range recipe.providerChoices() {
				if plan.ID == id {
					return plan, recipe.engine, true
				}
			}
		}
	}
	return ModelPlan{}, "", false
}

// PictureOfferNamed is the offer of engine whose plan has id.
func PictureOfferNamed(engine AppID, id string) (PictureOffer, bool) {
	for _, offer := range PictureModelOffers(engine) {
		if id != "" && offer.Plan.ID == id {
			return offer, true
		}
	}
	return PictureOffer{}, false
}

// PictureModelJournal is the model journal of an offered picture plan
// beside Setup's plan journal: the starter's for the starter, and one per
// other offer. The full-precision Klein keeps model-pytorch.json.
func PictureModelJournal(journal Journal, plan ModelPlan) string {
	dir := filepath.Dir(journal.Path)
	for _, recipe := range starterPictureRecipes {
		for _, extra := range recipe.extraOffers {
			if extra.journal != "" && extra.plan().ID == plan.ID {
				return filepath.Join(dir, extra.journal)
			}
		}
		// Only the historically mapped compact starter takes this route.
		if recipe.starterOfferJournal && recipe.plan().ID == plan.ID {
			return StarterModelJournal(journal, recipe.engine)
		}
	}
	return filepath.Join(dir, "model-"+string(plan.Engine)+"-"+plan.Model+".json")
}

// PictureModelConfigured reports whether plan is prepared, as its model
// journal records.
func PictureModelConfigured(ctx context.Context, journal Journal, plan ModelPlan) (bool, error) {
	c := ModelController{Plan: &plan, JournalPath: PictureModelJournal(journal, plan)}
	state, err := c.load(ctx)
	if err != nil {
		return false, err
	}
	return state.ProviderConfigured, nil
}

// PictureEngineHasModel reports whether any picture model Setup offers for
// engine is prepared. An app whose engine has one arrives with no model
// ticked.
func PictureEngineHasModel(ctx context.Context, journal Journal, engine AppID) (bool, error) {
	for _, offer := range PictureModelOffers(engine) {
		configured, err := PictureModelConfigured(ctx, journal, offer.Plan)
		if err != nil || configured {
			return configured, err
		}
	}
	return false, nil
}

// pyTorchKleinPlans are the Setup plans of the Klein sets the PyTorch
// engine serves, the full-precision one first.
func pyTorchKleinPlans() []ModelPlan {
	return []ModelPlan{PyTorchFullModelPlan(), PyTorchModelPlan()}
}

// PictureDefaultDue reports whether Setup writes the account's picture
// default for plan, a prepared PyTorch Klein plan whose own journal records
// no written default, when the engine serves served. The default is the
// account's, written once: when plan's set is the one now served (the
// served set changed to it), or when no other Klein journal records a
// written default. Otherwise the person's earlier default stays.
func PictureDefaultDue(ctx context.Context, journal Journal, plan ModelPlan, served string) (bool, error) {
	if served == plan.Model {
		return true, nil
	}
	for _, other := range pyTorchKleinPlans() {
		if other.ID == plan.ID {
			continue
		}
		c := ModelController{Plan: &other, JournalPath: PictureModelJournal(journal, other)}
		state, err := c.load(ctx)
		if err != nil {
			return false, err
		}
		if state.DefaultWritten {
			return false, nil
		}
	}
	return true, nil
}

// PictureDefaultPending reports a prepared PyTorch Klein plan whose journal
// records no written picture default.
func PictureDefaultPending(ctx context.Context, journal Journal, plan ModelPlan) (bool, error) {
	c := ModelController{Plan: &plan, JournalPath: PictureModelJournal(journal, plan)}
	state, err := c.load(ctx)
	if err != nil {
		return false, err
	}
	return state.ProviderConfigured && !state.DefaultWritten, nil
}

// MarkPictureDefaultWritten records in plan's journal that Setup wrote the
// picture default.
func MarkPictureDefaultWritten(ctx context.Context, journal Journal, plan ModelPlan) error {
	c := ModelController{Plan: &plan, JournalPath: PictureModelJournal(journal, plan)}
	return cas.ChangeLimitContext(ctx, c.JournalPath, maxJournalBytes, func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, errors.New("the picture model has no journal")
		}
		state, err := c.decode(raw)
		if err != nil {
			return nil, err
		}
		state.DefaultWritten = true
		return json.Marshal(state)
	})
}

// StarterDefaultPending reports a starter picture model prepared in engine
// that Setup has not yet made the account's picture default: its model
// journal exists, records the prepared model and no DefaultWritten.
func StarterDefaultPending(ctx context.Context, journal Journal, engine AppID) (bool, error) {
	c, ok := starterController(journal, engine)
	if !ok {
		return false, nil
	}
	raw, err := cas.ReadLimitContext(ctx, c.JournalPath, maxJournalBytes)
	if err != nil || raw == nil {
		return false, err
	}
	state, err := c.decode(raw)
	if err != nil {
		return false, err
	}
	return state.ProviderConfigured && !state.DefaultWritten, nil
}

// MarkStarterDefaultWritten records in engine's starter model journal that
// Setup wrote the picture default.
func MarkStarterDefaultWritten(ctx context.Context, journal Journal, engine AppID) error {
	c, ok := starterController(journal, engine)
	if !ok {
		return errors.New("no starter picture model for this engine")
	}
	return cas.ChangeLimitContext(ctx, c.JournalPath, maxJournalBytes, func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, errors.New("the starter picture model has no journal")
		}
		state, err := c.decode(raw)
		if err != nil {
			return nil, err
		}
		state.DefaultWritten = true
		return json.Marshal(state)
	})
}

// starterController reads and changes engine's starter model journal.
func starterController(journal Journal, engine AppID) (ModelController, bool) {
	plan, ok := StarterPictureModel(engine)
	if !ok {
		return ModelController{}, false
	}
	return ModelController{Plan: &plan, JournalPath: StarterModelJournal(journal, engine)}, true
}

// starterPictureConfigured reports whether engine's starter picture model is
// prepared in the engine, as its model journal records.
func starterPictureConfigured(ctx context.Context, journal Journal, engine AppID) (bool, error) {
	c, ok := starterController(journal, engine)
	if !ok {
		return false, nil
	}
	state, err := c.load(ctx)
	if err != nil {
		return false, err
	}
	return state.ProviderConfigured, nil
}
