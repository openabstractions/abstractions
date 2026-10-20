package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/probe"
	identity "github.com/openabstractions/abstraction-identity"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

const delegatedQuestionAnswerTTL = 5 * time.Minute

type delegationQuestionReader interface {
	askclient.DetailsReader
	ListQuestionsContext(context.Context, string, int64) (askclient.OperatorPage, error)
}

type panelDelegationQuestions struct{ operator *askclient.Operator }

func (q panelDelegationQuestions) ReadAttributedQuestionContext(ctx context.Context, id string) (askclient.AttributedQuestion, error) {
	return q.operator.ReadAttributedQuestionContext(ctx, id)
}
func (q panelDelegationQuestions) ReadQuestionAskerContext(ctx context.Context, id string) (askclient.QuestionAsker, error) {
	return q.operator.ReadQuestionAskerContext(ctx, id)
}

func (q panelDelegationQuestions) ListQuestionsContext(ctx context.Context, cursor string, limit int64) (askclient.OperatorPage, error) {
	return q.operator.ListQuestionsContext(ctx, cursor, limit)
}

var delegationQuestionOperator = func(ctx context.Context) (delegationQuestionReader, error) {
	op, err := panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	return panelDelegationQuestions{operator: op}, nil
}

// panelRuntimeProgram is the runtime program the Panel binds: the one named
// on its command line, or the installed runtime's selection. It is empty
// when neither is known; the runtime still refuses itself as a responder.
var panelRuntimeProgram = func(ctx context.Context) string {
	if panelExplicit != nil {
		return panelExplicit.Program
	}
	selected, err := selectInstalled(ctx)
	if err != nil {
		return ""
	}
	return selected.Server.Program
}

type delegationQuestion struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Program  string   `json:"program"`
	Action   string   `json:"action"`
	Resource string   `json:"resource"`
	Options  []string `json:"options"`
	// Sentence is firstUseSentence's wording, the one the native prompt and
	// the Questions page show (live walkthrough 2026-10-02, D5).
	Sentence string `json:"sentence"`
}

type delegationPage struct {
	Outcome   string               `json:"outcome"`
	Revision  string               `json:"revision,omitempty"`
	Account   string               `json:"account,omitempty"`
	Questions []delegationQuestion `json:"questions"`
	// Names titles each question's program (program_titles.go).
	Names map[string]string `json:"names,omitempty"`
}

type delegationEdit struct {
	QuestionID      string `json:"questionId"`
	Responder       string `json:"responder"`
	Option          string `json:"option"`
	Revision        string `json:"revision"`
	ConfirmExactUse bool   `json:"confirmExactUse"`
}

func validDelegationWord(s string, max int) bool {
	return len(s) <= max && questionWord(s)
}

func (p *servicePanel) questionDelegation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	var edit delegationEdit
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&edit); err != nil ||
			!validDelegationWord(edit.QuestionID, 128) || !identity.ValidSubjectProgram(edit.Responder) ||
			(edit.Option != "allow" && edit.Option != "refuse") || !validDelegationWord(edit.Revision, 128) || !edit.ConfirmExactUse {
			http.Error(w, "choose an executable, a pending first-use question and one option; confirm the exact scope", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	account := probe.Self().Account
	if !validDelegationWord(account, 128) {
		http.Error(w, "Panel account identity is unavailable", http.StatusServiceUnavailable)
		return
	}
	reader, err := delegationQuestionOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := reader.ListQuestionsContext(ctx, "", 64)
	if err != nil {
		panelError(w, err)
		return
	}
	if page.Outcome != askclient.OperatorPageOutcomePage {
		panelJSON(w, delegationPage{Outcome: page.Outcome.String(), Questions: []delegationQuestion{}})
		return
	}
	if r.Method == http.MethodGet {
		policy, err := questionRules().ListPolicyContext(ctx, "", 1)
		if err != nil {
			panelError(w, err)
			return
		}
		if policy.Outcome.String() != "page" {
			panelJSON(w, delegationPage{Outcome: "policy_" + policy.Outcome.String(), Questions: []delegationQuestion{}})
			return
		}
		questions := make([]delegationQuestion, 0)
		names := p.programTitler(ctx)
		for _, record := range page.Records {
			if record.Option != "" {
				continue
			}
			details, err := askclient.ReadDetailsContext(ctx, reader, account, record.ID)
			if err != nil || details.Permission == nil {
				continue
			}
			primary := details.Permission.Targets[0]
			target, action, resource := primary.Program, primary.Action, primary.Resource
			if !slices.Contains(record.Options, "allow") || !slices.Contains(record.Options, "refuse") {
				continue
			}
			questions = append(questions, delegationQuestion{ID: record.ID, Text: record.Text, Program: target, Action: action, Resource: resource, Options: []string{"allow", "refuse"},
				Sentence: firstUseSentence(trayPublisher{Names: names}, target, action, resource)})
		}
		programs := make([]string, 0, len(questions))
		for _, q := range questions {
			programs = append(programs, q.Program)
		}
		panelJSON(w, delegationPage{Outcome: "page", Revision: policy.Revision, Account: account, Questions: questions, Names: names.names(programs)})
		return
	}
	var selected *delegationQuestion
	for _, record := range page.Records {
		if record.ID != edit.QuestionID || record.Option != "" {
			continue
		}
		details, err := askclient.ReadDetailsContext(ctx, reader, account, record.ID)
		if err != nil || details.Permission == nil || details.Question.Question.Record == nil || details.Question.Question.Record.Option != "" {
			break
		}
		primary := details.Permission.Targets[0]
		target, action, resource := primary.Program, primary.Action, primary.Resource
		if !slices.Contains(record.Options, edit.Option) {
			break
		}
		selected = &delegationQuestion{ID: record.ID, Text: record.Text, Program: target, Action: action, Resource: resource}
		break
	}
	if selected == nil {
		http.Error(w, "that question is no longer pending; refresh before setting up an answer", http.StatusConflict)
		return
	}
	// The runtime refuses such an answer (abstraction-asks ASK-R3); the Panel
	// says so before it writes a permit that could never be used.
	if identity.SameSubjectProgram(edit.Responder, selected.Program) {
		http.Error(w, "an app may not answer the question about itself; choose a different app to answer it", http.StatusBadRequest)
		return
	}
	if runtimeProgram := panelRuntimeProgram(ctx); runtimeProgram != "" && identity.SameSubjectProgram(edit.Responder, runtimeProgram) {
		http.Error(w, "the OpenAbstractions runtime asked this question and may not answer it; choose an app to answer it", http.StatusBadRequest)
		return
	}
	resource, err := asks.FirstUseAnswerResource(selected.Program, selected.Action, selected.Resource, edit.Option)
	if err != nil {
		http.Error(w, "the question scope is not valid", http.StatusBadRequest)
		return
	}
	p.logAction(ctx, "rights.delegate_question", map[string]string{"question.id": edit.QuestionID, "asks.option": edit.Option})
	rule := rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: account, Program: edit.Responder}, Action: asks.AnswerAction, Resource: resource, Permit: true}
	result, err := questionRules().SetRuleForContext(ctx, edit.Revision, rule, delegatedQuestionAnswerTTL, "operator delegated one first-use answer")
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}
