package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

const messageForwardProfile = "messages-forward@1"

// A source retains accepted forwarding custody, never a second body/outbox.
// Only an authenticated successful Send supplies Reference. Pending attempts
// and lost Send replies supply none and cannot construct this record.
type messageForwardRoute struct {
	Reference                                      msg.MessageRef
	Key, Digest, LocalKey, Generation, To, Context string
	Kind                                           msg.Kind
	NotifyDelivered                                bool
	NextObserveAt                                  int64 `json:",omitempty"`
}

func (s *messageStore) forwardKey(actor messageActor, peer, key string) string {
	return messageDigest("messages-forward-key@1\x00" + s.key(actor, key) + "\x00" + peer)
}

func messageForwardID(actor messageActor, reference msg.MessageRef, generation string) string {
	chat := ""
	if actor.Chat != nil {
		chat = actor.Chat.occurrence() + "\x00" + actor.Chat.ReturnContext
	}
	return "forward-" + messageDigest(actor.Account+"\x00"+actor.Program+"\x00"+actor.Context+"\x00"+chat+"\x00"+reference.OwnerPeerKey+"\x00"+reference.MessageID+"\x00"+generation)
}

func messageForwardCanonicalKey(actor messageActor, reference msg.MessageRef, generation string) string {
	return messageForwardID(actor, reference, generation)[len("forward-"):]
}

func validMessageForward(route *messageForwardRoute, key string) bool {
	if route == nil || route.Key != key || !agentHex(key, 32) || !agentHex(route.Digest, 32) || !validPairingHex(route.Reference.OwnerPeerKey, 32) || !validPairingHex(route.LocalKey, 32) || route.Reference.OwnerPeerKey == route.LocalKey || !validPairingHex(route.Generation, 32) || len(route.Reference.MessageID) < 1 || len(route.Reference.MessageID) > 128 || !applicationContextName.MatchString(route.Context) || route.NextObserveAt < 0 {
		return false
	}
	_, ok := messageApplicationResource(route.To)
	return ok && (route.Kind == msg.KindNote || route.Kind == msg.KindQuestion || route.Kind == msg.KindReply)
}

func readForwardRecord(raw []byte, key string) (*messageRecord, error) {
	var r messageRecord
	if err := messageStoredJSON(raw, &r); err != nil || r.Profile != messageForwardProfile || !validMessageForward(r.Forward, key) || r.Actor.Remote != nil || r.Actor.Account == "" || r.Actor.Program == "" || !providerName.MatchString(r.Actor.Application) || !applicationContextName.MatchString(r.Actor.Context) || !applicationContextName.MatchString(r.Actor.returnContext()) || !agentHex(r.Token, 24) || r.ID != messageForwardID(r.Actor, r.Forward.Reference, r.Forward.Generation) || r.ContextID == "" || len(r.ContextID) > 128 || r.AcceptedAt <= 0 || r.ExpiresUnixMs <= 0 || len(r.Spec) != 0 || r.Control || r.Lost || r.Pair != messagePairKey(r.Actor, r.Receiver) || r.Receiver != "remote:"+r.Forward.Reference.OwnerPeerKey || !messageTargetValid(r.TargetKey, r.TargetToken) {
		return nil, errors.New("messages: invalid accepted forwarding custody")
	}
	if r.Alias && (r.TargetKey == "" || r.TargetToken == "") {
		return nil, errors.New("messages: forwarding alias missing canonical custody")
	}
	canonical := messageForwardCanonicalKey(r.Actor, r.Forward.Reference, r.Forward.Generation)
	if !r.Alias && key != canonical || r.Alias && r.TargetKey != canonical {
		return nil, errors.New("messages: forwarding canonical locator differs")
	}
	return &r, nil
}

func messageForwardReceipt(r *messageRecord) msg.SendResult {
	ref := r.Forward.Reference
	return msg.SendResult{Outcome: msg.OutcomeSent, MessageID: ref.MessageID, ContextID: r.ContextID, Reference: &ref, AwaitingPermission: r.Held && !r.Refused, Refused: r.Refused}
}

