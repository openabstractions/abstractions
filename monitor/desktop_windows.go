//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/openabstractions/abstractions/monitor/win"
	config "github.com/openabstractions/abstraction-config/go"
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

// desk is the delegation screen as a window on the desktop. It reads and writes
// nothing of its own: every value on it comes from window.delegation and every
// button calls the same method the page's /act does.
type desk struct {
	m  *window
	ui *win.Window

	supervisor *win.Control
	tiers      *win.List
	reason     *win.Control
	store      *win.Control
	hosts      *win.List
	share      *win.Control
	folder     *win.Control
	trial      *win.Control
	status     *win.Control

	seen      delegation
	drawn     bool
	searching bool
}

func (m *window) desktop() error {
	return win.Run("Abstraction — control panel", 780, 505, func(ui *win.Window) {
		d := &desk{m: m, ui: ui}
		d.build()
		go d.watch()
	})
}

func (d *desk) build() {
	ui := d.ui
	head := ui.Head("Where this machine's downloads go")
	d.supervisor = ui.Dim("")
	d.tiers = ui.List(
		win.Column{Title: "Tier", Width: 110},
		win.Column{Title: "Serving", Width: 70},
		win.Column{Title: "State", Width: 560},
	)
	why := ui.Dim("Reason")
	d.reason = ui.Field("")
	off := ui.Button("Switch off", func() { d.switchTier(false) })
	on := ui.Button("Switch on", func() { d.switchTier(true) })

	head2 := ui.Head("A store on the network")
	d.store = ui.Dim("")
	find := ui.Button("Find a NAS", func() { d.done("", d.m.find()) })
	forget := ui.Button("Forget it", func() { d.done("Forgotten.", d.m.forget()) })
	d.hosts = ui.List(
		win.Column{Title: "Address", Width: 130},
		win.Column{Title: "Name", Width: 150},
		win.Column{Title: "Serves files", Width: 90},
		win.Column{Title: "Shares", Width: 370},
	)
	shareLabel := ui.Dim("Share")
	d.share = ui.Field("")
	folderLabel := ui.Dim("Folder")
	d.folder = ui.Field("abstraction/store")
	check := ui.Button("Check it", func() { d.useHost(false) })
	use := ui.Button("Use this", func() { d.useHost(true) })

	head3 := ui.Head("Does any of it move bytes")
	test := ui.Button("Run a quick test", func() { d.done("Fetching.", d.m.test("")) })
	d.trial = ui.Dim("")
	d.status = ui.Dim("")

	ui.OnSize(func(cw, ch int) {
		x, w, y := 16, cw-32, 12
		head.Place(x, y, w, 24)
		y += 26
		d.supervisor.Place(x, y, w, 34)
		y += 40
		d.tiers.Place(x, y, w, 96)
		y += 104
		why.Place(x, y+4, 46, 18)
		d.reason.Place(x+48, y, w-48-232, 24)
		off.Place(x+w-226, y, 110, 24)
		on.Place(x+w-110, y, 110, 24)
		y += 36
		head2.Place(x, y, w, 24)
		y += 28
		d.store.Place(x, y+3, w-238, 20)
		find.Place(x+w-232, y, 112, 24)
		forget.Place(x+w-114, y, 114, 24)
		y += 30
		d.hosts.Place(x, y, w, 96)
		y += 104
		shareLabel.Place(x, y+4, 40, 18)
		d.share.Place(x+40, y, 140, 24)
		folderLabel.Place(x+192, y+4, 44, 18)
		d.folder.Place(x+236, y, w-236-232, 24)
		check.Place(x+w-226, y, 110, 24)
		use.Place(x+w-110, y, 110, 24)
		y += 38
		head3.Place(x, y, w, 24)
		y += 28
		test.Place(x, y, 130, 24)
		d.trial.Place(x+140, y-4, w-140, 34)
		d.status.Place(x, ch-24, w, 18)
	})
}

