package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

func postAgentConnectionCompletion(ctx context.Context, result applications.AgentUpdateResult) error {
	// Applications owns the one notification and its native Undo action after
	// independently observing the producer. The controller consumes Ready only.
	if result.Outcome != applications.OutcomeApplied || result.Reason != "updated" || result.Phase == nil || *result.Phase != applications.AgentUpdatePhaseReady || result.ReadyUnixMs == nil || *result.ReadyUnixMs <= 0 {
		return errors.New("connection update has no native Ready evidence")
	}
	return nil
}

func sendAgentConnectionCompletion(ctx context.Context, result applications.AgentUpdateResult, read func(context.Context, string) (askclient.RecordMetadata, error), post func(context.Context, askclient.NotificationRequest) (askclient.NotificationObservation, error)) error {
	if result.Outcome != applications.OutcomeApplied || result.Reason != "updated" || result.Intent == "" || result.Phase == nil || *result.Phase != applications.AgentUpdatePhaseReady || result.ReadyUnixMs == nil || *result.ReadyUnixMs <= 0 {
		return errors.New("connection update has no terminal native proof")
	}
	var record askclient.RecordMetadata
	if result.Question != "" {
		var err error
		record, err = read(ctx, result.Question)
		if err != nil {
			return err
		}
	}
	request, err := agentConnectionCompletionRequest(result, record)
	if err != nil {
		return err
	}
	posted, err := post(ctx, request)
	if err != nil {
		return err
	}
	switch posted.Outcome {
	case askclient.NotificationOutcomePosted, askclient.NotificationOutcomeActed, askclient.NotificationOutcomeDismissed, askclient.NotificationOutcomeWithdrawn, askclient.NotificationOutcomeExpired, askclient.NotificationOutcomeGone:
		return nil
	default:
		return fmt.Errorf("connection update completion notification pending: %s", posted.Outcome)
	}
}

func agentConnectionCompletionRequest(result applications.AgentUpdateResult, record askclient.RecordMetadata) (askclient.NotificationRequest, error) {
	if result.Question != "" && (record.ID != result.Question || record.Key != asks.AgentUpdateKey) {
		return askclient.NotificationRequest{}, errors.New("connection completion question changed")
	}
	if result.Phase == nil || *result.Phase != applications.AgentUpdatePhaseReady || result.ReadyUnixMs == nil || *result.ReadyUnixMs <= 0 || *result.ReadyUnixMs > int64(^uint64(0)>>1)-asks.MaxChoiceReplayMilliseconds {
		return askclient.NotificationRequest{}, errors.New("connection completion has no retained native readback time")
	}
	// ASK-W5/N3: reconstruct the same complete reference after a lost response
	// or restart. The immutable service readback timestamp supplies retention;
	// retries never refresh the deadline or create another occurrence.
	until := *result.ReadyUnixMs + asks.MaxChoiceReplayMilliseconds
	seen := map[string]bool{}
	for _, entry := range result.Enrollments {
		if seen[entry.Application] || (entry.Application != "oa-codex-integration" && entry.Application != "oa-claude-integration" && entry.Application != "oa-opencode-integration") {
			return askclient.NotificationRequest{}, errors.New("connection completion membership changed")
		}
		seen[entry.Application] = true
	}
	var hosts []string
	for _, party := range agentHelperParties {
		for _, entry := range result.Enrollments {
			if entry.Application == "oa-"+string(party.party)+"-integration" {
				if entry.State != applications.AgentEnrollmentStateEnrolled {
					return askclient.NotificationRequest{}, errors.New("connection completion member is not enrolled")
				}
				hosts = append(hosts, party.name)
				break
			}
		}
	}
	if len(hosts) == 0 {
		return askclient.NotificationRequest{}, errors.New("connection completion has no installed hosts")
	}
	key := "oa-agent-update-complete-" + result.Intent
	q := askclient.NotificationRequest{RequestRef: askclient.RequestRef{Key: key, ReplayUntilMs: until}, Title: "Connections updated", Body: "Connections updated. Reopen " + strings.Join(hosts, " and ") + ".", Tag: "agent-update-" + result.Intent, Delivery: askclient.NotificationDeliveryNow, ExpiresMs: until, Actions: []askclient.NotificationAction{}}
	if !asks.ValidNotificationRequest(q) {
		return askclient.NotificationRequest{}, errors.New("connection completion notification is invalid")
	}
	return q, nil
}
