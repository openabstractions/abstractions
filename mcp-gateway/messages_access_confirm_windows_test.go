//go:build windows

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// gapThenListedInboxes answers the first listing gap, as the runtime does
// when the listing changes between its two reads, then lists the receiver.
type gapThenListedInboxes struct {
	nativeMessages
	lists atomic.Int64
}

func (g *gapThenListedInboxes) InboxesList(msg.InboxQuery) (msg.InboxPage, error) {
	if g.lists.Add(1) == 1 {
		return msg.InboxPage{Outcome: msg.OutcomeGap}, nil
	}
	mine := false
	return msg.InboxPage{Outcome: msg.OutcomePage, Inboxes: []msg.Inbox{{Program: "app:oa-claude-integration", Context: "chat-receiver", Mine: &mine}}}, nil
}

// Journey J-MSG, 2026-11-10: the person allowed within the access request's
// wait, and the call answered "not saved yet". Two causes: the answer and its
// rule are separate commits, and the rule can land after the person's 4 s
// wait; and the first inbox listing after the rule answered gap, which the
// confirmation read as final. An Allow at 2 s now reads allowed.
func TestMessagesAccessAllowWithinWaitReadsAllowed(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "rule-with-answer"
		if late {
			name = "rule-after-wait"
		}
		t.Run(name, func(t *testing.T) {
			book, policy, client, operator, _ := newMessageAccessFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var questionID atomic.Value
			inboxes := new(gapThenListedInboxes)
			backend := &messagesBackend{contextName: "active-native-call", permission: func(context.Context) (messagePermissionClient, error) { return client, nil },
				native: func(context.Context) (nativeMessages, error) { return inboxes, nil }}
			backend.checkAccess = func(ctx context.Context, receiver string) (askswire.PermissionObservation, error) {
				return backend.checkMessageAccess(ctx, receiver, backend.resolveMessageReceiver, func(ctx context.Context, resource string) (rwire.Decision, error) {
					id, _ := questionID.Load().(string)
					record, found, err := book.ApplicationRecord(id)
					if err != nil || !found {
						return rwire.Decision{Outcome: rwire.DecisionOutcomeNotGranted}, nil
					}
					projected, _, _ := grants.AgentMessageLegacyEffect(record.TypedPermission.Effect)
					decision := policy.CheckLegacyPermissionEffectContext(ctx, projected, "")
					if len(decision.Outcomes) == 1 && decision.Outcomes[0] == rwire.DecisionOutcomePermitted {
						return rwire.Decision{Outcome: rwire.DecisionOutcomePermitted, PolicyRevision: decision.Revision}, nil
					}
					return rwire.Decision{Outcome: rwire.DecisionOutcomeNotGranted, PolicyRevision: decision.Revision}, nil
				})
			}
			type result struct {
				out askswire.PermissionObservation
				err error
			}
			finished := make(chan result, 1)
			go func() {
				out, err := backend.MessagesRequestAccess(ctx, gateway.MessagesAccessInput{Receiver: "Claude", IdempotencyKey: "allow-within-wait"})
				finished <- result{out, err}
			}()
			for questionID.Load() == nil {
				pending, err := book.PendingTypedPermissions()
				if err != nil {
					t.Fatal(err)
				}
				if len(pending) == 1 {
					questionID.Store(pending[0].ID)
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			id := questionID.Load().(string)
			record, _, _ := book.ApplicationRecord(id)
			// The person allows 2 s into the 4 s wait.
			time.Sleep(2 * time.Second)
			if _, err := operator.AnswerPermissionIfPendingContext(ctx, id, "allow"); err != nil {
				t.Fatal(err)
			}
			if late {
				// A busy runtime saves the rule after the person's wait ended.
				time.Sleep(messageAccessWait - 2*time.Second + 500*time.Millisecond)
			}
			projected, _, _ := grants.AgentMessageLegacyEffect(record.TypedPermission.Effect)
			revision, _, _ := policy.SelectorSnapshotContext(ctx)
			edit, err := policy.ApplyLegacyPermissionEffect(ctx, revision, projected, rights.RuleEdit{By: projected.Subject, Exact: true}, record.ID, []string{record.ID}, func() error { return nil })
			if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
				t.Fatal(edit, err)
			}
			got := <-finished
			if got.err != nil || got.out.Outcome != askswire.PermissionOutcomePermitted || got.out.PolicyRevision == "" {
				t.Fatal("an Allow within the wait did not read allowed", got.out, got.err)
			}
			if inboxes.lists.Load() < 2 {
				t.Fatal("the gap listing was not listed again", inboxes.lists.Load())
			}
		})
	}
}
