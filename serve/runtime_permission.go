package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-identity/go/budget"
	"slices"
	"strconv"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// preparePermission is the capability-owned public request boundary. Native
// receiving proof binds the beneficiary; owning profile bindings resolve every
// public target before any Book or Rights lock is acquired.
func (a *runtimeAsks) preparePermission(ctx context.Context, peer *identity.Peer, intent askswire.PermissionIntent) (asksservice.PreparedPermission, error) {
	// Receiver startup consent is created by message progression from native
	// captures. Apps cannot manufacture a startup question through Asks.
	if intent.Selection != nil && intent.Selection.Permission.Action == grants.ApplicationMessageStartAction {
		return asksservice.PreparedPermission{Outcome: askswire.PermissionOutcomeForbidden}, nil
	}
	if intent.Selection != nil && intent.Selection.Permission.Action == ActionApplicationMessage {
		return a.prepareMessagePermission(ctx, peer, intent)
	}
	if intent.Selection != nil && intent.Selection.Permission.ResourceProfile == rights.LegacyExactResourceProfile {
		return a.prepareFirstUseJoin(ctx, peer, intent)
	}
	refuse := func(outcome askswire.PermissionOutcome) (asksservice.PreparedPermission, error) {
		return asksservice.PreparedPermission{Outcome: outcome}, nil
	}
	if a == nil || a.rights == nil || a.rights.policy == nil || a.rights.policy.Definitions == nil {
		return refuse(askswire.PermissionOutcomeUnavailable)
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || subject.Account != a.rights.owner {
		return refuse(askswire.PermissionOutcomeForbidden)
	}
	var selections []rwire.PermissionSelection
	if intent.Selection != nil {
		selections = []rwire.PermissionSelection{*intent.Selection}
	}
	effect, err := a.rights.policy.Definitions.PreparePermissionEffect(subject, selections, intent.BundleRef, intent.BundleSelector)
	if err != nil {
		if errors.Is(err, rights.ErrPermissionUnavailable) {
			return refuse(askswire.PermissionOutcomeUnavailable)
		}
		return refuse(askswire.PermissionOutcomeInvalid)
	}
	p, err := a.permissionPresentation(effect, "application", intent.Purpose)
	if err != nil {
		return refuse(askswire.PermissionOutcomeUnavailable)
	}
	decision := a.rights.policy.CheckPermissionEffectContext(ctx, effect, "")
	outcome := permissionDisposition(decision, len(effect.Selections))
	prepared := asksservice.PreparedPermission{Permission: p, Outcome: outcome, Revision: decision.Revision}
	prepared.Recheck = func() error {
		current := a.rights.policy.CheckPermissionEffectContext(ctx, effect, decision.Revision)
		if current.Outcome != rwire.BatchCheckOutcomeChecked || permissionDisposition(current, len(effect.Selections)) != askswire.PermissionOutcomePending {
			return errors.New("asks: permission admission changed")
		}
		return ctx.Err()
	}
	return prepared, nil
}

func permissionDisposition(decision rwire.BatchDecision, count int) askswire.PermissionOutcome {
	if decision.Outcome != rwire.BatchCheckOutcomeChecked || len(decision.Outcomes) != count || decision.Revision == "" {
		return askswire.PermissionOutcomeUnavailable
	}
	missing := false
	for _, outcome := range decision.Outcomes {
		switch outcome {
		case rwire.DecisionOutcomePermitted:
		case rwire.DecisionOutcomeNotGranted:
			missing = true
		case rwire.DecisionOutcomeDenied:
			return askswire.PermissionOutcomeDenied
		default:
			return askswire.PermissionOutcomeUnavailable
		}
	}
	if missing {
		return askswire.PermissionOutcomePending
	}
	return askswire.PermissionOutcomePermitted
}

// permissionPresentation retains only compiled owner explanations. Purpose is
// a separate app annotation and supplies no operation, target or privilege.
func (a *runtimeAsks) permissionPresentation(effect rwire.PermissionEffect, origin, purpose string) (asks.TypedPermission, error) {
	if _, _, ok := grants.ApplicationPermissionRules(effect); ok {
		return asks.TypedPermission{}, errors.New("asks: captured native application title required")
	}
	catalog := a.rights.policy.Definitions
	p := asks.TypedPermission{Effect: effect, Digest: asks.PermissionEffectDigest(effect), Origin: origin, Purpose: purpose, Display: askswire.PermissionDisplay{Selections: []askswire.PermissionDisplaySelection{}}}
	for _, selection := range effect.Selections {
		definition, ok := catalog.SelectorDefinition(selection.Permission)
		if !ok || !definition.Definition.Served || !slices.Contains(definition.FirstUseSelectors, selection.Selector.Kind) {
			return asks.TypedPermission{}, errors.New("asks: unsupported first-use selector")
		}
		target, err := catalog.DescribeSelection(selection)
		if err != nil {
			return asks.TypedPermission{}, err
		}
		p.Display.Selections = append(p.Display.Selections, askswire.PermissionDisplaySelection{Operation: definition.Label, Target: target, Collection: definition.Collection, AllEffect: definition.AllEffect, Disclosure: definition.Definition.Disclosure})
	}
	if effect.Bundle != nil {
		bundle, ok := catalog.PermissionBundle(effect.Bundle.BundleRef)
		if !ok || bundle.DefinitionDigest != effect.Bundle.DefinitionDigest {
			return asks.TypedPermission{}, errors.New("asks: bundle definition changed")
		}
		p.Display.BundleLabel, p.Display.BundleDisclosure = bundle.Label, bundle.Disclosure
	}
	if !validTypedPermissionDisplay(p) {
		return asks.TypedPermission{}, errors.New("asks: permission presentation exceeds retained bounds")
	}
	return asks.CloneTypedPermission(p), nil
}

func (a *runtimeAsks) permissionDecision(ctx context.Context, effect rwire.PermissionEffect, expected string) rwire.BatchDecision {
	ctx, _ = a.rights.bindCaller(ctx, effect.Subject)
	if projected, _, ok := grants.AgentMessageLegacyEffect(effect); ok {
		return a.rights.policy.CheckLegacyPermissionEffectContext(ctx, projected, expected)
	}
	if _, _, ok := grants.ApplicationPermissionRules(effect); ok {
		return a.rights.policy.CheckLegacyPermissionEffectContext(ctx, effect, expected)
	}
	return a.rights.policy.CheckPermissionEffectContext(ctx, effect, expected)
}

// observePermission binds a retained question to its actual native caller and
// reads one current policy snapshot. It admits no request and writes no effect.
func (a *runtimeAsks) observePermission(ctx context.Context, peer *identity.Peer, record asks.Record) (askswire.PermissionObservation, error) {
	unavailable := askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}
	if a == nil || a.rights == nil || a.rights.policy == nil {
		return unavailable, nil
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || subject.Account != a.rights.owner {
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeForbidden}, nil
	}
	var decision rwire.BatchDecision
	count := 0
	if record.TypedPermission != nil {
		effect := record.TypedPermission.Effect
		if effect.Subject.Account != subject.Account || !identity.SameSubjectProgram(effect.Subject.Program, subject.Program) || record.TypedPermission.Digest != asks.PermissionEffectDigest(effect) {
			return unavailable, nil
		}
		count = len(effect.Selections)
		decision = a.permissionDecision(ctx, effect, "")
	} else {
		permission, ok := asks.PermissionForRecord(a.rights.owner, record)
		if !ok {
			return unavailable, nil
		}
		effect := rwire.PermissionEffect{Subject: subject}
		for _, target := range permission.Targets {
			if target.Account != subject.Account || !identity.SameSubjectProgram(target.Program, subject.Program) {
				return unavailable, nil
			}
			raw, err := json.Marshal(target.Resource)
			if err != nil {
				return unavailable, err
			}
			effect.Selections = append(effect.Selections, rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: target.Action, ResourceProfile: rights.LegacyExactResourceProfile}, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindOne, Target: raw}})
		}
		count = len(effect.Selections)
		if count == 1 {
			target := permission.Targets[0]
			current := a.rights.decide(ctx, subject, target.Action, target.Resource)
			decision = rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked, Revision: current.PolicyRevision, Outcomes: []rwire.DecisionOutcome{current.Outcome}}
		} else {
			slices.SortFunc(effect.Selections, func(x, y rwire.PermissionSelection) int {
				return strings.Compare(x.Permission.Action, y.Permission.Action)
			})
			decision = a.rights.policy.CheckLegacyPermissionEffectContext(ctx, effect, "")
		}
	}
	return askswire.PermissionObservation{Outcome: permissionDisposition(decision, count), PolicyRevision: decision.Revision}, nil
}

