package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"unicode"
	"unicode/utf8"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// One non-plaintext request pin in the question's existing terminal owner.
// Instance binds a possible dispatch to its actual native occurrence; another
// occurrence can read an accepted receipt but cannot dispatch under that pin.
type messageSimpleReplyIntent struct {
	IdempotencyKey        string `json:"RequestKey"`
	Fingerprint, Instance string
}

func messageReplySubject(subject string) string {
	clean := []rune{}
	for _, r := range subject {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069' {
			continue
		}
		if len(clean) == 76 {
			break
		}
		clean = append(clean, r)
	}
	return "Re: " + string(clean)
}

// messageReplyGrace is how long past the question's answer_by a reply waits
// for the asker's chat to read it (owner R34, 2026-11-12: an answer sent a
// minute after the question expired with it, unread, ten minutes later).
const messageReplyGrace = 24 * time.Hour

// messageReplyExpiry keeps a reply for the asker until its question's own
// expiry or its answer_by plus messageReplyGrace, whichever is later, within
// the seven-day message lifetime counted from the question's acceptance. It
// reads only the question, so a retried reply carries the same request.
func messageReplyExpiry(question *messageRecord, original *msg.SendRequest) int64 {
	expires := max(question.ExpiresUnixMs, original.AnswerByUnixMs+messageReplyGrace.Milliseconds())
	return min(expires, question.AcceptedAt+messageSendLifetimeMs)
}

func simpleReplyKey(actor messageActor, instance, id, body string) string {
	raw, _ := json.Marshal([]string{actor.Application, instance, actor.Context, id, body})
	hash := sha256.Sum256(raw)
	return "reply-" + hex.EncodeToString(hash[:])
}

func (s *messageStore) pinSimpleReply(ctx context.Context, key string, question *messageRecord, instance string, request *msg.SendRequest) (*messageSimpleReplyIntent, msg.Outcome) {
	var pin *messageSimpleReplyIntent
	out := msg.OutcomeUnknown
	err := cas.ChangeLimitContext(ctx, s.statePath(key, question.Token), messageStateLimit, func(raw []byte) ([]byte, error) {
		current, err := s.readRecord(ctx, key)
		if err != nil || current == nil || current.Token != question.Token {
			return nil, errors.New("messages: reply question changed")
		}
		original, err := decodeRetainedMessageRequest(current.Spec)
		if err != nil || original.Kind != msg.KindQuestion {
			return nil, errors.New("messages: reply question invalid")
		}
		state, err := readMessageState(raw, current)
		if err != nil {
			return nil, err
		}
		if prior := state.SimpleReply; prior != nil {
			request.IdempotencyKey = prior.IdempotencyKey
			if !messageRequestDigestMatches(prior.Fingerprint, msg.EncodeSendRequest(request)) {
				out = msg.OutcomeConflict
				return raw, nil
			}
			copy := *prior
			pin = &copy
			return raw, nil
		}
		if current.ExpiresUnixMs <= s.now().UnixMilli() || original.AnswerByUnixMs <= s.now().UnixMilli() || state.Held || state.Refused || messageTerminal(state.TaskState) {
			out = msg.OutcomeForbidden
			return raw, nil
		}
		state.SimpleReply = &messageSimpleReplyIntent{IdempotencyKey: request.IdempotencyKey, Fingerprint: messageDigest(string(msg.EncodeSendRequest(request))), Instance: instance}
		copy := *state.SimpleReply
		pin = &copy
		return json.Marshal(state)
	})
	if err != nil {
		return nil, msg.OutcomeUnknown
	}
	return pin, out
}

