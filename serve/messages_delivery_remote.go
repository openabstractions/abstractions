package main

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/remote"
)

// The existing bounded ID pointer locates canonical or accepted-route custody.
// The pointer grants no observation authority.
func (s *messageStore) readDeliveryByID(ctx context.Context, id string) (*messageRecord, string, error) {
	if len(id) == 0 || len(id) > 128 {
		return nil, "", nil
	}
	raw, err := cas.ReadLimitContext(ctx, filepath.Join(s.dir, "ids", messageDigest(id)+".json"), 4096)
	if err != nil || raw == nil {
		return nil, "", err
	}
	var ref messageIDPointer
	if err = messageStoredJSON(raw, &ref); err != nil || ref.Profile != "messages-id@1" || ref.ID != id || !agentHex(ref.Key, 32) || !agentHex(ref.Token, 24) {
		return nil, "", errors.New("messages: invalid delivery id pointer")
	}
	record, err := s.recordForPointer(ctx, messagePointer{Key: ref.Key, Token: ref.Token})
	if err != nil {
		return nil, "", err
	}
	if record == nil || record.Token != ref.Token || record.ID != id {
		return nil, "", nil
	}
	if record.Profile == "messages-record@1" && record.ExpiresUnixMs <= s.now().UnixMilli() {
		if err := s.expireRecord(ctx, messagePointer{Key: ref.Key, Token: ref.Token}); err != nil {
			return nil, "", err
		}
		record, err = s.recordForPointer(ctx, messagePointer{Key: ref.Key, Token: ref.Token})
		if err != nil {
			return nil, "", err
		}
		if record == nil || record.Token != ref.Token || record.ID != id {
			return nil, "", nil
		}
	}
	return record, ref.Key, nil
}

func messageDeliveryPairState(ctx context.Context, s *runtimePairingState, key, local, generation string) msg.DeliveryObservationOutcome {
	if s == nil || !s.mu.TryLock() {
		return msg.DeliveryObservationOutcomeUnavailable
	}
	defer s.mu.Unlock()
	d, _, err := s.read(ctx)
	if err != nil {
		return msg.DeliveryObservationOutcomeUnavailable
	}
	identity, err := pairingIdentity(d)
	if err != nil {
		return msg.DeliveryObservationOutcomeUnavailable
	}
	peer, ok := d.Peers[key]
	if !ok || peer.State != "active" || key == local || pairingKey(identity.Leaf) != local || hex.EncodeToString(peer.Transcript[:]) != generation {
		return msg.DeliveryObservationOutcomeForbidden
	}
	return msg.DeliveryObservationOutcomeObserved
}

// ObserveDelivery discloses body-free canonical delivery to the authenticated
// peer that supplied the acceptance under the current pairing generation.
func (r *messagesReceiver) ObserveDelivery(reference msg.MessageRef, pairingGeneration string) (msg.ObserveDeliveryResult, error) {
	if r.remote == nil || r.host == nil {
		return msg.ObserveDeliveryResult{Outcome: msg.DeliveryObservationOutcomeForbidden}, nil
	}
	if outcome := messageDeliveryPairState(r.ctx, r.host.messagePairing, r.remote.key, reference.OwnerPeerKey, pairingGeneration); outcome != msg.DeliveryObservationOutcomeObserved {
		return msg.ObserveDeliveryResult{Outcome: outcome}, nil
	}
	if err := r.remote.err; err != nil {
		outcome := msg.DeliveryObservationOutcomeUnavailable
		if !errors.Is(err, errPairingState) && !errors.Is(err, errPairingBusy) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && errors.Is(err, remote.ErrTrust) {
			outcome = msg.DeliveryObservationOutcomeForbidden
		}
		return msg.ObserveDeliveryResult{Outcome: outcome}, nil
	}
	if r.remote.local == "" || r.remote.generation == "" {
		return msg.ObserveDeliveryResult{Outcome: msg.DeliveryObservationOutcomeUnavailable}, nil
	}
	if pairingGeneration != r.remote.generation || reference.OwnerPeerKey != r.remote.local {
		return msg.ObserveDeliveryResult{Outcome: msg.DeliveryObservationOutcomeForbidden}, nil
	}
	if len(reference.MessageID) == 0 || len(reference.MessageID) > 128 {
		return msg.ObserveDeliveryResult{Outcome: msg.DeliveryObservationOutcomeInvalid}, nil
	}
	if r.host.messages == nil {
		return msg.ObserveDeliveryResult{Outcome: msg.DeliveryObservationOutcomeUnavailable}, nil
	}
	record, key, err := r.host.messages.readDeliveryByID(r.ctx, reference.MessageID)
	out := msg.ObserveDeliveryResult{Outcome: msg.DeliveryObservationOutcomeUnknown}
	if err != nil {
		out.Outcome = msg.DeliveryObservationOutcomeUnavailable
	} else if record != nil {
		actor := record.Actor
		if actor.Account == "" || actor.Remote == nil {
			// Legacy body-free tombstones may lack source provenance.
		} else if actor.Account != r.host.owner || actor.Remote.PeerKey != r.remote.key || actor.Remote.LocalKey != r.remote.local || actor.Remote.Generation != r.remote.generation || record.Alias || record.Control {
			out.Outcome = msg.DeliveryObservationOutcomeForbidden
		} else {
			if record.Profile == "messages-record@1" && record.Delivery == nil {
				_, _, err = r.host.reconcileMessageState(r.ctx, key, record)
				if errors.Is(err, errMessageStateInvalid) {
					err = r.host.messages.finishDelivery(r.ctx, key, record.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonLost)
				}
				if err == nil {
					var current *messageRecord
					current, err = r.host.messages.readRecord(r.ctx, key)
					if err == nil && current != nil && current.ID == record.ID && current.Token == record.Token {
						record = current
					} else if err == nil {
						err = errors.New("messages: observed acceptance changed")
					}
				}
			}
			if err != nil {
				out.Outcome = msg.DeliveryObservationOutcomeUnavailable
			} else {
				delivery := msg.DeliveryQueued
				if record.Delivery != nil {
					delivery = record.Delivery.State
					if delivery == msg.DeliveryUndelivered {
						out.Undelivered = new(record.Delivery.Reason)
					}
				} else if record.Acked {
					delivery = msg.DeliveryDelivered
				}
				out.Outcome = msg.DeliveryObservationOutcomeObserved
				out.Delivery = new(delivery)
			}
		}
	}
	if outcome := messageDeliveryPairState(r.ctx, r.host.messagePairing, r.remote.key, r.remote.local, r.remote.generation); outcome != msg.DeliveryObservationOutcomeObserved {
		return msg.ObserveDeliveryResult{Outcome: outcome}, nil
	}
	return out, nil
}
