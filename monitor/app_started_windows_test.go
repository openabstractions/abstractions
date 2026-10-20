//go:build windows

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	"golang.org/x/sys/windows"
)

// filetimeOf is pid's creation time as a FILETIME count.
func filetimeOf(t *testing.T, pid int) string {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	return strconv.FormatUint(uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime), 10)
}

// The Apps page's line and Stop for a server OpenAbstractions started for an
// app (ruling 2026-11-12, points 3 and 5): the line shows while the recorded
// process runs with its recorded creation time; Stop asks the integration
// first, then stops the process by PID after the grace and removes the
// record. A record whose process has ended is repaired away.
func TestTheAppsPageStopsAServerOpenAbstractionsStarted(t *testing.T) {
	folder := t.TempDir()
	previousFolder, previousGrace := appStartedFolder, appStopGrace
	appStartedFolder, appStopGrace = func() (string, error) { return folder, nil }, 500*time.Millisecond
	t.Cleanup(func() { appStartedFolder, appStopGrace = previousFolder, previousGrace })

	server := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"), "-n", "120", "127.0.0.1")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Process.Kill(); _, _ = server.Process.Wait() })
	created := filetimeOf(t, server.Process.Pid)
	record := func(name string, processes ...appStartedProcess) {
		raw, _ := json.Marshal(appStartedRecord{V: 1, Application: name, Title: "ComfyUI", StartedUnixMs: time.Now().UnixMilli(), Port: 8188, Processes: processes})
		if err := os.WriteFile(filepath.Join(folder, name+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A reused PID with another creation time is no server of OA's.
	record("comfyui", appStartedProcess{PID: server.Process.Pid, Created: "1"})
	if _, _, ok := readAppStarted("comfyui"); ok {
		t.Fatal("a reused PID counts as the started server")
	}
	if _, err := os.Stat(filepath.Join(folder, "comfyui.json")); !os.IsNotExist(err) {
		t.Fatal("the stale record was not repaired away")
	}

	launcher := appStartedProcess{PID: server.Process.Pid, Created: created}
	record("comfyui", launcher)
	path, stopPath := filepath.Join(folder, "comfyui.json"), filepath.Join(folder, "comfyui.stop")
	previous := appStartedRecord{Application: "comfyui", Processes: []appStartedProcess{launcher}}
	if _, err := supervision.RequestStartStop(t.Context(), path, stopPath, previous, appStartedProcess{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	next := appStartedRecord{Application: "comfyui", Title: "ComfyUI", Port: 8188,
		Processes: []appStartedProcess{launcher, {PID: 42, Created: "1"}}}
	if err := supervision.UpdateStartRecord(path, previous, next); err != nil {
		t.Fatal(err)
	}
	// The old timer expires after the replacement listener was recorded.
	appStopGrace = 0
	appFinishStop(t.Context(), folder, "comfyui", previous)
	appStopGrace = 500 * time.Millisecond
	if !appProcessRuns(launcher) {
		t.Fatal("the old stop killed the successor's shared launcher")
	}
	got, _, ok := readAppStarted("comfyui")
	if !ok {
		t.Fatal("the started server is not shown")
	}
	// The runtime's own log names who opened the host that started it: this
	// test process is the server's parent, standing in for the connector.
	host, hostCreated := strconv.Itoa(os.Getpid()), filetimeOf(t, os.Getpid())
	const opencode = `C:\Users\person\AppData\Local\opencode\opencode.exe`
	line := func(application, pid, created, opener string) logwire.Record {
		return logwire.Record{Time: "2026-11-12T20:00:00Z", Msg: "Started an app for the program that opened it",
			Attrs: map[string]string{"oa_event": "applications.owned.start", "application": application, "opener": opener, "host_pid": pid, "host_created": created}}
	}
	history := &messageHistoryFake{page: logclient.ApplicationLogPage{Outcome: logwire.HistoryPageOutcomePage, AtEnd: true, Records: []logwire.Record{
		line("comfyui", host, hostCreated, opencode),
		line("comfyui", host, "99999999999999999999", `C:\later\host.exe`),
		line("comfyui", "1", hostCreated, `C:\other\host.exe`),
		line("other", host, hostCreated, `C:\other\app.exe`),
	}}}
	previousHistory := messageActivityHistory
	t.Cleanup(func() { messageActivityHistory = previousHistory })
	messageActivityHistory = func(context.Context) (messageHistoryReader, logclient.ApplicationLogTarget, error) {
		return history, logclient.ApplicationLogTarget{Selection: logwire.ApplicationLogSelectionOneApp, Program: "native-runtime"}, nil
	}
	if opener := appStartedOpener(context.Background(), "comfyui", got); opener != opencode {
		t.Fatalf("the join named %q", opener)
	}
	if appStartedLine("ComfyUI", got, "OpenCode") != "ComfyUI is running (started by OpenAbstractions for OpenCode)" ||
		appStartedLine("ComfyUI", got, "") != "ComfyUI is running (started by OpenAbstractions)" {
		t.Fatal("started line")
	}

	p := newTestPanel(t)
	mux := http.NewServeMux()
	p.appPageRoutes(mux, "test-key")
	w := panelRequest(t, mux, "/app/comfyui/stop", appActionBody{Confirm: true, StartOccurrence: got.Occurrence()})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ComfyUI stops once its current prompt ends.") {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	stop, err := os.ReadFile(filepath.Join(folder, "comfyui.stop"))
	if err != nil || !strings.Contains(string(stop), `"processes":[{"pid":`+strconv.Itoa(server.Process.Pid)+`,"created":"`+created+`"},`) {
		t.Fatalf("stop request %s %v", stop, err)
	}
	exited := make(chan struct{})
	go func() { _, _ = server.Process.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the server outlived Stop's grace")
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(folder, "comfyui.json")); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the record outlived the stopped server")
		}
	}
	if w := panelRequest(t, mux, "/app/comfyui/stop", appActionBody{Confirm: true}); !strings.Contains(w.Body.String(), "It has already stopped.") {
		t.Fatalf("second stop: %s", w.Body)
	}
}

func TestStaleAppStopAndPruneLeaveTheSuccessorOccurrence(t *testing.T) {
	folder := t.TempDir()
	oldFolder := appStartedFolder
	appStartedFolder = func() (string, error) { return folder, nil }
	t.Cleanup(func() { appStartedFolder = oldFolder })
	path, stop := filepath.Join(folder, "comfyui.json"), filepath.Join(folder, "comfyui.stop")
	old := appStartedProcess{PID: os.Getpid(), Created: "1"}
	current := appStartedProcess{PID: os.Getpid(), Created: filetimeOf(t, os.Getpid())}
	oldRecord := appStartedRecord{Application: "comfyui", Processes: []appStartedProcess{old}}
	newRecord := appStartedRecord{Application: "comfyui", Processes: []appStartedProcess{current}}
	for _, record := range []appStartedRecord{oldRecord, newRecord} {
		if err := supervision.PublishStartRecord(path, record, appProcessRuns); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := supervision.RequestStartStop(t.Context(), path, stop, newRecord, appStartedProcess{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := newTestPanel(t)
	mux := http.NewServeMux()
	p.appPageRoutes(mux, "test-key")
	w := panelRequest(t, mux, "/app/comfyui/stop", appActionBody{Confirm: true, StartOccurrence: oldRecord.Occurrence()})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"outcome":"stale"`) {
		t.Fatalf("stale stop: %d %s", w.Code, w.Body)
	}
	appFinishStop(t.Context(), folder, "comfyui", oldRecord)
	if got, ok := supervision.ReadStartRecord(path, "comfyui"); !ok || got.Processes[0] != current {
		t.Fatalf("successor record was cleared: %+v", got)
	}
	if !supervision.HasStartStopRequest(stop, newRecord) {
		t.Fatal("successor stop request was cleared")
	}
}