func (s *messageStore) lookupForwardReceipt(ctx context.Context, actor messageActor, request msg.SendRequest, local, generation string) (msg.SendResult, bool) {
	r, err := s.readRecord(ctx, s.forwardKey(actor, request.PeerKey, request.IdempotencyKey))
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}, true
	}
	if r == nil {
		return msg.SendResult{}, false
	}
	if r.Forward == nil {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}, true
	}
	if r.ExpiresUnixMs <= s.now().UnixMilli() {
		if !r.Alias && (r.Delivery == nil || !r.Delivery.Reported || r.Audit != nil && !r.AuditDone) {
			return msg.SendResult{Outcome: msg.OutcomeUnknown}, true
		}
		return msg.SendResult{}, false
	}
	if !messageSame(r, actor, msg.EncodeSendRequest(&request)) {
		return msg.SendResult{Outcome: msg.OutcomeConflict}, true
	}
	if r.Forward.LocalKey != local || r.Forward.Generation != generation {
		return msg.SendResult{Outcome: msg.OutcomeForbidden}, true
	}
	if r.Alias {
		canonical, e := s.recordForPointer(ctx, messagePointer{Key: r.TargetKey, Token: r.TargetToken})
		if e != nil || canonical == nil || canonical.Token != r.TargetToken || canonical.Alias || canonical.Forward == nil || canonical.ID != r.ID {
			return msg.SendResult{Outcome: msg.OutcomeUnknown}, true
		}
		r = canonical
	}
	if !r.Alias {
		if err := s.ensureForwardPublication(ctx, r.Forward.Key, r); err != nil {
			return msg.SendResult{Outcome: msg.OutcomeUnknown}, true
		}
	}
	return messageForwardReceipt(r), true
}

func (s *messageStore) retainForwardRoute(ctx context.Context, key string, r *messageRecord) error {
	if r.Alias {
		return nil
	}
	f := r.Forward
	route := &messageAcceptedRoute{Key: key, Token: r.Token, Pair: r.Pair, ID: r.ID, ContextID: r.ContextID, Receiver: r.Receiver, Actor: r.Actor, AcceptedAt: r.AcceptedAt, ExpiresUnixMs: r.ExpiresUnixMs, To: f.To, Context: f.Context, Kind: f.Kind, NotifyDelivered: f.NotifyDelivered, Digest: f.Digest, Audit: r.Audit, Forward: f}
	return cas.ChangeLimitContext(ctx, s.statePath(key, r.Token), messageStateLimit, func(raw []byte) ([]byte, error) {
		state := messageState{Profile: "messages-state@1", Token: r.Token, TaskState: msg.TaskStateSubmitted, Events: []messageStateEvent{}}
		if raw != nil {
			if err := messageStoredJSON(raw, &state); err != nil || state.Profile != "messages-state@1" || state.Token != r.Token {
				return nil, errors.New("messages: forwarding route state unavailable")
			}
		}
		if state.Acceptance != nil {
			if !acceptedMessageRouteValid(state.Acceptance, key, r.Token) || state.Acceptance.Digest != f.Digest || state.Acceptance.ID != r.ID {
				return nil, errors.New("messages: forwarding accepted route differs")
			}
			return raw, nil
		}
		state.Acceptance = route
		return json.Marshal(state)
	})
}

// The existing ID owner joins aliases for the same genuine foreign acceptance.
// It is published after canonical custody. A pointer alone never establishes it.
func (s *messageStore) ensureForwardPublication(ctx context.Context, key string, r *messageRecord) error {
	if r.Alias {
		return nil
	}
	if err := s.publishID(ctx, r, key); err != nil {
		// The locator is a repairable hint. A valid post-canonical state receiver
		// already owns this acceptance even if that locator later becomes damaged.
		raw, e := cas.ReadLimitContext(ctx, s.statePath(key, r.Token), messageStateLimit)
		var state messageState
		if e == nil && messageStoredJSON(raw, &state) == nil && state.Profile == "messages-state@1" && state.Token == r.Token && acceptedMessageRouteValid(state.Acceptance, key, r.Token) && state.Acceptance.ID == r.ID {
			return nil
		}
		return err
	}
	return s.retainForwardRoute(ctx, key, r)
}

