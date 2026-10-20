package main

import (
	"context"
	"errors"
	"runtime"
	"strings"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

var errMessageReceiverAmbiguous = errors.New("authorized receiver ambiguous")
var errMessageReceiverUnavailable = errors.New("authorized receiver unavailable")
var errMessageReceiverNotListed = errors.New("authorized receiver not listed")

type messageReceiverChoicesError struct{ choices []wire.Inbox }

func (e *messageReceiverChoicesError) Error() string { return errMessageReceiverAmbiguous.Error() }
func (e *messageReceiverChoicesError) Unwrap() error { return errMessageReceiverAmbiguous }

// Used only for authorized access confirmation. Initial Prepare/RequestAccess
// family resolution belongs to the native registered-application owner.
func (b *messagesBackend) resolveMessageReceiver(ctx context.Context, label, conversation string) (string, string, error) {
	if strings.HasPrefix(label, "app:") {
		return strings.TrimPrefix(label, "app:"), conversation, nil
	}
	base := gateway.MessageReceiverName(label)
	if _, connector := nativecli.AgentTitle(base); runtime.GOOS != "darwin" || !connector {
		return base, conversation, nil
	}
	return b.resolveVersionedMessageReceiver(ctx, base, conversation)
}

func (b *messagesBackend) resolveVersionedMessageReceiver(ctx context.Context, base, conversation string) (string, string, error) {
	if b.native == nil {
		return "", "", errMessageReceiverUnavailable
	}
	c, err := b.native(ctx)
	if err != nil {
		return "", "", err
	}
	return discoverMessageReceiver(ctx, base, conversation, c.InboxesList, nativecli.AgentApplicationBase)
}

func discoverMessageReceiver(ctx context.Context, base, conversation string, list func(wire.InboxQuery) (wire.InboxPage, error), classify func(string) string) (string, string, error) {
	cursor := ""
	seen := map[string]bool{}
	var selected *wire.Inbox
	var choices []wire.Inbox
	ambiguous := false
	restarts := 0
	for {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		page, err := list(wire.InboxQuery{Cursor: cursor})
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", "", err
			}
			if cause := messageReplyCause("InboxesList", err); messageVersionSkew(cause) {
				return "", "", &messageReceiverReplyError{cause: cause}
			}
			return "", "", errMessageReceiverUnavailable
		}
		if page.Outcome == wire.OutcomeGap && restarts < 3 {
			// The listing changed between reads: list again from the start.
			restarts++
			cursor, seen, selected, choices, ambiguous = "", map[string]bool{}, nil, nil, false
			continue
		}
		if page.Outcome != wire.OutcomePage {
			return "", "", errMessageReceiverUnavailable
		}
		// A counted program stands for its chats without naming one.
		rows := page.Inboxes
		if conversation == "" {
			for _, other := range page.OtherChats {
				rows = append(rows, wire.Inbox{Program: other.Program})
			}
		}
		for _, inbox := range rows {
			if inbox.Mine != nil && *inbox.Mine || !strings.HasPrefix(inbox.Program, "app:") || classify(strings.TrimPrefix(inbox.Program, "app:")) != base || (conversation != "" && inbox.Context != conversation) {
				continue
			}
			if selected != nil {
				choices = append(choices, inbox)
				if len(choices) > 64 {
					return "", "", errMessageReceiverUnavailable
				}
				if conversation == "" && selected.Program == inbox.Program {
					selected.Context = ""
					continue
				}
				ambiguous = true
				continue
			}
			copy := inbox
			selected = &copy
			choices = append(choices, inbox)
		}
		if page.Cursor == "" {
			break
		}
		if seen[page.Cursor] {
			return "", "", errMessageReceiverUnavailable
		}
		seen[page.Cursor] = true
		cursor = page.Cursor
	}
	if selected == nil {
		return "", "", errMessageReceiverNotListed
	}
	if ambiguous {
		return "", "", &messageReceiverChoicesError{choices: choices}
	}
	return strings.TrimPrefix(selected.Program, "app:"), selected.Context, nil
}
