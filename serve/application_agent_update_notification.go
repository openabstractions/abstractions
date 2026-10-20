package main

import (
	"context"
	"errors"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	askwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-identity/listen"
)

const agentUpdateNotificationPrefix = "oa-agent-update-"

func agentUpdateNotificationReference(u *applicationAgentUpdate) (askwire.RequestRef, error) {
	if u == nil || !u.Committed || u.ReadyAt <= 0 || u.ReadyAt > int64(^uint64(0)>>1)-asks.MaxChoiceReplayMilliseconds {
		return askwire.RequestRef{}, errors.New("agent update has no Ready evidence")
	}
	return askwire.RequestRef{Key: agentUpdateNotificationPrefix + u.ID, ReplayUntilMs: u.ReadyAt + asks.MaxChoiceReplayMilliseconds}, nil
}

func (d *applicationDirectory) agentUpdateUndoAvailable(ctx context.Context, u *applicationAgentUpdate) bool {
	producer, ok := d.companionProducer().(agentCompanionBackups)
	if !d.agentMaintenance || !ok || u.Request.UndoOf != "" {
		return false
	}
	for _, c := range u.Members {
		if c.Maintenance == nil || !c.Maintenance.Kept.Current.provenFor(c.Old) || !updateImageMatches(ctx, c.Old, c.Backup) {
			return false
		}
		// An image-only update changed no recorded companions.
		if len(c.CompanionsBefore) > 0 && (len(c.CompanionBackup) == 0 || producer.ValidateBackup(c.CompanionBackup) != nil || !companionDeclarationBounded(c.CompanionsBefore) || d.companionProducer().Validate(c.CompanionsBefore) != nil) {
			return false
		}
	}
	return len(u.Members) > 0
}

func (d *applicationDirectory) postAgentUpdateReady(ctx context.Context, u *applicationAgentUpdate) {
	if d.agentQuestions == nil {
		return
	}
	a := d.agentQuestions.a
	ref, err := agentUpdateNotificationReference(u)
	if err != nil {
		if a.report != nil {
			a.report(err)
		}
		return
	}
	titles := make([]string, 0, len(u.Members))
	for _, c := range u.Members {
		titles = append(titles, c.Title)
	}
	q := askwire.NotificationRequest{RequestRef: ref, Title: "Connections updated", Body: "Connections updated. Reopen " + strings.Join(titles, " and ") + ".", Tag: ref.Key, Delivery: askwire.NotificationDeliveryNow, ExpiresMs: ref.ReplayUntilMs, Actions: []askwire.NotificationAction{}}
	if d.agentUpdateUndoAvailable(ctx, u) {
		q.Actions = append(q.Actions, askwire.NotificationAction{ID: "undo", Title: "Undo", Activation: askwire.NotificationActivationApp})
	}
	if !asks.ValidNotificationRequest(q) {
		if a.report != nil {
			a.report(errors.New("agent update completion is invalid"))
		}
		return
	}
	_, _, err = a.book.PostNotificationFrom(a.scope, a.rights.self().Program, q, listen.Seen{Why: "native connection update Ready readback"}, asks.Publisher{}, func() error { return a.notificationBlocked(ctx, a.rights.self().Program) })
	if err != nil && a.report != nil {
		a.report(err)
	}
}

func (d *applicationDirectory) agentUpdateUndoAction(u *applicationAgentUpdate) (askwire.NotificationResult, error) {
	ref, err := agentUpdateNotificationReference(u)
	if err != nil {
		return askwire.NotificationResult{}, err
	}
	if ref.ReplayUntilMs <= d.now().UnixMilli() {
		return askwire.NotificationResult{}, errors.New("agent undo native action expired")
	}
	if d.agentQuestions == nil {
		return askwire.NotificationResult{}, errors.New("agent undo notification owner unavailable")
	}
	a := d.agentQuestions.a
	seen, err := a.book.ObserveNotification(a.scope, ref)
	if err != nil || seen.Outcome != askwire.NotificationOutcomeActed || seen.Result == nil || seen.Result.Action != "undo" || seen.Result.DecidedMs <= 0 || seen.Result.DecidedBy == "" {
		return askwire.NotificationResult{}, errors.New("agent undo has no authenticated native action")
	}
	return *seen.Result, nil
}
