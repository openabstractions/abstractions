package main

import (
	"fmt"
	"sync"
	"time"
)

// A presence or inbox call slower than this is logged. Agent helpers give each
// such call 2 seconds, so a call past 1 second is near losing its helper's
// presence.
const slowPresenceCall = time.Second

// slowPresenceLines bounds the slow-call lines to one a second; the next line
// counts the slow calls it did not report.
type slowPresenceLines struct {
	mu        sync.Mutex
	last      time.Time
	unwritten int
}

// reportSlowPresence logs one line for a presence or inbox call that took
// longer than slowPresenceCall. It names the call and its duration only.
func (h *applicationsHost) reportSlowPresence(call string, started time.Time) {
	took := time.Since(started)
	if took <= slowPresenceCall || h == nil || h.report == nil {
		return
	}
	h.slowPresence.mu.Lock()
	now := time.Now()
	if now.Sub(h.slowPresence.last) < time.Second {
		h.slowPresence.unwritten++
		h.slowPresence.mu.Unlock()
		return
	}
	unwritten := h.slowPresence.unwritten
	h.slowPresence.last, h.slowPresence.unwritten = now, 0
	h.slowPresence.mu.Unlock()
	line := fmt.Sprintf("applications: slow %s took %s", call, took.Round(time.Millisecond))
	if unwritten > 0 {
		line += fmt.Sprintf(" (%d more slow presence calls since the last line)", unwritten)
	}
	h.report(fmt.Errorf("%s", line))
}
