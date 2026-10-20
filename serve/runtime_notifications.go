package main

import (
	"context"
	"errors"
	"fmt"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The rule that decides whether a program may post notifications (ASK-N1,
// ASK-N6). A person's block on it turns one program's notifications off.
const (
	notificationPostAction   = grants.NotificationPostAction
	notificationPostResource = grants.NotificationPostResource
)

// notificationPolicy decides one poster quietly: a permit admits, an explicit
// block suppresses, and no rule refuses until the account-wide default
// supplies one. A notification is never a first-use question.
func (a *runtimeAsks) notificationPolicy(ctx context.Context, peer *identity.Peer) error {
	err := a.rights.RequireQuietly(ctx, peer, notificationPostAction, notificationPostResource)
	if err == nil {
		return nil
	}
	var decision *rightsclient.DecisionError
	if errors.As(err, &decision) {
		switch decision.Outcome {
		case rwire.DecisionOutcomeDenied.String():
			return asks.ErrNotificationSuppressed
		case rwire.DecisionOutcomeNotGranted.String(), rwire.DecisionOutcomeForbidden.String():
			return err
		}
	}
	return errors.Join(asksservice.ErrAskPolicyUnavailable, err)
}

// notificationBlocked reports a live block on one program's notifications,
// for an action an operator records on that program's notification.
func (a *runtimeAsks) notificationBlocked(ctx context.Context, program string) error {
	decision := a.rights.decide(ctx, rwire.Subject{Account: a.rights.owner, Program: program}, notificationPostAction, notificationPostResource)
	switch decision.Outcome {
	case rwire.DecisionOutcomeDenied:
		return asks.ErrNotificationSuppressed
	case rwire.DecisionOutcomePermitted, rwire.DecisionOutcomeNotGranted:
		return nil
	}
	return fmt.Errorf("notification block decision %s", decision.Outcome)
}
