package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// staleRules lists the policy at a revision that is no longer current, so the
// rule write that follows meets a conflict.
type staleRules struct{ firstUseRules }

func (s staleRules) ListPolicyContext(ctx context.Context, cursor string, limit int64) (rightsclient.PolicyPage, error) {
	page, err := s.firstUseRules.ListPolicyContext(ctx, cursor, limit)
	page.Revision = "sha256:" + fmt.Sprintf("%064d", 0)
	return page, err
}

type failingRules struct{ firstUseRules }

func (f failingRules) SetRuleForContext(context.Context, string, rightsclient.PolicyRule, time.Duration, string) (rightsclient.PolicyEdit, error) {
	return rightsclient.PolicyEdit{}, fmt.Errorf("fixture rights service unavailable")
}

func (f failingRules) ApplyFirstUseAllowContext(context.Context, string, string) (rightsclient.PolicyEdit, error) {
	return rightsclient.PolicyEdit{}, fmt.Errorf("fixture rights service unavailable")
}

// The first-party panel keeps the service-authored first-use answer when its
// separate rule write meets a conflict. The question list says so for that
// question. Answering again is conflict (ASK-O8); Retry rule reads the
// recorded answer and writes the rule that permits the exact bound program,
// action, and resource.
func TestFirstPartyPanelFirstUseAnswerWhoseRuleConflictedOffersARetry(t *testing.T) {
	const action, resource = "abstraction.model/lookup", "hf"
	book, policy, program := panelFirstUseRuntime(t, action)
	slots := map[string]string{"program": program, "action": action, "resource": resource}
	if _, outcome, err := book.AskApplication("runtime-scope", "runtime", "first-use:panel", asks.FirstUseKey, slots, listen.Seen{Why: "test"}); err != nil || outcome != "pending" {
		t.Fatalf("first-use admission %q %v", outcome, err)
	}

	panel := (&servicePanel{}).handler("test-key")
	list := func() questionPage {
		r := panelRequest(t, panel, "/questions?cursor=", nil)
		return decodePanel[questionPage](t, r.Code, r.Body.Bytes())
	}
	type answer struct {
		askclient.OperatorDecision
		Rule        *rightsclient.PolicyRule `json:"rule"`
		Edit        *rightsclient.PolicyEdit `json:"edit"`
		RuleOutcome string                   `json:"ruleOutcome"`
	}
	page := list()
	if page.Outcome != askclient.OperatorPageOutcomePage || len(page.Records) != 1 || page.RuleStates[page.Records[0].ID] != "" {
		t.Fatalf("pending first-use question %+v", page)
	}
	id := page.Records[0].ID

	// The separate delegation setup grants one exact answer choice for the
	// currently pending first-use question; a direct POST cannot skip consent.
	setupReply := panelRequest(t, panel, "/rights/delegation", nil)
	setup := decodePanel[delegationPage](t, setupReply.Code, setupReply.Body.Bytes())
	if setup.Outcome != "page" || len(setup.Questions) != 1 || setup.Questions[0].ID != id || setup.Questions[0].Program != program || setup.Account == "" {
		t.Fatalf("delegation page should expose only the exact pending first-use scope: %+v", setup)
	}
	responder := filepath.Join(filepath.Dir(program), "oa-delegated-responder.exe")
	delegation := delegationEdit{QuestionID: id, Responder: responder, Option: "allow", Revision: setup.Revision}
	badSetup := panelRequest(t, panel, "/rights/delegation", delegation)
	if badSetup.Code != 400 {
		t.Fatalf("delegation without explicit consent = %d %s", badSetup.Code, badSetup.Body.String())
	}
	scope, err := asks.FirstUseAnswerResource(program, action, resource, "allow")
	if err != nil {
		t.Fatal(err)
	}
	if d := policy.Decide(rightsclient.Subject{Account: setup.Account, Program: responder}, asks.AnswerAction, scope); d.Outcome == rightsclient.DecisionOutcomePermitted {
		t.Fatalf("unconfirmed delegation unexpectedly granted the exact answer right: %+v", d)
	}
	// The question's own program is refused as its responder, with a
	// sentence, and no permit is written (ASK-R3).
	selfSetup := delegationEdit{QuestionID: id, Responder: program, Option: "allow", Revision: setup.Revision, ConfirmExactUse: true}
	selfReply := panelRequest(t, panel, "/rights/delegation", selfSetup)
	if selfReply.Code != 400 || !strings.Contains(selfReply.Body.String(), "an app may not answer the question about itself") {
		t.Fatalf("self delegation = %d %s", selfReply.Code, selfReply.Body.String())
	}
	if d := policy.Decide(rightsclient.Subject{Account: setup.Account, Program: program}, asks.AnswerAction, scope); d.Outcome == rightsclient.DecisionOutcomePermitted {
		t.Fatalf("self delegation wrote a permit: %+v", d)
	}
	// The runtime that asked is refused as its responder too.
	runtimeProgram := filepath.Join(filepath.Dir(program), "openabstractions.exe")
	realRuntimeProgram := panelRuntimeProgram
	t.Cleanup(func() { panelRuntimeProgram = realRuntimeProgram })
	panelRuntimeProgram = func(context.Context) string { return runtimeProgram }
	runtimeSetup := delegationEdit{QuestionID: id, Responder: runtimeProgram, Option: "allow", Revision: setup.Revision, ConfirmExactUse: true}
	runtimeReply := panelRequest(t, panel, "/rights/delegation", runtimeSetup)
	if runtimeReply.Code != 400 || !strings.Contains(runtimeReply.Body.String(), "runtime asked this question and may not answer it") {
		t.Fatalf("runtime delegation = %d %s", runtimeReply.Code, runtimeReply.Body.String())
	}
	if d := policy.Decide(rightsclient.Subject{Account: setup.Account, Program: runtimeProgram}, asks.AnswerAction, scope); d.Outcome == rightsclient.DecisionOutcomePermitted {
		t.Fatalf("runtime delegation wrote a permit: %+v", d)
	}
	delegation.ConfirmExactUse = true
	grantReply := panelRequest(t, panel, "/rights/delegation", delegation)
	grant := decodePanel[rightsclient.PolicyEdit](t, grantReply.Code, grantReply.Body.Bytes())
	if grant.Outcome != rightsclient.PolicyEditOutcomeApplied || grant.Current == nil || grant.Current.Resource != scope || grant.Current.Subject.Program != responder || grant.Current.Action != asks.AnswerAction {
		t.Fatalf("delegation should grant only the helper-generated selected scope: %+v", grant)
	}
	if d := policy.Decide(rightsclient.Subject{Account: setup.Account, Program: responder}, asks.AnswerAction, scope); d.Outcome != rightsclient.DecisionOutcomePermitted {
		t.Fatalf("confirmed responder answer right was not granted: %+v", d)
	}
	read, err := questionRules().ReadRuleContext(context.Background(), rightsclient.Subject{Account: setup.Account, Program: responder}, asks.AnswerAction, scope)
	if err != nil || read.Record == nil {
		t.Fatalf("delegation should be readable: %+v %v", read, err)
	}
	expires, err := time.Parse(time.RFC3339Nano, read.Record.Expires)
	if err != nil || !expires.After(time.Now()) || expires.Sub(time.Now()) > delegatedQuestionAnswerTTL {
		t.Fatalf("delegation must expire within five minutes: expires=%q err=%v", read.Record.Expires, err)
	}

	real := questionRules
	t.Cleanup(func() { questionRules = real })
	questionRules = func() firstUseRules { return staleRules{real()} }
	r := panelRequest(t, panel, "/questions", questionAction{Action: "answer", ID: id, Option: "allow"})
	conflicted := decodePanel[answer](t, r.Code, r.Body.Bytes())
	if conflicted.Outcome != askclient.OperatorDecisionOutcomeAnswered || conflicted.Rule == nil || conflicted.Edit == nil || conflicted.Edit.Outcome != rightsclient.PolicyEditOutcomeConflict {
		t.Fatalf("answer at a stale revision %+v edit %+v", conflicted, conflicted.Edit)
	}
	if d := policy.Decide(rightsclient.Subject{Account: conflicted.Rule.Subject.Account, Program: program}, action, resource); d.Outcome != rightsclient.DecisionOutcomeNotGranted {
		t.Fatalf("a conflicted rule write changed the decision: %+v", d)
	}
	if page := list(); page.RuleStates[id] != "not_written" {
		t.Fatalf("the list does not say the rule was not written: %+v", page)
	}

	questionRules = real
	r = panelRequest(t, panel, "/questions", questionAction{Action: "answer", ID: id, Option: "allow"})
	again := decodePanel[answer](t, r.Code, r.Body.Bytes())
	if again.Outcome != askclient.OperatorDecisionOutcomeConflict || again.Rule != nil || again.Edit != nil {
		t.Fatalf("answering an answered question again must conflict without a rule: %+v", again)
	}

	questionRules = func() firstUseRules { return failingRules{real()} }
	r = panelRequest(t, panel, "/questions", questionAction{Action: "rule", ID: id})
	if r.Code != 200 {
		t.Fatalf("partial answer should retain its typed decision, got %d %s", r.Code, r.Body.String())
	}
	partial := decodePanel[questionAnswerPanelResult](t, r.Code, r.Body.Bytes())
	if partial.Outcome != askclient.OperatorDecisionOutcomeAnswered || partial.Record == nil || partial.Rule == nil || partial.Edit != nil || !strings.Contains(partial.PartialError, "answer was recorded") {
		t.Fatalf("partial answer must preserve its answered state and permission failure: %+v", partial)
	}
	if d := policy.Decide(rightsclient.Subject{Account: partial.Rule.Subject.Account, Program: program}, action, resource); d.Outcome != rightsclient.DecisionOutcomeNotGranted {
		t.Fatalf("failed write changed the right unexpectedly: %+v", d)
	}

	questionRules = real
	r = panelRequest(t, panel, "/questions", questionAction{Action: "rule", ID: id})
	retried := decodePanel[answer](t, r.Code, r.Body.Bytes())
	if retried.Outcome != askclient.OperatorDecisionOutcomeAnswered || retried.Edit == nil || retried.Edit.Outcome != rightsclient.PolicyEditOutcomeApplied {
		t.Fatalf("retrying the rule %+v edit %+v", retried, retried.Edit)
	}
	if page := list(); page.RuleStates[id] != "written" {
		t.Fatalf("the list does not say the retried rule was written: %+v", page)
	}
	if d := policy.Decide(rightsclient.Subject{Account: retried.Rule.Subject.Account, Program: program}, action, resource); d.Outcome != rightsclient.DecisionOutcomePermitted {
		t.Fatalf("the retried first-use rule did not permit the bound operation: %+v", d)
	}
}
