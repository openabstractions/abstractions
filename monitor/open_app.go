package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// openRegisteredApplication sends one open to a registered application
// through the runtime's owned launch: the request the app page's Open sends,
// which starts the application or joins its running instance. While the
// application starts, the person sees an "Opening …" notice; it is withdrawn
// once the application answers or the open fails.
func openRegisteredApplication(ctx context.Context, apps setupApplications, name string) error {
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return err
	}
	entry, found, err := findApplicationEntry(page, name)
	if err != nil {
		return err
	} else if !found {
		return fmt.Errorf("%s is not in Apps", name)
	}
	title := entry.Title
	if title == "" {
		title = name
	}
	done := openingNotice(ctx, title)
	defer done()
	result, err := apps.Activate(ctx, name)
	if err != nil {
		return err
	}
	if result.Outcome != fwire.ApplicationActivationOutcomeReady {
		if result.Reason != "" {
			return fmt.Errorf("%s did not open (%s: %s)", name, result.Outcome.String(), result.Reason)
		}
		return fmt.Errorf("%s did not open (%s)", name, result.Outcome.String())
	}
	return nil
}

// openingNotice posts "Opening <title>…" through the notification owner and
// answers the function that withdraws it. A notice that cannot be posted is
// skipped; the open goes on. Tests replace it.
var openingNotice = func(ctx context.Context, title string) func() {
	notifications, err := panelMachine().ResolveAsksNotifications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return func() {}
	}
	now := time.Now()
	until := now.Add(3 * time.Minute).UnixMilli()
	ref := askclient.RequestRef{Key: "oa-open-app-" + strconv.FormatInt(now.UnixNano(), 36), ReplayUntilMs: until}
	q := askclient.NotificationRequest{RequestRef: ref, Title: "Opening " + title + "…", Body: "It appears in a moment.",
		Tag: "open-app", Delivery: askclient.NotificationDeliveryNow, ExpiresMs: until, Actions: []askclient.NotificationAction{}}
	if _, err := notifications.PostContext(ctx, q); err != nil {
		return func() {}
	}
	return func() {
		withdraw, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = notifications.WithdrawContext(withdraw, ref)
	}
}

func findApplicationEntry(page fwire.ApplicationPage, name string) (fwire.ApplicationDescriptor, bool, error) {
	if page.Outcome != fwire.ApplicationOutcomePage && page.Outcome != fwire.ApplicationOutcomeStale {
		return fwire.ApplicationDescriptor{}, false, fmt.Errorf("application list is %s", page.Outcome.String())
	}
	for _, entry := range page.Applications {
		if entry.Descriptor.Name == name {
			return entry.Descriptor, true, nil
		}
	}
	return fwire.ApplicationDescriptor{}, false, nil
}

// openAppFromShortcut is -open-app: a Start-menu shortcut's start of a
// registered application. It answers the Panel page to open instead: empty
// when the application opened, else the application's page, which shows its
// state, the reason and Open.
func openAppFromShortcut(name string) string {
	if !validApplicationName(name) {
		return "overview"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	apps, err := (&servicePanel{}).nativeApplicationsForPanel(ctx, name)
	if err == nil {
		err = openRegisteredApplication(ctx, apps, name)
	}
	if err == nil {
		return ""
	}
	if !errors.Is(err, context.Canceled) {
		log.Printf("open %s: %v", name, err)
	}
	return "agent:" + name
}
