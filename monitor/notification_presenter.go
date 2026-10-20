package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The session presenter shows application notifications natively (ASK-N7).
// It reads them through notification-operator@1, shows each posted one once
// per presentation token, removes native items whose occurrence ended, and
// sends each click back with the token it was shown with. It holds no
// durable state: the service record decides every outcome.

// notificationPollInterval is how often the presenter lists notifications.
const notificationPollInterval = 3 * time.Second

// notificationPopupWindow is how recently a notification must have been
// posted for a presenter that first sees it to pop it up; an older one goes
// to the notification center quietly.
const notificationPopupWindow = 10 * time.Minute

// notificationTurnOffWhy is the reason a person's Turn off records.
const notificationTurnOffWhy = "turned off from a notification"

const (
	notificationPostAction   = grants.NotificationPostAction
	notificationPostResource = grants.NotificationPostResource
)

// shownNotification is everything a native surface draws for one record.
type shownNotification struct {
	ID       string
	Group    string
	Token    string
	Title    string
	Body     string
	Sender   string
	Actions  []askclient.NotificationAction
	Expires  time.Time
	Silently bool
}

// notificationSurface is one platform's native notification center.
type notificationSurface interface {
	Show(n shownNotification) error
	Remove(id, group string) error
}

// notificationOperator is the part of notification-operator@1 the presenter uses.
type notificationOperator interface {
	ListAllNotificationsContext(ctx context.Context) ([]askclient.NotificationRecord, error)
	ReadNotificationContext(ctx context.Context, id string) (askclient.NotificationRead, error)
	ActContext(ctx context.Context, id, action, presentation string) (askclient.NotificationDecision, error)
	DismissContext(ctx context.Context, id string) (askclient.NotificationDecision, error)
	WithdrawProgramContext(ctx context.Context, program string) (askclient.NotificationDecision, error)
}

// notificationPresenter tracks what this process has on screen.
type notificationPresenter struct {
	surface notificationSurface
	now     func() time.Time
	mu      sync.Mutex
	// shown maps a record ID to the token and group it was shown with.
	shown map[string]shownKey
}

type shownKey struct{ token, group string }

func newNotificationPresenter(surface notificationSurface) *notificationPresenter {
	return &notificationPresenter{surface: surface, now: time.Now, shown: map[string]shownKey{}}
}

// notificationGroup groups one program's notifications natively; the group
// names no program in visible text.
func notificationGroup(program string) string {
	sum := sha256.Sum256([]byte(program))
	return hex.EncodeToString(sum[:8])
}

// senderName is the service-recorded sender in the presenter's naming rule.
func senderName(record askclient.NotificationRecord, names *programTitler) string {
	publisher := trayPublisher{Names: names, Prompt: true}
	if record.PublisherTrusted {
		publisher.Subject, publisher.Trusted = record.Publisher, true
		publisher.Description, publisher.OriginalName = record.ProgramDescription, record.ProgramOriginalName
	}
	return programDisplayName(publisher, record.Program)
}

// sync shows each posted record not yet shown with its current token, and
// removes the native item of each record that ended or left the list. A
// locked session shows nothing new.
func (p *notificationPresenter) sync(records []askclient.NotificationRecord, unlocked bool, names *programTitler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	live := map[string]bool{}
	for _, record := range records {
		if record.State != askclient.NotificationStatePosted {
			continue
		}
		live[record.ID] = true
		group := notificationGroup(record.Program)
		previous, seen := p.shown[record.ID]
		if seen && previous.token == record.Presentation || !unlocked {
			continue
		}
		posted := time.UnixMilli(record.PostedMs)
		n := shownNotification{ID: record.ID, Group: group, Token: record.Presentation, Title: record.Request.Title, Body: record.Request.Body,
			Sender: senderName(record, names), Actions: record.Request.Actions, Expires: time.UnixMilli(record.Request.ExpiresMs),
			// A replacement after a runtime restart, or an old notification
			// this presenter first sees, goes to the center quietly.
			Silently: seen || p.now().Sub(posted) > notificationPopupWindow}
		if err := p.surface.Show(n); err != nil {
			log.Printf("notification %s: %v", record.ID, err)
			continue
		}
		p.shown[record.ID] = shownKey{token: record.Presentation, group: group}
	}
	for id, key := range p.shown {
		if live[id] {
			continue
		}
		if err := p.surface.Remove(id, key.group); err != nil {
			log.Printf("notification %s removal: %v", id, err)
		}
		delete(p.shown, id)
	}
}

