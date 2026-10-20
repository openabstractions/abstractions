//go:build windows

package main

import (
	"context"
	"log"

	"github.com/openabstractions/abstractions/monitor/win"
	"golang.org/x/sys/windows"
)

// The Windows identity of OA-held notifications. The installer writes both on
// the Start-menu Panel shortcut, and registers the activator class's
// LocalServer32 as this program with --background (installer/abstraction.wxs).
// Windows lists every OA notification under the Panel; the posting program is
// named inside each one (ASK-N2).
const notificationAppID = "OpenAbstractions.Panel"

// notificationActivator is the CLSID of the INotificationActivationCallback
// class the background presenter registers: {cc32c67f-0e49-4cc5-a212-c4cb329cd06c}.
var notificationActivator = windows.GUID{Data1: 0xcc32c67f, Data2: 0x0e49, Data3: 0x4cc5, Data4: [8]byte{0xa2, 0x12, 0xc4, 0xcb, 0x32, 0x9c, 0xd0, 0x6c}}

// toastSurface draws notifications as Windows toasts.
type toastSurface struct{ center *win.ToastCenter }

func (s toastSurface) Show(n shownNotification) error {
	return s.center.Show(win.Toast{XML: toastDocument(n), Tag: n.ID, Group: n.Group, Expires: n.Expires, Silently: n.Silently})
}

func (s toastSurface) Remove(id, group string) error { return s.center.Remove(id, group) }

// startNotifications registers the activator, then follows the service's
// notifications until ctx ends. A person's click reaches the service through
// the activator even when Windows started this process for it. A close the
// person makes while this process holds the toast is recorded as a dismissal.
func startNotifications(ctx context.Context, unlocked func() bool) (stop func(), err error) {
	// Windows may deliver the click that started this process as soon as the
	// activator is registered; it waits until the click handler exists.
	ready := make(chan struct{})
	var clicks notificationClicks
	center, err := win.OpenToasts(notificationAppID, &notificationActivator, func(args string) {
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
		if err := clicks.handle(ctx, args); err != nil {
			log.Printf("notification click: %v", err)
		}
	})
	if err != nil {
		return nil, err
	}
	surface := toastSurface{center: center}
	clicks = panelNotificationClicks(surface)
	close(ready)
	center.OnDismissed = func(id string) {
		dismissCtx, cancel := context.WithTimeout(ctx, trayAnswerBudget)
		defer cancel()
		operator, err := clicks.operator(dismissCtx)
		if err == nil {
			_, err = operator.DismissContext(dismissCtx, id)
		}
		if err != nil {
			log.Printf("notification %s close: %v", id, err)
		}
	}
	if enabled, err := center.Enabled(); err == nil && !enabled {
		log.Print("notifications: Windows notifications are off for the OpenAbstractions Panel in Settings")
	}
	presenter := newNotificationPresenter(surface)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pollNotifications(ctx, presenter, unlocked)
	}()
	return func() { <-done; center.Close() }, nil
}
