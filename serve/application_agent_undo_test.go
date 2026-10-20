package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestAgentMaintenanceUndoCreatesNewChildAndKeepsParentImmutable(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	producer := run.d.agentCompanionProducer.(*agentCompanionFixture)
	prepared := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	publishAgentUpdateFixture(t, request)
	producer.current = true
	parentResult := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
	if parentResult.Phase == nil || *parentResult.Phase != applications.AgentUpdatePhaseReady {
		t.Fatal(parentResult)
	}
	anchor := prepared.StagedImages[0].Application
	parent := run.d.unconfirmed[anchor].Agent.Update
	original, _ := json.Marshal(parent)
	refused := run.d.prepareAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{UndoOf: parent.ID})
	if refused.Outcome == applications.OutcomeApplied {
		t.Fatal("Undo without person action", refused)
	}
	notices, _, err := run.f.a.book.NotificationHistory()
	if err != nil || len(notices) != 1 {
		t.Fatal(notices, err)
	}
	acted, err := run.f.a.book.DecideNotification(notices[0].ID, asks.NotificationAct, "undo", run.f.panel.Program, nil)
	if err != nil || acted.Outcome != askwire.NotificationDecisionOutcomeActed {
		t.Fatal(acted, err)
	}
	child := run.d.prepareAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{UndoOf: parent.ID})
	if child.Outcome != applications.OutcomeApplied || child.Reason != "update_ready" || child.Intent == parent.ID || child.Intent == "" || child.Question != "" {
		t.Fatal(child)
	}
	for _, stage := range child.StagedImages {
		program := ""
		for _, entry := range parentResult.Enrollments {
			if entry.Application == stage.Application {
				program = entry.Program
			}
		}
		if program == "" {
			t.Fatal(stage)
		}
		if err := os.Rename(program, stage.Program+".previous"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(stage.Program, program); err != nil {
			t.Fatal(err)
		}
	}
	restored := run.d.commitAgentUpdate(run.ctx, run.f.panel, child.Intent)
	if restored.Outcome != applications.OutcomeApplied || restored.Phase == nil || *restored.Phase != applications.AgentUpdatePhaseReady {
		t.Fatal(restored)
	}
	current := run.d.unconfirmed[anchor].Agent
	if len(current.UpdateHistory) == 0 {
		t.Fatal("parent history missing")
	}
	retained, _ := json.Marshal(current.UpdateHistory[len(current.UpdateHistory)-1])
	if string(retained) != string(original) {
		t.Fatal("child changed terminal parent")
	}
	retry := run.d.prepareAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{UndoOf: parent.ID})
	if retry.Intent != child.Intent || retry.Question != "" {
		t.Fatal("Undo replay made another child", retry)
	}
	for _, entry := range restored.Enrollments {
		found := run.d.findAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: entry.Application}}})
		if found.Intent != child.Intent {
			t.Fatal("current child hidden by prior occurrence provenance", found)
		}
	}
	for i := range request.Members {
		for _, entry := range restored.Enrollments {
			if entry.Application == request.Members[i].Application {
				request.Members[i].ExpectedRevision = entry.Revision
			}
		}
	}
	held := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if held.Outcome != applications.OutcomeApplied || held.Reason != "not_now" || held.Intent != child.Intent || held.Question != "" {
		t.Fatal("Undo generation was automatically reintroduced", held)
	}
	next := cloneAgentRelease(parent.SourceRelease)
	next.Release[3]++
	if id, err := run.d.agentUndoSuppression(run.ctx, request, next); err != nil || id != "" {
		t.Fatal("strictly newer signed generation suppressed", id, err)
	}
	entry := restored.Enrollments[0]
	agent1478Set(t, run, entry.Application, entry.Revision, false)
	other := restored.Enrollments[1]
	stale := run.d.findAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: other.Application}}})
	if stale.Outcome != applications.OutcomeUnknown {
		t.Fatal("superseded terminal batch blocks discovery", stale)
	}
}

func TestAgentMaintenanceUndoRefusesCompetingClaimsAndUnknownAuthority(t *testing.T) {
	run, _ := agentMaintenanceFixture(t)
	for _, request := range []applications.AgentUpdateRequest{{}, {UndoOf: strings.Repeat("a", 48), Members: []applications.AgentUpdateMember{{Application: "claimed"}}}, {UndoOf: strings.Repeat("a", 48), SourceProgram: "claimed-source"}} {
		got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
		if got.Outcome != applications.OutcomeInvalid || got.Intent != "" || len(got.StagedImages) != 0 || got.Question != "" {
			t.Fatal("invalid request disclosed intent", got)
		}
	}
	if got := run.d.findAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{}); got.Outcome != applications.OutcomeInvalid {
		t.Fatal("ordinary empty Find admitted", got)
	}
	for _, subject := range []rights.Subject{run.f.panel, run.f.answerer} {
		got := run.d.prepareAgentUpdate(run.ctx, subject, applications.AgentUpdateRequest{UndoOf: strings.Repeat("a", 48)})
		if got.Outcome != applications.OutcomeForbidden || got.Intent != "" || len(got.StagedImages) != 0 || got.Question != "" {
			t.Fatal("guessed reference disclosed parent", got)
		}
	}
}

