package main

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

// firstUseJoinable reports whether a permission@1 Request may raise or join
// the runtime's own first-use question for action on resource (asks
// ASK-A14): only an exact first-use action on a resource a refusal of that
// action can ask about. Enrollment additionally checks the shared native
// answering eligibility gate; application messages have their typed request,
// and application commands their
// native owner's typed occurrence.
func (a *runtimeAsks) firstUseJoinable(action, resource string) bool {
	if resource == "" || len(resource) > 1024 || !utf8.ValidString(resource) || strings.ContainsFunc(resource, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	switch {
	case action == credentials.ActionApply:
		target, ok := credentials.ParseResource(resource)
		if !ok {
			return false
		}
		binding, err := hostedcatalogue.Select(target.Name)
		return err == nil && binding.Credential == target.Name
	case action == inference.ActionComplete:
		name, ok := strings.CutPrefix(resource, "host:")
		return ok && a.listedServer(name)
	case action == grants.ModelLookupAction:
		return a.tierFacts.ListedRegistry != nil && a.tierFacts.ListedRegistry(resource)
	case action == grants.JobSubmitAction:
		// One job kind the job owner's grammar names (JOB-A13); the
		// contract-wide resource and any other text ask nothing.
		kind, ok := strings.CutPrefix(resource, "job:")
		return ok && host.JobKindName(kind) && grants.FirstUseMode(action, resource) == rwire.PermissionFirstUseExact
	case grants.Paired(action, resource):
		return true
	}
	return false
}

// listedServer reports whether the router lists the named model server, local
// or hosted.
func (a *runtimeAsks) listedServer(name string) bool {
	if a.servers == nil || name == "" {
		return false
	}
	return slices.ContainsFunc(a.servers(), func(h *router.Host) bool { return h != nil && h.Name == name })
}

// prepareFirstUseJoin prepares a permission@1 Request naming one exact
// first-use action under the legacy exact profile (asks ASK-A14). A current
// permit reads permitted and an explicit Block denied, asking nothing.
// Otherwise the runtime raises or joins its own first-use question for the
// caller's program, action and resource, the one a refused call of that
// action raises (ASK-R2), and the request observes it: pending, or the
// person's recent answer while it stands. Nothing is granted here.
func (a *runtimeAsks) prepareFirstUseJoin(ctx context.Context, peer *identity.Peer, intent askswire.PermissionIntent) (asksservice.PreparedPermission, error) {
	refuse := func(outcome askswire.PermissionOutcome) (asksservice.PreparedPermission, error) {
		return asksservice.PreparedPermission{Outcome: outcome}, nil
	}
	if a == nil || a.rights == nil || a.rights.policy == nil {
		return refuse(askswire.PermissionOutcomeUnavailable)
	}
	selection := intent.Selection
	if selection == nil || selection.Selector.Kind != rwire.ResourceSelectorKindOne || intent.Purpose != "" {
		return refuse(askswire.PermissionOutcomeInvalid)
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || subject.Account != a.rights.owner {
		return refuse(askswire.PermissionOutcomeForbidden)
	}
	return a.joinFirstUse(ctx, subject, requesterPublisher(peer), selection.Permission.Action, string(selection.Selector.Target))
}

// joinFirstUse is prepareFirstUseJoin for a subject already bound from its
// peer, whose code is publisher.
func (a *runtimeAsks) joinFirstUse(ctx context.Context, subject rwire.Subject, publisher func() asks.Publisher, action, resource string) (asksservice.PreparedPermission, error) {
	refuse := func(outcome askswire.PermissionOutcome) (asksservice.PreparedPermission, error) {
		return asksservice.PreparedPermission{Outcome: outcome}, nil
	}
	if action == rights.MetaAnswerAction {
		if resource != rights.MetaScopeApps {
			return refuse(askswire.PermissionOutcomeInvalid)
		}
		decision := a.answeringEligibility(ctx, answerer{subject: subject, publisher: publisher()})
		switch decision.Outcome {
		case rwire.DecisionOutcomePermitted:
			return asksservice.PreparedPermission{Exact: true, Outcome: askswire.PermissionOutcomePermitted, Revision: decision.PolicyRevision}, nil
		case rwire.DecisionOutcomeNotGranted, rwire.DecisionOutcomeDenied:
		case rwire.DecisionOutcomeForbidden:
			return refuse(askswire.PermissionOutcomeForbidden)
		default:
			return refuse(askswire.PermissionOutcomeUnavailable)
		}
	} else if !a.firstUseJoinable(action, resource) {
		return refuse(askswire.PermissionOutcomeInvalid)
	}
	permission, err := asks.NewPermissionRequest(asks.PermissionTarget{Account: subject.Account, Program: subject.Program, Action: action, Resource: resource})
	if err != nil {
		return refuse(askswire.PermissionOutcomeInvalid)
	}
	if action == credentials.ActionApply {
		if target, ok := credentials.ParseResource(resource); ok {
			if pair, ok := providerPermission(subject, target.Name); ok {
				permission = pair
			}
		}
	}
	permitted, revision := true, ""
	for _, target := range permission.Targets {
		decision := a.rights.decide(ctx, subject, target.Action, target.Resource)
		revision = decision.PolicyRevision
		switch decision.Outcome {
		case rwire.DecisionOutcomePermitted:
		case rwire.DecisionOutcomeDenied:
			return asksservice.PreparedPermission{Exact: true, Outcome: askswire.PermissionOutcomeDenied, Revision: revision}, nil
		case rwire.DecisionOutcomeNotGranted:
			permitted = false
		default:
			return refuse(askswire.PermissionOutcomeUnavailable)
		}
	}
	if permitted {
		return asksservice.PreparedPermission{Exact: true, Outcome: askswire.PermissionOutcomePermitted, Revision: revision}, nil
	}
	var id string
	id = a.admitPermissionPrepared(subject, publisher, permission)
	if id == "" {
		// No question is pending: the person's recent answer stands (ASK-R2's
		// ten-minute refuse), or none could be admitted.
		record, ok := a.latestFirstUse(permission)
		if !ok {
			return refuse(askswire.PermissionOutcomeUnavailable)
		}
		return asksservice.PreparedPermission{Exact: true, Outcome: askswire.PermissionOutcomeAnswered, Joined: &record}, nil
	}
	record, found, err := a.book.ApplicationRecord(id)
	if err != nil || !found {
		return refuse(askswire.PermissionOutcomeUnavailable)
	}
	outcome := askswire.PermissionOutcomeAnswered
	if record.Pending() {
		outcome = askswire.PermissionOutcomePending
	}
	return asksservice.PreparedPermission{Exact: true, Outcome: outcome, Joined: &record}, nil
}

// latestFirstUse is the newest retained answered first-use question for the
// program, action and resource, under the keys admitPrepared uses.
func (a *runtimeAsks) latestFirstUse(permission asks.PermissionRequest) (asks.Record, bool) {
	key := firstUsePermissionKey(permission)
	var latest asks.Record
	found := false
	for generation := 0; generation < maxFirstUseGenerations; generation++ {
		record, outcome, err := a.book.ObserveApplication(a.scope, firstUseGenerationKey(key, generation))
		if err != nil || outcome == "unknown" {
			break
		}
		if outcome == "answered" {
			latest, found = record, true
		}
	}
	return latest, found
}
