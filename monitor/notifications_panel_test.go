package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	askwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

type fakeNotifierOperator struct {
	fakeNotificationOperator
	days []askclient.NotifierDay
}

func (f *fakeNotifierOperator) ListNotifiersContext(context.Context, string, int64) (askclient.NotifierPage, error) {
	return askclient.NotifierPage{Outcome: askwire.OperatorPageOutcomePage, Complete: true,
		Notifiers: []askclient.NotifierCount{{Program: f.record.Program, Asker: "poster.exe", Days: f.days, LastPostedMs: f.record.PostedMs}}}, nil
}

// fakeNotificationRules holds one rule on notification.post.
type fakeNotificationRules struct {
	rule    *rightsclient.PolicyRule
	revoked int
}

func (f *fakeNotificationRules) ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error) {
	return rightsclient.PolicyPage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: "r1"}, nil
}
func (f *fakeNotificationRules) SetRuleForContext(_ context.Context, _ string, rule rightsclient.PolicyRule, _ time.Duration, _ string) (rightsclient.PolicyEdit, error) {
	f.rule = &rule
	return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeApplied}, nil
}
func (f *fakeNotificationRules) ReadRuleContext(context.Context, rightsclient.Subject, string, string) (rightsclient.RuleRead, error) {
	if f.rule == nil {
		return rightsclient.RuleRead{Outcome: rightsclient.RuleReadOutcomeUnknown}, nil
	}
	return rightsclient.RuleRead{Outcome: rightsclient.RuleReadOutcomeFound, Revision: "r1", Record: &rightsclient.RuleRecord{Rule: *f.rule}}, nil
}
func (f *fakeNotificationRules) RevokeRuleContext(context.Context, string, rightsclient.Subject, string, string) (rightsclient.PolicyEdit, error) {
	f.rule = nil
	f.revoked++
	return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeApplied}, nil
}

// ASK-N6: the Activity page counts a program's week beside its switch; off
// blocks and withdraws, on removes only that block and keeps a permit.
func TestPanelNotificationsCountsAndSwitch(t *testing.T) {
	record := postedRecord(t, "a1", "t1", time.Now())
	operator := &fakeNotifierOperator{fakeNotificationOperator: fakeNotificationOperator{record: record}, days: []askclient.NotifierDay{{Posts: 3}, {Posts: 4}}}
	rules := &fakeNotificationRules{}
	n := panelNotifications{
		operator: func(context.Context) (notificationOperatorReader, error) { return operator, nil },
		rules:    func(context.Context) (notificationRuleEditor, string, error) { return rules, "S-1-5-21-1", nil },
		names:    func(context.Context) *programTitler { return nil },
	}
	ctx := context.Background()
	view, err := n.read(ctx)
	if err != nil || len(view.Notifiers) != 1 || view.Notifiers[0].Week != 7 || view.Notifiers[0].Off || len(view.Notifications) != 1 || view.Notifications[0].State != "posted" {
		t.Fatalf("%+v %v", view, err)
	}
	if err := n.setOn(ctx, record.Program, false); err != nil || rules.rule == nil || rules.rule.Permit || len(operator.withdrawn) != 1 {
		t.Fatal("off blocks the program and withdraws its notifications", err, rules.rule, operator.withdrawn)
	}
	if view, _ = n.read(ctx); !view.Notifiers[0].Off {
		t.Fatal("the switch reads off")
	}
	if err := n.setOn(ctx, record.Program, true); err != nil || rules.rule != nil || rules.revoked != 1 {
		t.Fatal("on removes the block", err, rules.rule)
	}
	rules.rule = &rightsclient.PolicyRule{Permit: true}
	if err := n.setOn(ctx, record.Program, true); err != nil || rules.rule == nil || rules.revoked != 1 {
		t.Fatal("on keeps a permit the person set", err)
	}
}

// actingNotifierOperator answers Act as the service does: the presentation
// token must match, the first action wins, an expired record takes none.
type actingNotifierOperator struct {
	fakeNotifierOperator
}

func (f *actingNotifierOperator) ActContext(_ context.Context, id, action, token string) (askclient.NotificationDecision, error) {
	f.acted = append(f.acted, action+"/"+token)
	switch {
	case token != f.record.Presentation:
		return askclient.NotificationDecision{Outcome: askclient.NotificationDecisionStale}, nil
	case f.record.State == askclient.NotificationStateExpired:
		return askclient.NotificationDecision{Outcome: askclient.NotificationDecisionExpired}, nil
	case f.record.State == askclient.NotificationStateActed:
		r := f.record
		return askclient.NotificationDecision{Outcome: askclient.NotificationDecisionConflict, Record: &r}, nil
	}
	f.record.State = askclient.NotificationStateActed
	f.record.Result = &askclient.NotificationResult{ID: id, State: askclient.NotificationStateActed, Action: action}
	r := f.record
	return askclient.NotificationDecision{Outcome: askclient.NotificationDecisionActed, Record: &r}, nil
}