func TestAgentMaintenanceUndoRefusesExpiredRevokedForeignAndChangedSource(t *testing.T) {
	for _, mode := range []string{"expired", "revoked", "foreign", "source"} {
		t.Run(mode, func(t *testing.T) {
			run, request := agentMaintenanceFixture(t)
			prepared := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
			publishAgentUpdateFixture(t, request)
			run.d.agentCompanionProducer.(*agentCompanionFixture).current = true
			ready := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
			if ready.ReadyUnixMs == nil {
				t.Fatal(ready)
			}
			notices, _, err := run.f.a.book.NotificationHistory()
			if err != nil || len(notices) != 1 {
				t.Fatal(notices, err)
			}
			actor := run.f.panel.Program
			if mode == "foreign" {
				actor = run.f.answerer.Program
			}
			acted, err := run.f.a.book.DecideNotification(notices[0].ID, asks.NotificationAct, "undo", actor, nil)
			if err != nil || acted.Outcome != askwire.NotificationDecisionOutcomeActed {
				t.Fatal(acted, err)
			}
			switch mode {
			case "expired":
				run.d.now = func() time.Time { return time.UnixMilli(*ready.ReadyUnixMs + asks.MaxChoiceReplayMilliseconds) }
			case "revoked":
				entry := ready.Enrollments[0]
				agent1478Set(t, run, entry.Application, entry.Revision, false)
			case "source":
				if err := os.WriteFile(request.SourceProgram, []byte("changed source"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{UndoOf: prepared.Intent})
			if got.Outcome == applications.OutcomeApplied || got.Intent != "" || got.Question != "" || len(got.StagedImages) != 0 {
				t.Fatal("invalid native action disclosed child", got)
			}
		})
	}
}

// Undo of the latest update still applies when the anchor's history is full:
// the child trims the oldest retained intent instead of refusing.
func TestAgentMaintenanceUndoAppliesWithFullHistory(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	producer := run.d.agentCompanionProducer.(*agentCompanionFixture)
	prepared := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	publishAgentUpdateFixture(t, request)
	producer.current = true
	parentResult := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
	if parentResult.Phase == nil || *parentResult.Phase != applications.AgentUpdatePhaseReady {
		t.Fatal(parentResult)
	}
	anchor := prepared.StagedImages[0].Application
	path := filepath.Join(run.d.dir, selfRegisteredFile)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string]selfMark{}
	if err := decodeApplicationSelfMarks(body, &marks); err != nil {
		t.Fatal(err)
	}
	mark := marks[anchor]
	agent := *mark.Agent
	parent := agent.Update
	var older []string
	for range agentUpdateHistoryLimit {
		var clone applicationAgentUpdate
		raw, _ := json.Marshal(parent)
		if err := json.Unmarshal(raw, &clone); err != nil {
			t.Fatal(err)
		}
		if clone.ID, err = applicationToken(); err != nil {
			t.Fatal(err)
		}
		for i := range clone.Members {
			if clone.Members[i].CommittedRevision, err = applicationToken(); err != nil {
				t.Fatal(err)
			}
		}
		older = append(older, clone.ID)
		agent.UpdateHistory = append(agent.UpdateHistory, &clone)
	}
	mark.Agent = &agent
	marks[anchor] = mark
	if body, err = json.Marshal(marks); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	notices, _, err := run.f.a.book.NotificationHistory()
	if err != nil || len(notices) != 1 {
		t.Fatal(notices, err)
	}
	if acted, err := run.f.a.book.DecideNotification(notices[0].ID, asks.NotificationAct, "undo", run.f.panel.Program, nil); err != nil || acted.Outcome != askwire.NotificationDecisionOutcomeActed {
		t.Fatal(acted, err)
	}
	child := run.d.prepareAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{UndoOf: parent.ID})
	if child.Outcome != applications.OutcomeApplied || child.Reason != "update_ready" || child.Intent == "" {
		t.Fatal("Undo refused with a full history", child)
	}
	if body, err = os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	marks = map[string]selfMark{}
	if err := decodeApplicationSelfMarks(body, &marks); err != nil {
		t.Fatal(err)
	}
	history := marks[anchor].Agent.UpdateHistory
	if len(history) != agentUpdateHistoryLimit || history[len(history)-1].ID != parent.ID || history[0].ID != older[1] || marks[anchor].Agent.Update.ID != child.Intent {
		t.Fatal("history did not keep the newest intents", len(history))
	}
}