func (s *messageStore) acceptForward(ctx context.Context, actor messageActor, request msg.SendRequest, out msg.SendResult, local, generation string) msg.SendResult {
	unknown := msg.SendResult{Outcome: msg.OutcomeUnknown}
	if out.Outcome != msg.OutcomeSent || out.Reference == nil || out.Reference.OwnerPeerKey != request.PeerKey || out.Reference.MessageID != out.MessageID || out.ContextID == "" || len(out.ContextID) > 128 {
		return unknown
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, found := s.lookupForwardReceipt(ctx, actor, request, local, generation); found {
		if prior.Outcome == msg.OutcomeSent {
			if prior.Reference == nil || *prior.Reference != *out.Reference || prior.MessageID != out.MessageID || prior.ContextID != out.ContextID {
				return unknown
			}
			return out
		}
		return prior
	}
	receiptKey := s.forwardKey(actor, request.PeerKey, request.IdempotencyKey)
	key := messageForwardCanonicalKey(actor, *out.Reference, generation)
	canonical, err := s.readRecord(ctx, key)
	if err != nil {
		return unknown
	}
	f := &messageForwardRoute{Reference: *out.Reference, Key: key, Digest: messageDigest(string(msg.EncodeSendRequest(&request))), LocalKey: local, Generation: generation, To: request.To, Context: request.Context, Kind: request.Kind, NotifyDelivered: request.NotifyDelivered != nil && *request.NotifyDelivered}
	if !validMessageForward(f, key) {
		return unknown
	}
	if canonical == nil {
		token, e := applicationToken()
		if e != nil {
			return unknown
		}
		canonical = &messageRecord{Profile: messageForwardProfile, Pair: messagePairKey(actor, "remote:"+request.PeerKey), Actor: actor, Receiver: "remote:" + request.PeerKey, Token: token, ID: messageForwardID(actor, *out.Reference, generation), ContextID: out.ContextID, AcceptedAt: s.now().UnixMilli(), ExpiresUnixMs: request.ExpiresUnixMs, Held: out.AwaitingPermission, Refused: out.Refused, Forward: f, Origin: messageOriginFrom(ctx)}
		if s.audit != nil {
			copy := *canonical
			copy.Spec = msg.EncodeSendRequest(&request)
			canonical.Audit, err = s.audit.prepare(&copy)
			if err != nil {
				return unknown
			}
			canonical.Audit.MetadataOnly = true
			ref := f.Reference
			canonical.Audit.Reference = &ref
		}
		ref := messagePointer{Key: key, Token: token, ID: canonical.ID, Context: actor.returnContext(), AcceptedAt: canonical.AcceptedAt, ExpiresUnixMs: canonical.ExpiresUnixMs}
		if err := s.appendExpiry(ctx, ref); err != nil {
			return unknown
		}
		if err := cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
			if raw != nil {
				return nil, errors.New("messages: forwarding acceptance raced")
			}
			return json.Marshal(canonical)
		}); err != nil {
			return unknown
		}
		if err := s.at("forward_canonical_published"); err != nil {
			return unknown
		}
	}
	if canonical.Forward == nil || canonical.Alias || canonical.Forward.Reference != f.Reference || canonical.Forward.Generation != generation || canonical.ID != messageForwardID(actor, *out.Reference, generation) {
		return unknown
	}
	if err := s.ensureForwardPublication(ctx, key, canonical); err != nil {
		return unknown
	}
	if err := s.at("forward_route_published"); err != nil {
		return unknown
	}
	old, err := s.readRecord(ctx, receiptKey)
	if err != nil {
		return unknown
	}
	token, err := applicationToken()
	if err != nil {
		return unknown
	}
	aliasForward := *f
	aliasForward.Key = receiptKey
	receipt := &messageRecord{Profile: messageForwardProfile, Pair: canonical.Pair, Actor: actor, Receiver: canonical.Receiver, Token: token, ID: canonical.ID, ContextID: canonical.ContextID, AcceptedAt: s.now().UnixMilli(), ExpiresUnixMs: canonical.ExpiresUnixMs, Held: out.AwaitingPermission, Refused: out.Refused, Forward: &aliasForward, Alias: true, TargetKey: key, TargetToken: canonical.Token}
	ref := messagePointer{Key: receiptKey, Token: token, ID: receipt.ID, Context: actor.returnContext(), AcceptedAt: receipt.AcceptedAt, ExpiresUnixMs: receipt.ExpiresUnixMs, Alias: true}
	if err := s.appendExpiry(ctx, ref); err != nil {
		return unknown
	}
	if err := cas.ChangeLimitContext(ctx, s.recordPath(receiptKey), messageRecordLimit, func(raw []byte) ([]byte, error) {
		if raw != nil {
			current, e := readForwardRecord(raw, receiptKey)
			if e != nil {
				return nil, e
			}
			if old == nil || current.Token != old.Token || current.ExpiresUnixMs > s.now().UnixMilli() {
				return nil, errors.New("messages: forwarding receipt key changed")
			}
		}
		return json.Marshal(receipt)
	}); err != nil {
		return unknown
	}
	if err := s.at("forward_receipt_published"); err != nil {
		return unknown
	}
	s.gate.Advance()
	return out
}

