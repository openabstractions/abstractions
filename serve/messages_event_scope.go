package main

import (
	"context"
	"errors"
	"strings"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func messageStateReceiver(record *messageRecord, request *msg.SendRequest) messageActor {
	return messageActor{Account: record.Actor.Account, Program: record.Receiver, Application: strings.TrimPrefix(request.To, "app:"), Context: request.Context}
}

func (s *messageStore) stateEventCheckFor(actor messageActor) messageEventCheck {
	return func(ctx context.Context, ref messageEventRef) (messageEventDisposition, error) {
		return s.checkScopedEvent(ctx, ref, &actor)
	}
}

func messageEventReceiverFor(record *messageRecord, actor messageActor) (bool, error) {
	if record.Actor.Account != actor.Account {
		return false, errors.New("messages: event account mismatch")
	}
	if record.Actor.Program == actor.Program && record.Actor.Context == actor.Context {
		return false, nil
	}
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil {
		return false, err
	}
	if request.Kind == msg.KindQuestion && record.Receiver == actor.Program && request.Context == actor.Context {
		return true, nil
	}
	return false, errors.New("messages: event context mismatch")
}

// Both owning contexts get a recoverable hint before one canonical question
// transition. The two existing head owners are acquired in path order; no
// head is acquired from inside the state/canonical callback.
func (s *messageStore) reserveStateEvent(ctx context.Context, record *messageRecord, request *msg.SendRequest, ref messageEventRef) error {
	if request.Kind != msg.KindQuestion {
		return s.reserveMessageEvent(ctx, record.Actor, ref, s.checkEvent)
	}
	first, second := record.Actor, messageStateReceiver(record, request)
	if s.eventPath(first) > s.eventPath(second) {
		first, second = second, first
	}
	if err := s.reserveMessageEvent(ctx, first, ref, s.stateEventCheckFor(first)); err != nil {
		return err
	}
	return s.reserveMessageEvent(ctx, second, ref, s.stateEventCheckFor(second))
}
func (s *messageStore) publishStateEvent(ctx context.Context, record *messageRecord, request *msg.SendRequest, ref messageEventRef, commit func() error) error {
	if request.Kind != msg.KindQuestion {
		return s.publishMessageEvent(ctx, record.Actor, ref, commit)
	}
	first, second := record.Actor, messageStateReceiver(record, request)
	if s.eventPath(first) == s.eventPath(second) {
		return errors.New("messages: state contexts share a native principal")
	}
	if s.eventPath(first) > s.eventPath(second) {
		first, second = second, first
	}
	return s.publishMessageEvent(ctx, first, ref, func() error { return s.publishMessageEvent(ctx, second, ref, commit) })
}
