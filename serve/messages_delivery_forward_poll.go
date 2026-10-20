package main

import (
	"context"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Poll only the source's retained successful acceptance. All transport work
// occurs outside the canonical terminal owner's locks.
func (h *applicationsHost) observeForwardDelivery(ctx context.Context, key string, record *messageRecord) error {
	if h == nil || h.messages == nil || record == nil || record.Profile != messageForwardProfile || record.Forward == nil || record.Alias || record.Actor.Remote != nil || record.Actor.Account != h.owner {
		return nil
	}
	s := h.messages
	current, err := s.readRecord(ctx, key)
	if err != nil {
		return err
	}
	if current == nil || current.Token != record.Token || current.Profile != messageForwardProfile || current.Forward == nil || current.Alias || current.Actor.Remote != nil || current.Actor.Account != h.owner || current.Delivery != nil || !validMessageForward(current.Forward, key) {
		return nil
	}
	f := current.Forward
	finish := func(delivery msg.Delivery, reason msg.UndeliveredReason) error {
		var image applicationAgentImage
		source := h.directory.currentAgentEnrollmentImage(ctx, current.Actor.Application, &image)
		if source.Outcome == applications.OutcomeUnavailable {
			return nil
		}
		if source.Enrollment == nil || source.Enrollment.State != applications.AgentEnrollmentStateEnrolled || !samePrograms(image.Program, current.Actor.Program) {
			if err := s.markSenderRemoved(ctx, key, current.Token); err != nil {
				return err
			}
		}
		return s.finishForwardDelivery(ctx, key, current.Token, delivery, reason)
	}
	uncertain := func() error {
		if current.ExpiresUnixMs <= s.now().UnixMilli() {
			return finish(msg.DeliveryUndelivered, msg.UndeliveredReasonUnconfirmed)
		}
		return nil
	}
	observing, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	gate := messageDeliveryPairState(observing, h.messagePairing, f.Reference.OwnerPeerKey, f.LocalKey, f.Generation)
	if gate == msg.DeliveryObservationOutcomeForbidden {
		return finish(msg.DeliveryUndelivered, msg.UndeliveredReasonPeerRemoved)
	}
	if gate != msg.DeliveryObservationOutcomeObserved {
		return uncertain()
	}
	r := &messagesReceiver{host: h, ctx: observing}
	client, generation, err := r.pairedTransport(f.Reference.OwnerPeerKey)
	var out msg.ObserveDeliveryResult
	if err == nil && generation == f.Generation {
		out, err = client.ObserveDelivery(f.Reference, f.Generation)
	}
	gate = messageDeliveryPairState(observing, h.messagePairing, f.Reference.OwnerPeerKey, f.LocalKey, f.Generation)
	if gate == msg.DeliveryObservationOutcomeForbidden {
		return finish(msg.DeliveryUndelivered, msg.UndeliveredReasonPeerRemoved)
	}
	if gate != msg.DeliveryObservationOutcomeObserved || err != nil || generation != f.Generation || out.Outcome != msg.DeliveryObservationOutcomeObserved || out.Delivery == nil {
		return uncertain()
	}
	switch *out.Delivery {
	case msg.DeliveryDelivered:
		return finish(msg.DeliveryDelivered, "")
	case msg.DeliveryUndelivered:
		reason := msg.UndeliveredReason("")
		if out.Undelivered != nil {
			reason = *out.Undelivered
		}
		return finish(msg.DeliveryUndelivered, reason)
	default:
		return uncertain()
	}
}
