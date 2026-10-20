package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// preparePermission is the capability-owned public request boundary. Native
// receiving proof binds the beneficiary; owning profile bindings resolve every
// public target before any Book or Rights lock is acquired.
func (a *runtimeAsks) preparePermission(ctx context.Context, peer *identity.Peer, intent askswire.PermissionIntent) (asksservice.PreparedPermission, error) {
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
	if !asks.ValidTypedPermission(p) {
		return asks.TypedPermission{}, errors.New("asks: permission presentation exceeds retained bounds")
	}
	return asks.CloneTypedPermission(p), nil
}

// decideTypedAsking uses the concrete target already captured by its receiving
// capability. It keeps that evidence intact; it does not run public preparation
// or ask while holding a provider lock. Quiet final checks use CheckBatch alone.
func (r *runtimeRights) decideTypedAsking(ctx context.Context, subject rwire.Subject, operation rwire.ConcreteResource) rwire.BatchDecision {
	if r == nil || r.policy == nil {
		return rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeUnavailable, Outcomes: []rwire.DecisionOutcome{}}
	}
	decision := r.policy.CheckBatchContext(ctx, subject, rwire.CheckBatchRequest{Resources: []rwire.ConcreteResource{operation}})
	if decision.Outcome == rwire.BatchCheckOutcomeChecked && len(decision.Outcomes) == 1 && decision.Outcomes[0] == rwire.DecisionOutcomeNotGranted && r.typedFirstUse != nil {
		effect := rwire.PermissionEffect{Subject: subject, Selections: []rwire.PermissionSelection{{Permission: operation.Permission, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindOne, Target: slices.Clone(operation.Target)}}}}
		r.typedFirstUse(ctx, effect)
	}
	return decision
}

func (a *runtimeAsks) admitTypedService(ctx context.Context, effect rwire.PermissionEffect) {
	if a == nil || a.rights == nil || a.rights.policy == nil || effect.Subject.Account != a.rights.owner || !a.rights.policy.Definitions.ValidPermissionEffect(effect) {
		return
	}
	p, err := a.permissionPresentation(effect, "service", "")
	if err != nil {
		a.reportPermission(err)
		return
	}
	decision := a.rights.policy.CheckPermissionEffectContext(ctx, effect, "")
	if permissionDisposition(decision, len(effect.Selections)) != askswire.PermissionOutcomePending {
		return
	}
	sum := sha256.Sum256([]byte(p.Digest))
	base := "typed-first-use:" + hex.EncodeToString(sum[:20])
	for generation := 0; generation < maxFirstUseGenerations; generation++ {
		key := base + ":" + strconv.Itoa(generation)
		record, outcome, err := a.book.ObserveApplication(a.scope, key)
		if err != nil {
			a.reportPermission(err)
			return
		}
		if outcome == "answered" {
			if record.Option == "refuse" && !record.Answered.IsZero() && !time.Now().Before(record.Answered.Add(firstUseRefuseInterval)) {
				continue
			}
			if record.Option == "allow" {
				applied, err := a.rights.policy.AppliedPermissionEffect(record.ID)
				if err != nil {
					a.reportPermission(err)
					return
				}
				if applied {
					continue
				}
			}
			return
		}
		if outcome == "pending" {
			return
		}
		if outcome != "unknown" {
			continue
		}
		_, _, err = a.book.AskTypedPermissionAuthorizedFrom(a.scope, effect.Subject.Program, key, p, listen.Seen{Why: "admitted by the runtime after a typed not_granted decision"}, requesterPublisher(boundPeer(ctx, effect.Subject))(), func() error {
			current := a.rights.policy.CheckPermissionEffectContext(ctx, effect, decision.Revision)
			if permissionDisposition(current, len(effect.Selections)) != askswire.PermissionOutcomePending {
				return errors.New("asks: typed first-use admission changed")
			}
			return ctx.Err()
		})
		a.reportPermission(err)
		return
	}
}

func (a *runtimeAsks) reportPermission(err error) {
	if err != nil && a.report != nil {
		a.report(fmt.Errorf("asks: typed permission: %w", err))
	}
}

func (a *runtimeAsks) applyPermissionAllow(ctx context.Context, by rwire.Subject, expected, id string, authorize func() error) (rwire.SelectorEdit, error) {
	return a.changePermissionEffect(ctx, by, expected, id, authorize, false)
}
func (a *runtimeAsks) undoPermissionEffect(ctx context.Context, by rwire.Subject, expected, id string, authorize func() error) (rwire.SelectorEdit, error) {
	return a.changePermissionEffect(ctx, by, expected, id, authorize, true)
}
func (a *runtimeAsks) changePermissionEffect(ctx context.Context, by rwire.Subject, expected, id string, authorize func() error, undo bool) (rwire.SelectorEdit, error) {
	ctx, cancel := context.WithTimeout(ctx, decisionBudget)
	defer cancel()
	result := rwire.SelectorEdit{Outcome: rwire.PolicyEditOutcomeInvalid, Current: []rwire.SelectorRuleRecord{}}
	if a == nil || a.rights == nil || a.rights.policy == nil || by.Account != a.rights.owner || authorize == nil {
		return result, nil
	}
	_, err := a.book.WithApplicationRecordContext(ctx, id, func(record asks.Record, _ string, retained []string) error {
		if record.Key != asks.TypedPermissionKey || record.TypedPermission == nil || record.Pending() || record.Option != "allow" || record.AnsweredBy != asks.AnsweredByOperator || record.TypedPermission.Effect.Subject.Account != a.rights.owner {
			return nil
		}
		p := asks.CloneTypedPermission(*record.TypedPermission)
		if !asks.ValidTypedPermission(p) {
			return nil
		}
		check := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return authorize()
		}
		var err error
		if undo {
			result, err = a.rights.policy.UndoPermissionEffect(ctx, expected, p.Effect, by, record.ID, check)
		} else {
			permit := true
			result, err = a.rights.policy.ApplyPermissionEffect(ctx, expected, p.Effect, rights.RuleEdit{Permit: &permit, By: by, Why: "allowed typed permission", Exact: true}, record.ID, retained, check)
		}
		return err
	})
	return result, err
}