// Click arguments carry the record ID, the chosen action and the token. A
// body click names no action; Turn off is its own flag.
const notificationClickPrefix = "oa-notification:"

func notificationClickArgs(id, action, token string, turnOff bool) string {
	v := url.Values{"id": {id}, "t": {token}}
	if action != "" {
		v.Set("a", action)
	}
	if turnOff {
		v.Set("off", "1")
	}
	return notificationClickPrefix + v.Encode()
}

type notificationClick struct {
	ID, Action, Token string
	TurnOff           bool
}

func parseNotificationClick(args string) (notificationClick, bool) {
	raw, ok := strings.CutPrefix(args, notificationClickPrefix)
	if !ok || len(raw) > 1024 {
		return notificationClick{}, false
	}
	v, err := url.ParseQuery(raw)
	if err != nil || v.Get("id") == "" {
		return notificationClick{}, false
	}
	return notificationClick{ID: v.Get("id"), Action: v.Get("a"), Token: v.Get("t"), TurnOff: v.Get("off") == "1"}, true
}

// notificationRules is the part of the Rights operator Turn off uses.
type notificationRules interface {
	ListPolicyContext(ctx context.Context, cursor string, limit int64) (rightsclient.PolicyPage, error)
	SetRuleForContext(ctx context.Context, expected string, rule rightsclient.PolicyRule, ttl time.Duration, why string) (rightsclient.PolicyEdit, error)
}

// notificationClicks carries out clicks against the service.
type notificationClicks struct {
	operator func(context.Context) (notificationOperator, error)
	rules    func(context.Context) (notificationRules, string, error)
	activity func(operationID string) error
	surface  notificationSurface
	budget   time.Duration
}

// handle carries out one click (ASK-N4). A body click acts on the
// notification's Open Activity action when it offers one and otherwise only
// closes the item. Turn off writes the person's block, then withdraws the
// program's live notifications.
func (c notificationClicks) handle(ctx context.Context, args string) error {
	click, ok := parseNotificationClick(args)
	if !ok {
		return errors.New("a notification click with unreadable arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, c.budget)
	defer cancel()
	operator, err := c.operator(ctx)
	if err != nil {
		return err
	}
	read, err := operator.ReadNotificationContext(ctx, click.ID)
	if err != nil {
		return err
	}
	if read.Record == nil {
		return fmt.Errorf("notification %s is %s", click.ID, read.Outcome)
	}
	record := *read.Record
	group := notificationGroup(record.Program)
	if click.TurnOff {
		// Turn off writes the person's rule, so it needs the token this
		// record was shown with, as Act does (ASK-N4). A stale item or
		// arguments another process made up record nothing; the item goes.
		if record.Presentation == "" || !hmac.Equal([]byte(click.Token), []byte(record.Presentation)) {
			return c.surface.Remove(record.ID, group)
		}
		if err := c.turnOff(ctx, operator, record.Program); err != nil {
			return err
		}
		return c.surface.Remove(record.ID, group)
	}
	action := click.Action
	if action == "" {
		for _, a := range record.Request.Actions {
			if a.Activation == askclient.NotificationActivationOpenActivity {
				action = a.ID
				break
			}
		}
		if action == "" {
			return nil
		}
	}
	decision, err := operator.ActContext(ctx, click.ID, action, click.Token)
	if err != nil {
		return err
	}
	switch decision.Outcome {
	case askclient.NotificationDecisionActed:
		if decision.Record != nil && decision.Record.Result != nil && decision.Record.Result.Action == action {
			for _, a := range record.Request.Actions {
				if a.ID == action && a.Activation == askclient.NotificationActivationOpenActivity && c.activity != nil {
					return c.activity(record.Request.OperationID)
				}
			}
		}
		return nil
	case askclient.NotificationDecisionExpired, askclient.NotificationDecisionGone, askclient.NotificationDecisionUnknown:
		return c.surface.Remove(click.ID, group)
	}
	return fmt.Errorf("notification %s action %s: %s", click.ID, action, decision.Outcome)
}

// turnOff writes the person's block on program's notifications at the
// current policy revision, retrying only when another edit moved it first.
func (c notificationClicks) turnOff(ctx context.Context, operator notificationOperator, program string) error {
	rules, account, err := c.rules(ctx)
	if err != nil {
		return err
	}
	rule := rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: account, Program: program}, Action: notificationPostAction, Resource: notificationPostResource, Permit: false}
	applied := false
	for attempt := 0; attempt < 3 && !applied; attempt++ {
		page, err := rules.ListPolicyContext(ctx, "", 1)
		if err != nil {
			return err
		}
		if page.Outcome != rightsclient.PolicyPageOutcomePage {
			return fmt.Errorf("the permission list is %s", page.Outcome)
		}
		edit, err := rules.SetRuleForContext(ctx, page.Revision, rule, 0, notificationTurnOffWhy)
		if err != nil {
			return err
		}
		switch edit.Outcome {
		case rightsclient.PolicyEditOutcomeApplied:
			applied = true
		case rightsclient.PolicyEditOutcomeConflict:
		default:
			return fmt.Errorf("the permission service answered %s", edit.Outcome)
		}
	}
	if !applied {
		return errors.New("the permissions kept changing while turning notifications off")
	}
	_, err = operator.WithdrawProgramContext(ctx, program)
	return err
}

