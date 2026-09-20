package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
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

// The first-party panel keeps the service-authored first-use answer when its
// separate rule write meets a conflict. The question list says so for that
// question, and answering the same option again writes the rule that permits
// the exact bound program, action, and resource.
func TestFirstPartyPanelFirstUseAnswerWhoseRuleConflictedOffersARetry(t *testing.T) {
	own(t)
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	const action, resource = "abstraction.model/lookup", "hf"
	dir := t.TempDir()
	book, err := asks.LoadApplicationBook(filepath.Join(dir, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "policy.json"), []string{action})
	if err != nil {
		t.Fatal(err)
	}
	self := func(ctx context.Context, peer *identity.Peer) bool {
		process, err := peer.Process.AtLeast(listen.Program.Process)
		return err == nil && process.PID == os.Getpid() && ctx.Err() == nil
	}
	prefix := fmt.Sprintf("panel-first-use-%d", time.Now().UnixNano())
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		QuestionBook: book, QuestionEndpoint: listen.Endpoint(prefix + "q"),
		QuestionOperator: func(ctx context.Context, peer *identity.Peer) error {
			if !self(ctx, peer) {
				return fmt.Errorf("not this test")
			}
			return nil
		},
		RightsPolicy: policy, RightsEndpoint: listen.Endpoint(prefix + "r"),
		RightsOperator: func(ctx context.Context, peer *identity.Peer) error {
			if !self(ctx, peer) {
				return rightsservice.ErrOperatorForbidden
			}
			return nil
		}}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime failed to stop")
		}
	})
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", o.Endpoint)
	trustPanelRuntime(t, o.Endpoint)

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program = filepath.Clean(program)
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
