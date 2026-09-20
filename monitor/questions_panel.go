package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// panelRules resolves the rights operator when an answer first writes a rule.
type panelRules struct{ operator *rightsclient.Operator }

func (r *panelRules) resolve(ctx context.Context) (*rightsclient.Operator, error) {
	if r.operator == nil {
		operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			return nil, err
		}
		r.operator = operator
	}
	return r.operator, nil
}

func (r *panelRules) ListPolicyContext(ctx context.Context, cursor string, limit int64) (rightsclient.PolicyPage, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.PolicyPage{}, err
	}
	return operator.ListPolicyContext(ctx, cursor, limit)
}

func (r *panelRules) ReadRuleContext(ctx context.Context, subject rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.RuleRead{}, err
	}
	return operator.ReadRuleContext(ctx, subject, action, resource)
}

// firstUseRules is the rights operator surface the Questions section answers
// first-use questions through and checks their rules with.
type firstUseRules interface {
	grants.RuleOperator
	grants.RuleReader
}

// questionPage is a question listing with, for each answered first-use
// question, whether the rule its answer names is on record: written,
// not_written, or the rights service's word when it could not tell.
type questionPage struct {
	askclient.OperatorPage
	RuleStates map[string]string `json:"ruleStates,omitempty"`
}

func (r *panelRules) SetRuleForContext(ctx context.Context, expected string, rule rightsclient.PolicyRule, ttl time.Duration, why string) (rightsclient.PolicyEdit, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	return operator.SetRuleForContext(ctx, expected, rule, ttl, why)
}

// questionAction is one operator decision the person made in the panel.
type questionAction struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Option string `json:"option"`
}

func questionWord(s string) bool {
	return len(s) > 0 && len(s) <= 128 && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// questionRules returns the rights operator the Questions section writes and
// reads first-use rules through, resolved per request. Tests replace it.
var questionRules = func() firstUseRules { return &panelRules{} }

// questions lists, answers and retires application questions through the
// resolved operator service. The service's operator policy decides whether this
// panel may act; its typed outcomes are returned unchanged for display.
// Resolution and transport failures report unavailability without a local
// question store.
func (p *servicePanel) questions(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var action questionAction
	if r.Method == "POST" {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&action); err != nil {
			http.Error(w, "invalid question action", 400)
			return
		}
		switch {
		case action.Action == "answer" && questionWord(action.ID) && questionWord(action.Option):
		case action.Action == "retire" && questionWord(action.ID) && action.Option == "":
		default:
			http.Error(w, "invalid question action", 400)
			return
		}
	} else if cursor := r.URL.Query().Get("cursor"); len(cursor) > 256 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid question cursor", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	if action.Action != "" {
		p.logAction(ctx, "question."+action.Action, map[string]string{"question.id": action.ID})
	}
	rules := questionRules()
	account := probe.Self().Account
	var result any
	switch action.Action {
	case "":
		var page askclient.OperatorPage
		page, err = operator.ListQuestionsContext(ctx, r.URL.Query().Get("cursor"), 16)
		listed := questionPage{OperatorPage: page}
		for _, record := range page.Records {
			if _, ok := grants.FirstUseRule(account, record); ok {
				if listed.RuleStates == nil {
					listed.RuleStates = map[string]string{}
				}
				listed.RuleStates[record.ID] = grants.FirstUseRuleState(ctx, rules, account, record)
			}
		}
		result = listed
	case "answer":
		// A runtime's first-use question: the answer writes the rule it names.
		var answered grants.Answered
		answered, err = grants.AnswerQuestion(ctx, operator, rules, account, action.ID, action.Option)
		result = struct {
			askclient.OperatorDecision
			Rule        *rightsclient.PolicyRule `json:"rule,omitempty"`
			Edit        *rightsclient.PolicyEdit `json:"edit,omitempty"`
			RuleOutcome string                   `json:"ruleOutcome,omitempty"`
		}{answered.Decision, answered.Rule, answered.Edit, answered.RuleOutcome}
		if err != nil && answered.Decision.Outcome == askclient.OperatorDecisionOutcomeAnswered {
			err = fmt.Errorf("the answer was recorded and its rule may not be written; list the questions, then retry the rule: %w", err)
		}
	case "retire":
		result, err = operator.RetireQuestionContext(ctx, action.ID)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}
