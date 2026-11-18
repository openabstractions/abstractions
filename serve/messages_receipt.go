package main

import (
	"errors"
	"strings"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Preserve the authority owner's observed decision; an outage supplies no
// denied/not-granted conclusion and carries no invented refusal detail.
func messageRightsRefusal(decision rights.Decision) *rights.RefusalDetail {
	detail := &rights.RefusalDetail{Source: rights.RefusalSourceRights, Action: ActionApplicationMessage}
	switch decision.Outcome {
	case rights.DecisionOutcomeDenied:
		detail.Decision, detail.Ask = rights.RefusalDecisionDenied, rights.RefusalAskNone
	case rights.DecisionOutcomeNotGranted:
		detail.Decision, detail.Ask = rights.RefusalDecisionNotGranted, rights.RefusalAskAvailable
	default:
		return nil
	}
	return detail
}

// Acceptance identity stays immutable. Observation flags come from the
// original canonical acceptance, including for an independently keyed alias.
// This performs no admission, first-use question, queue write or budget charge.
func (r *messagesReceiver) projectSendReceipt(result msg.SendResult) msg.SendResult {
	if result.Outcome != msg.OutcomeSent {
		return result
	}
	store := r.host.messages
	original, key, err := store.readByID(r.ctx, result.MessageID)
	if err != nil || original == nil {
		result.Outcome = msg.OutcomeUnknown
		result.ReceiverWatching = false
		r.reportMessageError("receipt projection", errors.New("canonical acceptance observation unavailable"))
		return result
	}
	if err = r.host.flushMessageAudit(r.ctx, key, original.Token); err != nil {
		r.reportMessageError("audit append pending", err)
	}
	if original.Lost {
		result.ReceiverWatching = false
		if original.Actor.Remote != nil {
			result.Refused = original.Refused
		}
		return result
	}
	state, _, err := r.host.reconcileMessageState(r.ctx, key, original)
	if err != nil {
		// A quiet canonical state read still reports the service's confirmed hold
		// while its external eligibility/permission source is unavailable.
		state, err = store.currentState(r.ctx, key, original)
		if err != nil {
			result.Outcome = msg.OutcomeUnknown
			result.ReceiverWatching = false
			return result
		}
	}
	request, err := decodeRetainedMessageRequest(original.Spec)
	if err != nil {
		result.Outcome = msg.OutcomeUnknown
		result.ReceiverWatching = false
		return result
	}
	result.AwaitingPermission = state.Held && !state.Refused
	if original.Actor.Remote != nil {
		result.Refused = state.Refused
	}
	result.ReceiverWatching = r.host.messageIsWatching(r.ctx, original.Actor.Account, original.Receiver, request.Context)
	// A receiver whose wake is stuck behind an unconfirmed pointer holds its
	// watch but wakes no chat. On 2026-11-22 Codex saw no message after 2941
	// while every send reported receiver_watching=true.
	if application := strings.TrimPrefix(request.To, "app:"); result.ReceiverWatching && r.host.directory != nil && messageWakeUnconfirmed(application, r.host.directory.messageContextStatus(r.ctx, application, request.Context)) {
		result.ReceiverWatching = false
	}
	// A retained Codex chat whose helper has gone is watching while the
	// runtime would wake it (architect ruling 1, 2026-11-22); otherwise the
	// send says Codex is closed.
	if !result.ReceiverWatching && original.Actor.Remote == nil && request.Withdraw == "" {
		if codex, wakeable := r.host.messageCodexChatWakeable(r.ctx, original, request); wakeable {
			result.ReceiverWatching = true
		} else if codex && result.Reason == "" {
			result.Reason = messageCodexClosedReason
		}
	}
	// A local sent result names the chat it reached (architect ruling
	// 2026-11-12, (a)); a retry reads the same original request.
	if original.Actor.Remote == nil && original.Forward == nil && request.Withdraw == "" {
		result.Context = request.Context
		if !state.Held && !state.Refused {
			result.ChatTitle = r.messageChatTitle(request.To, request.Context)
		}
	}
	return result
}
