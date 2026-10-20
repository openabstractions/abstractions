package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	identity "github.com/openabstractions/abstraction-identity"
	routerclient "github.com/openabstractions/abstraction-router/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The setup step's picture part (research/first-run/DECISION.md §4, slice
// 2): the app Setup's catalog gives pictures gets them in the same one
// question, on the engine its catalog entry names. Every effect reuses the
// owner One Install drives: Setup's runner for the engine package and its
// feature, the starter picture-model controller for the reviewed plan, the
// grants picture rule set and Applications for the app's registration.

// stepPictureTitle names the reviewed starter picture model in sentences.
const stepPictureTitle = "FLUX.2 Klein 4B"

// stepPictureOwners is what the picture part reads and changes; tests
// replace each.
type stepPictureOwners struct {
	// engineOffer and engine are Setup's runner (StepEngineOffer and
	// StepEngine); confirmModel records the prepared model in Setup's plan
	// journal, as the Setup page's Prepare does.
	engineOffer  func(context.Context, setup.FeatureStatus, setup.AppID) (setup.StepPictureOffer, error)
	engine       func(context.Context, setup.StepPictureOffer) (bool, error)
	confirmModel func(context.Context, setup.AppID) error
	feature      func(setup.AppID) (setup.FeatureStatus, error)
	model        stepPictureModel
	apps         func(context.Context) (setupApplications, error)
	grant        func(ctx context.Context, program string, rules []setup.ApprovedRule) error
	// live is the runtime's own reading of the prepared picture model: the
	// engine's check and the router's route for the model by its name. A
	// Setup tick reads done only when the runtime observes it (architect,
	// 2026-11-09). nil observes nothing beyond the controller (tests).
	live func(ctx context.Context, engine setup.AppID, model string) error
	// routeWait paces configure's polls of live after a preparation: the
	// router counts a new model only once the runtime inventory has hashed
	// it. It answers an error once the overall deadline has passed. nil
	// polls once.
	routeWait func(context.Context) func(context.Context) error
	// check is the engine's own deep check, run once by configure after
	// preparation and never by an offer. nil checks nothing.
	check func(context.Context, setup.AppID) error
	// fallback is Show details' sentence for a faster engine this
	// computer's card fits but the step cannot use yet.
	fallback string
}

// stepPictureModel is the part of the picture-model controller the step
// reads and prepares.
type stepPictureModel interface {
	setup.StepModel
	Configure(context.Context, bool, func(int64, int64) error) (setup.ModelStatus, error)
	ReconcileProvider(context.Context) (setup.ModelStatus, error)
}

// stepPictureEngine is the picture engine of the first picture app's catalog
// choice on this computer (Runner.StepPictureEngine): its optimized engine
// where it fits, its fallback otherwise, with the sentence Show details
// gives when the card fits an engine the step cannot use yet.
func (p *servicePanel) stepPictureEngine(choices []setup.Choice) (setup.AppID, string) {
	for _, choice := range choices {
		if choice.Use != setup.UseImage || len(choice.Engines) == 0 {
			continue
		}
		last := choice.Engines[len(choice.Engines)-1]
		runner, _, err := p.setupRunnerForPanel()
		if err != nil {
			return last, ""
		}
		engine, fallback, err := runner.StepPictureEngine(context.Background(), choice.Engines, p.engineFeatureState)
		if err != nil {
			return last, ""
		}
		return engine, fallback
	}
	return "", ""
}

// stepPictureModel is the picture-model controller of engine's reviewed
// plan: the starter plan for the fallback engine, the engine's own plan for
// an optimized one.
func (p *servicePanel) stepPictureModel(engine setup.AppID) (setup.ModelController, error) {
	if engine == setup.PyTorch {
		journal, err := p.setupJournalForPanel()
		if err != nil {
			return setup.ModelController{}, err
		}
		// A prepared full-precision Klein is the engine's model; the step
		// prepares the compact starter only for an engine with neither.
		return setup.PyTorchServedController(context.Background(), journal)
	}
	return setup.DefaultStepBF16Controller()
}

// stepPictureLive is the runtime's reading of engine's prepared picture
// model: the PyTorch engine's registration, then the router's route for the
// model by the name apps ask for it with. The engine's own check runs once,
// in configure (stepPictureCheck).
func (p *servicePanel) stepPictureLive(ctx context.Context, engine setup.AppID, model string) error {
	if engine == setup.PyTorch {
		journal, err := p.setupJournalForPanel()
		if err != nil {
			return err
		}
		registered, err := setup.PyTorchEngineRegistered(ctx, journal)
		if err != nil {
			return err
		}
		if !registered {
			return errors.New("the picture engine is not registered")
		}
		if served, err := setup.PyTorchServedModel(ctx, journal); err == nil && served != "" {
			model = served
		}
	}
	return stepPictureRouted(ctx, model)
}

