package main

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

type fakeToastSurface struct {
	shown   []shownNotification
	removed []string
}

func (s *fakeToastSurface) Show(n shownNotification) error { s.shown = append(s.shown, n); return nil }
func (s *fakeToastSurface) Remove(id, group string) error {
	s.removed = append(s.removed, id)
	return nil
}

func postedRecord(t *testing.T, id, token string, posted time.Time) askclient.NotificationRecord {
	t.Helper()
	program := filepath.Join(t.TempDir(), "poster.exe")
	return askclient.NotificationRecord{ID: id, Asker: "poster.exe", Program: program, State: askclient.NotificationStatePosted, AdmittedMs: posted.UnixMilli(), PostedMs: posted.UnixMilli(), Presentation: token,
		Request: askclient.NotificationRequest{Title: "Download finished", Body: "a <b> & c", ExpiresMs: posted.Add(time.Hour).UnixMilli(), Delivery: askclient.NotificationDeliveryNow,
			Actions: []askclient.NotificationAction{{ID: "load", Title: "Load now", Activation: askclient.NotificationActivationApp}}}}
}

// ASK-N7: a posted record is shown once per token, a later token replaces it
// quietly, a locked session shows nothing new, and an ended record is removed.
func TestNotificationPresenterSync(t *testing.T) {
	surface := &fakeToastSurface{}
	p := newNotificationPresenter(surface)
	now := time.Now()
	p.now = func() time.Time { return now }
	record := postedRecord(t, "a1", "t1", now)
	p.sync([]askclient.NotificationRecord{record}, false, nil)
	if len(surface.shown) != 0 {
		t.Fatal("a locked session shows nothing new")
	}
	p.sync([]askclient.NotificationRecord{record}, true, nil)
	p.sync([]askclient.NotificationRecord{record}, true, nil)
	if len(surface.shown) != 1 || surface.shown[0].Silently || surface.shown[0].Sender != "poster (not registered)" || surface.shown[0].Token != "t1" {
		t.Fatalf("shown once with its sender: %+v", surface.shown)
	}
	record.Presentation = "t2"
	p.sync([]askclient.NotificationRecord{record}, true, nil)
	if len(surface.shown) != 2 || !surface.shown[1].Silently || surface.shown[1].Token != "t2" {
		t.Fatalf("a new token replaces the item quietly: %+v", surface.shown)
	}
	old := postedRecord(t, "a2", "t1", now.Add(-time.Hour))
	record.State = askclient.NotificationStateActed
	p.sync([]askclient.NotificationRecord{record, old}, true, nil)
	if len(surface.removed) != 1 || surface.removed[0] != "a1" {
		t.Fatalf("an ended record is removed: %+v", surface.removed)
	}
	if last := surface.shown[len(surface.shown)-1]; last.ID != "a2" || !last.Silently {
		t.Fatal("an old notification first seen goes to the center quietly", last)
	}
}

// ASK-N2: the toast names the service-recorded sender in the attribution
// line, escapes the app's text and adds OA's Turn off.
func TestToastDocumentEscapesAndAttributes(t *testing.T) {
	n := shownNotification{ID: "a1", Token: "t&1", Title: `Title "x" <y>`, Body: "a <b> & c", Sender: "Pretend Lemonade",
		Actions: []askclient.NotificationAction{{ID: "load|now", Title: "Load <now>", Activation: askclient.NotificationActivationApp}}}
	doc := toastDocument(n)
	if err := xml.Unmarshal([]byte(doc), new(struct{})); err != nil {
		t.Fatalf("not well-formed: %v\n%s", err, doc)
	}
	for _, want := range []string{`placement="attribution">From Pretend Lemonade</text>`, `placement="contextMenu"`, "Turn off notifications from Pretend Lemonade", "a &lt;b&gt; &amp; c"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("missing %q in %s", want, doc)
		}
	}
	click, ok := parseNotificationClick(notificationClickArgs("a1", "load|now", "t&1", false))
	if !ok || click.ID != "a1" || click.Action != "load|now" || click.Token != "t&1" || click.TurnOff {
		t.Fatal(click, ok)
	}
	if off, ok := parseNotificationClick(notificationClickArgs("a1", "", "t", true)); !ok || !off.TurnOff || off.Action != "" {
		t.Fatal(off, ok)
	}
	if _, ok := parseNotificationClick("other:id=a1"); ok {
		t.Fatal("foreign arguments must be refused")
	}
}

type fakeNotificationOperator struct {
	record    askclient.NotificationRecord
	acted     []string
	outcome   askclient.NotificationDecisionOutcome
	withdrawn []string
}

