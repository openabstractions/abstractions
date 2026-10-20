package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The person's Set up answer keeps the self-registered apps Setup's
// question named (VISION 2026-10-31): the runtime reads the answered
// occurrence and records each Keep with provenance setup
// (serve/setup_keep.go). Setup reports Ready only once those Keeps are
// written, so its Confirm waits for each named app to read as kept, and adds
// one Activity line with Undo (CONV-U1). Undo asks the runtime about each app
// again (AskConfirmation): an app Setup kept returns to unconfirmed, and the
// person is asked whether to keep it. An app the question did not name keeps
// waiting for the person's click in Apps.

// setupKeepWait bounds the wait for the runtime's Keeps.
var setupKeepWait = 30 * time.Second

// setupKeepPoll is the interval between reads of the directory.
var setupKeepPoll = 200 * time.Millisecond

// confirmSetupKeeps waits for the runtime to keep the self-registered apps
// among pending, and tells the Keeps in Activity.
func (p *servicePanel) confirmSetupKeeps(ctx context.Context, pending []setup.StepApp) error {
	var named []setup.StepApp
	for _, app := range pending {
		if app.Unconfirmed && app.Manifest == "" {
			named = append(named, app)
		}
	}
	if len(named) == 0 {
		return nil
	}
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return err
	}
	kept, err := awaitSetupKeeps(ctx, apps, named)
	if err != nil {
		return err
	}
	p.tellSetupKeeps(kept)
	return nil
}

// awaitSetupKeeps reads the directory until no named app waits unconfirmed,
// and returns the ones kept. An app removed or registered by another program
// meanwhile is no longer the one the question named, and is left out.
func awaitSetupKeeps(ctx context.Context, apps setupApplications, named []setup.StepApp) ([]setup.StepApp, error) {
	deadline := time.Now().Add(setupKeepWait)
	for {
		page, err := apps.Observe(ctx, "", 0)
		if err == nil && (page.Outcome == fwire.ApplicationOutcomePage || page.Outcome == fwire.ApplicationOutcomeStale) {
			var kept []setup.StepApp
			waiting := false
			for _, app := range named {
				i := slices.IndexFunc(page.Applications, func(e fwire.ApplicationEntry) bool { return e.Descriptor.Name == app.Name })
				switch {
				case i < 0 || !identity.SameSubjectProgram(page.Applications[i].Descriptor.Program, app.Program):
				case page.Applications[i].SelfRegistered:
					waiting = true
				default:
					kept = append(kept, app)
				}
			}
			if !waiting {
				return kept, nil
			}
		}
		if !time.Now().Before(deadline) {
			return nil, errors.New("OpenAbstractions did not keep the apps you set up")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(setupKeepPoll):
		}
	}
}

// tellSetupKeeps adds the one Activity line for the apps Setup kept, with
// Undo.
func (p *servicePanel) tellSetupKeeps(kept []setup.StepApp) {
	if len(kept) == 0 {
		return
	}
	titles, names := make([]string, 0, len(kept)), make([]string, 0, len(kept))
	for _, app := range kept {
		titles, names = append(titles, app.Title), append(names, app.Name)
	}
	line := applicationActivity{App: joinTitles(titles), What: "Setup kept them when you chose Set up", Outcome: "Kept", Undo: names}
	if len(kept) == 1 {
		line.Program, line.What = kept[0].Program, "Setup kept it when you chose Set up"
	}
	p.appActivity.add(line)
}

// joinTitles words titles as one list: "A", "A and B", "A, B and C".
func joinTitles(titles []string) string {
	if len(titles) < 2 {
		return strings.Join(titles, "")
	}
	return strings.Join(titles[:len(titles)-1], ", ") + " and " + titles[len(titles)-1]
}

// setupKeepUndo is the body of the Undo of one Setup Keep line.
type setupKeepUndo struct {
	At   int64    `json:"at"`
	Apps []string `json:"apps"`
}

// packageUpdateUndo binds one declared registration to its durable receipt
// and the owner that can restore it under the native maintenance claim.
type packageUpdateUndo struct {
	registration string
	read         func() setup.PackageMaintenanceRecord
	restore      func(http.ResponseWriter, context.Context, int64, applicationActivity)
}

