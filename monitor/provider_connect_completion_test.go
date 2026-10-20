package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

func providerCompletionFixture(t *testing.T) (askclient.RecordMetadata, *askclient.PermissionRequest) {
	t.Helper()
	record := firstUseFixture("0123456789abcdef0123456789abcdef", nativePresenterProgram("requester.exe"), "abstraction.credentials/apply", "credential:deepseek")
	// The book replays against the wall clock, so the answer is stamped now.
	record.Option, record.Answered = "allow", time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	target := askclient.PermissionTarget{Account: "fixture", Program: record.About, Action: "abstraction.credentials/apply", Resource: "credential:deepseek"}
	p := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{target}}
	target.Action, target.Resource = "abstraction.inference/complete", "host:deepseek"
	p.Targets = append(p.Targets, target)
	return record, p
}

func TestProviderCompletionUsesRetainedOccurrenceAndIdenticalReplay(t *testing.T) {
	r, p := providerCompletionFixture(t)
	answered, _ := time.Parse(time.RFC3339, r.Answered)
	one, err := providerCompletionRequest(r, p, trayPublisher{})
	if err != nil {
		t.Fatal(err)
	}
	two, err := providerCompletionRequest(r, p, trayPublisher{})
	if err != nil || !reflect.DeepEqual(one, two) || one.Title != "DeepSeek connected" || one.Body != "requester can use DeepSeek." || one.RequestRef.Key != providerConnectedPrefix+r.ID || one.ExpiresMs-one.RequestRef.ReplayUntilMs != 0 || one.RequestRef.ReplayUntilMs != answered.UnixMilli()+asks.MaxChoiceReplayMilliseconds || len(one.Actions) != 1 || one.Actions[0].ID != "undo" {
		t.Fatal(one, two, err)
	}
	for _, mutation := range []string{"pending", "time", "id", "single"} {
		changed := r
		permission := *p
		permission.Targets = append([]askclient.PermissionTarget{}, p.Targets...)
		switch mutation {
		case "pending":
			changed.Option = ""
		case "time":
			changed.Answered = ""
		case "id":
			changed.ID = "foreign"
		case "single":
			permission.Targets = permission.Targets[:1]
		}
		if _, err := providerCompletionRequest(changed, &permission, trayPublisher{}); err == nil {
			t.Fatal(mutation)
		}
	}
}

type providerCompletionBook struct {
	book     *asks.Book
	posts    int
	observed *askclient.NotificationObservation
}

func (n *providerCompletionBook) ObserveContext(_ context.Context, ref askclient.RequestRef, _ int64) (askclient.NotificationObservation, error) {
	if n.observed != nil {
		return *n.observed, nil
	}
	return n.book.ObserveNotification("panel-copy-fixture", ref)
}
func (n *providerCompletionBook) PostContext(ctx context.Context, request askclient.NotificationRequest) (askclient.NotificationObservation, error) {
	n.posts++
	out, _, err := n.book.PostNotificationFrom("panel-copy-fixture", nativePresenterProgram("Panel.exe"), request, listen.Seen{Why: "inert copy fixture"}, asks.Publisher{}, func() error { return ctx.Err() })
	return out, err
}