// watch redraws on the same signals the page's event stream uses: this
// machine's answers changing, and something the panel learned by asking. The
// only timer is set from the drawing, for the heartbeat that goes stale with
// nobody writing anything, and with nothing to wait for it is not set.
func (d *desk) watch() {
	cfg := config.Watch()
	defer cfg.Close()
	clock := time.NewTimer(time.Hour)
	defer clock.Stop()
	for {
		// Built here and drawn there: it reads a share and a store, and the
		// window's own thread is the one thing on this machine that must not
		// wait for a NAS.
		g := d.m.delegation(cfg.Current())
		d.ui.Do(func() { d.draw(g) })
		clock.Stop()
		if next := g.redrawIn(); next > 0 {
			clock.Reset(next)
		}
		select {
		case <-cfg.Changes():
			d.m.panel.stale()
		case <-d.m.panel.change:
		case <-clock.C:
		}
	}
}

func (d *desk) draw(g delegation) {
	d.seen = g
	d.supervisor.SetText(g.Supervisor)

	rows := make([][]string, 0, len(g.Tiers))
	for _, t := range g.Tiers {
		serving := ""
		if t.Serving {
			serving = "yes"
		}
		rows = append(rows, []string{t.System, serving, standing(t)})
	}
	d.tiers.Set(rows)

	if g.NASStore == "" {
		d.store.SetText("No network store. Downloads land in this machine's own store.")
	} else {
		d.store.SetText(g.NASStore)
	}

	seen := make([][]string, 0, len(g.Found))
	for _, f := range g.Found {
		serves := ""
		if f.Files {
			serves = "yes"
		}
		seen = append(seen, []string{f.Address, f.Name, serves, strings.Join(f.Shares, "  ")})
	}
	d.hosts.Set(seen)
	d.trial.SetText(outcome(g.Trial))

	switch {
	case !d.drawn:
		d.status.SetText("Answers are kept in " + g.File + ", and a change to them arrives by " + g.Told)
	case g.Searching && !d.searching:
		d.status.SetText("Asking the network who is there.")
	case d.searching && !g.Searching:
		d.status.SetText(fmt.Sprintf("%d answered. Choose one, name its share, then check it.", len(g.Found)))
	}
	d.drawn, d.searching = true, g.Searching
}

func standing(t tier) string {
	switch {
	case t.Off:
		return "switched off — " + text(t.Why, "no reason written down")
	case !t.Usable:
		return "cannot serve — " + text(t.Why, "no reason given")
	case t.Serving:
		return "this one is serving"
	}
	return "ready, and something above it is serving"
}

func outcome(t trial) string {
	switch {
	case t.Running:
		return "Fetching " + t.Source
	case t.Error != "":
		return "Failed at " + t.At + ": " + t.Error
	case t.Served != "":
		return fmt.Sprintf("%s served %d bytes in %s at %s\n%s", t.Served, t.Bytes, t.Took, t.At, t.Path)
	}
	return "Nothing tested yet. This fetches something small down the real path."
}

func (d *desk) switchTier(on bool) {
	i := d.tiers.Chosen()
	if i < 0 || i >= len(d.seen.Tiers) {
		d.status.SetText("Choose a tier in the list first.")
		return
	}
	system := d.seen.Tiers[i].System
	err := d.m.serve(system, d.reason.Text(), on)
	d.done(fmt.Sprintf("%s is %s.", system, switched(on)), err)
}

func switched(on bool) string {
	if on {
		return "back on"
	}
	return "off"
}

// useHost checks a share, and adopts it when asked. Both reach a NAS and take
// seconds, so they run off the window's thread and report back onto it.
func (d *desk) useHost(adopt bool) {
	i := d.hosts.Chosen()
	if i < 0 || i >= len(d.seen.Found) {
		d.status.SetText("Choose a host in the list first.")
		return
	}
	address, share, folder := d.seen.Found[i].Address, d.share.Text(), d.folder.Text()
	good := "That share is writable."
	if adopt {
		good = "That share is writable, and this machine now stores downloads on it."
	}
	d.status.SetText("Writing a file to " + address + " and reading it back.")
	go func() {
		err := d.m.check(address, share, folder)
		if err == nil && adopt {
			err = d.m.adopt(address, share, folder)
		}
		d.ui.Do(func() { d.done(good, err) })
	}()
}

func (d *desk) done(good string, err error) {
	if err != nil {
		d.status.SetText(err.Error())
		return
	}
	d.status.SetText(good)
}

func text(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