// panelNotificationClicks resolves the runtime's services for each click.
func panelNotificationClicks(surface notificationSurface) notificationClicks {
	return notificationClicks{
		operator: func(ctx context.Context) (notificationOperator, error) {
			return panelMachine().ResolveAsksNotificationOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		},
		rules: func(ctx context.Context) (notificationRules, string, error) {
			_, subject, err := panelApplicationReads{}.resolve(ctx)
			if err != nil {
				return nil, "", err
			}
			operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
			return operator, subject.Account, err
		},
		activity: launchActivityPanel,
		surface:  surface,
		budget:   10 * time.Second,
	}
}

// pollNotifications keeps the native surface in step with the service until
// ctx ends. unlocked reports the person's unlocked interactive session.
func pollNotifications(ctx context.Context, presenter *notificationPresenter, unlocked func() bool) {
	ticker := time.NewTicker(notificationPollInterval)
	defer ticker.Stop()
	degraded := false
	for {
		listCtx, cancel := context.WithTimeout(ctx, trayPollBudget)
		operator, err := panelMachine().ResolveAsksNotificationOperator(listCtx, facade.Requirements{Scope: facade.ScopeLocal})
		var records []askclient.NotificationRecord
		if err == nil {
			records, err = operator.ListAllNotificationsContext(listCtx)
		}
		cancel()
		if (err != nil) != degraded {
			degraded = err != nil
			if degraded {
				log.Printf("notifications: runtime unavailable; continuing to poll: %v", err)
			} else {
				log.Print("notifications: runtime available")
			}
		}
		if err == nil {
			presenter.sync(records, unlocked(), machineProgramTitler(ctx))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// toastText escapes text for a toast document.
func toastText(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return ""
	}
	return b.String()
}

// toastAttr escapes an attribute value.
func toastAttr(s string) string {
	return strings.NewReplacer(`"`, "&quot;").Replace(toastText(s))
}

// toastDocument is the ToastGeneric document for one notification: the
// app's title and body, the service-recorded sender in the attribution line
// the app cannot write (ASK-N2), its actions, and OA's Turn off in the
// context menu.
func toastDocument(n shownNotification) string {
	doc := `<toast launch="` + toastAttr(notificationClickArgs(n.ID, "", n.Token, false)) + `" activationType="foreground"><visual><binding template="ToastGeneric">` +
		`<text hint-maxLines="1">` + toastText(n.Title) + `</text>`
	if n.Body != "" {
		doc += `<text>` + toastText(n.Body) + `</text>`
	}
	doc += `<text placement="attribution">` + toastText("From "+n.Sender) + `</text></binding></visual><actions>`
	for _, a := range n.Actions {
		doc += `<action content="` + toastAttr(a.Title) + `" arguments="` + toastAttr(notificationClickArgs(n.ID, a.ID, n.Token, false)) + `" activationType="foreground"/>`
	}
	return doc + `<action content="` + toastAttr("Turn off notifications from "+n.Sender) + `" arguments="` +
		toastAttr(notificationClickArgs(n.ID, "", n.Token, true)) + `" placement="contextMenu" activationType="foreground"/></actions></toast>`
}
