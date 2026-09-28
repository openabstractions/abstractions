//go:build windows

package main

import (
	"context"
	"time"

	"github.com/openabstractions/abstractions/monitor/win"
)

// runTray serves the Panel exactly as the browser front end does — the same
// bound listener, key and URL — but shows a notification-area icon instead
// of opening a browser tab or drawing the inventory window. It is meant to
// run at logon (installer/README.md, "Notifications" in monitor/README.md).
func runTray(address string) error {
	_, server, listener, url, err := bindPanel(address)
	if err != nil {
		return err
	}
	defer server.Close()
	go server.Serve(listener)

	// The Questions section already has this id (monitor/service_page.go);
	// the browser's own fragment navigation opens straight to it.
	questionsURL := url + "#questions"
	openQuestions := func() { launch(questionsURL) }

	lifetime, stop := context.WithCancel(context.Background())
	defer stop()

	return win.RunHidden(trayDisplayName, func(w *win.Window) {
		var tray *win.Tray
		newTray, err := win.NewTray(w, trayDisplayName, openQuestions, []win.MenuItem{
			// Runs on its own goroutine and locked OS thread (see
			// openNativeWindow in desktop_windows.go), so opening the Panel
			// window does not block the tray's own message pump.
			{Text: "Open Panel", Do: func() { go openNativeWindow(url) }},
			{Text: "Questions", Do: openQuestions},
			{Text: "Quit", Do: func() {
				if tray != nil {
					tray.Close()
				}
				w.Close()
			}},
		})
		if err != nil {
			fail(true, err)
			return
		}
		tray = newTray
		go pollTrayLoop(lifetime, w, tray)
	})
}

// pollTrayLoop polls the question book every trayPollInterval until ctx ends,
// marshalling every Win32 call back onto w's owning thread through w.Do. A
// runtime that cannot be reached changes the tooltip once and goes back to
// polling silently; it never shows a notification for its own absence.
func pollTrayLoop(ctx context.Context, w *win.Window, tray *win.Tray) {
	announcer := newTrayAnnouncer()
	degraded := false
	ticker := time.NewTicker(trayPollInterval)
	defer ticker.Stop()
	for {
		fresh, unavailable := pollTray(ctx, panelMachine(), announcer)
		w.Do(func() {
			if unavailable != degraded {
				degraded = unavailable
				if degraded {
					tray.SetTooltip(trayDisplayName + " — runtime unavailable")
				} else {
					tray.SetTooltip(trayDisplayName)
				}
			}
			for _, record := range fresh {
				title, body := trayNotificationText(record)
				tray.Notify(title, body)
			}
		})
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
