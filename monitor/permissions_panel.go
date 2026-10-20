package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"unicode/utf8"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
)

// The Questions page's typed permission requests (asks ASK-A10, ASK-O10): the
// same requests the native permission dialog presents, worded by the same
// projection (trayTypedPermissionView) and answered through the same call
// (grants.AnswerPermissionQuestion), after the same re-read of the frozen
// effect. A person who closed the dialog, or runs without the presenter,
// answers here.

// permissionsOperator resolves the typed permission operator per request.
// Tests replace it.
var permissionsOperator = func(ctx context.Context) (presenterPermissionOperator, error) {
	return defaultPresenterPermissions(ctx)
}

// permissionsRules is the rights operator an Allow completes its effect
// through, as the native dialog's. Tests replace it.
var permissionsRules = func() grants.PermissionRuleOperator { return &panelPermissionRules{} }

// permissionRequest is one waiting typed request as the page shows it. Digest
// is the frozen effect's digest the answer must still match.
type permissionRequest struct {
	ID          string               `json:"id"`
	Asked       string               `json:"asked,omitempty"`
	Instruction string               `json:"instruction"`
	Content     string               `json:"content"`
	Warning     string               `json:"warning,omitempty"`
	Details     string               `json:"details"`
	Choices     []trayQuestionChoice `json:"choices"`
	Digest      string               `json:"digest"`
}

type permissionsPage struct {
	Outcome  string              `json:"outcome"`
	Requests []permissionRequest `json:"requests"`
	Next     string              `json:"next,omitempty"`
	Complete bool                `json:"complete"`
}

type permissionAction struct {
	ID     string `json:"id"`
	Option string `json:"option"`
	Digest string `json:"digest"`
}

// permissionAnswerResult is the answer's Asks outcome, the rule effect's
// outcome, and one sentence for the page.
type permissionAnswerResult struct {
	Outcome     string `json:"outcome"`
	RuleOutcome string `json:"ruleOutcome,omitempty"`
	Message     string `json:"message"`
}

// permissions answers GET with the waiting typed requests and POST {id,
// option, digest} with the person's answer.
func (p *servicePanel) permissions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	var action permissionAction
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		decoder.DisallowUnknownFields()
		var extra any
		if err := decoder.Decode(&action); err != nil || decoder.Decode(&extra) != io.EOF || !questionWord(action.ID) ||
			(action.Option != "allow" && action.Option != "refuse") || !questionWord(action.Digest) {
			http.Error(w, "choose one waiting permission request and Allow or Don't allow", http.StatusBadRequest)
			return
		}
	} else if cursor := r.URL.Query().Get("cursor"); len(cursor) > 256 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid permission request cursor", http.StatusBadRequest)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := permissionsOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	account := probe.Self().Account
	if r.Method == http.MethodGet {
		page, err := operator.ListPermissionsContext(ctx, r.URL.Query().Get("cursor"), 16)
		if err != nil {
			if ordinaryUnsupported(err) {
				panelJSON(w, permissionsPage{Outcome: "unsupported", Requests: []permissionRequest{}})
				return
			}
			panelError(w, err)
			return
		}
		out := permissionsPage{Outcome: page.Outcome.String(), Requests: []permissionRequest{}, Next: page.Next, Complete: page.Complete}
		names := p.programTitler(ctx)
		for _, record := range page.Records {
			if record.Option != "" {
				continue
			}
			details, err := askclient.ReadPermissionDetailsContext(ctx, operator, account, record.ID)
			read := details.Question.Question
			if err != nil || read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil || read.Record.Option != "" {
				continue // answered or withdrawn since the listing, or unreadable
			}
			view := trayTypedPermissionView(record, details, names)
			if len(view.Choices) == 0 {
				continue
			}
			out.Requests = append(out.Requests, permissionRequest{ID: record.ID, Asked: record.Asked, Instruction: view.Instruction,
				Content: view.Content, Warning: view.Warning, Details: view.Details, Choices: view.Choices, Digest: details.EffectDigest})
		}
		panelJSON(w, out)
		return
	}
	p.logAction(ctx, "permission.answer", map[string]string{"question.id": action.ID})
	// The native dialog's re-read (ASK-O10): the request must still be
	// pending with the effect the page showed.
	details, err := askclient.ReadPermissionDetailsContext(ctx, operator, account, action.ID)
	if err != nil {
		panelError(w, err)
		return
	}
	read := details.Question.Question
	switch {
	case read.Outcome == askclient.OperatorQuestionOutcomeUnknown:
		panelJSON(w, permissionAnswerResult{Outcome: "unknown", Message: "This request is no longer waiting; nothing was changed."})
		return
	case read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil:
		panelJSON(w, permissionAnswerResult{Outcome: read.Outcome.String(), Message: questionOutcomeSentence(read.Outcome.String())})
		return
	case read.Record.Option != "" || details.EffectDigest != action.Digest:
		panelJSON(w, permissionAnswerResult{Outcome: "conflict", Message: "This request was answered or changed while the page was open; nothing was changed. The list is fresh now."})
		return
	}
	answered, err := grants.AnswerPermissionQuestion(ctx, operator, permissionsRules(), account, action.ID, action.Option)
	outcome := answered.Decision.Outcome.String()
	if err != nil && outcome != "answered" {
		panelError(w, err)
		return
	}
	result := permissionAnswerResult{Outcome: outcome, RuleOutcome: answered.RuleOutcome}
	switch {
	case outcome != "answered":
		result.Message = questionOutcomeSentence(outcome)
	case action.Option == "refuse":
		result.Message = "Not allowed. No permission was changed."
	case err != nil || answered.Edit == nil || answered.Edit.Outcome.String() != "applied":
		result.Message = "Allowed, but the permission was not saved yet; it is saved on the next try."
	default:
		result.Message = "Allowed. The app may try again."
	}
	panelJSON(w, result)
}

// questionOutcomeSentence words an answer that did not record.
func questionOutcomeSentence(outcome string) string {
	switch outcome {
	case "conflict":
		return "This request was answered elsewhere; nothing was changed."
	case "forbidden":
		return "This Panel is not authorized to answer permission requests; nothing was changed."
	case "invalid":
		return "The permission service refused this answer as invalid; nothing was changed."
	}
	return "The permission service could not complete this; no decision is assumed."
}
