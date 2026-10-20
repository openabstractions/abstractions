package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstraction-identity/listen"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

func TestPanelUsesCompletePermissionForLongDelegationAndAllow(t *testing.T) {
	const action = "abstraction.model/lookup"
	book, policy, _ := panelFirstUseRuntime(t, action)
	target := filepath.Join(t.TempDir(), strings.Repeat("p", 220), "application.exe")
	resource := strings.Repeat("r", 1024)
	permission, err := asks.NewPermissionRequest(asks.PermissionTarget{Account: probe.Self().Account, Program: target, Action: action, Resource: resource})
	if err != nil {
		t.Fatal(err)
	}
	record, outcome, err := book.AskPermissionFrom("runtime-scope", "runtime", "long-panel", permission, listen.Seen{}, asks.Publisher{})
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	panel := (&servicePanel{}).handler("test-key")
	response := panelRequest(t, panel, "/questions", nil)
	page := decodePanel[questionPage](t, response.Code, response.Body.Bytes())
	if page.Outcome != askclient.OperatorPageOutcomePage || page.Permissions[record.ID].Targets[0] != permission.Targets[0] || page.Sentences[record.ID] == "" {
		t.Fatalf("incomplete Panel target: %+v", page)
	}
	response = panelRequest(t, panel, "/rights/delegation", nil)
	setup := decodePanel[delegationPage](t, response.Code, response.Body.Bytes())
	if len(setup.Questions) != 1 || setup.Questions[0].Program != target || setup.Questions[0].Resource != resource {
		t.Fatalf("incomplete delegation: %+v", setup)
	}
	responder := filepath.Join(t.TempDir(), "responder.exe")
	response = panelRequest(t, panel, "/rights/delegation", delegationEdit{QuestionID: record.ID, Responder: responder, Option: "allow", Revision: setup.Revision, ConfirmExactUse: true})
	edited := decodePanel[rightsclient.PolicyEdit](t, response.Code, response.Body.Bytes())
	canonical, err := asks.FirstUseAnswerResource(target, action, resource, "allow")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Outcome != rightsclient.PolicyEditOutcomeApplied || edited.Current == nil || edited.Current.Resource != canonical || !strings.HasPrefix(canonical, "first-use@2:") {
		t.Fatalf("delegation %+v", edited)
	}
	response = panelRequest(t, panel, "/questions", questionAction{Action: "answer", ID: record.ID, Option: "allow"})
	answered := decodePanel[questionAnswerPanelResult](t, response.Code, response.Body.Bytes())
	if answered.Outcome != askclient.OperatorDecisionOutcomeAnswered || answered.Edit == nil || answered.Edit.Outcome != rightsclient.PolicyEditOutcomeApplied {
		t.Fatalf("long Allow %+v", answered)
	}
	if got := policy.Decide(rightsclient.Subject{Account: probe.Self().Account, Program: target}, action, resource); got.Outcome != rightsclient.DecisionOutcomePermitted {
		t.Fatalf("full target %+v", got)
	}
	// A short/truncated display target has no authority.
	if got := policy.Decide(rightsclient.Subject{Account: probe.Self().Account, Program: target[:200]}, action, resource[:200]); got.Outcome == rightsclient.DecisionOutcomePermitted {
		t.Fatal("display truncation gained authority")
	}
	read, err := delegationQuestionOperator(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	askerDetails, err := read.ReadQuestionAskerContext(context.Background(), record.ID)
	details := askerDetails.Details
	if err != nil || details.Permission == nil || details.Permission.Targets[0] != permission.Targets[0] {
		t.Fatal(details, err)
	}
}