// stepPictureCheck is the PyTorch engine's own check of its registration,
// run once after preparation. The fallback controller probes in Configure.
func (p *servicePanel) stepPictureCheck(ctx context.Context, engine setup.AppID) error {
	if engine != setup.PyTorch {
		return nil
	}
	journal, err := p.setupJournalForPanel()
	if err != nil {
		return err
	}
	checked, err := setup.PyTorchEngineChecked(ctx, journal)
	if err != nil {
		return fmt.Errorf("the picture engine's check failed: %w", err)
	}
	if !checked {
		return errors.New("the picture engine's check did not pass")
	}
	return nil
}

// stepPictureRouted asks the runtime's router to route model for pictures
// on this computer, as an app's picture request is routed.
func stepPictureRouted(ctx context.Context, model string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	router, err := panelMachine().ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return fmt.Errorf("the model router cannot be reached: %w", err)
	}
	pick, err := router.PickContext(ctx, routerclient.PickRequest{Model: model, Profile: "image", Fresh: true})
	if err != nil {
		return fmt.Errorf("the model router cannot be reached: %w", err)
	}
	// The router's wire verdicts for a host on this computer that holds the
	// model or has it installed; hosted and every refusal are not this
	// computer's pictures.
	if !slices.Contains([]string{"resident", "would-load"}, pick.Decision.Verdict) {
		return fmt.Errorf("the runtime cannot route the picture model yet: %s", pick.Decision.Why)
	}
	return nil
}

func (p *servicePanel) stepPictures(engine setup.AppID, fallback string) (*setup.StepPictures, error) {
	model, err := p.stepPictureModel(engine)
	if err != nil {
		return nil, err
	}
	o := stepPictureOwners{feature: p.engineFeatureState, model: model, apps: p.setupApplicationsForPanel, grant: grantWriter, live: p.stepPictureLive, check: p.stepPictureCheck, routeWait: stepRouteWait, fallback: fallback}
	o.engineOffer = func(ctx context.Context, feature setup.FeatureStatus, engine setup.AppID) (setup.StepPictureOffer, error) {
		runner, _, err := p.setupRunnerForPanel()
		if err != nil {
			return setup.StepPictureOffer{}, err
		}
		return runner.StepEngineOffer(ctx, feature, engine)
	}
	o.engine = func(ctx context.Context, offer setup.StepPictureOffer) (bool, error) {
		runner, _, err := p.setupRunnerForPanel()
		if err != nil {
			return false, err
		}
		read := func() (setup.FeatureStatus, error) { return p.engineFeatureState(offer.Engine) }
		add := func(ctx context.Context) (setup.FeatureStatus, error) {
			return runner.AddEngineFeature(ctx, offer.Engine)
		}
		return runner.StepEngine(ctx, offer, read, add)
	}
	o.confirmModel = func(ctx context.Context, engine setup.AppID) error {
		_, journal, err := p.setupRunnerForPanel()
		if err != nil {
			return err
		}
		state, err := journal.Load(ctx)
		if err != nil {
			return err
		}
		_, err = journal.ConfirmConfiguration(ctx, state.Plan.ID, setup.ModelRequirement(engine))
		return err
	}
	return o.bind(), nil
}

// engineFeatureState is the core MSI's feature for engine; the Setup page's
// check stands in for it in tests.
func (p *servicePanel) engineFeatureState(engine setup.AppID) (setup.FeatureStatus, error) {
	if p.setupFeatureCheck != nil {
		return p.setupFeatureCheck()
	}
	return setup.EngineFeatureStatus(engine)
}

func (o stepPictureOwners) bind() *setup.StepPictures {
	return &setup.StepPictures{Model: o.model, Offer: o.offer, Engine: o.engine, Configure: o.configure, Grant: o.grantRules}
}