// decideTypedAsking uses the concrete target already captured by its receiving
// capability. It keeps that evidence intact; it does not run public preparation
// or ask while holding a provider lock. Quiet final checks use CheckBatch alone.
func (r *runtimeRights) decideTypedAsking(ctx context.Context, subject rwire.Subject, operation rwire.ConcreteResource) rwire.BatchDecision {
	if r == nil || r.policy == nil {
		return rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeUnavailable, Outcomes: []rwire.DecisionOutcome{}}
	}
	if r.owner != "" && subject.Account != r.owner {
		return rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeForbidden, Outcomes: []rwire.DecisionOutcome{}}
	}
	bound, note := r.bindCaller(ctx, subject)
	decision := r.policy.CheckBatchContext(bound, subject, rwire.CheckBatchRequest{Resources: []rwire.ConcreteResource{operation}})
	r.admitted(note, decision.Outcome == rwire.BatchCheckOutcomeChecked && len(decision.Outcomes) == 1 && decision.Outcomes[0] == rwire.DecisionOutcomePermitted, subject, operation.Permission.Action, "typed")
	if decision.Outcome == rwire.BatchCheckOutcomeChecked && len(decision.Outcomes) == 1 && decision.Outcomes[0] == rwire.DecisionOutcomeNotGranted && r.typedFirstUse != nil {
		effect := rwire.PermissionEffect{Subject: subject, Selections: []rwire.PermissionSelection{{Permission: operation.Permission, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindOne, Target: slices.Clone(operation.Target)}}}}
		// The refusing boundary names the question it raised or joined
		// (INF-W11, RIGHTS-FLOW ruling 2).
		inference.AskedFrom(ctx).Add(r.typedFirstUse(ctx, effect))
	}
	return decision
}

