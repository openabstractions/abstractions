package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

var errMessageDeliveryTransferred = errors.New("messages: delivery report custody already transferred")

func acceptedMessageRouteValid(route *messageAcceptedRoute, key, token string) bool {
	if route != nil && route.Forward != nil {
		return route.Key == key && route.Token == token && agentHex(token, 24) && validMessageForward(route.Forward, key) && route.Digest == route.Forward.Digest && route.ID == messageForwardID(route.Actor, route.Forward.Reference, route.Forward.Generation) && route.Pair == messagePairKey(route.Actor, route.Receiver) && route.Receiver == "remote:"+route.Forward.Reference.OwnerPeerKey && route.Actor.Account != "" && route.Actor.Remote == nil && applicationContextName.MatchString(route.Actor.Context) && applicationContextName.MatchString(route.Actor.returnContext()) && route.AcceptedAt > 0 && route.ExpiresUnixMs > 0
	}
	if route == nil || route.Key != key || route.Token != token || !agentHex(key, 32) || !agentHex(token, 24) || !agentHex(route.Digest, 32) || route.Pair != messagePairKey(route.Actor, route.Receiver) || route.ID == "" || len(route.ID) > 128 || route.ContextID == "" || len(route.ContextID) > 128 || route.AcceptedAt <= 0 || route.ExpiresUnixMs <= route.AcceptedAt || !applicationContextName.MatchString(route.Actor.Context) || !applicationContextName.MatchString(route.Context) || !providerName.MatchString(route.Actor.Application) {
		return false
	}
	_, ok := messageApplicationResource(route.To)
	return ok && (route.Kind == msg.KindNote || route.Kind == msg.KindQuestion || route.Kind == msg.KindReply)
}

// Existing state custody is a redundant body-free receiver of a canonical
// decision. It is written before event transfer or aggregate publication.
func (s *messageStore) retainDeliveryEcho(ctx context.Context, key, token string, delivery *messageDeliveryState) error {
	err := cas.ChangeLimitContext(ctx, s.statePath(key, token), messageStateLimit, func(raw []byte) ([]byte, error) {
		var state messageState
		if err := messageStoredJSON(raw, &state); err != nil || state.Profile != "messages-state@1" || state.Token != token || !acceptedMessageRouteValid(state.Acceptance, key, token) {
			return nil, fmt.Errorf("%w: delivery acceptance route unavailable", errMessageStateInvalid)
		}
		if state.Delivery != nil && state.Delivery.State != delivery.State {
			return nil, errors.New("messages: terminal delivery echo differs")
		}
		if state.Delivery != nil && state.Delivery.Reported {
			if !delivery.Reported {
				return nil, errMessageDeliveryTransferred
			}
			return raw, nil
		}
		copy := *delivery
		state.Delivery = &copy
		return json.Marshal(state)
	})
	return boundedMessageStateError(err)
}

