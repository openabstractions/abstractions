package main

import (
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func (r *messagesReceiver) withdrawMessage(instance, sourceContext string, actor messageActor, request msg.SendRequest) (msg.SendResult, error) {
	result := msg.SendResult{Outcome: msg.OutcomeForbidden}
	if request.Kind != msg.KindNote || request.ReplyTo != "" || request.AnswerByUnixMs != 0 {
		result.Outcome = msg.OutcomeInvalid
		return result, nil
	}
	question, key, err := r.host.messages.readByID(r.ctx, request.Withdraw)
	if err != nil {
		result.Outcome = msg.OutcomeUnavailable
		return result, nil
	}
	if question == nil || question.Actor.Account != actor.Account || question.Actor.Program != actor.Program || question.Actor.Context != sourceContext {
		return result, nil
	}
	original, err := decodeRetainedMessageRequest(question.Spec)
	if err != nil {
		result.Outcome = msg.OutcomeUnavailable
		return result, nil
	}
	if request.To != original.To || request.Context != original.Context {
		return result, nil
	}
	if original.Kind != msg.KindQuestion {
		result.Outcome = msg.OutcomeInvalid
		return result, nil
	}
	allowed, err := r.host.directory.withNativeMessageOwner(r.ctx, r.call, instance, sourceContext, nil, func(_ *nativeInbox) error {
		target := &messageReplyTarget{key: key, token: question.Token, operation: "cancel"}
		result = r.host.messages.acceptReply(r.ctx, actor, question.Receiver, request, false, question.ContextID, target)
		return nil
	})
	if err != nil {
		result.Outcome = msg.OutcomeUnknown
	} else if !allowed {
		result.Outcome = msg.OutcomeForbidden
	}
	if result.Outcome == msg.OutcomeSent {
		control, controlKey, e := r.host.messages.readByID(r.ctx, result.MessageID)
		if e == nil && control != nil {
			e = r.host.messages.completeWithdraw(r.ctx, control, controlKey)
		}
		if e != nil {
			r.reportMessageError("cancellation projection", e)
		}
	}
	return r.projectSendReceipt(result), nil
}
