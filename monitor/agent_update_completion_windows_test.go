package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/listen"
)

func completionFixture() (applications.AgentUpdateResult, askclient.RecordMetadata) {
	phase := applications.AgentUpdatePhaseReady
	readyAt := time.Now().UnixMilli()
	return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "updated", Intent: "retained-intent", Question: "native-question", Phase: &phase, ReadyUnixMs: &readyAt, Enrollments: []applications.AgentEnrollment{{Application: "oa-codex-integration", State: applications.AgentEnrollmentStateEnrolled}, {Application: "oa-claude-integration", State: applications.AgentEnrollmentStateEnrolled}}}, askclient.RecordMetadata{ID: "native-question", Key: asks.AgentUpdateKey, Answered: time.Now().UTC().Format(time.RFC3339Nano)}
}

func TestAgentUpdateCompletionFullNativeIntentFitsNotificationTag(t *testing.T) {
	result, record := completionFixture()
	result.Intent = strings.Repeat("a", 48)
	request, err := agentConnectionCompletionRequest(result, record)
	if err != nil || !asks.ValidNotificationRequest(request) {
		t.Fatal(request, err)
	}
}

func TestAgentUpdateCompletionOpenCodeUsesCommonBatchNotification(t *testing.T) {
	result, record := completionFixture()
	result.Enrollments = append(result.Enrollments, applications.AgentEnrollment{Application: "oa-opencode-integration", State: applications.AgentEnrollmentStateEnrolled})
	request, err := agentConnectionCompletionRequest(result, record)
	if err != nil || request.Body != "Connections updated. Reopen Codex and Claude and OpenCode." {
		t.Fatal(request, err)
	}
	result.Enrollments = result.Enrollments[2:]
	request, err = agentConnectionCompletionRequest(result, record)
	if err != nil || request.Body != "Connections updated. Reopen OpenCode." {
		t.Fatal(request, err)
	}
	result.Enrollments[0].State = applications.AgentEnrollmentStateNone
	if _, err := agentConnectionCompletionRequest(result, record); err == nil {
		t.Fatal("unkept OpenCode emitted completion")
	}
}

func TestAgentUpdateCompletionWaitsForFinalOwnerReadback(t *testing.T) {
	for _, mode := range []string{"pending", "not-now", "ready-failed", "readback-failed", "success"} {
		t.Run(mode, func(t *testing.T) {
			updated, _ := completionFixture()
			calls, notifications := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			commit := func(string) (applications.AgentUpdateResult, error) {
				calls++
				if mode == "pending" {
					cancel()
					return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "permission_pending"}, nil
				}
				if mode == "not-now" {
					return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "not_now"}, nil
				}
				if calls == 1 {
					return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready"}, nil
				}
				if mode == "readback-failed" && calls == 3 {
					return applications.AgentUpdateResult{}, errors.New("readback lost")
				}
				if calls == 2 {
					committed := updated
					phase := applications.AgentUpdatePhaseCommitted
					committed.Phase = &phase
					committed.ReadyUnixMs = nil
					return committed, nil
				}
				return updated, nil
			}
			err := finishAgentConnectionUpdateBatch(ctx, updated.Intent, commit, func() error { return nil }, func(applications.AgentUpdateResult) error {
				if mode == "ready-failed" {
					return errors.New("cache remains stale")
				}
				return nil
			}, func(result applications.AgentUpdateResult) error {
				notifications++
				if result.Intent != updated.Intent {
					t.Fatal(result)
				}
				return nil
			})
			if mode == "success" {
				if err != nil || notifications != 1 || calls != 3 {
					t.Fatal(calls, notifications, err)
				}
			} else if notifications != 0 {
				t.Fatal("early success notification", mode, notifications)
			}
		})
	}
}

func TestAgentUpdateCompletionLostReplyReplaysSameRetainedNotification(t *testing.T) {
	result, record := completionFixture()
	bookPath := filepath.Join(t.TempDir(), "notifications.json")
	book, err := asks.LoadApplicationBook(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "panel.exe")
	var first askclient.NotificationRequest
	var id string
	posts := 0
	read := func(context.Context, string) (askclient.RecordMetadata, error) { return record, nil }
	post := func(_ context.Context, q askclient.NotificationRequest) (askclient.NotificationObservation, error) {
		posts++
		got, _, err := book.PostNotificationFrom("completion-fixture", program, q, listen.Seen{}, asks.Publisher{}, func() error { return nil })
		if err != nil {
			return got, err
		}
		if posts == 1 {
			first = q
			id = got.ID
			return askclient.NotificationObservation{}, errors.New("post reply lost")
		}
		if !reflect.DeepEqual(first, q) || got.ID != id {
			t.Fatal("retry created another notification", q, got)
		}
		return got, nil
	}
	if err := sendAgentConnectionCompletion(context.Background(), result, read, post); err == nil {
		t.Fatal("lost response hidden")
	}
	book, err = asks.LoadApplicationBook(bookPath)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(string) (applications.AgentUpdateResult, error) { return result, nil }
	err = resumeAgentConnectionUpdates(func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: result.Intent}, nil
	}, commit, func(applications.AgentUpdateResult) error { return nil }, func(result applications.AgentUpdateResult) error {
		return sendAgentConnectionCompletion(context.Background(), result, read, post)
	})
	if err != nil || posts != 2 {
		t.Fatal(posts, err)
	}
}

