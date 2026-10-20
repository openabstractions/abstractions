package main

import (
	"unicode/utf8"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// A send the generated grammar refuses names its first malformed field in one
// sentence of at most 64 bytes. The native service names policy refusals
// (deadlines, receiver and context) itself.
func messageSendShapeReason(in gateway.MessagesSendInput) string {
	if _, ok := wire.ParseKind(in.Kind); !ok {
		return "kind must be question, note or reply"
	}
	switch {
	case !messageSendPrintable(in.To, 1, 68):
		return "to must name the receiver as app:<name>"
	case !messageSendPrintable(in.Context, 1, 64):
		return "context must be a listed receiver context"
	case !utf8.ValidString(in.Subject) || len(in.Subject) > 320:
		return "subject must be one line of at most 80 characters"
	case !utf8.ValidString(in.Body) || len(in.Body) > 8192:
		return "body must be UTF-8 of at most 8192 bytes"
	case in.IdempotencyKey == "" || len(in.IdempotencyKey) > 128:
		return "idempotency_key must be 1 to 128 bytes"
	case len(in.ReplyTo) > 128:
		return "reply_to must be a received message ID"
	case len(in.Withdraw) > 128:
		return "withdraw must be a sent message ID"
	case in.PeerKey != "" && !messageSendPrintable(in.PeerKey, 64, 64):
		return "peer_key must be 64 characters"
	}
	return "send arguments do not match oa_messages_send"
}

func messageSendPrintable(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, c := range value {
		if c < 0x20 || c >= 0x7f && c <= 0x9f {
			return false
		}
	}
	return true
}
