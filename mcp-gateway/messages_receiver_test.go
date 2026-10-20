package main

import (
	"context"
	"errors"
	"testing"
	"time"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestVersionedReceiverDiscoveryPreservesExactTargetAndAmbiguity(t *testing.T) {
	first, second := "oa-claude-integration-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "oa-claude-integration-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	classify := func(name string) string {
		if name == first || name == second || name == "oa-claude-integration" {
			return "oa-claude-integration"
		}
		return ""
	}
	for _, mode := range []string{"exact", "same-application-contexts", "ambiguous", "old-and-new", "context", "foreign", "unavailable", "page-gap"} {
		t.Run(mode, func(t *testing.T) {
			rows := []wire.Inbox{{Program: "app:" + first, Context: "chosen"}}
			conversation := ""
			switch mode {
			case "same-application-contexts":
				rows = append(rows, wire.Inbox{Program: "app:" + first, Context: "other"})
			case "ambiguous":
				rows = append(rows, wire.Inbox{Program: "app:" + second, Context: "other"})
			case "old-and-new":
				rows = append(rows, wire.Inbox{Program: "app:oa-claude-integration", Context: "old"})
			case "context":
				rows = append(rows, wire.Inbox{Program: "app:" + second, Context: "other"})
				conversation = "chosen"
			case "foreign":
				rows = []wire.Inbox{{Program: "app:" + first + "x", Context: "chosen"}}
			}
			read := func(q wire.InboxQuery) (wire.InboxPage, error) {
				if q.Receiver != "" {
					t.Fatal("family grammar used as receiver authority", q)
				}
				if mode == "unavailable" {
					return wire.InboxPage{Outcome: wire.OutcomeUnavailable}, nil
				}
				if mode == "page-gap" {
					if q.Cursor == "" {
						return wire.InboxPage{Outcome: wire.OutcomePage, Cursor: "next", Inboxes: rows}, nil
					}
					return wire.InboxPage{Outcome: wire.OutcomeGap}, nil
				}
				return wire.InboxPage{Outcome: wire.OutcomePage, Inboxes: rows}, nil
			}
			name, contextName, err := discoverMessageReceiver(context.Background(), "oa-claude-integration", conversation, read, classify)
			if mode == "same-application-contexts" {
				if err != nil || name != first || contextName != "" {
					t.Fatal("native default selection bypassed", name, contextName, err)
				}
				return
			}
			if mode == "exact" || mode == "context" {
				if err != nil || name != first || contextName != "chosen" {
					t.Fatal(name, contextName, err)
				}
				return
			}
			if mode == "ambiguous" || mode == "old-and-new" {
				if !errors.Is(err, errMessageReceiverAmbiguous) {
					t.Fatal(err)
				}
				var choices *messageReceiverChoicesError
				if !errors.As(err, &choices) || len(choices.choices) != 2 {
					t.Fatal("named choices lost", err)
				}
				return
			}
			want := errMessageReceiverUnavailable
			if mode == "foreign" {
				want = errMessageReceiverNotListed
			}
			if !errors.Is(err, want) || name != "" || contextName != "" {
				t.Fatal(name, contextName, err)
			}
		})
	}
}

type retainedAccessAnswer struct {
	messagePermissionClient
	calls int
}

func (p *retainedAccessAnswer) RequestContext(context.Context, askswire.PermissionIntent) (askswire.PermissionObservation, error) {
	p.calls++
	return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeAnswered,
		Answer: &askswire.Answer{ID: "durable-question", Option: "allow"}}, nil
}

type accessRosterRead struct {
	*messagesProbe
	read func() (wire.InboxPage, error)
}

func (p accessRosterRead) InboxesList(wire.InboxQuery) (wire.InboxPage, error) { return p.read() }

func TestMessagesAccessFriendlyReadbackPreservesDurableAnswer(t *testing.T) {
	for _, mode := range []string{"unlisted", "unavailable", "deadline", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			parent, stop := context.WithCancel(t.Context())
			defer stop()
			ctx, cancel := context.WithTimeout(parent, 100*time.Millisecond)
			defer cancel()
			reads := 0
			permission := &retainedAccessAnswer{}
			b := &messagesBackend{contextName: "original-chat",
				permission: func(context.Context) (messagePermissionClient, error) { return permission, nil },
				native: func(wait context.Context) (nativeMessages, error) {
					return accessRosterRead{read: func() (wire.InboxPage, error) {
						reads++
						switch mode {
						case "canceled":
							stop()
							return wire.InboxPage{}, wait.Err()
						case "deadline":
							<-wait.Done()
							return wire.InboxPage{}, wait.Err()
						case "unavailable":
							return wire.InboxPage{Outcome: wire.OutcomeUnavailable}, nil
						default:
							return wire.InboxPage{Outcome: wire.OutcomePage, NeedsPermission: []wire.AgentApplication{
								{Program: "app:oa-claude-integration-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
							}}, nil
						}
					}}, nil
				}}
			b.checkAccess = func(wait context.Context, receiver string) (askswire.PermissionObservation, error) {
				// Use the actual versioned-roster resolver and production
				// result mapping on all test platforms.
				return b.checkMessageAccess(wait, receiver, b.resolveVersionedMessageReceiver,
					func(context.Context, string) (rightswire.Decision, error) {
						t.Fatal("no authorized inbox selected")
						return rightswire.Decision{}, nil
					})
			}
			out, err := b.MessagesRequestAccess(ctx, gateway.MessagesAccessInput{Receiver: "Claude", IdempotencyKey: "same-request"})
			if permission.calls != 1 || reads != 1 {
				t.Fatal("expected one Request and one roster read", permission.calls, reads)
			}
			if mode == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("parent cancellation was lost", out, err)
				}
				return
			}
			// An Allow without a confirmed saved permission never reads as an
			// answer; the same receiver and key retry it.
			if err != nil || out.Outcome != askswire.PermissionOutcomeUnavailable || out.Answer != nil || out.PolicyRevision != "" {
				t.Fatal("unconfirmed Allow was reported or promoted", out, err)
			}
			if (mode == "unlisted" || mode == "deadline") && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal("expected the unconfirmed readback to end at its deadline")
			}
		})
	}
}
