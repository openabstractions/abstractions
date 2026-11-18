package main

import (
	"context"
	"encoding/json"
	"fmt"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"strconv"
	"strings"
	"time"
)

// The runtime's Prepare reason for a Codex chat whose presence status says
// its wake went unconfirmed (messagesWakeUnconfirmedText).
const messagePrepareWakeUnconfirmed = "wake_unconfirmed"

func (b *messagesBackend) MessagesPrepare(ctx context.Context, in gateway.MessagesPrepareInput) (gateway.MessagesPrepareOutput, error) {
	// Native registration/enrollment resolves family aliases before access;
	// permission-filtered inbox discovery cannot resolve a first-use target.
	receiver, conversation := gateway.MessageReceiverName(in.Receiver), in.Context
	request := wire.PrepareRequest{Receiver: gateway.MessageReceiverResource(receiver), Context: conversation, ContextTitle: in.ContextTitle, Subject: in.Subject, Body: in.Body, TTLMs: in.TTLMs, AnswerWithinMs: in.AnswerWithinMs}
	if in.Kind != "" {
		kind, ok := wire.ParseKind(in.Kind)
		if !ok {
			return gateway.MessagesPrepareOutput{Outcome: "invalid", Guidance: "Invalid message kind."}, nil
		}
		request.Kind = &kind
	}
	c, err := b.native(ctx)
	if err != nil {
		return gateway.MessagesPrepareOutput{Outcome: "unavailable", Guidance: "Native message preparation unavailable."}, nil
	}
	out, err := c.Prepare(b.inboxInstance(), b.contextName, request)
	if err != nil {
		cause := messageReplyCause("Prepare", err)
		if messageVersionSkew(cause) {
			return gateway.MessagesPrepareOutput{Outcome: "unknown", Reason: cause, Guidance: messageVersionSkewGuidance + " Nothing was sent by this operation."}, nil
		}
		return gateway.MessagesPrepareOutput{Outcome: "unknown", Reason: cause, Guidance: messagePrepareLostGuidance(cause)}, nil
	}
	result := gateway.MessagesPrepareOutput{Outcome: out.Outcome.String(), Guidance: out.Reason, Choices: out.Choices}
	// Preserve the established MCP presentation of native reason codes. The
	// native service remains the owner of discovery and selection decisions.
	switch {
	case out.Outcome == wire.OutcomeForbidden && strings.HasPrefix(out.Reason, "access:") && len(out.Reason) > len("access:"):
		result.Reason = out.Reason
		result.Guidance = fmt.Sprintf("Use oa_messages_request_access with receiver %q and a stable idempotency_key. Only the person can allow this direction. If access is pending or unconfirmed, tell the person to ask their agent to continue in the same chat, then end the turn. It will check access again before sending. When the person asks you to continue, use the same receiver and idempotency_key. Prepare and send only after permission is confirmed.", strings.TrimPrefix(out.Reason, "access:"))
	case out.Outcome == wire.OutcomeUnavailable && out.Reason == "not_found":
		result.Outcome = "not_found"
		result.Guidance = "No application is registered with this receiver name."
	case out.Outcome == wire.OutcomeUnavailable && out.Reason == "discovery_incomplete":
		result.Reason = out.Reason
		result.Guidance = "Inbox discovery could not be confirmed; prepare again. Nothing was sent."
	case out.Outcome == wire.OutcomeConflict && out.Reason == "ambiguous_receiver":
		result.Outcome = "ambiguous_receiver"
		if len(out.Choices) == 0 {
			result.Guidance = "More than one registered application matches this receiver. Use authorized inbox discovery to identify the intended application. Ask the person to clarify which application they meant when needed. Keep internal identifiers out of the conversation."
			break
		}
		if in.Context == "" && in.ContextTitle == "" {
			result.Guidance = messageProgramChoiceGuidance(gateway.MessageAgentTitle(request.Receiver), out.Choices)
			break
		}
		result.Guidance = messagePrepareChoiceGuidance(out.Choices)
	case out.Outcome == wire.OutcomeUnavailable && out.Reason == "no_reachable_chat":
		result.Outcome = "no_reachable_chat"
		program := gateway.MessageAgentTitle(request.Receiver)
		result.Guidance = "No " + program + " chat has OpenAbstractions open now, so nothing was sent. Tell the person to open a " + program + " chat with OpenAbstractions connected, or to name the chat to message."
	case out.Outcome == wire.OutcomeUnavailable && out.Reason == "receiver_lapsed":
		result.Outcome = "receiver_lapsed"
		result.Guidance = "The receiver's last OpenAbstractions connection stopped renewing without closing: its host ended it or it failed. Tell the person that agent's OA connection failed and needs its MCP server restarted or its chat reopened. Do not guess or ask the person for internal identifiers."
	case out.Outcome == wire.OutcomeUnavailable && out.Reason == "receiver_withdrawn":
		result.Outcome = "receiver_withdrawn"
		result.Guidance = "The receiver's last OpenAbstractions connection closed normally and none is open now. The person can reopen that agent's chat. Do not guess or ask the person for internal identifiers."
	case out.Outcome == wire.OutcomeInvalid && messagePrepareInvalidSteps[out.Reason] != "":
		result.Reason = out.Reason
		result.Guidance = messagePrepareInvalidSteps[out.Reason]
	case out.Outcome == wire.OutcomeUnavailable && out.Reason == "receiver_unavailable":
		result.Outcome = "receiver_unavailable"
		result.Guidance = "No authorized live inbox for that receiver is visible. Keep OpenAbstractions MCP connected in the intended agent app. After an update or closed connection, use that host's supported MCP restart/reconnect or close and reopen the client, then discover again. Permission alone does not keep an inbox open. Do not guess or ask the person for internal application/context identifiers. Tell the person to open a chat in that app with OpenAbstractions connected, then try again."
	}
	if out.Outcome != wire.OutcomePage || out.SendRequest == nil {
		return result, nil
	}
	var args gateway.MessagesSendInput
	if err := json.Unmarshal(wire.EncodeSendRequest(out.SendRequest), &args); err != nil {
		return gateway.MessagesPrepareOutput{Outcome: "unavailable", Guidance: "Native prepared request unavailable."}, nil
	}
	result.Outcome = "prepared"
	result.SendArguments = &args
	result.ChatTitle, result.Context = out.ChatTitle, args.Context
	result.Next = "Nothing is sent yet. Now call oa_messages_send with send_arguments exactly as returned, every field included."
	result.Expires = time.UnixMilli(args.ExpiresUnixMs).UTC().Format(time.RFC3339Nano)
	result.Guidance = "send_arguments.to and send_arguments.context name the receiver and its chat. Retain send_arguments before passing that exact object to oa_messages_send. Native Send rechecks authority and deadlines. Reconcile uncertain Send with identical original arguments on the same native connection; never prepare again or recompute deadlines."
	if out.Reason == wire.InboxReachQueued.String() {
		// The runtime selected an inbox no running reader holds and no start
		// can reach now: a closed chat, or one whose app is not running.
		result.Reach = out.Reason
		result.Tell = "That conversation is closed or cannot be woken now, so the message waits until the person next opens or uses it. Tell the person plainly that the chat is closed and the message is waiting there."
		result.Guidance += " That conversation is closed or cannot be woken now, so the message waits until the person next opens or uses it. After oa_messages_send answers sent, tell the person plainly that the chat is closed and the message is waiting there."
	}
	if out.Reason == messagePrepareWakeUnconfirmed {
		// The receiver's Codex chat holds an earlier wake Codex never took up,
		// or its last wake had an unknown outcome.
		result.Reach = wire.InboxReachQueued.String()
		result.Tell = "Codex did not confirm the last wake of that chat, so the message is queued and waits until that chat next reads its inbox. Tell the person: queued; Codex did not confirm the wake."
		result.Guidance += " Codex did not confirm the last wake of that chat, so the message is queued and waits until that chat next reads its inbox. After oa_messages_send answers sent, tell the person: queued; Codex did not confirm the wake."
	}
	return result, nil
}