func (a *runtimeAsks) admitTypedService(ctx context.Context, effect rwire.PermissionEffect) string {
	return a.admitTypedPermission(ctx, effect)
}

func (a *runtimeAsks) admitTypedPermission(ctx context.Context, effect rwire.PermissionEffect) string {
	if a == nil || a.rights == nil || a.rights.policy == nil || effect.Subject.Account != a.rights.owner || movedConnectorCaller(effect.Subject.Program) || !a.rights.policy.Definitions.ValidPermissionEffect(effect) {
		return ""
	}
	p, err := a.permissionPresentation(effect, "service", "")
	if err != nil {
		a.reportPermission(err)
		return ""
	}
	decision := a.permissionDecision(ctx, effect, "")
	if permissionDisposition(decision, len(effect.Selections)) != askswire.PermissionOutcomePending {
		return ""
	}
	sum := sha256.Sum256([]byte(p.Digest))
	base := "typed-first-use:" + hex.EncodeToString(sum[:20])
	for generation := 0; generation < maxFirstUseGenerations; generation++ {
		key := base + ":" + strconv.Itoa(generation)
		record, outcome, err := a.book.ObserveApplication(a.scope, key)
		if err != nil {
			a.reportPermission(err)
			return ""
		}
		if outcome == "answered" {
			if record.Option == "refuse" && !record.Answered.IsZero() && !time.Now().Before(record.Answered.Add(firstUseRefuseInterval)) {
				continue
			}
			if record.Option == "allow" {
				applied, err := a.rights.policy.AppliedPermissionEffect(record.ID)
				if err != nil {
					a.reportPermission(err)
					return ""
				}
				if applied {
					continue
				}
			}
			return ""
		}
		if outcome == "pending" {
			return record.ID
		}
		if outcome != "unknown" {
			continue
		}
		record, outcome, err = a.book.AskTypedPermissionAuthorizedFrom(a.scope, effect.Subject.Program, key, p, listen.Seen{Why: "admitted by the runtime after a typed not_granted decision"}, requesterPublisher(boundPeer(ctx, effect.Subject))(), func() error {
			current := a.permissionDecision(ctx, effect, decision.Revision)
			if permissionDisposition(current, len(effect.Selections)) != askswire.PermissionOutcomePending {
				return errors.New("asks: typed first-use admission changed")
			}
			return ctx.Err()
		})
		a.reportPermission(err)
		if err == nil && outcome == "pending" {
			return record.ID
		}
		return ""
	}
	return ""
}

