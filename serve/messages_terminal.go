package main

import (
	"context"
	"encoding/json"
	"errors"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func (s *messageStore) anchorTerminal(ctx context.Context, record *messageRecord, key, operation string) error {
	if record.TargetKey == "" {
		return nil
	}
	return cas.ChangeLimitContext(ctx, s.statePath(record.TargetKey, record.TargetToken), messageStateLimit, func(raw []byte) ([]byte, error) {
		question, err := s.readRecord(ctx, record.TargetKey)
		if err != nil {
			return nil, err
		}
		if question == nil || question.Token != record.TargetToken || question.ExpiresUnixMs <= s.now().UnixMilli() {
			return raw, nil
		}
		state, err := readMessageState(raw, question)
		if err != nil {
			return nil, err
		}
		expectedKey, expectedToken := state.ReplyKey, state.ReplyToken
		if operation == "cancel" {
			expectedKey, expectedToken = state.CancelKey, state.CancelToken
		}
		if expectedKey != key || expectedToken != record.Token {
			return nil, errors.New("messages: terminal acceptance target changed")
		}
		if state.AcceptedTerminal != "" && state.AcceptedTerminal != operation {
			return nil, errors.New("messages: conflicting accepted terminal operations")
		}
		state.AcceptedTerminal = operation
		return json.Marshal(state)
	})
}

// Anchor accepted terminal intent before child expiry/key reuse. Tombstones
// retain only target digests/tokens and authorization facts, never body text.
func (s *messageStore) retainTerminal(ctx context.Context, record *messageRecord, key string) (bool, error) {
	terminal := record.Terminal || record.Control
	if record.Alias {
		return terminal, nil
	}
	operation := "complete"
	if record.Control {
		operation = "cancel"
	}
	if record.TargetKey == "" {
		return terminal, nil
	}
	if terminal {
		return true, s.anchorTerminal(ctx, record, key, operation)
	}
	if record.Profile != "messages-record@1" {
		return false, nil
	}
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil {
		return false, err
	}
	if request.Kind != msg.KindReply {
		return false, nil
	}
	err = cas.ChangeLimitContext(ctx, s.statePath(key, record.Token), messageStateLimit, func(raw []byte) ([]byte, error) {
		state, err := readMessageState(raw, record)
		if err != nil {
			return nil, err
		}
		terminal = !record.Held || state.ReplyAuthorized
		if !terminal {
			question, e := s.readRecord(ctx, record.TargetKey)
			if e != nil {
				return nil, e
			}
			if question != nil && question.Token == record.TargetToken && question.Profile == "messages-record@1" {
				qstate, e := s.currentState(ctx, record.TargetKey, question)
				if e != nil {
					return nil, e
				}
				terminal = qstate.AcceptedTerminal == "complete" && qstate.ReplyKey == key && qstate.ReplyToken == record.Token
			}
		}
		if terminal {
			if err = s.anchorTerminal(ctx, record, key, "complete"); err != nil {
				return nil, err
			}
		}
		return json.Marshal(state)
	})
	return terminal, err
}

// Called under the reply-state owner. The retained question records person
// authorization before release projection and fences an opposing cancellation.
func (s *messageStore) authorizeHeldReply(ctx context.Context, record *messageRecord, key string) error {
	if record.TargetKey == "" {
		return errors.New("messages: held reply target missing")
	}
	return cas.ChangeLimitContext(ctx, s.statePath(record.TargetKey, record.TargetToken), messageStateLimit, func(raw []byte) ([]byte, error) {
		question, err := s.readRecord(ctx, record.TargetKey)
		if err != nil {
			return nil, err
		}
		if question == nil || question.Token != record.TargetToken || question.ExpiresUnixMs <= s.now().UnixMilli() {
			return nil, errors.New("messages: held reply question expired")
		}
		state, err := readMessageState(raw, question)
		if err != nil {
			return nil, err
		}
		if state.ReplyKey != key || state.ReplyToken != record.Token {
			return nil, errors.New("messages: held reply reservation changed")
		}
		if state.AcceptedTerminal == "complete" {
			return raw, nil
		}
		request, err := decodeRetainedMessageRequest(question.Spec)
		if err != nil {
			return nil, err
		}
		if state.AcceptedTerminal != "" || messageTerminal(state.TaskState) || request.AnswerByUnixMs <= s.now().UnixMilli() || record.ExpiresUnixMs <= s.now().UnixMilli() {
			return nil, errors.New("messages: held reply deadline closed")
		}
		if opposite, err := s.oppositeQuestionOperation(ctx, state, ""); err != nil || opposite {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("messages: question canceled before reply release")
		}
		state.AcceptedTerminal = "complete"
		return json.Marshal(state)
	})
}