// offer reads the picture part for the app's registration: its engine's
// package plan and feature, whether the engine holds the reviewed model, and
// the rules with their sentences. A shared interpreter is no program to name
// in rules (VISION 2026-10-07), so such a registration gets no offer.
func (o stepPictureOwners) offer(ctx context.Context, app setup.StepApp) (setup.StepPictureOffer, error) {
	if app.Program == "" {
		return setup.StepPictureOffer{Reason: app.Title + "'s registration names no program"}, nil
	}
	if identity.SharedInterpreterProgram(app.Program, identity.Code{}) {
		return setup.StepPictureOffer{Reason: app.Title + "'s registered program is a shared interpreter"}, nil
	}
	feature, err := o.feature(app.Engine)
	if err != nil {
		return setup.StepPictureOffer{}, err
	}
	offer, err := o.engineOffer(ctx, feature, app.Engine)
	if err != nil || offer.Reason != "" {
		return offer, err
	}
	status, err := o.model.Status(ctx)
	if err != nil {
		return setup.StepPictureOffer{}, err
	}
	// Prepared means the runtime serves it now; the journal's record of an
	// earlier preparation decides nothing.
	offer.Configured = status.Approved && status.ProviderConfigured && status.Ready && o.served(ctx) == nil
	if offer.Rules, err = picturePermissionRulesFor(setup.PictureHost(app.Engine)); err != nil {
		return setup.StepPictureOffer{}, err
	}
	for _, rule := range offer.Rules {
		sentence, err := permissionSentence(rule, usePictures)
		if err != nil {
			return setup.StepPictureOffer{}, err
		}
		offer.Permissions = append(offer.Permissions, sentence)
	}
	offer.Title, offer.Program, offer.App, offer.Engine, offer.Fallback = stepPictureTitle, app.Program, app.Name, app.Engine, o.fallback
	return offer, nil
}

// configure prepares the verified model in the engine, as the Setup page's
// Prepare does, and records the configuration in Setup's plan journal.
func (o stepPictureOwners) configure(ctx context.Context) error {
	status, err := o.model.Status(ctx)
	if err != nil {
		return err
	}
	// Configure resolves a preparing marker under the provider operation
	// lock: a finished preparation is recorded, an ended one runs again.
	// A prepared PyTorch model whose registration serves fewer prepared
	// models than it should is registered again (ModelController.Refresh).
	if !status.ProviderConfigured || !status.Ready && o.model.ReviewedPlan().PictureEngine() == setup.PyTorch {
		status, err = o.model.Configure(ctx, false, nil)
	}
	if err != nil {
		return err
	}
	if !status.ProviderConfigured {
		return errors.New("the picture engine has not prepared the model yet")
	}
	if !status.Ready {
		return errors.New("the picture engine does not serve " + stepPictureTitle + "; its own check did not pass")
	}
	if o.check != nil {
		if err := o.check(ctx, o.model.ReviewedPlan().PictureEngine()); err != nil {
			return err
		}
	}
	if err := o.routed(ctx); err != nil {
		return err
	}
	return o.confirmModel(ctx, o.model.ReviewedPlan().PictureEngine())
}

// served is the runtime's live reading of the prepared model (live).
func (o stepPictureOwners) served(ctx context.Context) error {
	if o.live == nil {
		return nil
	}
	plan := o.model.ReviewedPlan()
	return o.live(ctx, plan.PictureEngine(), plan.Model)
}

// routed polls served until the router routes the prepared model or the
// wait's deadline passes; the last answer is the failure.
func (o stepPictureOwners) routed(ctx context.Context) error {
	var wait func(context.Context) error
	if o.routeWait != nil {
		wait = o.routeWait(ctx)
	}
	for {
		err := o.served(ctx)
		if err == nil || wait == nil {
			return err
		}
		if wait(ctx) != nil {
			return err
		}
	}
}

// stepRouteWait waits the step's 5-second cadence until about 10 minutes
// after the first poll.
func stepRouteWait(context.Context) func(context.Context) error {
	deadline := time.Now().Add(10 * time.Minute)
	return func(ctx context.Context) error {
		if time.Now().After(deadline) {
			return errors.New("the route deadline passed")
		}
		t := time.NewTimer(5 * time.Second)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
}

// grantRules writes exactly the offered rules for the confirmed registration
// of the app the offer names. The rule set and the program must still be the
// ones the answer named; a changed set or an unconfirmed or changed
// registration writes nothing.
func (o stepPictureOwners) grantRules(ctx context.Context, offer setup.StepPictureOffer) error {
	rules, err := picturePermissionRulesFor(setup.PictureHost(offer.Engine))
	if err != nil {
		return err
	}
	if setup.RulesDigest(rules) != setup.RulesDigest(offer.Rules) {
		return errors.New("the picture permissions changed; set up again")
	}
	apps, err := o.apps(ctx)
	if err != nil {
		return err
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return err
	}
	for _, entry := range page.Applications {
		if entry.Descriptor.Name != offer.App {
			continue
		}
		if entry.SelfRegistered {
			return errors.New("the picture app's registration is not confirmed yet")
		}
		if entry.Descriptor.Program != offer.Program {
			return errors.New("the picture app's registration changed; set up again")
		}
		return o.grant(ctx, offer.Program, rules)
	}
	return errors.New("the picture app is no longer registered")
}