func TestAgentUpdateCompletionRecoveredOwnerFailureNotifiesOnlyAfterSuccess(t *testing.T) {
	result, _ := completionFixture()
	readyFailed := true
	proved := false
	notifications := 0
	commit := func(string) (applications.AgentUpdateResult, error) {
		if !proved {
			committed := result
			phase := applications.AgentUpdatePhaseCommitted
			committed.Phase = &phase
			committed.ReadyUnixMs = nil
			return committed, nil
		}
		return result, nil
	}
	ready := func(applications.AgentUpdateResult) error {
		if readyFailed {
			return errors.New("actual cache not current")
		}
		proved = true
		return nil
	}
	complete := func(applications.AgentUpdateResult) error { notifications++; return nil }
	if err := finishAgentConnectionUpdateBatch(context.Background(), result.Intent, commit, func() error { t.Fatal("completed image republished"); return nil }, ready, complete); err == nil || notifications != 0 {
		t.Fatal(notifications, err)
	}
	readyFailed = false
	err := resumeAgentConnectionUpdates(func(applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		return applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Intent: result.Intent}, nil
	}, commit, ready, complete)
	if err != nil || notifications != 1 {
		t.Fatal(notifications, err)
	}
}

func TestAgentUpdateCompletionNamesOnlyEligibleInstalledHosts(t *testing.T) {
	result, record := completionFixture()
	for _, single := range []bool{false, true} {
		if single {
			result.Enrollments = result.Enrollments[1:]
		}
		q, err := agentConnectionCompletionRequest(result, record)
		if err != nil {
			t.Fatal(err)
		}
		want := "Connections updated. Reopen Codex and Claude."
		if single {
			want = "Connections updated. Reopen Claude."
		}
		if q.Body != want || len(q.Actions) != 0 || q.Delivery != askclient.NotificationDeliveryNow {
			t.Fatal(q)
		}
		if q.RequestRef.ReplayUntilMs-*result.ReadyUnixMs != asks.MaxChoiceReplayMilliseconds || q.ExpiresMs != q.RequestRef.ReplayUntilMs {
			t.Fatal("deadline not anchored to retained service time", q)
		}
		again, err := agentConnectionCompletionRequest(result, record)
		if err != nil || !reflect.DeepEqual(q, again) {
			t.Fatal("retained deadline changed", q, again, err)
		}
	}
}

func TestAgentUpdateCompletionRequiresNativeReadyWithoutAnotherPersonQuestion(t *testing.T) {
	result, _ := completionFixture()
	result.Question = ""
	posts := 0
	read := func(context.Context, string) (askclient.RecordMetadata, error) {
		t.Fatal("maintenance synthesized or read another person question")
		return askclient.RecordMetadata{}, nil
	}
	post := func(context.Context, askclient.NotificationRequest) (askclient.NotificationObservation, error) {
		posts++
		return askclient.NotificationObservation{Outcome: askclient.NotificationOutcomePosted}, nil
	}
	committed := applications.AgentUpdatePhaseCommitted
	result.Phase = &committed
	if err := sendAgentConnectionCompletion(context.Background(), result, read, post); err == nil || posts != 0 {
		t.Fatal("native Commit without plugin proof announced Ready", err, posts)
	}
	ready := applications.AgentUpdatePhaseReady
	result.Phase = &ready
	if err := sendAgentConnectionCompletion(context.Background(), result, read, post); err != nil || posts != 1 {
		t.Fatal(err, posts)
	}
}

func TestAgentUpdateCompletionRefusesPendingOrUnprovedMembership(t *testing.T) {
	result, record := completionFixture()
	for _, reason := range []string{"permission_pending", "update_ready", "not_now"} {
		result.Reason = reason
		if err := sendAgentConnectionCompletion(context.Background(), result, func(context.Context, string) (askclient.RecordMetadata, error) {
			t.Fatal("pending read completion metadata")
			return record, nil
		}, func(context.Context, askclient.NotificationRequest) (askclient.NotificationObservation, error) {
			t.Fatal("pending emitted completion")
			return askclient.NotificationObservation{}, nil
		}); err == nil {
			t.Fatal(reason)
		}
	}
	result.Reason = "updated"
	result.Enrollments[0].State = applications.AgentEnrollmentStateNone
	if _, err := agentConnectionCompletionRequest(result, record); err == nil {
		t.Fatal("unenrolled host announced")
	}
}
