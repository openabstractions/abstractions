package main

import (
	"context"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstraction-identity/listen"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The typed-only Book entry must reach both actual Panel HTTP receivers; a
// stubbed navigation count cannot establish this part of the Original.
func TestTypedBookRequestReachesPanelListAndNavigationBadge(t *testing.T) {
	book, legacy := trayTestBook(t)
	_, details := typedNativeFixture(t, true, true)
	details.Effect.Subject.Account = probe.Self().Account
	permission := asks.TypedPermission{Effect: *details.Effect, Digest: asks.PermissionEffectDigest(*details.Effect), Origin: details.Origin, Purpose: details.Purpose, Display: *details.Display}
	record, outcome, err := book.AskTypedPermissionFrom("panel-typed-fixture", "fixture-service", "one-panel-typed-request", permission, listen.Seen{}, asks.Publisher{})
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	if page, err := legacy.ListQuestionsContext(t.Context(), "", 64); err != nil || len(page.Records) != 0 {
		t.Fatal("typed request leaked through old list", page, err)
	}
	panel := (&servicePanel{}).handler("test-key")
	badge := func(want int) {
		t.Helper()
		r := panelRequest(t, panel, "/panel/nav.json?path=/questions", nil)
		view := decodePanel[navJSON](t, r.Code, r.Body.Bytes())
		for _, item := range view.Items {
			if item.ID == navBadgeID {
				if item.Badge == nil || *item.Badge != want {
					t.Fatalf("typed badge = %v, want %d", item.Badge, want)
				}
				return
			}
		}
		t.Fatal("Questions navigation item absent")
	}
	badge(1)
	r := panelRequest(t, panel, "/rights/requests", nil)
	page := decodePanel[permissionsPage](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || len(page.Requests) != 1 || page.Requests[0].ID != record.ID || page.Requests[0].Digest != permission.Digest {
		t.Fatal("actual typed list", page)
	}
	request := page.Requests[0]
	r = panelRequest(t, panel, "/rights/requests", permissionAction{ID: record.ID, Option: "refuse", Digest: "sha256:changed"})
	if result := decodePanel[permissionAnswerResult](t, r.Code, r.Body.Bytes()); result.Outcome != "conflict" {
		t.Fatal("stale digest answered", result)
	}
	badge(1)
	r = panelRequest(t, panel, "/rights/requests", permissionAction{ID: record.ID, Option: "refuse", Digest: request.Digest})
	if result := decodePanel[permissionAnswerResult](t, r.Code, r.Body.Bytes()); result.Outcome != "answered" {
		t.Fatal(result)
	}
	badge(0)
	r = panelRequest(t, panel, "/rights/requests", nil)
	if page := decodePanel[permissionsPage](t, r.Code, r.Body.Bytes()); page.Outcome != "page" || len(page.Requests) != 0 {
		t.Fatal("answered request stayed visible", page)
	}
}

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

func TestMessagesPermissionQuestionsShowsOnlyAfterClickHandoff(t *testing.T) {
	for _, test := range []struct {
		name, option string
		edit         rightsclient.PolicyEditOutcome
		message      string
		applies      int
		released     bool
	}{
		{"saved", "allow", rightsclient.PolicyEditOutcomeApplied, "Codex can send messages to Claude and OpenCode.", 1, false},
		// Owner 2026-11-09: an Allow serve could not save, and released.
		{"not saved", "allow", rightsclient.PolicyEditOutcomeUnavailable, "OpenAbstractions couldn't save that permission. Codex will ask again.", 1, true},
		{"unconfirmed", "allow", rightsclient.PolicyEditOutcomeUnavailable, "Allow is saved but not confirmed yet. Ask your agent to continue in the same chat to check again.", 1, false},
		{"unwritten", "allow", rightsclient.PolicyEditOutcomeConflict, "Allow was recorded, but this answer did not save a permission change. Review Questions before asking your agent to continue.", 1, false},
		{"refuse", "refuse", 0, "Codex can't send messages to Claude and OpenCode.", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			operator := panelTypedFixture(t)
			details := &operator.details
			record := details.Question.Question.Record
			subject := details.Effect.Subject
			// The sender's program is OA's own Codex connector, which shows no
			// Publisher line; its connector name alone vouches for nothing.
			savedOwn := ownProgramTitles
			ownProgramTitles = func() []oaProgramTitle { return []oaProgramTitle{{subject.Program, "Codex"}} }
			t.Cleanup(func() { ownProgramTitles = savedOwn })
			images := installedConnectorImages(t, "oa-opencode-integration", "oa-claude-integration")
			permission, err := grants.AgentMessagePermission(subject,
				applications.CapturedAgentApplication{Name: "oa-codex-integration", Program: subject.Program, FileKey: "sender-key", EnrollmentRevision: "sender-revision"},
				[]applications.CapturedAgentApplication{
					{Name: "oa-opencode-integration", Program: images["oa-opencode-integration"], FileKey: "opencode-key", EnrollmentRevision: "opencode-revision"},
					{Name: "oa-claude-integration", Program: images["oa-claude-integration"], FileKey: "claude-key", EnrollmentRevision: "claude-revision"},
				})
			if err != nil {
				t.Fatal(err)
			}
			details.Effect, details.Display, details.EffectDigest = &permission.Effect, &permission.Display, permission.Digest
			details.Origin, details.Purpose = permission.Origin, permission.Purpose
			view := trayTypedPermissionView(*record, *details, nil)
			if view.Instruction != "Allow Codex to send tasks to Claude and OpenCode?" || view.Content != "They can act on them while you're away." || !strings.Contains(view.Details, "You can remove each one in Permissions.") {
				t.Fatalf("message question words: %q / %q", view.Instruction, view.Content)
			}
			if len(view.Choices) != 2 || view.Choices[0].Label != "Allow" || view.Choices[1].Label != "Not now" || view.Permission.Publisher != "" || view.Default != "refuse" || view.Dismiss != "refuse" {
				t.Fatalf("message question buttons or publisher: %+v %q", view.Choices, view.Permission.Publisher)
			}
			for _, gone := range []string{"One:", "send requests", "(not registered)", "Only you can allow this direction", "Publisher"} {
				if strings.Contains(view.Instruction+view.Content+view.Permission.Scope+view.Permission.Requester+view.Details, gone) {
					t.Fatalf("message question still reads %q", gone)
				}
			}
			rules := &fakePermissionRules{outcome: test.edit}
			if test.released {
				rules.onApply = operator.release
			}
			permissionsRules = func() grants.PermissionRuleOperator { return rules }
			panel := (&servicePanel{}).handler("test-key")
			r := panelRequest(t, panel, "/rights/requests", nil)
			page := decodePanel[permissionsPage](t, r.Code, r.Body.Bytes())
			if len(page.Requests) != 1 {
				t.Fatalf("expected a waiting request: %+v", page)
			}
			for _, body := range []string{page.Requests[0].Content, view.PermissionContent, view.Permission.Scope} {
				if strings.Contains(body, "continue in the same chat") {
					t.Fatalf("after-click guidance leaked into the waiting prompt: %s", body)
				}
			}
			r = panelRequest(t, panel, "/rights/requests", permissionAction{ID: record.ID, Option: test.option, Digest: permission.Digest})
			result := decodePanel[permissionAnswerResult](t, r.Code, r.Body.Bytes())
			if result.Outcome != "answered" || result.Message != test.message || len(rules.applied) != test.applies {
				t.Fatalf("unexpected answer result: %+v, attempts: %v", result, rules.applied)
			}
		})
	}
}
