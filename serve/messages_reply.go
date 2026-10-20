package main

import (
	"context"
	"encoding/json"
	"errors"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

type messageReplyTarget struct{ key, token, operation string }

// A successfully accepted reply retains its own body/expiry. Repair the
// original question's outgoing completion after a lost projection response.
func (s *messageStore) completeReply(ctx context.Context, reply *messageRecord, key string) error {
	request, err := decodeRetainedMessageRequest(reply.Spec)
	if err != nil {
		return err
	}
	if request.Kind != msg.KindReply {
		return nil
	}
	question, questionKey, err := s.readByID(ctx, request.ReplyTo)
	if err != nil || question == nil {
		return err
	}
	state, err := s.currentState(ctx, questionKey, question)
	if err != nil {
		return err
	}
	if state.ReplyKey != key || state.ReplyToken != reply.Token {
		return errors.New("messages: accepted reply linkage changed")
	}
	if err := s.transitionState(ctx, questionKey, question, "complete"); err != nil {
		return err
	}
	// A reply proves its question reached the receiver. A reply held for
	// the reverse permit completes here at its release, past Reply's own
	// delivered mark.
	return s.finishDelivery(ctx, questionKey, question.Token, msg.DeliveryDelivered, "")
}

// questionAnswered reports a question its receiver completed or linked a
// reply to. State that cannot be read answers false; expiry then decides.
func (s *messageStore) questionAnswered(ctx context.Context, key string, record *messageRecord) bool {
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil || request.Kind != msg.KindQuestion {
		return false
	}
	state, err := s.currentState(ctx, key, record)
	if err != nil {
		return false
	}
	return state.TaskState == msg.TaskStateCompleted || state.ReplyKey != ""
}

func (s *messageStore) completeWithdraw(ctx context.Context, control *messageRecord, key string) error {
	request, err := decodeRetainedMessageRequest(control.Spec)
	if err != nil {
		return err
	}
	if request.Withdraw == "" || !control.Control {
		return nil
	}
	question, questionKey, err := s.readByID(ctx, request.Withdraw)
	if err != nil || question == nil {
		return err
	}
	state, err := s.currentState(ctx, questionKey, question)
	if err != nil {
		return err
	}
	if state.CancelKey != key || state.CancelToken != control.Token {
		return errors.New("messages: accepted cancellation linkage changed")
	}
	return s.transitionState(ctx, questionKey, question, "cancel")
}

func (s *messageStore) oppositeQuestionOperation(ctx context.Context, state messageState, operation string) (bool, error) {
	if state.RemoteReply != nil {
		return true, nil
	}
	if state.AcceptedTerminal != "" {
		return true, nil
	}
	key, token := state.CancelKey, state.CancelToken
	if operation == "cancel" {
		key, token = state.ReplyKey, state.ReplyToken
	}
	if key == "" {
		return false, nil
	}
	record, err := s.readRecord(ctx, key)
	if err != nil || record == nil || record.Token != token {
		return false, err
	}
	if operation != "cancel" {
		return record.Control, nil
	}
	if record.Profile != "messages-record@1" {
		return record.Terminal, nil
	}
	current, err := s.currentState(ctx, key, record)
	return record.Terminal || !record.Held || current.ReplyAuthorized, err
}

func (s *messageStore) questionTerminalIntent(ctx context.Context, state messageState) (string, error) {
	if state.RemoteReply != nil {
		if state.RemoteReply.Completed {
			return "complete", nil
		}
		if state.RemoteReply.Failed {
			return "fail", nil
		}
	}
	if state.AcceptedTerminal != "" {
		return state.AcceptedTerminal, nil
	}
	if state.CancelKey != "" {
		r, err := s.readRecord(ctx, state.CancelKey)
		if err != nil {
			return "", err
		}
		if r != nil && r.Token == state.CancelToken && r.Control {
			return "cancel", nil
		}
	}
	if state.ReplyKey != "" {
		r, err := s.readRecord(ctx, state.ReplyKey)
		if err != nil {
			return "", err
		}
		if r != nil && r.Token == state.ReplyToken && r.Terminal {
			return "complete", nil
		}
		if r != nil && r.Token == state.ReplyToken && r.Profile == "messages-record@1" {
			current, err := s.currentState(ctx, state.ReplyKey, r)
			if err != nil {
				return "", err
			}
			if !r.Held || current.ReplyAuthorized {
				return "complete", nil
			}
		}
	}
	return "", nil
}

func (s *messageStore) replyQuestion(ctx context.Context, target *messageReplyTarget) (*messageRecord, *msg.SendRequest, error) {
	r, err := s.readRecord(ctx, target.key)
	if err != nil || r == nil || r.Token != target.token || r.ExpiresUnixMs <= s.now().UnixMilli() {
		return nil, nil, errors.New("messages: reply question unavailable")
	}
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil || request.Kind != msg.KindQuestion || request.AnswerByUnixMs <= s.now().UnixMilli() {
		return nil, nil, errors.New("messages: reply deadline closed")
	}
	return r, request, nil
}

// The small question-owned reservation precedes reply publication. Replacing
// an interrupted absent reply fences its old publisher at this same state CAS.
// A published canonical reply remains authoritative across lost responses.
func (s *messageStore) reserveReply(ctx context.Context, target *messageReplyTarget, key, token string) error {
	_, _, err := s.replyQuestion(ctx, target)
	if err != nil {
		return err
	}
	return cas.ChangeLimitContext(ctx, s.statePath(target.key, target.token), messageStateLimit, func(raw []byte) ([]byte, error) {
		current, _, err := s.replyQuestion(ctx, target)
		if err != nil {
			return nil, err
		}
		state, err := readMessageState(raw, current)
		if err != nil {
			return nil, err
		}
		if (state.Held && target.operation != "cancel") || state.Refused || messageTerminal(state.TaskState) {
			return nil, errors.New("messages: question is not accepting replies")
		}
		if opposite, err := s.oppositeQuestionOperation(ctx, state, target.operation); err != nil || opposite {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("messages: question already has a terminal operation")
		}
		priorKey, priorToken := state.ReplyKey, state.ReplyToken
		if target.operation == "cancel" {
			priorKey, priorToken = state.CancelKey, state.CancelToken
		}
		if priorKey != "" {
			prior, err := s.readRecord(ctx, priorKey)
			if err != nil {
				return nil, err
			}
			if prior != nil && prior.Token == priorToken {
				return nil, errors.New("messages: question already has an accepted reply")
			}
		}
		if target.operation == "cancel" {
			state.CancelKey, state.CancelToken = key, token
		} else {
			state.ReplyKey, state.ReplyToken = key, token
		}
		return json.Marshal(state)
	})
}

// Lock order is pair/queue/expiry, then question state, then the new reply's
// canonical record. This callback invokes no event head, native owner or policy.
func (s *messageStore) publishReply(ctx context.Context, target *messageReplyTarget, key, token string, publish func() error) error {
	return cas.ChangeLimitContext(ctx, s.statePath(target.key, target.token), messageStateLimit, func(raw []byte) ([]byte, error) {
		r, _, err := s.replyQuestion(ctx, target)
		if err != nil {
			return nil, err
		}
		state, err := readMessageState(raw, r)
		if err != nil {
			return nil, err
		}
		reservedKey, reservedToken := state.ReplyKey, state.ReplyToken
		if target.operation == "cancel" {
			reservedKey, reservedToken = state.CancelKey, state.CancelToken
		}
		if reservedKey != key || reservedToken != token || (state.Held && target.operation != "cancel") || state.Refused || messageTerminal(state.TaskState) {
			return nil, errors.New("messages: reply reservation changed")
		}
		if opposite, err := s.oppositeQuestionOperation(ctx, state, target.operation); err != nil || opposite {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("messages: terminal operation published first")
		}
		if err = publish(); err != nil {
			return nil, err
		}
		if err = s.at("terminal_child_published"); err != nil {
			return nil, err
		}
		accepted, err := s.readRecord(ctx, key)
		if err != nil || accepted == nil || accepted.Token != token {
			return nil, errors.New("messages: terminal child acceptance unavailable")
		}
		if accepted.Terminal {
			state.AcceptedTerminal = "complete"
			if target.operation == "cancel" {
				state.AcceptedTerminal = "cancel"
			}
		}
		return json.Marshal(state)
	})
}

// awaitedReply proves that this wake-eligible reply was published while the
// original question was open. The question CAS admits the canonical reply
// only before both deadlines and preserves its exact terminal link.
func (s *messageStore) awaitedReply(ctx context.Context, owner messageActor, key string, reply *messageRecord, replyTo string, observed int64) bool {
	if reply.Profile != "messages-record@1" || reply.Actor.Remote != nil || reply.TargetKey == "" || reply.Alias {
		return false
	}
	question, err := s.readRecord(ctx, reply.TargetKey)
	if err != nil || question == nil || question.Token != reply.TargetToken || question.ID != replyTo ||
		question.Actor.Remote != nil || question.Actor.Account != owner.Account || question.Actor.Program != owner.Program ||
		question.Actor.Application != owner.Application || question.Actor.returnContext() != owner.Context ||
		question.ContextID != reply.ContextID || question.ExpiresUnixMs <= max(observed, reply.AcceptedAt) {
		return false
	}
	original, err := decodeRetainedMessageRequest(question.Spec)
	if err != nil || original.Kind != msg.KindQuestion || original.AnswerByUnixMs <= max(observed, reply.AcceptedAt) ||
		question.Receiver != reply.Actor.Program || original.Context != reply.Actor.Context {
		return false
	}
	state, err := s.currentState(ctx, reply.TargetKey, question)
	return err == nil && !state.Held && !state.Refused && !state.SenderRemoved &&
		(state.TaskState == msg.TaskStateSubmitted || state.TaskState == msg.TaskStateCompleted) &&
		state.ReplyKey == key && state.ReplyToken == reply.Token
}