func (f *fakeNotificationOperator) ListAllNotificationsContext(context.Context) ([]askclient.NotificationRecord, error) {
	return []askclient.NotificationRecord{f.record}, nil
}
func (f *fakeNotificationOperator) ReadNotificationContext(_ context.Context, id string) (askclient.NotificationRead, error) {
	r := f.record
	return askclient.NotificationRead{Record: &r}, nil
}
func (f *fakeNotificationOperator) ActContext(_ context.Context, id, action, token string) (askclient.NotificationDecision, error) {
	f.acted = append(f.acted, action+"/"+token)
	r := f.record
	r.Result = &askclient.NotificationResult{ID: id, State: askclient.NotificationStateActed, Action: action}
	return askclient.NotificationDecision{Outcome: f.outcome, Record: &r}, nil
}
func (f *fakeNotificationOperator) DismissContext(context.Context, string) (askclient.NotificationDecision, error) {
	return askclient.NotificationDecision{}, nil
}
func (f *fakeNotificationOperator) WithdrawProgramContext(_ context.Context, program string) (askclient.NotificationDecision, error) {
	f.withdrawn = append(f.withdrawn, program)
	return askclient.NotificationDecision{Outcome: askclient.NotificationDecisionWithdrawn, Count: 1}, nil
}

type fakeTurnOffRules struct{ set []rightsclient.PolicyRule }

func (f *fakeTurnOffRules) ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error) {
	return rightsclient.PolicyPage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: "r1"}, nil
}
func (f *fakeTurnOffRules) SetRuleForContext(_ context.Context, _ string, rule rightsclient.PolicyRule, _ time.Duration, _ string) (rightsclient.PolicyEdit, error) {
	f.set = append(f.set, rule)
	return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeApplied}, nil
}

// ASK-N4, ASK-N6: a click carries its token; a body click acts on Open
// Activity and opens that operation; Turn off blocks and withdraws; an
// expired record's item is removed.
func TestNotificationClicks(t *testing.T) {
	record := postedRecord(t, "a1", "t1", time.Now())
	record.Request.OperationID = "op-1"
	record.Request.Actions = append(record.Request.Actions, askclient.NotificationAction{ID: "open", Title: "Open", Activation: askclient.NotificationActivationOpenActivity})
	operator := &fakeNotificationOperator{record: record, outcome: askclient.NotificationDecisionActed}
	rules := &fakeTurnOffRules{}
	surface := &fakeToastSurface{}
	var opened []string
	clicks := notificationClicks{
		operator: func(context.Context) (notificationOperator, error) { return operator, nil },
		rules:    func(context.Context) (notificationRules, string, error) { return rules, "S-1-5-21-1", nil },
		activity: func(id string) error { opened = append(opened, id); return nil },
		surface:  surface, budget: time.Second,
	}
	ctx := context.Background()
	if err := clicks.handle(ctx, notificationClickArgs("a1", "load", "t1", false)); err != nil || len(opened) != 0 || operator.acted[0] != "load/t1" {
		t.Fatal("an app action is recorded with its token and opens nothing", err, opened, operator.acted)
	}
	if err := clicks.handle(ctx, notificationClickArgs("a1", "", "t1", false)); err != nil || len(opened) != 1 || opened[0] != "op-1" || operator.acted[1] != "open/t1" {
		t.Fatal("a body click acts on Open Activity and opens its operation", err, opened, operator.acted)
	}
	// A Turn off writes the person's rule, so it needs the shown token: a
	// stale item from another showing and arguments another process made up
	// record nothing and only remove the item (confused-deputy check).
	surface.removed = nil
	for _, args := range []string{notificationClickArgs("a1", "", "t0-earlier-showing", true), "oa-notification:id=a1&off=1"} {
		if err := clicks.handle(ctx, args); err != nil || len(rules.set) != 0 || len(operator.withdrawn) != 0 {
			t.Fatal("a Turn off without the shown token records nothing", args, err, rules.set, operator.withdrawn)
		}
	}
	if len(surface.removed) != 2 {
		t.Fatal("a refused Turn off removes the item", surface.removed)
	}
	if err := clicks.handle(ctx, notificationClickArgs("a1", "", "t1", true)); err != nil || len(rules.set) != 1 || rules.set[0].Permit || rules.set[0].Action != notificationPostAction || rules.set[0].Subject.Program != record.Program || len(operator.withdrawn) != 1 {
		t.Fatal("Turn off blocks the program and withdraws its notifications", err, rules.set, operator.withdrawn)
	}
	operator.outcome = askclient.NotificationDecisionExpired
	surface.removed = nil
	if err := clicks.handle(ctx, notificationClickArgs("a1", "load", "t1", false)); err != nil || len(surface.removed) != 1 {
		t.Fatal("an expired notification's item is removed", err, surface.removed)
	}
	if err := clicks.handle(ctx, "garbage"); err == nil {
		t.Fatal("unreadable click arguments are refused")
	}
}