func (s *messageStore) finishForwardDelivery(ctx context.Context, key, token string, state msg.Delivery, reason msg.UndeliveredReason) error {
	return s.finishDelivery(ctx, key, token, state, reason)
}

// eventMu is held by the shared terminal owner. Remote evidence is observed
// outside all CAS locks; this callback commits only the source's body-free fact.
func (s *messageStore) finishForwardDeliveryLocked(ctx context.Context, key string, r *messageRecord, state msg.Delivery, reason msg.UndeliveredReason) error {
	if state != msg.DeliveryDelivered && state != msg.DeliveryUndelivered {
		return errors.New("messages: nonterminal forwarding transition")
	}
	if err := s.ensureForwardPublication(ctx, key, r); err != nil {
		return err
	}
	current, err := s.readRecord(ctx, key)
	if err != nil {
		return err
	}
	if current == nil || current.Token != r.Token || current.Alias {
		return nil
	}
	if current.Delivery != nil {
		return s.publishDeliveryReport(ctx, key, r.Token, current.Delivery)
	}
	current.SenderRemoved = current.SenderRemoved || r.SenderRemoved
	f := current.Forward
	request := msg.SendRequest{To: f.To, Context: f.Context, Kind: f.Kind, NotifyDelivered: new(f.NotifyDelivered)}
	next := &messageDeliveryState{State: state, Reason: reason, At: s.now().UnixMilli(), Actor: messageDeliveryActor(current)}
	next.Report = messageDeliveryReport(current, &request, state, reason)
	next.Reported = next.Report == ""
	err = cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
		current, e := readForwardRecord(raw, key)
		if e != nil {
			return nil, e
		}
		if current.Token != r.Token {
			return raw, nil
		}
		if current.Delivery != nil {
			return nil, errMessageDeliveryTerminal
		}
		current.Delivery = next
		current.SenderRemoved = current.SenderRemoved || r.SenderRemoved
		current.AuditDone = false
		if state == msg.DeliveryDelivered {
			current.Acked = true
			current.Held = false
		}
		if state == msg.DeliveryUndelivered && reason == msg.UndeliveredReasonRefused {
			current.Refused = true
			current.Held = false
		}
		return json.Marshal(current)
	})
	if errors.Is(err, errMessageDeliveryTerminal) {
		return nil
	}
	if err != nil {
		return err
	}
	s.gate.Advance()
	if err := s.retainDeliveryEcho(ctx, key, r.Token, next); err != nil && !errors.Is(err, errMessageStateInvalid) {
		return err
	}
	return s.publishDeliveryReport(ctx, key, r.Token, next)
}

func (s *messageStore) claimForwardObservation(ctx context.Context, key, token string) (bool, error) {
	claimed := false
	err := cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
		r, err := readForwardRecord(raw, key)
		if err != nil {
			return nil, err
		}
		now := s.now().UnixMilli()
		if r.Token != token || r.Alias || r.Delivery != nil || r.Forward.NextObserveAt > now {
			return raw, nil
		}
		r.Forward.NextObserveAt = math.MaxInt64
		if now <= math.MaxInt64-1000 {
			r.Forward.NextObserveAt = now + 1000
		}
		claimed = true
		return json.Marshal(r)
	})
	return claimed, err
}