func (r *messagesReceiver) Reply(instance, sourceContext, id, body string) (msg.SendResult, error) {
	result := func(out msg.Outcome) (msg.SendResult, error) { return msg.SendResult{Outcome: out}, nil }
	if r.remote != nil || r.selectedPeer != "" {
		return result(msg.OutcomeForbidden)
	}
	if id == "" || len(id) > 128 || !utf8.ValidString(body) || len(body) > 8192 {
		return result(msg.OutcomeInvalid)
	}
	actor, _, info, ok := r.owner(instance, sourceContext)
	if !ok {
		return result(msg.OutcomeForbidden)
	}
	store := r.host.messages
	if store == nil || r.host.messageRights == nil {
		return result(msg.OutcomeUnavailable)
	}
	question, key, err := store.readRetainedByID(r.ctx, id)
	if err != nil {
		return result(msg.OutcomeUnavailable)
	}
	if question == nil || question.Actor.Account != actor.Account {
		return result(msg.OutcomeForbidden)
	}
	if question.ExpiresUnixMs <= store.now().UnixMilli() {
		return r.reconcileExpiredSimpleReply(actor, instance, sourceContext, body, key, question), nil
	}
	if !samePrograms(question.Receiver, actor.Program) {
		return result(msg.OutcomeForbidden)
	}
	original, err := decodeRetainedMessageRequest(question.Spec)
	if err != nil {
		return result(msg.OutcomeUnavailable)
	}
	if original.Kind != msg.KindQuestion || original.Context != sourceContext {
		return result(msg.OutcomeForbidden)
	}
	request := msg.SendRequest{To: "app:" + question.Actor.Application, Context: question.Actor.returnContext(), Kind: msg.KindReply, Subject: messageReplySubject(original.Subject), Body: body, IdempotencyKey: simpleReplyKey(actor, instance, id, body), ExpiresUnixMs: messageReplyExpiry(question, original)}
	if remote := question.Actor.Remote; remote != nil {
		if remote.ReceiverKey != info.Image.Key {
			return result(msg.OutcomeForbidden)
		}
		request.PeerKey = remote.PeerKey
		request.ReplyReference = &msg.MessageRef{OwnerPeerKey: remote.LocalKey, MessageID: question.ID}
	} else {
		request.ReplyTo = question.ID
	}
	if _, err := msg.DecodeSendRequest(msg.EncodeSendRequest(&request)); err != nil {
		return result(msg.OutcomeInvalid)
	}
	var pin *messageSimpleReplyIntent
	var outcome msg.Outcome
	owned, err := r.host.directory.withNativeMessageOwner(r.ctx, r.call, instance, sourceContext, nil, func(*nativeInbox) error {
		pin, outcome = store.pinSimpleReply(r.ctx, key, question, instance, &request)
		return nil
	})
	if err != nil {
		return result(msg.OutcomeUnknown)
	}
	if !owned {
		return result(msg.OutcomeForbidden)
	}
	if pin == nil {
		out, _ := result(outcome)
		if outcome == msg.OutcomeForbidden {
			// The replier learns at once that the asker no longer waits.
			out.Reason = "question_closed"
		}
		return out, nil
	}
	if request.PeerKey != "" {
		return r.sendPairedMessageWithReceipt(instance, sourceContext, request, true, pin.Instance != instance), nil
	}
	if receipt, found := store.lookupReceipt(r.ctx, actor, request); found {
		return r.projectSendReceipt(receipt), nil
	}
	if pin.Instance != instance {
		return result(msg.OutcomeUnknown)
	}
	if err := store.at("simple_reply_pinned"); err != nil {
		return result(msg.OutcomeUnknown)
	}
	return r.Send(instance, sourceContext, request)
}

// The question can expire before its accepted reply. Use its retained route
// and request pin only to recover a matching live local reply receipt.
func (r *messagesReceiver) reconcileExpiredSimpleReply(actor messageActor, instance, sourceContext, body, key string, question *messageRecord) msg.SendResult {
	store := r.host.messages
	out := msg.SendResult{Outcome: msg.OutcomeForbidden}
	owned, err := r.host.directory.withNativeMessageOwner(r.ctx, r.call, instance, sourceContext, nil, func(*nativeInbox) error {
		raw, err := cas.ReadLimitContext(r.ctx, store.statePath(key, question.Token), messageStateLimit)
		if err != nil || raw == nil {
			return err
		}
		var state messageState
		if err := messageStoredJSON(raw, &state); err != nil {
			return err
		}
		route, pin := state.Acceptance, state.SimpleReply
		if state.Profile != "messages-state@1" || state.Token != question.Token || !acceptedMessageRouteValid(route, key, question.Token) ||
			route.ID != question.ID || route.Kind != msg.KindQuestion || route.Actor.Remote != nil ||
			route.Actor.Account != actor.Account || route.ExpiresUnixMs != question.ExpiresUnixMs ||
			route.Context != sourceContext || !samePrograms(route.Receiver, actor.Program) || !validSimpleReplyPin(pin) {
			return nil
		}
		replyKey := store.key(actor, pin.IdempotencyKey)
		reply, err := store.readRecord(r.ctx, replyKey)
		if err != nil || reply == nil || reply.Profile != "messages-record@1" || reply.ExpiresUnixMs <= store.now().UnixMilli() {
			return err
		}
		request, err := decodeRetainedMessageRequest(reply.Spec)
		if err != nil {
			return err
		}
		if request.Kind != msg.KindReply || request.ReplyTo != question.ID || reply.TargetKey != key || reply.TargetToken != question.Token ||
			!messageRequestDigestMatches(pin.Fingerprint, reply.Spec) || !messageSame(reply, actor, reply.Spec) {
			return nil
		}
		if request.Body != body {
			out.Outcome = msg.OutcomeConflict
		} else {
			out = messageReceipt(reply)
		}
		return nil
	})
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}
	}
	if !owned {
		return msg.SendResult{Outcome: msg.OutcomeForbidden}
	}
	return r.projectSendReceipt(out)
}

func validSimpleReplyPin(pin *messageSimpleReplyIntent) bool {
	return pin != nil && agentHex(pin.Fingerprint, 32) && len(pin.IdempotencyKey) == 70 &&
		pin.IdempotencyKey[:6] == "reply-" && agentHex(pin.IdempotencyKey[6:], 32) &&
		len(pin.Instance) >= 1 && len(pin.Instance) <= 128
}
