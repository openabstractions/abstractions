package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
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
	ApplyFirstUseAllowContext(ctx context.Context, expected, id string) (rightsclient.PolicyEdit, error)
}

// questionPage is a question listing with, for each answered first-use
// question, whether the rule its answer names is on record: written,
// not_written, or the rights service's word when it could not tell.
type questionPage struct {
	askclient.OperatorPage
	RuleStates map[string]string `json:"ruleStates,omitempty"`
	// Programs names, for each first-use question whose requester had a
	// trusted signature recorded at admission (asks ASK-A6, ASK-O7), the
	// program as the Tray prompt names it, its publisher, and the sentence a
	// script host adds (ASK-A8).
	Programs map[string]questionProgram `json:"programs,omitempty"`
	// Sentences words each first-use question with firstUseSentence, the
	// sentence the native prompt shows (live walkthrough 2026-10-02, D5).
	Sentences map[string]string `json:"sentences,omitempty"`
	// Choices labels each question's options as the native prompt's buttons
	// do (questionChoices).
	Choices     map[string][]trayQuestionChoice        `json:"choices,omitempty"`
	Permissions map[string]askclient.PermissionRequest `json:"permissions,omitempty"`
	// Askers names, for each question whose bound asker program the service
	// recorded (asks ASK-A2, ASK-O11), that program and the display name
	// derived from it, so two programs with one file name read apart.
	Askers map[string]questionAsker `json:"askers,omitempty"`
}

// questionAsker is the program that asked a question: Name is the display
// name derived from Program (asks.AskerDisplay).
type questionAsker struct {
	Name    string `json:"name"`
	Program string `json:"program"`
}

// questionProgram is the requesting program as the Questions page names it.
// Name is programDisplayName's: the signed description, shortened, or the
// file name. Description is the full recorded description. An empty
// Publisher is a verified signature whose signer name was not recorded.
type questionProgram struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Publisher    string `json:"publisher,omitempty"`
	OriginalName string `json:"originalName,omitempty"`
	ScriptHost   string `json:"scriptHost,omitempty"`
}

// recordedProgram reads one first-use question's recorded publisher, the
// same read the Tray prompt makes. ok is false when the read fails or no
// trusted signature was recorded; the page then names the program by its
// file name and shows no publisher. The publisher returned is the zero value
// unless ok.
func recordedProgram(ctx context.Context, operator askclient.DetailsReader, record askclient.RecordMetadata) (trayPublisher, questionProgram, bool) {
	details, err := askclient.ReadDetailsContext(ctx, operator, probe.Self().Account, record.ID)
	read := details.Question.Question
	if err != nil || read.Outcome != askclient.OperatorQuestionOutcomeFound || !read.PublisherTrusted {
		return trayPublisher{}, questionProgram{}, false
	}
	if details.Permission == nil {
		return trayPublisher{}, questionProgram{}, false
	}
	program := details.Permission.Targets[0].Program
	publisher := publisherOfRead(read)
	return publisher, questionProgram{Name: programDisplayName(publisher, program), Description: read.ProgramDescription,
		Publisher: read.Publisher, OriginalName: read.ProgramOriginalName,
		ScriptHost: scriptHostSentence(publisher, program)}, true
}

func (r *panelRules) SetRuleForContext(ctx context.Context, expected string, rule rightsclient.PolicyRule, ttl time.Duration, why string) (rightsclient.PolicyEdit, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	return operator.SetRuleForContext(ctx, expected, rule, ttl, why)
}

func (r *panelRules) ApplyFirstUseAllowContext(ctx context.Context, expected, id string) (rightsclient.PolicyEdit, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	return operator.ApplyFirstUseAllowContext(ctx, expected, id)
}
func (r *panelRules) SetRulesForContext(ctx context.Context, expected string, rules []rightsclient.PolicyRule, ttl time.Duration, why string) (rightsclient.PolicyEdit, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	return operator.SetRulesForContext(ctx, expected, rules, ttl, why)
}

// questionAction is one operator decision the person made in the panel.
type questionAction struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Option string `json:"option"`
}

// questionAnswerPanelResult preserves the asks decision when a separate
// first-use permission write fails after the answer was durably recorded.
type questionAnswerPanelResult struct {
	askclient.OperatorDecision
	Rule *rightsclient.PolicyRule `json:"rule,omitempty"`
	Edit *rightsclient.PolicyEdit `json:"edit,omitempty"`
	// Paired and PairedEdit are the content.read rule a paired input-profile
	// answer writes after Rule (INF-V4).
	Paired       *rightsclient.PolicyRule `json:"paired,omitempty"`
	PairedEdit   *rightsclient.PolicyEdit `json:"pairedEdit,omitempty"`
	RuleOutcome  string                   `json:"ruleOutcome,omitempty"`
	PartialError string                   `json:"partialError,omitempty"`
}

// promptOnly reports an answer the Questions page must leave to the native
// prompt: once on a system-access question, which the prompt follows with
// the administrator prompt (asks ASK-S1, facade FAC-H3).
func promptOnly(record *askclient.RecordMetadata, option string) bool {
	return record != nil && record.Key == asks.SystemSecurityLogKey && option == "once"
}