func actPanel(t *testing.T, operator *actingNotifierOperator) *servicePanel {
	t.Helper()
	p := newTestPanel(t)
	p.notifications = panelNotifications{
		operator: func(context.Context) (notificationOperatorReader, error) { return operator, nil },
		rules: func(context.Context) (notificationRuleEditor, string, error) {
			return &fakeNotificationRules{}, "S-1-5-21-1", nil
		},
		names: func(context.Context) *programTitler { return nil },
	}
	return p
}

func actRequest(t *testing.T, p *servicePanel, body string) (int, notificationActReply) {
	t.Helper()
	rec := appPagePost(t, p, "/notifications/act", body)
	var reply notificationActReply
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, reply
}

// Design §4.9: the Activity page lists a shown notification with its actions,
// and a chosen action goes through the native click's path (ASK-N4) with the
// record's presentation token; the first action wins; a stale token or an
// expired record sends nothing; Open Activity names its job.
func TestPanelNotificationActionsGoThroughTheClickPath(t *testing.T) {
	record := postedRecord(t, "a1", "t1", time.Now())
	record.Request.OperationID = "op-1"
	record.Request.Actions = append(record.Request.Actions, askclient.NotificationAction{ID: "open", Title: "Show in Activity", Activation: askclient.NotificationActivationOpenActivity})
	operator := &actingNotifierOperator{fakeNotifierOperator{fakeNotificationOperator: fakeNotificationOperator{record: record}}}
	p := actPanel(t, operator)

	view, err := p.notifications.read(context.Background())
	if err != nil || len(view.Notifications) != 1 {
		t.Fatal(view, err)
	}
	shown := view.Notifications[0]
	if shown.Token != "t1" || shown.Body != "a <b> & c" || len(shown.Actions) != 2 || shown.Actions[0].Title != "Load now" || shown.Actions[0].Activity || !shown.Actions[1].Activity {
		t.Fatalf("a shown notification lacks its actions: %+v", shown)
	}

	if code, _ := actRequest(t, p, `{"id":"a1","action":"load","token":"stale"}`); code == 200 || operator.record.State != askclient.NotificationStatePosted {
		t.Fatalf("a stale token was accepted (%d)", code)
	}
	code, reply := actRequest(t, p, `{"id":"a1","action":"load","token":"t1"}`)
	if code != 200 || reply.Outcome != "acted" || reply.Message != "Chose Load now." || reply.Operation != "" {
		t.Fatalf("act %d %+v", code, reply)
	}
	if code, reply = actRequest(t, p, `{"id":"a1","action":"open","token":"t1"}`); code != 200 || reply.Message != "This notification was already answered; nothing more was sent." {
		t.Fatalf("a second action %d %+v", code, reply)
	}
	if got := operator.acted; len(got) != 3 || got[1] != "load/t1" {
		t.Fatalf("Act calls %v", got)
	}
	if view, _ := p.notifications.read(context.Background()); view.Notifications[0].Action != "Load now" || len(view.Notifications[0].Actions) != 0 {
		t.Fatalf("an answered notification reads %+v", view.Notifications[0])
	}

	// Open Activity names its job for the page.
	operator.record = record
	if code, reply = actRequest(t, p, `{"id":"a1","action":"open","token":"t1"}`); code != 200 || reply.Operation != "op-1" || reply.Message != "Chose Show in Activity." {
		t.Fatalf("open activity %d %+v", code, reply)
	}

	operator.record = record
	operator.record.State = askclient.NotificationStateExpired
	if code, reply = actRequest(t, p, `{"id":"a1","action":"load","token":"t1"}`); code != 200 || reply.Outcome != "expired" || reply.Message != "This notification expired; nothing was sent." {
		t.Fatalf("expired %d %+v", code, reply)
	}

	for _, bad := range []string{`{"id":"a1","action":"load"}`, `{"id":"","action":"load","token":"t1"}`, `{"id":"a1","action":"load","token":"t1","x":1}`} {
		if code, _ := actRequest(t, p, bad); code != 400 {
			t.Errorf("%s answered %d", bad, code)
		}
	}
}
