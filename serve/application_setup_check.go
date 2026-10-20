package main

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	modelwire "github.com/openabstractions/abstraction-model/go/abstraction/model/api"
	modelclient "github.com/openabstractions/abstraction-model/go/client"
	modelservice "github.com/openabstractions/abstraction-model/go/service"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// setupChatPolicy is the account default the setup step writes.
const setupChatPolicy = "chat"

// runtimeSetupCheck answers RequestSetup for use code from the runtime's own
// catalog@1 Fit and the account default chat policy, the reads the setup
// step makes: cannot_run when no entry fits or is served here, and
// already_set_up when Fit's pick is served here and the account default
// selects it first. Anything else is the person's to answer in the step.
func runtimeSetupCheck(fit func(context.Context, modelwire.CatalogUse) modelwire.FitResult, chat func(context.Context, string) iwire.SelectionReadV2) setupCheck {
	return func(ctx context.Context, account, use string) (applications.SetupOutcome, string, bool) {
		if use != "code" {
			return 0, "", false
		}
		result := fit(ctx, modelwire.CatalogUseCode)
		if result.Outcome != modelwire.FitOutcomeFitted {
			return 0, "", false
		}
		// The one pick rule the step and the gateway use.
		index, least := modelclient.LocalPick(result)
		if index < 0 {
			smallest, need := "", int64(0)
			if least >= 0 {
				smallest, need = result.Entries[least].Title, result.Entries[least].NeedBytes
			}
			return applications.SetupOutcomeCannotRun, asks.SetupCannotRunSentence(result.PoolBytes, smallest, need), true
		}
		pick := &result.Entries[index]
		if pick.Availability != modelwire.AvailabilityServed {
			return 0, "", false
		}
		read := chat(ctx, account)
		if read.Outcome == iwire.SelectionReadOutcomeFound && read.Policy != nil && len(read.Policy.Candidates) > 0 &&
			read.Policy.Candidates[0].Model == pick.Family && read.Policy.Candidates[0].Placement == iwire.CandidatePlacementLocal {
			return applications.SetupOutcomeAlreadySetUp, pick.Title + " answers apps on this computer.", true
		}
		return 0, "", false
	}
}

// videoSetupCheck answers RequestSetup for use video from the reviewed
// video model set (architect ruling 2026-11-11, Setup use video):
// already_set_up when the router serves it on this computer, cannot_run
// when this platform has no reviewed engine for it or the memory free for
// models (catalog@1 Fit's pool) is below the set's need. A Fit that cannot be
// read leaves the answer to the person's question.
func videoSetupCheck(set modelhost.VideoModelSet, engine bool, served func(context.Context, string) bool, fit func(context.Context, modelwire.CatalogUse) modelwire.FitResult) setupCheck {
	title, _, _ := strings.Cut(set.DisplayTitle(), " (")
	return func(ctx context.Context, _, use string) (applications.SetupOutcome, string, bool) {
		if use != "video" {
			return 0, "", false
		}
		if !engine {
			return applications.SetupOutcomeCannotRun, title + " has no reviewed engine for this computer yet.", true
		}
		if served != nil && served(ctx, set.Name) {
			return applications.SetupOutcomeAlreadySetUp, title + " makes video with sound on this computer.", true
		}
		if result := fit(ctx, modelwire.CatalogUseCode); result.Outcome == modelwire.FitOutcomeFitted && result.PoolBytes < set.PeakBytes {
			return applications.SetupOutcomeCannotRun, asks.SetupCannotRunSentence(result.PoolBytes, title, set.PeakBytes), true
		}
		return 0, "", false
	}
}

// setupChecks answers a use from the first check that answers it.
func setupChecks(checks ...setupCheck) setupCheck {
	return func(ctx context.Context, account, use string) (applications.SetupOutcome, string, bool) {
		for _, check := range checks {
			if outcome, reason, ok := check(ctx, account, use); ok {
				return outcome, reason, true
			}
		}
		return 0, "", false
	}
}

// routerServesVideo reports whether r routes model for video to a host on
// this computer that holds it or has it installed.
func routerServesVideo(r *router.Router) func(context.Context, string) bool {
	return func(ctx context.Context, model string) bool {
		d, _, err := r.RouteContext(ctx, router.Request{Model: model, Profile: router.ProfileVideo})
		return err == nil && d != nil && (d.Verdict == router.Resident || d.Verdict == router.WouldLoad)
	}
}

// bindSetupCheck binds RequestSetup's check to this runtime's router,
// resource table and selection store.
func bindSetupCheck(h *applicationsHost, r *router.Router, table *resourceservice.Table, leases *resourceservice.Book, o *inferenceOperator) {
	if h == nil || r == nil || table == nil || o == nil {
		return
	}
	sources := host.ModelCatalogSources(r, table, leases)
	validate := setupFactsCheck(func(ctx context.Context, useName string, model asks.SetupModelFact) error {
		if model.CatalogVersion == "" {
			// Picture plans are native Setup's reviewed fixed plans, not rows
			// of model/catalog@1. Keep their exact opaque identity; claim no
			// catalogue verification and grant no installation authority.
			if useName == "image" && agentHex(model.Plan, 32) && model.Digest == "" {
				return nil
			}
			return errors.New("setup: catalogue reference absent")
		}
		use, ok := modelwire.ParseCatalogUse(useName)
		if !ok {
			return errors.New("setup: model use")
		}
		fit := modelservice.FitCatalog(ctx, use, sources)
		if fit.Outcome != modelwire.FitOutcomeFitted || fit.CatalogVersion != model.CatalogVersion {
			return errors.New("setup: catalogue changed")
		}
		for _, entry := range fit.Entries {
			if entry.Family == model.Family && entry.Digest == model.Digest {
				return nil
			}
		}
		return errors.New("setup: model reference changed")
	})
	h.setup.factsCheck.Store(&validate)
	fit := func(ctx context.Context, use modelwire.CatalogUse) modelwire.FitResult {
		return modelservice.FitCatalog(ctx, use, sources)
	}
	_, engine := modelhost.PinnedVideoEngine(goruntime.GOOS, goruntime.GOARCH)
	h.setup.bind(setupChecks(
		runtimeSetupCheck(fit,
			func(ctx context.Context, account string) iwire.SelectionReadV2 {
				return o.readSelectionScopeV2(ctx, inference.Subject{Account: account}, setupChatPolicy)
			}),
		videoSetupCheck(modelhost.LTX25, engine, routerServesVideo(r), fit)))
}
