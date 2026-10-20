package main

import (
	"context"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
)

// panelTypedFixture is typedNativeFixture for this Panel's own account.
func panelTypedFixture(t *testing.T) *fakePermissionOperator {
	t.Helper()
	_, details := typedNativeFixture(t, false, false)
	effect := *details.Effect
	effect.Subject.Account = probe.Self().Account
	details.Effect, details.EffectDigest = &effect, asks.PermissionEffectDigest(effect)
	operator := &fakePermissionOperator{details: details}
	previous, previousRules := permissionsOperator, permissionsRules
	permissionsOperator = func(context.Context) (presenterPermissionOperator, error) { return operator, nil }
	t.Cleanup(func() { permissionsOperator, permissionsRules = previous, previousRules })
	return operator
}

// asks ASK-A10, ASK-O10: the Questions page lists a typed permission request
// worded as the native dialog words it, and answers it through the dialog's
// call only while it is pending with the effect the page showed.
func TestTheQuestionsPageListsAndAnswersTypedPermissionRequests(t *testing.T) {
	operator := panelTypedFixture(t)
	rules := &fakePermissionRules{}
	permissionsRules = func() grants.PermissionRuleOperator { return rules }
	panel := (&servicePanel{}).handler("test-key")

	r := panelRequest(t, panel, "/rights/requests", nil)
	page := decodePanel[permissionsPage](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || len(page.Requests) != 1 {
		t.Fatalf("the waiting typed requests %+v", page)
	}
	request := page.Requests[0]
	view := trayTypedPermissionView(*operator.details.Question.Question.Record, operator.details, nil)
	if request.Instruction != view.Instruction || request.Content != view.Content || request.Digest != operator.details.EffectDigest ||
		len(request.Choices) != 2 || request.Choices[0].Option != "allow" || request.Choices[1].Option != "refuse" {
		t.Fatalf("the request is not worded as the native dialog: %+v", request)
	}

	// A digest the page did not show answers nothing.
	r = panelRequest(t, panel, "/rights/requests", permissionAction{ID: request.ID, Option: "allow", Digest: "sha256:other"})
	if result := decodePanel[permissionAnswerResult](t, r.Code, r.Body.Bytes()); result.Outcome != "conflict" || len(operator.answers) != 0 {
		t.Fatalf("a changed request was answered: %+v %v", result, operator.answers)
	}

	r = panelRequest(t, panel, "/rights/requests", permissionAction{ID: request.ID, Option: "allow", Digest: request.Digest})
	result := decodePanel[permissionAnswerResult](t, r.Code, r.Body.Bytes())
	if result.Outcome != "answered" || result.Message != "Allowed. The app may try again." || len(operator.answers) != 1 || operator.answers[0] != request.ID+":allow" ||
		len(rules.applied) != 1 || !strings.HasSuffix(rules.applied[0], ":"+request.ID) {
		t.Fatalf("the Allow %+v answers=%v applied=%v", result, operator.answers, rules.applied)
	}

	// Answered: the list leaves it out, and a second answer changes nothing.
	r = panelRequest(t, panel, "/rights/requests", nil)
	if page := decodePanel[permissionsPage](t, r.Code, r.Body.Bytes()); page.Outcome != "page" || len(page.Requests) != 0 {
		t.Fatalf("an answered request is still waiting %+v", page)
	}
	r = panelRequest(t, panel, "/rights/requests", permissionAction{ID: request.ID, Option: "refuse", Digest: request.Digest})
	if result := decodePanel[permissionAnswerResult](t, r.Code, r.Body.Bytes()); result.Outcome != "conflict" || len(operator.answers) != 1 {
		t.Fatalf("an answered request was answered again %+v", result)
	}
	for _, bad := range []permissionAction{{ID: request.ID, Option: "never", Digest: request.Digest}, {ID: "", Option: "allow", Digest: request.Digest}} {
		if r := panelRequest(t, panel, "/rights/requests", bad); r.Code != 400 {
			t.Fatalf("%+v = %d", bad, r.Code)
		}
	}
}
