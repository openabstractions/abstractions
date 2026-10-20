package main

import (
	"strconv"
	"time"

	logging "github.com/openabstractions/abstraction-logging/go"
)

// The runtime records who opened an app it started (architect ruling
// 2026-11-12). When an open starts an owned host, the runtime
// writes one line to its own log: the application, the program whose open
// started it (empty for the person's own Open in the Panel) and the host's
// process ID and creation time. The host learns nothing of it. An
// integration that starts a server records only the processes it started
// (<local app data>\OpenAbstractions\started\<application>.json); the Panel
// joins the two, the server's parent being the host by PID and creation
// time, and shows "started by OpenAbstractions for <app>"
// (monitor/app_started.go).

// ownedStartEvent is the log event the Panel's Apps page reads.
const ownedStartEvent = "applications.owned.start"

// filetimeEpoch is 1970-01-01 in FILETIME 100 ns ticks.
const filetimeEpoch = 116444736000000000

// processFiletime is t as the FILETIME count Windows gives a process's
// creation time, the form an integration records.
func processFiletime(t time.Time) string {
	return strconv.FormatUint(uint64(t.UnixNano()/100)+filetimeEpoch, 10)
}

// ownedStartRecord is the log line of one owned host start.
func ownedStartRecord(at time.Time, application, opener string, b ownedApplicationBinding) logging.Record {
	return logging.Record{Schema: logging.SchemaVersion, Time: logging.At(at), Level: logging.LevelInfo, Msg: "Started an app for the program that opened it",
		Attrs: map[string]string{"oa_event": ownedStartEvent, "application": application, "opener": opener,
			"host_pid": strconv.Itoa(b.pid), "host_created": processFiletime(b.started)}}
}

// recordOwnedStart writes the line to the runtime's own log. A write failure
// is reported and the start stands.
func (g *applicationV3Gateway) recordOwnedStart(application, opener string, b ownedApplicationBinding) bool {
	if g.policy == nil || b.pid <= 0 || b.started.IsZero() {
		return false
	}
	g.policy.elevated.mu.Lock()
	sink := g.policy.elevated.logs
	g.policy.elevated.mu.Unlock()
	if sink == nil {
		return false
	}
	if err := sink.Write(ownedStartRecord(time.Now(), application, opener, b)); err != nil {
		if g.report != nil {
			g.report(err)
		}
		return false
	}
	return true
}
