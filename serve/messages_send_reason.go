package main

import (
	"strings"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

const messageSendLifetimeMs = int64(7 * 24 * time.Hour / time.Millisecond)

// Send refuses a malformed request with one sentence naming the field, so a
// sender that built its arguments by hand can correct them. SendResult.Reason
// holds at most 64 bytes.
func messageSendInvalidReason(request msg.SendRequest, now int64) string {
	switch {
	case !strings.HasPrefix(request.To, "app:") || !providerName.MatchString(strings.TrimPrefix(request.To, "app:")):
		return "to must name the receiver as app:<name>"
	case !applicationContextName.MatchString(request.Context):
		return "context must be a listed receiver context"
	case !nativeMessageText(request.Subject, "", "key"):
		return "subject must be one line of at most 80 characters"
	case !nativeMessageText("", request.Body, "key"):
		return "body must be UTF-8 of at most 8192 bytes"
	case !nativeMessageText("", "", request.IdempotencyKey):
		return "idempotency_key must be 1 to 128 bytes"
	}
	return messageSendTimesReason(request, now)
}

// Both local and remote acceptance own the same lifetime rule.
func messageSendTimesReason(request msg.SendRequest, now int64) string {
	switch {
	case request.ExpiresUnixMs <= now || request.ExpiresUnixMs > now+messageSendLifetimeMs:
		return "expires_unix_ms must be within the next 7 days"
	case request.Kind == msg.KindQuestion && request.AnswerByUnixMs == 0:
		return "a question needs answer_by_unix_ms"
	case request.Kind == msg.KindQuestion && (request.AnswerByUnixMs <= now || request.AnswerByUnixMs > request.ExpiresUnixMs):
		return "answer_by_unix_ms must be after now and by expires_unix_ms"
	}
	return ""
}