// A published pointer locates post-canonical state evidence; the pointer itself
// never proves acceptance. Missing or corrupt evidence supplies no sender route.
func (s *messageStore) recordForPointer(ctx context.Context, ref messagePointer) (*messageRecord, error) {
	record, recordErr := s.readRecord(ctx, ref.Key)
	if recordErr == nil && record != nil {
		return record, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	observed, err := cas.ReadLimitContext(ctx, s.recordPath(ref.Key), messageRecordLimit)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw, err := cas.ReadLimitContext(ctx, s.statePath(ref.Key, ref.Token), messageStateLimit)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return record, recordErr
	}
	var state messageState
	if err := messageStoredJSON(raw, &state); err != nil || state.Profile != "messages-state@1" || state.Token != ref.Token || !acceptedMessageRouteValid(state.Acceptance, ref.Key, ref.Token) {
		return record, recordErr
	}
	route := state.Acceptance
	if route.Forward != nil {
		restored := messageRecord{Profile: messageForwardProfile, Pair: route.Pair, Actor: route.Actor, Receiver: route.Receiver, Token: route.Token, ID: route.ID, ContextID: route.ContextID, AcceptedAt: route.AcceptedAt, ExpiresUnixMs: route.ExpiresUnixMs, Forward: route.Forward, Delivery: state.Delivery, Audit: route.Audit}
		restored.SenderRemoved = state.SenderRemoved
		encoded, encodeErr := json.Marshal(restored)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if _, err := readForwardRecord(encoded, ref.Key); err != nil {
			return record, recordErr
		}
		err = cas.ChangeLimitContext(ctx, s.recordPath(ref.Key), messageRecordLimit, func(current []byte) ([]byte, error) {
			if !bytes.Equal(current, observed) {
				return nil, errors.New("messages: forwarding custody changed during recovery")
			}
			return json.Marshal(restored)
		})
		if err != nil {
			return nil, err
		}
		s.advance()
		return s.readRecord(ctx, ref.Key)
	}
	delivery := state.Delivery
	if delivery != nil && (delivery.State != msg.DeliveryDelivered && delivery.State != msg.DeliveryUndelivered || delivery.At <= 0 || delivery.Actor.Account != route.Actor.Account || delivery.Actor.Program != route.Actor.Program || delivery.Actor.Context != route.Actor.returnContext()) {
		return record, recordErr
	}
	if delivery != nil && delivery.Report != "" {
		h, e := decodeRetainedMessageHeader([]byte(delivery.Report))
		if e != nil || h.Delivery == nil || *h.Delivery != delivery.State || h.MessageID != route.ID {
			return record, recordErr
		}
	}
	if delivery == nil {
		request := msg.SendRequest{To: route.To, Context: route.Context, Kind: route.Kind, NotifyDelivered: new(route.NotifyDelivered)}
		source := &messageRecord{ID: route.ID, ContextID: route.ContextID, Actor: route.Actor, SenderRemoved: state.SenderRemoved}
		delivery = &messageDeliveryState{State: msg.DeliveryUndelivered, Reason: msg.UndeliveredReasonLost, At: s.now().UnixMilli(), Actor: messageDeliveryActor(source)}
		delivery.Report = messageDeliveryReport(source, &request, delivery.State, delivery.Reason)
		delivery.Reported = delivery.Report == ""
	}
	audit := route.Audit
	if audit != nil {
		copy := *audit
		audit = &copy
		if route.Kind == msg.KindQuestion {
			audit.Outcome = state.TaskState.String()
			audit.Version = state.Version
			if len(state.Events) > 0 {
				audit.UpdatedAt = state.Events[len(state.Events)-1].At
			}
		} else if state.Held {
			audit.Outcome = "awaiting_permission"
		} else if state.Refused {
			audit.Outcome = "refused"
		} else if delivery.State == msg.DeliveryDelivered {
			audit.Outcome = "delivered"
			audit.UpdatedAt = delivery.At
		}
		audit.DeliveryState = delivery.State.String()
		audit.UndeliveredReason = string(delivery.Reason)
		audit.DeliveryAt = delivery.At
	}
	tombstone := messageExpiredRecord{Profile: "messages-expired@1", Pair: route.Pair, Token: route.Token, ID: route.ID, ContextID: route.ContextID, AcceptedAt: route.AcceptedAt, ExpiresUnixMs: route.ExpiresUnixMs, Actor: route.Actor, Lost: true, LostDigest: route.Digest, Delivery: delivery, Audit: audit}
	tombstone.SenderRemoved = state.SenderRemoved
	tombstone.Held, tombstone.Refused = state.Held, state.Refused
	err = cas.ChangeLimitContext(ctx, s.recordPath(ref.Key), messageRecordLimit, func(current []byte) ([]byte, error) {
		if !bytes.Equal(current, observed) {
			return nil, errors.New("messages: damaged acceptance changed during recovery")
		}
		return json.Marshal(tombstone)
	})
	if err != nil {
		return nil, err
	}
	s.advance()
	if err := s.retainDeliveryEcho(ctx, ref.Key, ref.Token, delivery); err != nil {
		return nil, err
	}
	if err := s.finishDelivery(ctx, ref.Key, ref.Token, delivery.State, delivery.Reason); err != nil {
		return nil, err
	}
	return s.readRecord(ctx, ref.Key)
}
