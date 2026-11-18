package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
)

// Architect popup ruling 2026-11-10, row 28: Setup the person started that
// didn't finish is one notification with Open Catalog; a removed permission is
// no popup.
func TestSetupThatDidNotFinishIsOneNoticeWithOpenSetup(t *testing.T) {
	report := presenterResultReport(presentResult{Outcome: presentUnavailable, Err: providerConnectError("DeepSeek refused the key.")})
	if report.OpenQuestions || report.Notice == nil {
		t.Fatalf("report %+v", report)
	}
	if n := *report.Notice; n.Title != "Couldn't connect" || n.Body != "DeepSeek refused the key." || n.Action != noticeOpenSetup {
		t.Fatalf("notice %+v", n)
	}
	named := presenterResultReport(presentResult{Outcome: presentUnavailable, Err: errProviderStore, Provider: "DeepSeek"})
	if named.Notice == nil || named.Notice.Title != "DeepSeek not connected" || named.Notice.Body != "Couldn't save the key. Try again." {
		t.Fatalf("named provider notice %+v", named.Notice)
	}
	if removed := presenterResultReport(presentResult{Outcome: presentUnavailable, Err: errProviderPermissionRemoved}); removed.Notice != nil || removed.OpenQuestions {
		t.Fatalf("a removed permission raised %+v", removed)
	}
	// Rows 29 and 30: a confirmation and a changed question close silently.
	record := pendingFixture()
	for _, result := range []presentResult{
		{Outcome: presentAnswered, Answered: answeredWith(record, "completed")},
		{Outcome: presentAnswered, Answered: answeredDecision(askclient.OperatorDecisionOutcomeConflict)},
		{Outcome: presentAnswered, Answered: answeredDecision(askclient.OperatorDecisionOutcomeForbidden), Err: errors.New("pipe closed")},
	} {
		if report := presenterResultReport(result); report.OpenQuestions || report.Notice != nil {
			t.Fatalf("silent outcome raised %+v", report)
		}
	}
}

// The notice carries its one action, and the action opens Setup.
func TestPresenterNoticeOpensSetup(t *testing.T) {
	q := presenterNoticeRequest(presenterNotice{Title: "DeepSeek not connected", Body: "The key was refused.", Action: noticeOpenSetup}, time.Now())
	if !asks.ValidNotificationRequest(q) || !strings.HasPrefix(q.RequestRef.Key, presenterNoticePrefix) || len(q.Actions) != 1 ||
		q.Actions[0].ID != noticeOpenSetup || q.Actions[0].Title != "Open Catalog" || q.Actions[0].Activation != askclient.NotificationActivationApp {
		t.Fatalf("request %+v", q)
	}
	if plain := presenterNoticeRequest(presenterNotice{Title: "Couldn't save that permission", Body: "OpenCode will ask again."}, time.Now()); len(plain.Actions) != 0 || !asks.ValidNotificationRequest(plain) {
		t.Fatalf("plain notice %+v", plain)
	}
	opened := 0
	saved := launchSetupPanel
	launchSetupPanel = func() error { opened++; return nil }
	t.Cleanup(func() { launchSetupPanel = saved })
	record := askclient.NotificationRecord{Request: q}
	if handled, err := activatePresenterNotice(record, noticeOpenSetup); !handled || err != nil || opened != 1 {
		t.Fatal(handled, err, opened)
	}
	if handled, err := activatePresenterNotice(record, "undo"); !handled || err == nil || opened != 1 {
		t.Fatal("an unknown action ran", handled, err)
	}
	other := askclient.NotificationRecord{Request: askclient.NotificationRequest{RequestRef: askclient.RequestRef{Key: "oa-agent-update-x"}}}
	if handled, _ := activatePresenterNotice(other, noticeOpenSetup); handled || opened != 1 {
		t.Fatal("another owner's notice opened Setup")
	}
}

// Row 19: the app's own question, "From <App>." and the inline publisher
// line; the publisher and what closing does sit in the details.
func TestAppChoiceReadsFromTheApp(t *testing.T) {
	record := askclient.ChoiceRecord{ID: "c1", Asker: `C:\apps\opencode.exe`, Request: askclient.ChoiceRequest{Title: "Which model?", Options: []askclient.ChoiceOption{{ID: "a", Label: "Small"}}}}
	view := trayChoiceView(record, nil)
	if view.Title != "OpenAbstractions" || view.Instruction != "Which model?" || view.Content != "From opencode.\nPublisher not verified." || view.Footer != "" ||
		!strings.Contains(view.Details, "Closing this window dismisses the request") {
		t.Fatalf("%+v", view)
	}
	record.PublisherTrusted, record.Publisher = true, "Example Ltd"
	if signed := trayChoiceView(record, nil); strings.Contains(signed.Content, "Publisher") || !strings.Contains(signed.Details, "Publisher: Example Ltd (verified)") {
		t.Fatalf("%+v", signed)
	}
}

func answeredDecision(outcome askclient.OperatorDecisionOutcome) grants.Answered {
	return grants.Answered{Decision: askclient.OperatorDecision{Outcome: outcome}}
}

func answeredWith(record askclient.RecordMetadata, rule string) grants.Answered {
	record.Option, record.Yes = "allow", true
	answered := grants.Answered{Decision: askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeAnswered, Record: &record}}
	if rule == "completed" {
		answered.RuleOutcome = grants.RuleCompleted
	}
	return answered
}