// pageOptions are the answers the Questions page offers for record.
func pageOptions(record askclient.RecordMetadata) []string {
	return slices.DeleteFunc(slices.Clone(record.Options), func(o string) bool { return promptOnly(&record, o) })
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
		case (action.Action == "retire" || action.Action == "rule") && questionWord(action.ID) && action.Option == "":
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
		names := p.programTitler(ctx)
		for _, record := range page.Records {
			if record.Key == asks.FirstUseKey && record.Option != "" {
				if listed.RuleStates == nil {
					listed.RuleStates = map[string]string{}
				}
				listed.RuleStates[record.ID] = grants.ListedFirstUseRuleState(ctx, operator, rules, account, record)
			}
			// One read per record: the recorded publisher and asker program
			// are not on RecordMetadata, which refuses unknown fields
			// (ASK-O7, ASK-O11). A page holds at most 16 records, each read a
			// local IPC call within this request's budget; a failed read
			// names the program by its display name.
			asker, readErr := askclient.ReadAskerContext(ctx, operator, account, record.ID)
			if readErr == nil && asker.Program != "" {
				if listed.Askers == nil {
					listed.Askers = map[string]questionAsker{}
				}
				listed.Askers[record.ID] = questionAsker{Name: names.name(asker.Program), Program: asker.Program}
			}
			if record.Key == asks.FirstUseKey {
				details := asker.Details
				if readErr != nil || details.Permission == nil {
					continue
				}
				target := details.Permission.Targets[0]
				program, action, resource := target.Program, target.Action, target.Resource
				read := details.Question.Question
				publisher := publisherOfRead(read).withNames(names)
				if listed.Permissions == nil {
					listed.Permissions = map[string]askclient.PermissionRequest{}
				}
				listed.Permissions[record.ID] = *details.Permission
				if read.PublisherTrusted {
					if listed.Programs == nil {
						listed.Programs = map[string]questionProgram{}
					}
					listed.Programs[record.ID] = questionProgram{Name: programDisplayName(publisher, program), Description: read.ProgramDescription, Publisher: read.Publisher, OriginalName: read.ProgramOriginalName, ScriptHost: scriptHostSentence(publisher, program)}
				}
				if listed.Sentences == nil {
					listed.Sentences = map[string]string{}
				}
				listed.Sentences[record.ID] = firstUseSentence(publisher, program, action, resource)
			}
			// The runtime's question about an app that added itself reads as
			// the native prompt words it; its path is the line's title.
			if program, sentence, ok := selfRegisteredWords(record, asker.Program); ok && readErr == nil {
				if listed.Sentences == nil {
					listed.Sentences = map[string]string{}
				}
				listed.Sentences[record.ID] = sentence
				if read := asker.Details.Question.Question; read.PublisherTrusted {
					publisher := publisherOfRead(read).withNames(names)
					if listed.Programs == nil {
						listed.Programs = map[string]questionProgram{}
					}
					listed.Programs[record.ID] = questionProgram{Name: programDisplayName(publisher, program), Description: read.ProgramDescription, Publisher: read.Publisher, OriginalName: read.ProgramOriginalName}
				}
			}
			if choices := questionChoices(pageOptions(record)); len(choices) > 0 {
				if listed.Choices == nil {
					listed.Choices = map[string][]trayQuestionChoice{}
				}
				listed.Choices[record.ID] = choices
			}
		}
		result = listed
	case "answer", "rule":
		// A runtime's first-use question: the answer writes the rule it names.
		// The answer is recorded only while the question is pending (ASK-O8).
		// "rule" retries a rule that did not land from the recorded answer,
		// read through ReadQuestionAsker; it never answers again.
		var answered grants.Answered
		if action.Action == "rule" {
			answered, err = grants.RetryFirstUseRule(ctx, operator, rules, account, action.ID)
		} else {
			if action.Option == "once" {
				attributedRead, readErr := operator.ReadAttributedQuestionContext(ctx, action.ID)
				read := attributedRead.Question
				if readErr != nil {
					panelError(w, readErr)
					return
				}
				if promptOnly(read.Record, action.Option) {
					http.Error(w, "answer this question in the OpenAbstractions prompt; Windows asks for administrator approval right after it", 409)
					return
				}
			}
			answered, err = grants.AnswerQuestion(ctx, operator, rules, account, action.ID, action.Option)
		}
		answerResult := questionAnswerPanelResult{
			OperatorDecision: answered.Decision,
			Rule:             answered.Rule,
			Edit:             answered.Edit,
			Paired:           answered.Paired,
			PairedEdit:       answered.PairedEdit,
			RuleOutcome:      answered.RuleOutcome,
		}
		if err != nil && answered.Decision.Outcome == askclient.OperatorDecisionOutcomeAnswered && answered.Decision.Record != nil {
			answerResult.PartialError = fmt.Sprintf("the answer was recorded and its rule may not be written; list the questions, then retry the rule: %v", err)
			result = answerResult
			err = nil
		} else {
			result = answerResult
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
