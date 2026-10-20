package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The Activity timeline's installer requests: what an installer's or
// uninstaller's run did without asking (application_native.go), such as an
// app removed by its uninstaller or one refused for a reserved title, and
// the apps the person's Set up answer kept, with Undo (setup_step_keep.go).
// The Panel owns this record; it keeps the latest applicationActivityLimit
// lines in one small file.

// applicationActivity is one line: the app's title and program, what its
// installer asked and what happened.
type applicationActivity struct {
	At      int64  `json:"at"`
	App     string `json:"app"`
	Program string `json:"program,omitempty"`
	What    string `json:"what"`
	Outcome string `json:"outcome"`
	// Undo names the apps Setup kept that the line's Undo asks about again;
	// empty once undone, and for every other line.
	Undo []string `json:"undo,omitempty"`
	// UndoRemoves makes the line's Undo remove the registration of each app
	// it names: the line of a Keep the person's Connect click made.
	UndoRemoves bool `json:"undo_removes,omitempty"`
	// UndoRestores makes the line's Undo install the integration an update
	// replaced and re-pin the app to that integration.
	UndoRestores bool `json:"undo_restores,omitempty"`
}

// applicationActivityLog is the Panel's record of installer requests.
type applicationActivityLog struct {
	// path is the record's file; "" is this account's
	// (<setup home>/application-activity.json).
	path string
	// now stamps a line; nil is time.Now.
	now func() time.Time
}

const (
	applicationActivityLimit = 64
	applicationActivityBytes = 64 << 10
)

func (l *applicationActivityLog) file() (string, error) {
	if l.path != "" {
		return l.path, nil
	}
	home, err := setup.CurrentHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home.Config, "application-activity.json"), nil
}

func (l *applicationActivityLog) timestamp() int64 {
	if l.now != nil {
		return l.now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

// add appends one line, keeping the latest applicationActivityLimit. A
// record that cannot be written is logged; the request's outcome stands.
func (l *applicationActivityLog) add(line applicationActivity) {
	if line.At == 0 {
		line.At = l.timestamp()
	}
	path, err := l.file()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		err = cas.ChangeLimit(path, applicationActivityBytes, func(current []byte) ([]byte, error) {
			var lines []applicationActivity
			if json.Unmarshal(current, &lines) != nil {
				lines = nil
			}
			lines = append(lines, line)
			for len(lines) > applicationActivityLimit {
				lines = lines[1:]
			}
			raw, err := json.Marshal(lines)
			for err == nil && len(raw) > applicationActivityBytes && len(lines) > 1 {
				lines = lines[1:]
				raw, err = json.Marshal(lines)
			}
			return raw, err
		})
	}
	if err != nil {
		log.Printf("apps: the Activity line could not be recorded: %v", err)
	}
}

// find is the line recorded at at.
func (l *applicationActivityLog) find(at int64) (applicationActivity, bool) {
	for _, line := range l.lines() {
		if line.At == at {
			return line, true
		}
	}
	return applicationActivity{}, false
}

// clearUndo drops the Undo of the line recorded at at; a record that cannot
// be written is logged.
func (l *applicationActivityLog) clearUndo(at int64) {
	path, err := l.file()
	if err == nil {
		err = cas.ChangeLimit(path, applicationActivityBytes, func(current []byte) ([]byte, error) {
			var lines []applicationActivity
			if err := json.Unmarshal(current, &lines); err != nil {
				return nil, err
			}
			for i := range lines {
				if lines[i].At == at {
					lines[i].Undo = nil
				}
			}
			return json.Marshal(lines)
		})
	}
	if err != nil {
		log.Printf("apps: the Activity line could not be updated: %v", err)
	}
}

// lines reads the record; a missing or unreadable one lists nothing.
func (l *applicationActivityLog) lines() []applicationActivity {
	path, err := l.file()
	if err != nil {
		return nil
	}
	raw, err := cas.ReadLimit(path, applicationActivityBytes)
	if err != nil {
		return nil
	}
	var lines []applicationActivity
	if json.Unmarshal(raw, &lines) != nil {
		return nil
	}
	return lines
}

// activityAppRequests is the timeline's read of the installer requests, one
// complete page, newest first; POST is the Undo of a Setup Keep line or of a
// Connect line.
func (p *servicePanel) activityAppRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		p.undoSetupKeeps(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	out := activityPage{Outcome: "page", Events: []activityEvent{}, Complete: true}
	lines := p.appActivity.lines()
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		event := activityEvent{At: line.At, App: line.App, Program: line.Program, What: line.What, Result: line.Outcome}
		if len(line.Undo) > 0 {
			event.Undo = &activityUndo{At: line.At, Apps: line.Undo}
		}
		out.Events = append(out.Events, event)
	}
	w.Header().Set("Cache-Control", "no-store")
	panelJSON(w, out)
}
