//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/openabstractions/abstractions/monitor/win"
)

func windowed() bool { return win.Windowed() }

// fail says what went wrong somewhere a person will see it. A binary built for
// the desktop has no stderr at all, so the log line this program would print
// goes nowhere and the window simply never appears.
func fail(native bool, err error) {
	if !native {
		log.Fatal(err)
	}
	win.Say("Abstraction — control panel", err.Error())
	os.Exit(1)
}

// The service launcher draws a native inventory window. Typed forms and
// recovery records live in the browser UI.
func (p *servicePanel) desktop(url string) error {
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	return win.Run("OpenAbstractions services", 780, 440, func(ui *win.Window) {
		title := ui.Head("Service-owned work for this panel")
		status := ui.Dim("Refresh to query the runtime. No provider files are opened.")
		rows := ui.List(win.Column{Title: "Operation", Width: 330}, win.Column{Title: "State", Width: 110}, win.Column{Title: "Progress", Width: 200})
		busy := false
		refresh := ui.Button("Refresh", func() {
			if busy {
				return
			}
			busy = true
			go func() {
				ctx, cancel := context.WithTimeout(lifetime, 5*time.Second)
				defer cancel()
				_, inventory, _, err := p.binding(ctx)
				var values [][]string
				message := ""
				if err == nil {
					page, e := inventory.ListWork(ctx, "", 32)
					err = e
					if err == nil {
						message = "Inventory: " + page.Outcome
						if !page.Complete {
							message += "; more pages available in full controls"
						}
						for _, s := range page.Snapshots {
							values = append(values, []string{s.Receipt.OperationId, s.State, fmt.Sprintf("%d / %d", s.Progress.Done, s.Progress.Total)})
						}
					}
				}
				if err != nil {
					message = "Unavailable: " + err.Error()
				}
				ui.Do(func() { busy = false; rows.Set(values); status.SetText(message) })
			}()
		})
		controls := ui.Button("Open full controls", func() { launch(url) })
		ui.OnSize(func(w, h int) {
			title.Place(14, 10, w-28, 28)
			refresh.Place(14, 46, 120, 30)
			controls.Place(150, 46, 200, 30)
			rows.Place(14, 90, w-28, h-142)
			status.Place(14, h-42, w-28, 30)
		})
	})
}
