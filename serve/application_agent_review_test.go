package main

import (
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"os"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

func agentReviewUndoFixture(t *testing.T) (*selfRegisteredRun, applications.AgentUpdateRequest, applications.AgentUpdateResult) {
	t.Helper()
	run, request := agentMaintenanceFixture(t)
	prepared := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	publishAgentUpdateFixture(t, request)
	run.d.agentCompanionProducer.(*agentCompanionFixture).current = true
	parent := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent)
	if parent.ReadyUnixMs == nil {
		t.Fatal(parent)
	}
	notices, _, err := run.f.a.book.NotificationHistory()
	if err != nil || len(notices) != 1 {
		t.Fatal(notices, err)
	}
	acted, err := run.f.a.book.DecideNotification(notices[0].ID, asks.NotificationAct, "undo", run.f.panel.Program, nil)
	if err != nil || acted.Outcome != askwire.NotificationDecisionOutcomeActed {
		t.Fatal(acted, err)
	}
	child := run.d.prepareAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{UndoOf: prepared.Intent})
	if child.Outcome != applications.OutcomeApplied {
		t.Fatal(child)
	}
	for _, stage := range child.StagedImages {
		for _, entry := range parent.Enrollments {
			if entry.Application != stage.Application {
				continue
			}
			if err := os.Rename(entry.Program, stage.Program+".previous"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(stage.Program, entry.Program); err != nil {
				t.Fatal(err)
			}
		}
	}
	restored := run.d.commitAgentUpdate(run.ctx, run.f.panel, child.Intent)
	if restored.ReadyUnixMs == nil {
		t.Fatal(restored)
	}
	for i := range request.Members {
		for _, entry := range restored.Enrollments {
			if entry.Application == request.Members[i].Application {
				request.Members[i].ExpectedRevision = entry.Revision
			}
		}
		payload, err := os.ReadFile(request.SourceProgram)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(request.Members[i].StagedProgram, payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return run, request, restored
}

func TestAgentReviewAfterUndoRequiresActualPersonAndOffersCompletionUndo(t *testing.T) {
	run, request, child := agentReviewUndoFixture(t)
	before, _, _ := run.f.a.book.ApplicationHistory()
	for i := 0; i < 2; i++ {
		got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
		if got.Reason != "not_now" || got.Intent != child.Intent || got.Question != "" {
			t.Fatal(got)
		}
	}
	request.ReviewOf = child.Intent
	review := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if review.Reason != "permission_pending" || review.Question == "" || review.Intent == child.Intent {
		t.Fatal(review)
	}
	again := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if again.Intent != review.Intent || again.Question != review.Question {
		t.Fatal("review replay", again)
	}
	after, _, _ := run.f.a.book.ApplicationHistory()
	if len(after) != len(before)+1 {
		t.Fatal("review did not ask exactly once", len(before), len(after))
	}
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, review.Intent); got.Reason != "permission_pending" {
		t.Fatal(got)
	}
	answer(t, run.f, review.Question, "update")
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, review.Intent); got.Reason != "update_ready" {
		t.Fatal(got)
	}
	publishAgentUpdateFixture(t, request)
	run.d.agentCompanionProducer.(*agentCompanionFixture).current = true
	final := run.d.commitAgentUpdate(run.ctx, run.f.panel, review.Intent)
	if final.Reason != "updated" || final.ReadyUnixMs == nil {
		t.Fatal(final)
	}
	notices, _, err := run.f.a.book.NotificationHistory()
	if err != nil || len(notices) < 2 {
		t.Fatal("completion with Undo absent", notices, err)
	}
	found := false
	for _, notice := range notices {
		if notice.Request.RequestRef.Key != agentUpdateNotificationPrefix+review.Intent {
			continue
		}
		for _, action := range notice.Request.Actions {
			if action.ID == "undo" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("review completion did not offer its own Undo", notices)
	}
}

func TestAgentReviewAfterNotNowRenewsOnlyOnExplicitReference(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	run.d.agentMaintenance = false
	first := prepareAgentUpdateFixture(t, run, request)
	answer(t, run.f, first.Question, "not_now")
	if got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request); got.Intent != first.Intent || got.Reason != "not_now" {
		t.Fatal(got)
	}
	request.ReviewOf = first.Intent
	review := prepareAgentUpdateFixture(t, run, request)
	if review.Intent == first.Intent || review.Question == first.Question {
		t.Fatal(review)
	}
	answer(t, run.f, review.Question, "not_now")
	background := request
	background.ReviewOf = ""
	if got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, background); got.Reason != "not_now" || got.Intent != review.Intent {
		t.Fatal("background renewed decline", got)
	}
	request.ReviewOf = review.Intent
	next := prepareAgentUpdateFixture(t, run, request)
	if next.Question == review.Question || next.Intent == review.Intent {
		t.Fatal(next)
	}
}

