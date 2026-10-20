package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	identity "github.com/openabstractions/abstraction-identity"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

const providerConnectedPrefix = "provider-connected-"

func providerOccurrenceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

func providerCompletionRequest(record askclient.RecordMetadata, permission *askclient.PermissionRequest, publisher trayPublisher) (askclient.NotificationRequest, error) {
	name, ok := providerForPermission(permission)
	if !ok || !providerOccurrenceID(record.ID) || record.Key != asks.FirstUseKey || record.Option != "allow" || record.About != permission.Targets[0].Program {
		return askclient.NotificationRequest{}, errProviderConnectionChanged
	}
	answered, err := time.Parse(time.RFC3339Nano, record.Answered)
	if err != nil || answered.UnixMilli() <= 0 || answered.UnixMilli() > int64(^uint64(0)>>1)-asks.MaxChoiceReplayMilliseconds {
		return askclient.NotificationRequest{}, errProviderConnectionChanged
	}
	preset, _ := hostedcatalogue.Lookup(name)
	until := answered.UnixMilli() + asks.MaxChoiceReplayMilliseconds
	key := providerConnectedPrefix + record.ID
	// Retained native publisher evidence and the retained program path keep
	// replay text stable across a later registry display-name edit.
	app := programStem(permission.Targets[0].Program)
	if publisher.Trusted && publisher.Description != "" {
		app = programDisplayName(publisher, permission.Targets[0].Program)
	}
	if runes := []rune(app); len(runes) > maxProgramName {
		app = string(runes[:maxProgramName-1]) + "…"
	}
	request := askclient.NotificationRequest{RequestRef: askclient.RequestRef{Key: key, ReplayUntilMs: until}, Title: preset.Label + " connected", Body: app + " can use " + preset.Label + ".", Tag: key, Delivery: askclient.NotificationDeliveryNow, ExpiresMs: until, Actions: []askclient.NotificationAction{{ID: "undo", Title: "Undo", Activation: askclient.NotificationActivationApp}}}
	if !asks.ValidNotificationRequest(request) {
		return askclient.NotificationRequest{}, errProviderConnectionChanged
	}
	return request, nil
}

// Only the native presenter invokes this after applied rights and fresh setup
// readback. The original tool continuation posts no notice.
func completeProviderConnection(ctx context.Context, shown askclient.RecordMetadata, permission *askclient.PermissionRequest) error {
	operator, err := defaultPresenterOperator(ctx)
	if err != nil {
		return err
	}
	content, err := readQuestionContent(ctx, operator, panelAccount(ctx), shown.ID)
	read := content.Question.Details.Question.Question
	if err != nil || read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil || !sameQuestionContent(shown, *read.Record) || !samePermissionTargets(permission, content.Question.Details.Permission) || read.AnsweredBy != nil && *read.AnsweredBy == askclient.AnswererResponder {
		return errProviderConnectionChanged
	}
	request, err := providerCompletionRequest(*read.Record, permission, publisherOfRead(read))
	if err != nil {
		return err
	}
	notifications, err := panelMachine().ResolveAsksNotifications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	return reconcileProviderCompletion(ctx, notifications, request)
}

type providerCompletionNotifications interface {
	ObserveContext(context.Context, askclient.RequestRef, int64) (askclient.NotificationObservation, error)
	PostContext(context.Context, askclient.NotificationRequest) (askclient.NotificationObservation, error)
}

func reconcileProviderCompletion(ctx context.Context, notifications providerCompletionNotifications, request askclient.NotificationRequest) error {
	observed, err := notifications.ObserveContext(ctx, request.RequestRef, 0)
	if err != nil {
		return err
	}
	switch observed.Outcome {
	case askclient.NotificationOutcomePosted, askclient.NotificationOutcomeActed, askclient.NotificationOutcomeDismissed, askclient.NotificationOutcomeWithdrawn, askclient.NotificationOutcomeExpired, askclient.NotificationOutcomeGone:
		return nil
	case askclient.NotificationOutcomeUnknown:
	default:
		return errProviderConnectionChanged
	}
	posted, err := notifications.PostContext(ctx, request)
	if err != nil {
		return err
	}
	switch posted.Outcome {
	case askclient.NotificationOutcomePosted, askclient.NotificationOutcomeActed, askclient.NotificationOutcomeDismissed, askclient.NotificationOutcomeWithdrawn, askclient.NotificationOutcomeExpired, askclient.NotificationOutcomeGone:
		return nil
	}
	return errProviderConnectionChanged
}