func (u packageUpdateUndo) owns(apps []string) bool {
	return len(apps) == 1 && apps[0] == u.registration
}

// undoSetupKeeps answers POST /activity/app-requests: the Undo of the line
// at At, which names the apps Setup kept. The runtime returns each app Setup
// kept to unconfirmed and asks the person about it; the line keeps no Undo.
func (p *servicePanel) undoSetupKeeps(w http.ResponseWriter, r *http.Request) {
	var undo setupKeepUndo
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&undo); err != nil || undo.At == 0 || len(undo.Apps) == 0 {
		http.Error(w, "choose one Setup line to undo", http.StatusBadRequest)
		return
	}
	line, ok := p.appActivity.find(undo.At)
	ctx, cancel := panelCall(r)
	defer cancel()
	if !ok {
		owner := p.packageUpdateUndo()
		if owner.owns(undo.Apps) {
			if pending := owner.read().Undo; pending != nil && pending.At == undo.At {
				// Activity aging does not revoke an already recorded Undo.
				owner.restore(w, ctx, undo.At, applicationActivity{})
				return
			}
		}
	}
	if !ok || !slices.Equal(line.Undo, undo.Apps) {
		http.Error(w, "This Setup line can no longer be undone.", http.StatusConflict)
		return
	}
	if line.UndoRemoves {
		p.undoConnectKeep(w, ctx, undo.At, line)
		return
	}
	if line.UndoRestores {
		owner := p.packageUpdateUndo()
		if !owner.owns(line.Undo) {
			http.Error(w, "This Setup line can no longer be undone.", http.StatusConflict)
			return
		}
		owner.restore(w, ctx, undo.At, line)
		return
	}
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	asking, ok := apps.(confirmingApplications)
	if !ok {
		http.Error(w, "this OpenAbstractions cannot ask about an app that added itself", http.StatusConflict)
		return
	}
	for _, name := range line.Undo {
		change, err := asking.AskConfirmation(ctx, name)
		if err != nil {
			panelError(w, err)
			return
		}
		if change.Outcome != fwire.ApplicationOutcomeApplied && change.Outcome != fwire.ApplicationOutcomeUnknown {
			http.Error(w, "OpenAbstractions could not undo this. Try again.", http.StatusConflict)
			return
		}
	}
	p.appActivity.clearUndo(undo.At)
	p.logAction(ctx, "setup.keep_undo", map[string]string{"application.names": strings.Join(line.Undo, ",")})
	panelJSON(w, struct {
		Message string `json:"message"`
	}{"Undone: OpenAbstractions asks whether to keep " + line.App + "."})
}

// undoConnectKeep is the Undo of the line a Connect click's Keep added: it
// removes the registration of each app the line names, when the entry still
// names the line's program. An entry already gone counts as undone.
func (p *servicePanel) undoConnectKeep(w http.ResponseWriter, ctx context.Context, at int64, line applicationActivity) {
	for _, name := range line.Undo {
		apps, err := p.nativeApplicationsForPanel(ctx, name)
		if err != nil {
			panelError(w, err)
			return
		}
		entry, readable := appPageEntry(ctx, apps, name)
		if !readable {
			http.Error(w, "OpenAbstractions could not undo this. Try again.", http.StatusConflict)
			return
		}
		if entry == nil {
			continue
		}
		if line.Program != "" && !identity.SameSubjectProgram(entry.Descriptor.Program, line.Program) {
			http.Error(w, line.App+" was registered again by another program; remove it in Apps.", http.StatusConflict)
			return
		}
		change, err := apps.Remove(ctx, name)
		if err != nil {
			panelError(w, err)
			return
		}
		if change.Outcome != fwire.ApplicationOutcomeApplied && change.Outcome != fwire.ApplicationOutcomeUnknown {
			http.Error(w, "OpenAbstractions could not undo this. Try again.", http.StatusConflict)
			return
		}
	}
	p.appActivity.clearUndo(at)
	p.logAction(ctx, "application.connect_undo", map[string]string{"application.names": strings.Join(line.Undo, ",")})
	panelJSON(w, struct {
		Message string `json:"message"`
	}{"Undone: " + line.App + " was removed from OpenAbstractions."})
}