func TestAgentReviewRejectsForeignPendingMembersBindingAndSource(t *testing.T) {
	for _, mode := range []string{"foreign", "pending", "member", "revision", "source", "undo", "agent"} {
		t.Run(mode, func(t *testing.T) {
			run, request := agentMaintenanceFixture(t)
			run.d.agentMaintenance = false
			first := prepareAgentUpdateFixture(t, run, request)
			if mode != "pending" {
				answer(t, run.f, first.Question, "not_now")
			}
			request.ReviewOf = first.Intent
			subject := run.f.panel
			switch mode {
			case "foreign":
				request.ReviewOf = strings.Repeat("a", 48)
			case "member":
				request.Members[0].Application = "foreign-application"
			case "revision":
				request.Members[0].ExpectedRevision = strings.Repeat("b", 48)
			case "source":
				if err := os.WriteFile(request.SourceProgram, []byte("different source"), 0600); err != nil {
					t.Fatal(err)
				}
			case "undo":
				request.UndoOf = first.Intent
			case "agent":
				subject = run.f.answerer
			}
			got := run.d.prepareAgentUpdate(run.ctx, subject, request)
			want := applications.OutcomeConflict
			if mode == "agent" {
				want = applications.OutcomeForbidden
			}
			if got.Outcome != want || got.Question != "" || got.Intent != "" {
				t.Fatal(mode, got)
			}
		})
	}
}

func TestAgentReviewCurrentBindingAndAccountRechecked(t *testing.T) {
	run, request := agentMaintenanceFixture(t)
	run.d.agentMaintenance = false
	first := prepareAgentUpdateFixture(t, run, request)
	answer(t, run.f, first.Question, "not_now")
	if !run.d.lock(run.ctx) {
		t.Fatal("lock")
	}
	defer run.d.unlock()
	err := run.d.withRegistrationReadLocked(run.ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, _, err := run.d.readSelfMarksAtDescriptorLocked(run.ctx)
		if err != nil {
			return err
		}
		prior := agentUpdateIndex(marks)[first.Intent]
		if !run.d.agentReviewMembersCurrent(legacy, marks, request, prior) {
			t.Fatal("current reference refused")
		}
		name := request.Members[0].Application
		mark := marks[name]
		copy := *mark.Agent
		originalBinding := copy.Binding
		mark.Agent = &copy
		marks[name] = mark
		copy.Binding = strings.Repeat("c", 64)
		if run.d.agentReviewMembersCurrent(legacy, marks, request, prior) {
			t.Fatal("changed binding admitted")
		}
		copy.Binding = originalBinding
		copy.Account = "foreign-account"
		if run.d.agentReviewMembersCurrent(legacy, marks, request, prior) {
			t.Fatal("foreign account admitted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentReviewAllowsCurrentMemberSubset(t *testing.T) {
	run, request, child := agentReviewUndoFixture(t)
	request.Members = request.Members[:1]
	request.ReviewOf = child.Intent
	got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if got.Reason != "permission_pending" || got.Question == "" || len(got.StagedImages) != 1 {
		t.Fatal(got)
	}
}
