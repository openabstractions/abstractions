package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
)

// An app integration records a server OpenAbstractions started for it
// (architect ruling 2026-11-12). OA keeps one file per application,
//
//	<local app data>\OpenAbstractions\started\<application>.json
//	{"v":1, "application", "title", "started_unix_ms", "port",
//	 "processes":[{"pid":N, "created":"<FILETIME>"}, ...]}
//
// The file names only the processes the integration started, the launcher
// first. While a recorded process runs
// with its recorded creation time, the app's page shows "<title> is running
// (started by OpenAbstractions for <app>)" with Stop. The app that asked
// comes from the runtime's own log: one applications.owned.start line per
// owned host the runtime started, naming the opening program and the host's
// PID and creation time (serve/application_owned_start_record.go). The
// launcher's parent is that host: the line whose host PID is the launcher's
// parent and whose host was created before the launcher names the app. Stop writes
// <application>.stop naming the launcher and each target's PID and creation
// time. The integration
// ends the server once its current work is done. After appStopGrace the
// Panel stops each recorded process that still runs, by PID and creation
// time, then removes both files. The Panel is the only stopper an agent
// cannot reach: no agent tool writes the request.

// appStopGrace bounds the wait for the current prompt; tests shorten it.
var appStopGrace = 5 * time.Minute

type appStartedProcess = supervision.StartProcess
type appStartedRecord = supervision.StartRecord

// appStarted is the facts' part: the page's line.
type appStarted struct {
	Line       string `json:"line"`
	Occurrence string `json:"occurrence"`
}

// appStartedFolder is OA's started folder; tests replace it.
var appStartedFolder = func() (string, error) {
	local, _, err := accountFolders()
	if err != nil {
		return "", err
	}
	return filepath.Join(local, "OpenAbstractions", "started"), nil
}

// readAppStarted is name's record while one of its processes runs.
func readAppStarted(name string) (appStartedRecord, string, bool) {
	folder, err := appStartedFolder()
	if err != nil {
		return appStartedRecord{}, "", false
	}
	r, ok := supervision.ReadStartRecord(filepath.Join(folder, name+".json"), name)
	if !ok {
		return appStartedRecord{}, folder, false
	}
	for _, p := range r.Processes {
		if appProcessRuns(p) {
			return r, folder, true
		}
	}
	// OA repairs its own records: one whose processes all ended goes.
	pruneAppStart(context.Background(), folder, name, r)
	return appStartedRecord{}, folder, false
}

// appStartedLine is the page's sentence about a running server; opener is
// the title of the app that asked, "" for the person's own Open or when the
// runtime's record is unreadable.
func appStartedLine(title string, r appStartedRecord, opener string) string {
	if r.Title != "" {
		title = r.Title
	}
	by := "OpenAbstractions"
	if opener != "" {
		by += " for " + opener
	}
	return title + " is running (started by " + by + ")"
}

// ownedStartEvent is the runtime's log line of an owned host start.
const ownedStartEvent = "applications.owned.start"

// appStartedPages bounds the runtime log pages one join reads.
const appStartedPages = 8

// appStartedOpeners keeps each launcher's join, so the page's reread every
// ten seconds reads the runtime's log once per server.
var appStartedOpeners sync.Map

// appStartedOpener is the program whose open started the host that started
// record's launcher, "" when the runtime's log names none. A log that cannot
// be read degrades to "" with one log line.
func appStartedOpener(ctx context.Context, name string, record appStartedRecord) string {
	launcher := record.Processes[0]
	key := name + "\x00" + strconv.Itoa(launcher.PID) + "\x00" + launcher.Created
	if known, ok := appStartedOpeners.Load(key); ok {
		return known.(string)
	}
	parent, ok := appProcessParent(launcher.PID)
	born, err := strconv.ParseUint(launcher.Created, 10, 64)
	if !ok || err != nil {
		return ""
	}
	reader, target, err := messageActivityHistory(ctx)
	if err != nil {
		log.Printf("app page: who opened %s is unknown: %v", name, err)
		return ""
	}
	opener, latest, cursor := "", uint64(0), ""
	for range appStartedPages {
		page, err := reader.ReadContext(ctx, target, cursor, 64, 65536)
		if err != nil || page.Outcome != logwire.HistoryPageOutcomePage {
			log.Printf("app page: who opened %s is unknown: the runtime's log is unreadable", name)
			return opener
		}
		for _, line := range page.Records {
			a := line.Attrs
			created, err := strconv.ParseUint(a["host_created"], 10, 64)
			if a["oa_event"] != ownedStartEvent || a["application"] != name || a["host_pid"] != strconv.Itoa(parent) || err != nil || created > born || created < latest {
				continue
			}
			opener, latest = a["opener"], created
		}
		if page.AtEnd || page.Next == "" || page.Next == cursor {
			break
		}
		cursor = page.Next
	}
	if latest != 0 {
		appStartedOpeners.Store(key, opener)
	}
	return opener
}

// appStop is the Stop of a server OpenAbstractions started for name.
func (p *servicePanel) appStop(w http.ResponseWriter, r *http.Request, name string) {
	body, ok := decodeAppAction(w, r)
	if !ok {
		return
	}
	record, folder, ok := readAppStarted(name)
	if !ok {
		panelJSON(w, appActionReply{Outcome: "applied", Message: "It has already stopped."})
		return
	}
	title := record.Title
	if title == "" {
		title = name
	}
	if body.StartOccurrence != record.Occurrence() {
		panelJSON(w, appActionReply{Outcome: "stale", Message: "The running instance changed. Review it before stopping it."})
		return
	}
	record, err := supervision.RequestStartStop(r.Context(), filepath.Join(folder, name+".json"),
		filepath.Join(folder, name+".stop"), record, appStartedProcess{}, time.Now())
	if errors.Is(err, supervision.ErrStartChanged) {
		panelJSON(w, appActionReply{Outcome: "stale", Message: "The running instance changed. Review it before stopping it."})
		return
	}
	if err != nil {
		panelError(w, errors.New(title+" could not be asked to stop: "+err.Error()))
		return
	}
	p.logAction(r.Context(), "application.stop", map[string]string{"application.name": name})
	go appFinishStop(context.Background(), folder, name, record)
	panelJSON(w, appActionReply{Outcome: "applied", Message: title + " stops once its current prompt ends."})
}

// appFinishStop waits up to appStopGrace for the recorded processes to end,
// stops those still running by PID, and removes the record.
func appFinishStop(ctx context.Context, folder, name string, record appStartedRecord) {
	deadline := time.Now().Add(appStopGrace)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		running := false
		for _, process := range record.Processes {
			running = running || appProcessRuns(process)
		}
		if !running {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	path, stop := filepath.Join(folder, name+".json"), filepath.Join(folder, name+".stop")
	err := supervision.ForceStopStartRecord(ctx, path, stop, record, func(processes []appStartedProcess) {
		for _, process := range processes {
			if appProcessRuns(process) {
				if err := appStopProcess(process); err != nil {
					log.Printf("app page: %s's process %d did not stop: %v", name, process.PID, err)
				}
			}
		}
	})
	if err != nil && !errors.Is(err, supervision.ErrStartChanged) {
		log.Printf("app page: %s's stop could not be confirmed: %v", name, err)
	}
	pruneAppStart(ctx, folder, name, record)
}

func pruneAppStart(ctx context.Context, folder, name string, record appStartedRecord) {
	_ = supervision.PruneStartRecord(ctx, filepath.Join(folder, name+".json"),
		filepath.Join(folder, name+".stop"), record, appProcessRuns)
}