func TestProviderCompletionRetainsLegacyTitleAndPostsOnlyUnknown(t *testing.T) {
	r, p := providerCompletionFixture(t)
	request, err := providerCompletionRequest(r, p, trayPublisher{})
	if err != nil {
		t.Fatal(err)
	}
	book, err := asks.LoadApplicationBook(filepath.Join(t.TempDir(), "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	n := &providerCompletionBook{book: book}
	legacy := request
	legacy.Title = "Provider connected"
	first, err := n.PostContext(t.Context(), legacy)
	if err != nil || first.Outcome != askclient.NotificationOutcomePosted {
		t.Fatal(first, err)
	}
	n.posts = 0
	if err := reconcileProviderCompletion(t.Context(), n, request); err != nil || n.posts != 0 {
		t.Fatal("retained title was reposted", n.posts, err)
	}
	replayed, err := n.PostContext(t.Context(), legacy)
	if err != nil || replayed.ID != first.ID {
		t.Fatal("retained bytes changed", replayed, err)
	}
	if !sameProviderCompletionRequest(request, legacy) {
		t.Fatal("legacy cosmetic title broke Undo")
	}
	for _, mutation := range []string{"body", "action", "key", "replay", "expiry", "tag", "delivery"} {
		changed := legacy
		changed.Actions = append([]askclient.NotificationAction{}, legacy.Actions...)
		switch mutation {
		case "body":
			changed.Body = "requester can use another server."
		case "action":
			changed.Actions[0].ID = "other"
		case "key":
			changed.RequestRef.Key += "other"
		case "replay":
			changed.RequestRef.ReplayUntilMs++
		case "expiry":
			changed.ExpiresMs++
		case "tag":
			changed.Tag += "other"
		case "delivery":
			changed.Delivery = askclient.NotificationDeliveryOperationEnd
		}
		if sameProviderCompletionRequest(request, changed) {
			t.Fatal("Undo accepted changed binding", mutation)
		}
	}
	for _, outcome := range []askclient.NotificationOutcome{askclient.NotificationOutcomeUnavailable, askclient.NotificationOutcomeConflict} {
		n.observed = &askclient.NotificationObservation{Outcome: outcome}
		n.posts = 0
		if err := reconcileProviderCompletion(t.Context(), n, request); err == nil || n.posts != 0 {
			t.Fatal("uncertain observation posted", outcome, n.posts, err)
		}
	}
	n.observed = nil
	request.RequestRef.Key += "new"
	request.Tag = request.RequestRef.Key
	n.posts = 0
	if err := reconcileProviderCompletion(t.Context(), n, request); err != nil || n.posts != 1 {
		t.Fatal("unknown request did not post once", n.posts, err)
	}
}

type providerUndoPolicy struct {
	policy   *rights.DecisionPolicy
	order    []string
	failHost bool
}

func (p *providerUndoPolicy) ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error) {
	s, err := p.policy.OperatorSnapshot()
	return rightsclient.PolicyPage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: s.Revision}, err
}
func (p *providerUndoPolicy) RevokeAnswerPermitsContext(_ context.Context, rev string, by, target rightsclient.Subject, actions []string, resource, id string) (rightsclient.PolicyEdit, error) {
	p.order = append(p.order, resource)
	if p.failHost && resource == "host:deepseek" {
		return rightsclient.PolicyEdit{}, errors.New("inert transport loss")
	}
	return p.policy.RevokeAnswerPermits(rev, by, target, actions, resource, id, func() error { return nil })
}

func TestProviderUndoRevokesApplyFirstAndPreservesPersonReplacement(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "original", true: "person-replacement"}[replace], func(t *testing.T) {
			record, permission := providerCompletionFixture(t)
			policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), []string{permission.Targets[0].Action, permission.Targets[1].Action})
			if err != nil {
				t.Fatal(err)
			}
			self := rightsclient.Subject{Account: "fixture", Program: nativePresenterProgram("Panel.exe")}
			target := rightsclient.Subject{Account: "fixture", Program: record.About}
			rules := []rightsclient.PolicyRule{}
			for _, p := range permission.Targets {
				rules = append(rules, rightsclient.PolicyRule{Subject: target, Action: p.Action, Resource: p.Resource, Permit: true})
			}
			before, _ := policy.OperatorSnapshot()
			yes := true
			edit, err := policy.ApplyFirstUseAllow(t.Context(), before.Revision, rules, rights.RuleEdit{Permit: &yes, By: self}, record.ID, []string{record.ID}, func() error { return nil })
			if err != nil || edit.Outcome != rightsclient.PolicyEditOutcomeApplied {
				t.Fatal(edit, err)
			}
			if replace {
				current, _ := policy.OperatorSnapshot()
				replacement, err := policy.ChangeRule(current.Revision, target, rules[1].Action, rules[1].Resource, rights.RuleEdit{Permit: &yes, By: self, Why: "person replacement", Exact: true}, func() error { return nil })
				if err != nil || replacement.Outcome != rightsclient.PolicyEditOutcomeApplied {
					t.Fatal(replacement, err)
				}
			}
			operator := &providerUndoPolicy{policy: policy, failHost: true}
			if err := undoProviderPermits(t.Context(), operator, self, record.ID, permission); err == nil {
				t.Fatal("partial loss hidden")
			}
			if got := policy.Decide(target, rules[0].Action, rules[0].Resource); got.Outcome.String() == "permitted" {
				t.Fatal("credential permit survived first stage")
			}
			operator.failHost = false
			if err := undoProviderPermits(t.Context(), operator, self, record.ID, permission); err != nil {
				t.Fatal(err)
			}
			if got := policy.Decide(target, rules[1].Action, rules[1].Resource); (got.Outcome.String() == "permitted") != replace {
				t.Fatal("replacement custody", got)
			}
			if !reflect.DeepEqual(operator.order, []string{"credential:deepseek", "host:deepseek", "credential:deepseek", "host:deepseek"}) {
				t.Fatal(operator.order)
			}
		})
	}
}
