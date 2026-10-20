package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// messagesCauseLog receives one line per failed native Messages reply. The
// MCP host keeps a stdio server's stderr as that server's log.
var messagesCauseLog io.Writer = os.Stderr

// messageReplyCause names why a native Messages call produced no result, for
// the tool result and for the host's log of this server. A refusal by this
// image's generated decoder means this integration and the runtime read
// messages@1 differently: one was updated without the other. A refusal never
// proves that the runtime did not act, so the outcome stays unknown.
func messageReplyCause(method string, err error) string {
	var refusal *wire.Refusal
	var dispatch wire.DispatchError
	var service *wire.ServiceError
	switch {
	case errors.As(err, &service) && (service.Code == wire.ServiceErrorCodeUnknownMethod || service.Code == wire.ServiceErrorCodeUnknownVersion || service.Code == wire.ServiceErrorCodeUnknownService):
		return "dispatch_" + string(service.Code)
	case errors.As(err, &refusal):
		cause := "reply_refused_" + refusal.Word
		fmt.Fprintf(messagesCauseLog, "OA_MESSAGES %s %s: this OpenAbstractions integration cannot read the runtime's reply (%v). The runtime and this integration differ in version; update the agent connection in OpenAbstractions.\n", method, cause, err)
		return cause
	case errors.As(err, &dispatch):
		cause := "dispatch_" + string(dispatch)
		if len(cause) > 64 {
			cause = "dispatch_refused"
		}
		fmt.Fprintf(messagesCauseLog, "OA_MESSAGES %s %s: the runtime refused this integration's request frame. The runtime and this integration differ in version; update the agent connection in OpenAbstractions.\n", method, cause)
		return cause
	}
	cause := "reply_lost"
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded):
		// The runtime did not answer within the native call's budget.
		cause = messageReplyTimeout
	case errors.Is(err, context.Canceled):
		cause = "reply_cancelled"
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed):
		cause = "reply_connection_closed"
	}
	fmt.Fprintf(messagesCauseLog, "OA_MESSAGES %s %s: %v\n", method, cause, err)
	return cause
}

// messageReplyTimeout is a native call the runtime did not answer within the
// call's budget. The runtime may still have acted.
const messageReplyTimeout = "reply_timeout"

// messageVersionSkew reports whether cause names a disagreement between this
// integration and the runtime.
func messageVersionSkew(cause string) bool {
	return strings.HasPrefix(cause, "reply_refused_") || strings.HasPrefix(cause, "dispatch_")
}

// messagePrepareLostGuidance says what a Prepare without a reply means.
func messagePrepareLostGuidance(cause string) string {
	switch cause {
	case messageReplyTimeout:
		return "OpenAbstractions did not answer within 35 seconds because it is busy. Nothing was sent by this operation. Try again once; if it repeats, tell the person OpenAbstractions is too busy to message now."
	case "reply_connection_closed":
		return "The connection to OpenAbstractions closed before it answered, as when OpenAbstractions restarts or updates. Nothing was sent by this operation. Try again once."
	}
	return "Native preparation reply unavailable; nothing was sent by this operation."
}

// messageReceiverReplyError carries a version-skew cause out of discovery.
type messageReceiverReplyError struct{ cause string }

func (e *messageReceiverReplyError) Error() string { return "messages: discovery reply " + e.cause }

const messageVersionSkewGuidance = "This OpenAbstractions connection is a different version from the OpenAbstractions runtime and cannot read its reply. Tell the person to update the agent connection in OpenAbstractions; retrying does not help until then."
