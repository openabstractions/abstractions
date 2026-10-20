package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
	"golang.org/x/sys/windows"
)

// The background retry records a stop other than running apps against the
// approved intents, so the Panel offers Update again with one fixed reason.
func TestAgentConnectionRetryRecordsOnlyNonBusyStops(t *testing.T) {
	approved := applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready", Intent: "batch"}
	find := func(setup.AgentParty) applications.AgentUpdateResult { return approved }
	busy := &agentConnectionUpdatePending{Intent: "batch", RestartRequired: true, Err: setup.ErrAgentUpdateRestartRequired}
	for _, c := range []struct {
		name    string
		err     error
		intents string
		reason  string
	}{
		{"published", nil, "", ""},
		{"apps still running", busy, "", ""},
		{"busy publication error", fmt.Errorf("previous image retained: %w", setup.ErrAgentUpdateRestartRequired), "", ""},
		{"another program holds a connector", fmt.Errorf("previous image retained at ; staged image x: current image admission: %w", windows.STATUS_SHARING_VIOLATION), "batch", agentStopInUse},
		{"resume proof", setup.ErrAgentUpdateResumeProofRequired, "batch", agentStopReview},
		{"connection changed", errors.New("Claude connection changed; preserved"), "batch", agentStopChanged},
		{"staged copy differs", errors.New("staged connection differs from installed component"), "batch", agentStopCheck},
		{"service refused", errors.New("native connection update refused: members"), "batch", agentStopSave},
		{"other", errors.New("something new"), "batch", agentStopOther},
	} {
		var intents []string
		reason := "unset"
		recordAgentConnectionRetry(context.Background(), c.err, find, func(i []string, r string) error { intents, reason = i, r; return nil })
		if strings.Join(intents, ",") != c.intents || reason != c.reason {
			t.Errorf("%s: %v %q", c.name, intents, reason)
		}
	}
	none := func(setup.AgentParty) applications.AgentUpdateResult {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "not_now", Intent: "batch"}
	}
	intents, reason := []string{"unset"}, "unset"
	got := recordAgentConnectionRetry(context.Background(), errors.New("disk full"), none, func(i []string, r string) error { intents, reason = i, r; return nil })
	if len(intents) != 0 || reason != "" || got != "" {
		t.Fatal("a stop without an approved intent was recorded", intents, reason, got)
	}
}

// Errors carrying a path or a system error code record one fixed reason; the
// path and code reach only the log.
func TestAgentConnectionRetryRecordsNoPathOrCode(t *testing.T) {
	approved := applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready", Intent: "batch"}
	find := func(setup.AgentParty) applications.AgentUpdateResult { return approved }
	path := `C:\Users\someone\AppData\Roaming\OpenAbstractions\setup\agent-integrations\oa-claude-integration.exe`
	for _, c := range []struct {
		err    error
		reason string
	}{
		{&fs.PathError{Op: "open", Path: path, Err: windows.ERROR_SHARING_VIOLATION}, agentStopInUse},
		{fmt.Errorf("staged image %s: %w", path, &fs.PathError{Op: "write", Path: path, Err: windows.ERROR_DISK_FULL}), agentStopSave},
		{fmt.Errorf("previous image retained at %s: %w", path, windows.ERROR_ACCESS_DENIED), agentStopInUse},
	} {
		reason := ""
		recordAgentConnectionRetry(context.Background(), c.err, find, func(_ []string, r string) error { reason = r; return nil })
		if reason != c.reason || strings.ContainsAny(reason, `\:`) || strings.Contains(reason, "0x") {
			t.Errorf("%v: recorded %q", c.err, reason)
		}
	}
}

func TestAgentHelperRetryShortensWhileRestartPending(t *testing.T) {
	busy := &agentConnectionUpdatePending{Intent: "batch", RestartRequired: true, Err: setup.ErrAgentUpdateRestartRequired}
	if got := agentHelperRetryPause(busy, 30*time.Second); got != 10*time.Second {
		t.Fatal(got)
	}
	if got := agentHelperRetryPause(errors.New("runtime starting"), 30*time.Second); got != 30*time.Second {
		t.Fatal(got)
	}
	if got := agentHelperRetryPause(busy, time.Millisecond); got != time.Millisecond {
		t.Fatal(got)
	}
}

// The 10 s retries log "restart required" once per update and again only when
// the stop changes or after a publication.
func TestAgentPublicationStopLogsOncePerUpdate(t *testing.T) {
	agentPublicationStopChanged("")
	t.Cleanup(func() { agentPublicationStopChanged("") })
	stages := []*agentConnectionBatchStage{{program: "oa-claude-integration.exe.aaa.tmp"}}
	busy := map[string]string{"connection.hosts": "claude", "reason": "restart_required"}
	key := agentPublicationStopKey(stages, busy)
	if !agentPublicationStopChanged(key) || agentPublicationStopChanged(key) || agentPublicationStopChanged(key) {
		t.Fatal("restart required logged more than once for one update")
	}
	other := agentPublicationStopKey(stages, map[string]string{"connection.hosts": "claude", "reason": "other"})
	if !agentPublicationStopChanged(other) || !agentPublicationStopChanged(key) {
		t.Fatal("a changed stop was not logged")
	}
	next := agentPublicationStopKey([]*agentConnectionBatchStage{{program: "oa-claude-integration.exe.bbb.tmp"}}, busy)
	if !agentPublicationStopChanged(next) {
		t.Fatal("a new update was not logged")
	}
	agentPublicationStopChanged("")
	if !agentPublicationStopChanged(next) {
		t.Fatal("a stop after a publication was not logged")
	}
}

func TestAgentManualUpdateClearsRetryRecordOnlyWhenFinished(t *testing.T) {
	for _, c := range []struct {
		outcome string
		err     error
		clear   bool
	}{
		{"updated", nil, true},
		{"current", nil, true},
		{"update_pending", nil, false},
		{"permission_pending", nil, false},
		{"not_now", nil, false},
		{"updated", errors.New("readback pending"), false},
	} {
		if got := agentManualUpdateFinished(agentHelperUpdateResult{Outcome: c.outcome}, c.err); got != c.clear {
			t.Errorf("%s %v: %v", c.outcome, c.err, got)
		}
	}
}