// applicationPermissionConsent is the native gateway composition seam. key is
// its complete scoped receipt key; recheck binds the held declaration, native
// peer/session and call. Replay only checks authority and never asks or edits.
func (a *runtimeAsks) applicationPermissionConsent(ctx context.Context, peer *identity.Peer, resource, title, class, key string, replay bool, recheck func(context.Context) error) (string, string, func(context.Context) error) {
	if a == nil || a.rights == nil || a.rights.policy == nil || peer == nil || recheck == nil || !asks.ValidRequestKey(key) {
		return "unavailable", "consent", nil
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || subject.Account != a.rights.owner || movedConnectorCaller(subject.Program) {
		return "not_permitted", "caller", nil
	}
	// One wording per effect: the routine question shares its digest with
	// every other question on the same effect, so it never claims a start.
	p, err := grants.ApplicationPermission(subject, resource, title, class)
	if err != nil {
		return "unavailable", "consent", nil
	}
	action, _ := grants.ApplicationCommandAction(class, resource)
	quiet := func(checkCtx context.Context) error {
		if err := checkCtx.Err(); err != nil {
			return err
		}
		if err := recheck(checkCtx); err != nil {
			return err
		}
		bound, err := rightsclient.SubjectFromPeer(peer)
		if err != nil || bound != subject {
			return asksservice.ErrResponderForbidden
		}
		decision := a.rights.decide(checkCtx, subject, action, resource)
		if decision.Outcome != rwire.DecisionOutcomePermitted {
			return &rightsclient.DecisionError{Outcome: decision.Outcome.String(), PolicyRevision: decision.PolicyRevision}
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return "unavailable", "caller", nil
	}
	if err := recheck(ctx); err != nil {
		return "unavailable", "caller", nil
	}
	decision := a.rights.decide(ctx, subject, action, resource)
	if decision.Outcome == rwire.DecisionOutcomeDenied {
		return "not_permitted", "rights", nil
	}
	if decision.Outcome == rwire.DecisionOutcomePermitted {
		if quiet(ctx) != nil {
			return "unavailable", "rights", nil
		}
		return "permitted", "", quiet
	}
	if decision.Outcome != rwire.DecisionOutcomeNotGranted {
		return "unavailable", "rights", nil
	}
	if replay {
		return "not_permitted", "rights", nil
	}
	batch := a.permissionDecision(ctx, p.Effect, "")
	if permissionDisposition(batch, 3) == askswire.PermissionOutcomeDenied {
		return "not_permitted", "rights", nil
	}
	if permissionDisposition(batch, 3) != askswire.PermissionOutcomePending {
		return "unavailable", "rights", nil
	}
	raw, _ := json.Marshal([]string{subject.Account, subject.Program, key})
	sum := sha256.Sum256(raw)
	requestKey := "native-app:" + hex.EncodeToString(sum[:])
	via := listen.Seen{Why: "admitted by the native application gateway"}
	if seen, proof := peer.Path.Get(); proof > identity.ProofNone {
		via.Path, via.Bound = seen, true
	}
	record, outcome, err := a.book.AskTypedPermissionOccurrenceAuthorizedFrom(a.scope, subject.Program, requestKey, p, via, requesterPublisher(peer)(), func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := recheck(ctx); err != nil {
			return err
		}
		current := a.permissionDecision(ctx, p.Effect, batch.Revision)
		if permissionDisposition(current, 3) != askswire.PermissionOutcomePending {
			return errors.New("asks: application permission admission changed")
		}
		return nil
	})
	if err != nil {
		return "unavailable", "consent", nil
	}
	if outcome != "pending" && outcome != "answered" {
		return "refused", "consent", nil
	}
	if !record.Pending() && record.Option != "allow" {
		return "refused", "consent", nil
	}
	a.awaitQuestionsChecking(ctx, subject, []string{record.ID}, func(retained asks.TypedPermission) (bool, bool) {
		if retained.Digest != p.Digest || retained.Origin != "service" || retained.Purpose != "" || recheck(ctx) != nil {
			return false, true
		}
		current := a.rights.decide(ctx, subject, action, resource)
		return current.Outcome == rwire.DecisionOutcomePermitted, current.Outcome == rwire.DecisionOutcomeDenied
	})
	if err := quiet(ctx); err == nil {
		return "permitted", "", quiet
	}
	if ctx.Err() != nil {
		return "unavailable", "caller", nil
	}
	if recheck(ctx) != nil {
		return "unavailable", "caller", nil
	}
	current := a.rights.decide(ctx, subject, action, resource)
	if current.Outcome == rwire.DecisionOutcomeDenied {
		return "not_permitted", "rights", nil
	}
	if current.Outcome != rwire.DecisionOutcomeNotGranted {
		return "unavailable", "rights", nil
	}
	return "refused", "consent", nil
}

func (a *runtimeAsks) authorizeApplicationAnswerer(ctx context.Context, who answerer, record asks.Record, option string) (asksservice.ResponderEffect, error) {
	p := asks.CloneTypedPermission(*record.TypedPermission)
	_, kind, ok := grants.ApplicationPermissionRules(p.Effect)
	if !ok || kind != "routine" || !validTypedPermissionDisplay(p) || p.Origin != "service" || p.Purpose != "" || p.Effect.Subject.Account != a.rights.owner || (option != "allow" && option != "refuse") || !slices.Contains(record.Options, option) {
		return nil, asksservice.ErrResponderForbidden
	}
	if a.rights.applications.unsignedProgram(ctx, p.Effect.Subject.Program) {
		return nil, asksservice.ErrResponderForbidden
	}
	enrolled, _, err := a.answererGate(ctx, who)
	if err != nil {
		return nil, err
	}
	if !enrolled {
		return nil, asksservice.ErrResponderForbidden
	}
	if err := a.checkAnswerParties(ctx, who, asks.FirstUseParties(record, p.Effect.Subject.Program)); err != nil {
		return nil, err
	}
	switch permissionDisposition(a.permissionDecision(ctx, p.Effect, ""), 3) {
	case askswire.PermissionOutcomeDenied:
		return nil, asksservice.ErrResponderForbidden
	case askswire.PermissionOutcomeUnavailable:
		return nil, errors.New("asks: application permission state unavailable")
	}
	return func(effectCtx context.Context, recheck func() error, first bool) (askswire.ResponderEffectOutcome, string) {
		current := func() string {
			switch permissionDisposition(a.permissionDecision(effectCtx, p.Effect, ""), 3) {
			case askswire.PermissionOutcomePermitted:
				return "permitted"
			case askswire.PermissionOutcomeDenied:
				return "denied"
			case askswire.PermissionOutcomePending:
				return "not_granted"
			default:
				return "unavailable"
			}
		}
		if !first {
			return askswire.ResponderEffectOutcomeConflict, current()
		}
		guard := func() error {
			if err := effectCtx.Err(); err != nil {
				return err
			}
			if err := recheck(); err != nil {
				return err
			}
			return a.checkAnswerParties(effectCtx, who, asks.FirstUseParties(record, p.Effect.Subject.Program))
		}
		edit, err := a.rights.policy.ChangeLegacyPermissionEffectUnderAnswer(effectCtx, who.subject, p.Effect, answererWhy, record.ID, option == "allow", guard)
		switch {
		case errors.Is(err, rights.ErrNoMetaRule), errors.Is(err, rights.ErrMetaTarget), errors.Is(err, rights.ErrMetaHolder), errors.Is(err, asksservice.ErrResponderForbidden):
			return askswire.ResponderEffectOutcomeForbidden, current()
		case err != nil:
			return askswire.ResponderEffectOutcomeUnavailable, current()
		case edit.Outcome == rwire.PolicyEditOutcomeConflict:
			return askswire.ResponderEffectOutcomeConflict, current()
		case edit.Outcome == rwire.PolicyEditOutcomeForbidden:
			return askswire.ResponderEffectOutcomeForbidden, current()
		case edit.Outcome != rwire.PolicyEditOutcomeApplied:
			return askswire.ResponderEffectOutcomeUnavailable, current()
		case option == "refuse":
			return askswire.ResponderEffectOutcomeNone, current()
		default:
			return askswire.ResponderEffectOutcomeApplied, current()
		}
	}, nil
}

func (a *runtimeAsks) reportPermission(err error) {
	if err != nil && a.report != nil {
		a.report(fmt.Errorf("asks: typed permission: %w", err))
	}
}

func (a *runtimeAsks) applyPermissionAllow(ctx context.Context, by rwire.Subject, expected, id string, authorize func() error) (rwire.SelectorEdit, error) {
	key := a.unsavedReaskKey(id)
	edit, attempted, err := a.savePermissionEffect(ctx, by, expected, id, authorize, false)
	if err == nil && edit.Outcome == rwire.PolicyEditOutcomeApplied {
		unsavedReasks.saved(key)
	}
	if err != nil || edit.Outcome == rwire.PolicyEditOutcomeUnavailable || edit.Outcome == rwire.PolicyEditOutcomeInvalid {
		// The operator call returns only a closed outcome; keep the cause.
		a.reportPermission(fmt.Errorf("permission Allow %s not saved: outcome=%s: %v", id, edit.Outcome, err))
	}
	if attempted && allowNotSaved(edit.Outcome, err) {
		a.releaseUnsavedAllow(id, by, edit.Outcome.String(), err, authorize)
	}
	return edit, err
}
func (a *runtimeAsks) undoPermissionEffect(ctx context.Context, by rwire.Subject, expected, id string, authorize func() error) (rwire.SelectorEdit, error) {
	return a.changePermissionEffect(ctx, by, expected, id, authorize, true)
}
func (a *runtimeAsks) changePermissionEffect(ctx context.Context, by rwire.Subject, expected, id string, authorize func() error, undo bool) (rwire.SelectorEdit, error) {
	edit, _, err := a.savePermissionEffect(ctx, by, expected, id, authorize, undo)
	return edit, err
}

// savePermissionEffect also reports whether it attempted the save of an
// operator's Allow it owns: the pinned record reached Rights, or a native
// message Allow's preparation for that save ran. A record it never saves,
// such as another owner's consent, is not attempted.
func (a *runtimeAsks) savePermissionEffect(ctx context.Context, by rwire.Subject, expected, id string, authorize func() error, undo bool) (_ rwire.SelectorEdit, attempted bool, _ error) {
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, allowSaveBudget)
	defer cancel()
	result := rwire.SelectorEdit{Outcome: rwire.PolicyEditOutcomeInvalid, Current: []rwire.SelectorRuleRecord{}}
	if a == nil || a.rights == nil || a.rights.policy == nil || by.Account != a.rights.owner || authorize == nil {
		return result, false, nil
	}
	prepare := false
	var messageEffect *rwire.PermissionEffect
	var graphEffect *rwire.PermissionEffect
	var graphTarget grants.KeptApplicationClassTarget
	_, err := a.book.WithApplicationRecordContext(ctx, id, func(record asks.Record, _ string, _ []string) error {
		if record.TypedPermission != nil {
			if target, ok := grants.KeptGraphEditTarget(record.TypedPermission.Effect); ok && !undo && !record.Pending() && record.Option == "allow" &&
				record.AnsweredBy == asks.AnsweredByOperator && record.TypedPermission.Origin == "service" && record.TypedPermission.Purpose == "" {
				effect := record.TypedPermission.Effect
				graphEffect, graphTarget, attempted = &effect, target, true
			}
			if _, _, ok := grants.AgentMessageLegacyEffect(record.TypedPermission.Effect); ok {
				copy := record.TypedPermission.Effect
				messageEffect = &copy
				// The same qualification the pinned save below applies.
				attempted = !undo && record.Key == asks.TypedPermissionKey && !record.Pending() && record.Option == "allow" &&
					record.AnsweredBy == asks.AnsweredByOperator && record.TypedPermission.Origin == "application" && record.TypedPermission.Purpose == ""
			}
		}
		if !undo && record.TypedPermission != nil {
			for _, selection := range record.TypedPermission.Effect.Selections {
				prepare = prepare || selection.Permission.Action == ActionApplicationMessage
			}
		}
		return nil
	})
	if err != nil {
		return result, false, err
	}
	nativeMessage := messageEffect != nil && !undo
	if nativeMessage {
		// Native preparation runs before the policy decision and its locks,
		// within the existing authenticated operator call's deadline.
		cancel()
		ctx = caller
	}
	if prepare {
		ctx, err = a.rights.policy.PrepareMetaHolderContext(ctx)
		if err != nil {
			return rwire.SelectorEdit{Outcome: rwire.PolicyEditOutcomeUnavailable, Current: []rwire.SelectorRuleRecord{}}, attempted, err
		}
	}
	var messageGuard func(context.Context) error
	var graphGuard func(context.Context) error
	if graphEffect != nil {
		if a.rights.applications == nil {
			return result, attempted, errors.New("application graph edit: directory unavailable")
		}
		cancel()
		ctx = caller
		var close func()
		graphGuard, close, err = a.rights.applications.prepareGraphEditAllow(ctx, *graphEffect, graphTarget)
		if err != nil {
			return result, attempted, err
		}
		defer close()
		var finish context.CancelFunc
		ctx, finish = context.WithTimeout(ctx, allowSaveBudget)
		defer finish()
	}
	if nativeMessage {
		messageGuard, err = a.messagePermissionGuard(ctx, *messageEffect)
		if err != nil {
			return rwire.SelectorEdit{Outcome: rwire.PolicyEditOutcomeUnavailable, Current: []rwire.SelectorRuleRecord{}}, attempted, err
		}
		// The permit admission runs under the Book and policy locks and must
		// not prepare facts there. Carry the facts prepared here, so a
		// concurrent registration write that clears the published roles does
		// not refuse the person's Allow (owner R13, 2026-11-08).
		facts, err := a.rights.applications.currentMessagingFacts(ctx)
		if err != nil {
			return rwire.SelectorEdit{Outcome: rwire.PolicyEditOutcomeUnavailable, Current: []rwire.SelectorRuleRecord{}}, attempted, err
		}
		ctx = withMessagingFacts(ctx, facts)
		var finish context.CancelFunc
		ctx, finish = budget.Within(ctx, decisionBudget, decisionBudget)
		defer finish()
		// Check the same complete projected effect at the caller's expected
		// revision. The existing policy CAS fences changes after this check.
		decision := a.permissionDecision(ctx, *messageEffect, expected)
		pairs := len(messageEffect.Selections)
		if decision.Outcome == rwire.BatchCheckOutcomeConflict || permissionDisposition(decision, pairs) == askswire.PermissionOutcomeDenied {
			return rwire.SelectorEdit{Outcome: rwire.PolicyEditOutcomeConflict, Revision: decision.Revision, Current: []rwire.SelectorRuleRecord{}}, attempted, nil
		}
		state := permissionDisposition(decision, pairs)
		if decision.Outcome != rwire.BatchCheckOutcomeChecked || state != askswire.PermissionOutcomePending && state != askswire.PermissionOutcomePermitted {
			return rwire.SelectorEdit{Outcome: rwire.PolicyEditOutcomeUnavailable, Current: []rwire.SelectorRuleRecord{}}, attempted, nil
		}
	}
	_, err = a.book.WithApplicationRecordContext(ctx, id, func(record asks.Record, scope string, retained []string) error {
		// The pinned save decides afresh which record it saves. A pin that
		// never ran keeps the message Allow's attempt from its preparation.
		attempted = false
		if record.Key != asks.TypedPermissionKey || record.TypedPermission == nil || record.Pending() || record.Option != "allow" || record.TypedPermission.Effect.Subject.Account != a.rights.owner {
			return nil
		}
		p := asks.CloneTypedPermission(*record.TypedPermission)
		if !validTypedPermissionDisplay(p) {
			return nil
		}
		if _, graph := grants.KeptGraphEditTarget(p.Effect); !graph && slices.ContainsFunc(p.Effect.Selections, func(s rwire.PermissionSelection) bool {
			return s.Permission.ResourceProfile == grants.KeptApplicationClassProfile
		}) {
			return nil
		}
		check := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if messageGuard != nil {
				if err := messageGuard(ctx); err != nil {
					return err
				}
			}
			return authorize()
		}
		var err error
		if projected, _, message := grants.AgentMessageLegacyEffect(p.Effect); message {
			if p.Origin != "application" || p.Purpose != "" || record.AnsweredBy != asks.AnsweredByOperator {
				return nil
			}
			if undo {
				result, err = a.rights.policy.UndoLegacyPermissionEffect(ctx, expected, projected, nil, record.ID, check)
			} else {
				if messageGuard == nil {
					return nil
				}
				attempted = true
				result, err = a.rights.policy.ApplyLegacyPermissionEffect(ctx, expected, projected, rights.RuleEdit{By: by, Why: "allowed native agent messaging", Exact: true}, record.ID, retained, check)
			}
		} else if _, graph := grants.KeptGraphEditTarget(p.Effect); graph {
			if scope != a.scope || p.Origin != "service" || p.Purpose != "" || record.AnsweredBy != asks.AnsweredByOperator {
				return nil
			}
			if undo {
				result, err = a.rights.policy.UndoPermissionEffect(ctx, expected, p.Effect, by, record.ID, check)
			} else if graphGuard != nil && asks.PermissionEffectDigest(*graphEffect) == p.Digest {
				attempted = true
				permit := true
				result, err = a.rights.policy.ApplyPermissionEffect(ctx, expected, p.Effect, rights.RuleEdit{Permit: &permit, By: by, Why: "allowed exact workflow edit", Exact: true}, record.ID, retained, func() error {
					if err := graphGuard(ctx); err != nil {
						return err
					}
					return check()
				})
			}
		} else if _, kind, application := grants.ApplicationPermissionRules(p.Effect); application {
			if scope != a.scope || p.Origin != "service" || p.Purpose != "" {
				return nil
			}
			if undo {
				var answerer *rwire.Subject
				if record.AnsweredBy == asks.AnsweredByResponder && kind == "routine" && record.Answerer != "" {
					actor := rwire.Subject{Account: a.rights.owner, Program: record.Answerer}
					answerer = &actor
				} else if record.AnsweredBy != asks.AnsweredByOperator {
					return nil
				}
				result, err = a.rights.policy.UndoLegacyPermissionEffect(ctx, expected, p.Effect, answerer, record.ID, check)
			} else if record.AnsweredBy == asks.AnsweredByOperator {
				attempted = true
				result, err = a.rights.policy.ApplyLegacyPermissionEffect(ctx, expected, p.Effect, rights.RuleEdit{By: by, Why: "allowed application permission", Exact: true}, record.ID, retained, check)
			}
		} else if record.AnsweredBy != asks.AnsweredByOperator {
			return nil
		} else if undo {
			result, err = a.rights.policy.UndoPermissionEffect(ctx, expected, p.Effect, by, record.ID, check)
		} else {
			permit := true
			attempted = true
			result, err = a.rights.policy.ApplyPermissionEffect(ctx, expected, p.Effect, rights.RuleEdit{Permit: &permit, By: by, Why: "allowed typed permission", Exact: true}, record.ID, retained, check)
		}
		return err
	})
	return result, attempted, err
}
