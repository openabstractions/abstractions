package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
	"unicode/utf8"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Activity page's Notifications group (ASK-N6): how often each program
// notified in the last seven days, beside the switch that stops it, and the
// notifications the service holds now. Turning a program off writes the
// person's block on abstraction.asks/notification.post and withdraws its
// live notifications; turning it on removes that block. A permit the person
// set stays as it is.

type notifierView struct {
	Program string `json:"program"`
	Name    string `json:"name"`
	Week    int64  `json:"week"`
	LastMs  int64  `json:"last_ms"`
	Off     bool   `json:"off"`
}

type notificationView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Name      string `json:"name"`
	State     string `json:"state"`
	At        int64  `json:"at"`
	Action    string `json:"action,omitempty"`
	Operation string `json:"operation,omitempty"`
	// A shown notification's actions and the presentation token they are
	// sent with (ASK-N4), as a native click sends them.
	Token   string                   `json:"token,omitempty"`
	Actions []notificationActionView `json:"actions,omitempty"`
}

type notificationActionView struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Activity bool   `json:"activity,omitempty"`
}

type notificationsView struct {
	Outcome       string             `json:"outcome"`
	Notifiers     []notifierView     `json:"notifiers"`
	Notifications []notificationView `json:"notifications"`
}

// notificationRuleEditor reads, sets and removes one exact rule.
type notificationRuleEditor interface {
	notificationRules
	ReadRuleContext(ctx context.Context, subject rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error)
	RevokeRuleContext(ctx context.Context, expected string, subject rightsclient.Subject, action, resource string) (rightsclient.PolicyEdit, error)
}

type notificationOperatorReader interface {
	notificationOperator
	ListNotifiersContext(ctx context.Context, cursor string, limit int64) (askclient.NotifierPage, error)
}

// panelNotifications reaches the runtime's services; tests supply fakes.
type panelNotifications struct {
	operator func(context.Context) (notificationOperatorReader, error)
	rules    func(context.Context) (notificationRuleEditor, string, error)
	names    func(context.Context) *programTitler
}

func defaultPanelNotifications(p *servicePanel) panelNotifications {
	return panelNotifications{
		operator: func(ctx context.Context) (notificationOperatorReader, error) {
			return panelMachine().ResolveAsksNotificationOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		},
		rules: func(ctx context.Context) (notificationRuleEditor, string, error) {
			_, subject, err := panelApplicationReads{}.resolve(ctx)
			if err != nil {
				return nil, "", err
			}
			operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
			return operator, subject.Account, err
		},
		names: p.programTitler,
	}
}

// notificationsHandler serves GET (the group's state) and POST {program, on}.
func (p *servicePanel) notificationsHandler(w http.ResponseWriter, r *http.Request) {
	n := p.notifications
	if n.operator == nil {
		n = defaultPanelNotifications(p)
	}
	ctx, done := panelCall(r)
	defer done()
	switch r.Method {
	case http.MethodGet:
		view, err := n.read(ctx)
		if err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, view)
	case http.MethodPost:
		var change struct {
			Program string `json:"program"`
			On      bool   `json:"on"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<13)).Decode(&change); err != nil || change.Program == "" || len(change.Program) > 4096 || !utf8.ValidString(change.Program) {
			http.Error(w, "invalid change: name one program", 400)
			return
		}
		p.logAction(ctx, "notifications.switch", map[string]string{"program": change.Program, "on": fmt.Sprint(change.On)})
		if err := n.setOn(ctx, change.Program, change.On); err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, map[string]bool{"on": change.On})
	default:
		http.Error(w, "GET or POST required", 405)
	}
}

func (n panelNotifications) read(ctx context.Context) (notificationsView, error) {
	view := notificationsView{Outcome: "page", Notifiers: []notifierView{}, Notifications: []notificationView{}}
	operator, err := n.operator(ctx)
	if err != nil {
		return view, err
	}
	names := n.names(ctx)
	page, err := operator.ListNotifiersContext(ctx, "", 64)
	if err != nil {
		return view, err
	}
	if page.Outcome.String() != "page" {
		view.Outcome = page.Outcome.String()
		return view, nil
	}
	rules, account, rulesErr := n.rules(ctx)
	for _, row := range page.Notifiers {
		v := notifierView{Program: row.Program, Name: programDisplayName(trayPublisher{Names: names}, row.Program), LastMs: row.LastPostedMs}
		for _, d := range row.Days {
			v.Week += d.Posts
		}
		if rulesErr == nil {
			read, err := rules.ReadRuleContext(ctx, rightsclient.Subject{Account: account, Program: row.Program}, notificationPostAction, notificationPostResource)
			v.Off = err == nil && read.Outcome == rightsclient.RuleReadOutcomeFound && read.Record != nil && !read.Record.Rule.Permit
		}
		view.Notifiers = append(view.Notifiers, v)
	}
	sort.Slice(view.Notifiers, func(i, j int) bool { return view.Notifiers[i].LastMs > view.Notifiers[j].LastMs })
	records, err := operator.ListAllNotificationsContext(ctx)
	if err != nil {
		return view, err
	}
	for i := len(records) - 1; i >= 0 && len(view.Notifications) < 20; i-- {
		record := records[i]
		v := notificationView{ID: record.ID, Title: record.Request.Title, Name: senderName(record, names), State: record.State.String(), At: record.AdmittedMs, Operation: record.Request.OperationID}
		if record.PostedMs > 0 {
			v.At = record.PostedMs
		}
		if record.Result != nil {
			v.Action = record.Result.Action
			for _, a := range record.Request.Actions {
				if a.ID == record.Result.Action {
					v.Action = a.Title
				}
			}
		}
		if record.State == askclient.NotificationStatePosted && record.Presentation != "" {
			v.Body, v.Token = record.Request.Body, record.Presentation
			for _, a := range record.Request.Actions {
				v.Actions = append(v.Actions, notificationActionView{ID: a.ID, Title: a.Title,
					Activity: a.Activation == askclient.NotificationActivationOpenActivity})
			}
		}
		view.Notifications = append(view.Notifications, v)
	}
	return view, nil
}

// notificationActReply is one action the person chose on the Activity page.
type notificationActReply struct {
	Outcome string `json:"outcome"`
	Message string `json:"message"`
	// Operation is the job an Open Activity action names; the page shows its row.
	Operation string `json:"operation,omitempty"`
}

// panelNoSurface stands in for the native surface when the Panel acts: the
// session presenter removes its own native item once the record ends.
type panelNoSurface struct{}

func (panelNoSurface) Show(shownNotification) error { return nil }
func (panelNoSurface) Remove(string, string) error  { return nil }

// notificationsAct carries out one action the person chose for a shown
// notification on the Activity page (design §4.9: a missed notification stays
// answerable in the Panel). It sends the action with the record's
// presentation token through notificationClicks.handle, the path a native
// click takes (ASK-N4), and reads the record back for one sentence.
func (p *servicePanel) notificationsAct(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	var choice struct {
		ID     string `json:"id"`
		Action string `json:"action"`
		Token  string `json:"token"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&choice) != nil || choice.ID == "" || len(choice.ID) > 128 || choice.Action == "" || len(choice.Action) > 64 ||
		choice.Token == "" || len(choice.Token) > 256 || !utf8.ValidString(choice.ID+choice.Action+choice.Token) {
		http.Error(w, "invalid action: name one notification, one of its actions and its token", 400)
		return
	}
	n := p.notifications
	if n.operator == nil {
		n = defaultPanelNotifications(p)
	}
	ctx, done := panelCall(r)
	defer done()
	p.logAction(ctx, "notifications.act", map[string]string{"notification.id": choice.ID, "notification.action": choice.Action})
	reply, err := n.act(ctx, choice.ID, choice.Action, choice.Token)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, reply)
}

