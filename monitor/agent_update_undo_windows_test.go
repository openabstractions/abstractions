package main

import (
	"context"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"testing"
	"time"
)

func TestAgentConnectionUndoAppActivationRequiresNativeAct(t *testing.T) {
	for _, outcome := range []askclient.NotificationDecisionOutcome{askclient.NotificationDecisionActed, askclient.NotificationDecisionConflict, askclient.NotificationDecisionExpired} {
		record := postedRecord(t, "undo-id", "native-token", time.Now())
		record.Request.Actions = []askclient.NotificationAction{{ID: "undo", Title: "Undo", Activation: askclient.NotificationActivationApp}}
		operator := &fakeNotificationOperator{record: record, outcome: outcome}
		calls := 0
		clicks := notificationClicks{operator: func(context.Context) (notificationOperator, error) { return operator, nil }, application: func(_ context.Context, r askclient.NotificationRecord, action string) error {
			calls++
			if r.Result == nil || r.Result.Action != "undo" || action != "undo" {
				t.Fatal(r, action)
			}
			return nil
		}, surface: &fakeToastSurface{}, budget: time.Second}
		_ = clicks.handle(t.Context(), notificationClickArgs("undo-id", "undo", "native-token", false))
		if (outcome == askclient.NotificationDecisionActed && calls != 1) || (outcome != askclient.NotificationDecisionActed && calls != 0) {
			t.Fatal("unauthenticated app activation", outcome, calls)
		}
	}
}

func TestAgentConnectionUndoReadyReplayPreservesPriorCompanionRecipe(t *testing.T) {
	result, _ := completionFixture()
	completed := 0
	err := finishAgentConnectionReady(result.Intent, result, func(string) (applications.AgentUpdateResult, error) {
		t.Fatal("Ready replay called Commit unnecessarily")
		return applications.AgentUpdateResult{}, nil
	}, func(applications.AgentUpdateResult) error {
		t.Fatal("Ready replay installed current recipe over retained version")
		return nil
	}, func(applications.AgentUpdateResult) error { completed++; return nil })
	if err != nil || completed != 1 {
		t.Fatal(err, completed)
	}
}