// messagePrepareInvalidSteps names, for each refusal of the request's own
// fields, the one change that makes Prepare succeed.
var messagePrepareInvalidSteps = map[string]string{
	"lifetime": "Nothing was sent. Set ttl_ms from 1000 to 604800000 and at least as long as answer_within_ms, and leave answer_within_ms out of a note.",
	"kind":     "Nothing was sent. Set kind to question or note, or leave it out.",
	"text":     "Nothing was sent. Give a non-empty plain-text body of at most 8192 bytes and a one-line subject of at most 80 characters.",
	"receiver": "Nothing was sent. Name the receiver as Claude, Codex, OpenCode or app:<name>.",
}

const messageUnclearChoiceGuidance = " If the displayed titles do not identify the intended chat, ask the person to rename it in its app, then discover the choices again. OA connection last-seen times describe connection observations and cannot establish which chat the person used."

// messageProgramChoiceGuidance answers a program-only send that found several
// live chats (architect ruling P3, 2026-11-22). The sending agent chooses; the
// hints are shown and OA acts on none of them.
func messageProgramChoiceGuidance(program string, choices []wire.Inbox) string {
	titles := make([]string, 0, len(choices))
	for _, choice := range choices {
		title := "an unnamed chat"
		if choice.ChatTitle != "" {
			title = strconv.Quote(choice.ChatTitle)
		}
		if len(choice.Hints) != 0 {
			title += " (" + strings.Join(choice.Hints, ", ") + ")"
		}
		titles = append(titles, title)
	}
	return "Several " + program + " chats can receive. Choose the one this message belongs to, using the person's words and these hints, and resend with its context. Choices: " + strings.Join(titles, ", ") + ". Nothing was sent. Ask the person only if you can't choose."
}

// The native listing gives each chat that shares a host title a distinct
// title, so the choices name themselves for the model and the person.
func messagePrepareChoiceGuidance(choices []wire.Inbox) string {
	titles := make([]string, 0, len(choices))
	for _, choice := range choices {
		if choice.ChatTitle == "" {
			titles = append(titles, "an unnamed chat")
			continue
		}
		titles = append(titles, strconv.Quote(choice.ChatTitle))
	}
	lead := "More than one authorized chat matches: "
	if len(choices) == 1 {
		// A name that can pass to another chat is confirmed before sending.
		lead = "This chat matches, but its name can pass to another chat: "
	}
	guidance := lead + strings.Join(titles, ", ") + ". Call oa_messages_prepare again with the chosen choice's context as context, or ask the person which chat they mean when the titles do not decide it; keep the context out of the conversation."
	return guidance + messageUnclearChoiceGuidance
}