// The native poster and retained occurrence are checked by the caller. The
// title is cosmetic; every routing, body, action and expiry field stays exact.
// This keeps Undo valid for notices retained before a display-copy correction.
func sameProviderCompletionRequest(expected, actual askclient.NotificationRequest) bool {
	expected.Title = actual.Title
	return reflect.DeepEqual(expected, actual)
}

type providerUndoRules interface {
	ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error)
	RevokeAnswerPermitsContext(context.Context, string, rightsclient.Subject, rightsclient.Subject, []string, string, string) (rightsclient.PolicyEdit, error)
}

func undoProviderPermits(ctx context.Context, rules providerUndoRules, self rightsclient.Subject, id string, permission *askclient.PermissionRequest) error {
	if _, ok := providerForPermission(permission); !ok || !providerOccurrenceID(id) {
		return errProviderConnectionChanged
	}
	for _, target := range permission.Targets {
		applied := false
		for attempt := 0; attempt < 3; attempt++ {
			page, err := rules.ListPolicyContext(ctx, "", 1)
			if err != nil || page.Outcome != rightsclient.PolicyPageOutcomePage || page.Revision == "" {
				return errProviderConnectionChanged
			}
			edit, err := rules.RevokeAnswerPermitsContext(ctx, page.Revision, self, rightsclient.Subject{Account: target.Account, Program: target.Program}, []string{target.Action}, target.Resource, id)
			if err != nil {
				return err
			}
			if edit.Outcome == rightsclient.PolicyEditOutcomeApplied {
				applied = true
				break
			}
			// The native owner rejects duplicate state before this result. At
			// the unchanged expected revision, conflict means this occurrence
			// has no still-current permit here: a replay or person replacement.
			if edit.Outcome == rightsclient.PolicyEditOutcomeConflict && edit.Revision == page.Revision {
				applied = true
				break
			}
			if edit.Outcome != rightsclient.PolicyEditOutcomeConflict {
				return errProviderConnectionChanged
			}
		}
		if !applied {
			return errProviderConnectionChanged
		}
	}
	return nil
}

func activatePanelApplicationNotification(ctx context.Context, record askclient.NotificationRecord, action string) error {
	if handled, err := activatePresenterNotice(record, action); handled {
		return err
	}
	if !strings.HasPrefix(record.Request.RequestRef.Key, providerConnectedPrefix) {
		return activateAgentUpdateNotification(ctx, record, action)
	}
	id := strings.TrimPrefix(record.Request.RequestRef.Key, providerConnectedPrefix)
	if action != "undo" || !providerOccurrenceID(id) || record.State != askclient.NotificationStateActed || record.Result == nil || record.Result.Action != "undo" {
		return errProviderConnectionChanged
	}
	_, self, err := (panelApplicationReads{}).resolve(ctx)
	if err != nil || !identity.SameSubjectProgram(self.Program, record.Program) {
		return errors.New("connection notice is not owned by this Panel")
	}
	operator, err := defaultPresenterOperator(ctx)
	if err != nil {
		return err
	}
	content, err := readQuestionContent(ctx, operator, self.Account, id)
	read := content.Question.Details.Question.Question
	if err != nil || read.Record == nil || read.Outcome != askclient.OperatorQuestionOutcomeFound || read.AnsweredBy != nil && *read.AnsweredBy == askclient.AnswererResponder {
		return errProviderConnectionChanged
	}
	expected, err := providerCompletionRequest(*read.Record, content.Question.Details.Permission, publisherOfRead(read))
	if err != nil || !sameProviderCompletionRequest(expected, record.Request) {
		return errProviderConnectionChanged
	}
	rules, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	return undoProviderPermits(ctx, rules, self, id, content.Question.Details.Permission)
}