func (n panelNotifications) act(ctx context.Context, id, action, token string) (notificationActReply, error) {
	operation := ""
	clicks := notificationClicks{
		operator: func(ctx context.Context) (notificationOperator, error) { return n.operator(ctx) },
		rules: func(ctx context.Context) (notificationRules, string, error) {
			rules, account, err := n.rules(ctx)
			return rules, account, err
		},
		activity: func(op string) error { operation = op; return nil },
		surface:  panelNoSurface{},
		budget:   10 * time.Second,
	}
	handleErr := clicks.handle(ctx, notificationClickArgs(id, action, token, false))
	operator, err := n.operator(ctx)
	if err != nil {
		return notificationActReply{}, err
	}
	read, err := operator.ReadNotificationContext(ctx, id)
	if err != nil {
		return notificationActReply{}, err
	}
	if read.Record == nil {
		return notificationActReply{Outcome: read.Outcome.String(), Message: "This notification is no longer held."}, nil
	}
	record := *read.Record
	title := action
	for _, a := range record.Request.Actions {
		if a.ID == action {
			title = a.Title
		}
	}
	reply := notificationActReply{Outcome: record.State.String()}
	switch record.State {
	case askclient.NotificationStateActed:
		chose := ""
		if record.Result != nil {
			chose = record.Result.Action
		}
		if chose != action {
			reply.Message = "This notification was already answered; nothing more was sent."
			break
		}
		// The app performs the action's effect when it next runs (ASK-N2).
		reply.Operation = operation
		reply.Message = "Chose " + title + "."
	case askclient.NotificationStateExpired:
		reply.Message = "This notification expired; nothing was sent."
	case askclient.NotificationStatePosted:
		if handleErr != nil {
			return notificationActReply{}, handleErr
		}
		reply.Message = "Nothing was sent; the notification changed. Try again."
	default:
		reply.Message = "This notification has ended; nothing was sent."
	}
	return reply, nil
}

// setOn turns one program's notifications on or off (ASK-N6).
func (n panelNotifications) setOn(ctx context.Context, program string, on bool) error {
	rules, account, err := n.rules(ctx)
	if err != nil {
		return err
	}
	operator, err := n.operator(ctx)
	if err != nil {
		return err
	}
	if !on {
		clicks := notificationClicks{rules: func(context.Context) (notificationRules, string, error) { return rules, account, nil }}
		return clicks.turnOff(ctx, operator, program)
	}
	subject := rightsclient.Subject{Account: account, Program: program}
	for attempt := 0; attempt < 3; attempt++ {
		read, err := rules.ReadRuleContext(ctx, subject, notificationPostAction, notificationPostResource)
		if err != nil {
			return err
		}
		switch read.Outcome {
		case rightsclient.RuleReadOutcomeUnknown:
			return nil
		case rightsclient.RuleReadOutcomeFound, rightsclient.RuleReadOutcomeExpired:
		default:
			return fmt.Errorf("the permission service answered %s", read.Outcome)
		}
		if read.Record != nil && read.Record.Rule.Permit {
			return nil
		}
		edit, err := rules.RevokeRuleContext(ctx, read.Revision, subject, notificationPostAction, notificationPostResource)
		if err != nil {
			return err
		}
		switch edit.Outcome {
		case rightsclient.PolicyEditOutcomeApplied:
			return nil
		case rightsclient.PolicyEditOutcomeConflict:
			continue
		default:
			return fmt.Errorf("the permission service answered %s", edit.Outcome)
		}
	}
	return errors.New("the permissions kept changing while turning notifications on")
}
