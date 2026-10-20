package main

import (
	"context"
	"encoding/json"
	"errors"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// The existing question owns this one terminal fence. It retains no outgoing
// body and drives no retry. Possible remote acceptance is never inferred absent.
type messageRemoteReplyIntent struct {
	Fingerprint       string
	Reference         *msg.MessageRef `json:",omitempty"`
	Completed, Failed bool
	ContextID         string `json:",omitempty"`
}

func (s *messageStore) reserveRemoteReply(ctx context.Context, key string, question *messageRecord, request msg.SendRequest) error {
	fingerprint := messageDigest(string(msg.EncodeSendRequest(&request)))
	return cas.ChangeLimitContext(ctx, s.statePath(key, question.Token), messageStateLimit, func(raw []byte) ([]byte, error) {
		current, err := s.readRecord(ctx, key)
		if err != nil || current == nil || current.Token != question.Token || current.ExpiresUnixMs <= s.now().UnixMilli() {
			return nil, errors.New("messages: remote reply question unavailable")
		}
		original, err := decodeRetainedMessageRequest(current.Spec)
		if err != nil || original.Kind != msg.KindQuestion {
			return nil, errors.New("messages: remote reply deadline")
		}
		state, err := readMessageState(raw, current)
		if err != nil {
			return nil, err
		}
		if state.RemoteReply != nil {
			if !messageRequestDigestMatches(state.RemoteReply.Fingerprint, msg.EncodeSendRequest(&request)) {
				return nil, errors.New("messages: another remote reply owns the terminal fence")
			}
			return raw, nil
		}
		if original.AnswerByUnixMs <= s.now().UnixMilli() {
			return nil, errors.New("messages: remote reply deadline")
		}
		if state.Held || state.Refused || messageTerminal(state.TaskState) || state.AcceptedTerminal != "" {
			return nil, errors.New("messages: question is terminal or held")
		}
		for _, prior := range []struct{ key, token string }{{state.ReplyKey, state.ReplyToken}, {state.CancelKey, state.CancelToken}} {
			if prior.key != "" {
				record, e := s.readRecord(ctx, prior.key)
				if e != nil {
					return nil, e
				}
				if record != nil && record.Token == prior.token {
					return nil, errors.New("messages: local terminal operation already accepted")
				}
			}
		}
		// Positive local absence retires interrupted local publishers at the same
		// existing reservation CAS. Remote uncertainty never takes this branch.
		state.ReplyKey, state.ReplyToken, state.CancelKey, state.CancelToken = "", "", "", ""
		state.RemoteReply = &messageRemoteReplyIntent{Fingerprint: fingerprint}
		return json.Marshal(state)
	})
}

func (s *messageStore) finishRemoteReply(ctx context.Context, key string, question *messageRecord, request msg.SendRequest, result msg.SendResult) error {
	if result.Reference == nil || result.Reference.OwnerPeerKey != request.PeerKey || result.Reference.MessageID != result.MessageID {
		return errors.New("messages: remote canonical reply reference unavailable")
	}
	err := cas.ChangeLimitContext(ctx, s.statePath(key, question.Token), messageStateLimit, func(raw []byte) ([]byte, error) {
		current, e := s.readRecord(ctx, key)
		if e != nil || current == nil || current.Token != question.Token {
			return nil, errors.New("messages: remote question changed")
		}
		state, e := readMessageState(raw, current)
		if e != nil {
			return nil, e
		}
		if state.RemoteReply == nil || !messageRequestDigestMatches(state.RemoteReply.Fingerprint, msg.EncodeSendRequest(&request)) {
			return nil, errors.New("messages: remote reply intent changed")
		}
		if state.RemoteReply.Completed || state.RemoteReply.Failed {
			if state.RemoteReply.Reference == nil || *state.RemoteReply.Reference != *result.Reference {
				return nil, errors.New("messages: remote terminal reference changed")
			}
			return raw, nil
		}
		ref := *result.Reference
		state.RemoteReply.Reference = &ref
		state.RemoteReply.ContextID = result.ContextID
		if result.Refused {
			state.RemoteReply.Failed = true
		} else {
			state.RemoteReply.Completed = true
		}
		return json.Marshal(state)
	})
	if err != nil {
		return err
	}
	op := "complete"
	if result.Refused {
		op = "fail"
	}
	return s.transitionState(ctx, key, question, op)
}
