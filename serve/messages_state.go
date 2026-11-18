package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

type messageStateEvent struct {
	Version       uint64
	Change        string
	Held, Refused bool
	TaskState     msg.TaskState
	At            int64 `json:",omitempty"`
}
type messageState struct {
	Profile, Token         string
	Version                uint64
	Held, Refused          bool
	TaskState              msg.TaskState
	Observed               uint16
	ReceiverObserved       uint16
	Events                 []messageStateEvent
	ReplyKey, ReplyToken   string
	CancelKey, CancelToken string
	ReplyAuthorized        bool
	AcceptedTerminal       string
	RemoteReply            *messageRemoteReplyIntent `json:",omitempty"`
	SimpleReply            *messageSimpleReplyIntent `json:",omitempty"`
	Acceptance             *messageAcceptedRoute     `json:",omitempty"`
	Delivery               *messageDeliveryState     `json:",omitempty"`
	SenderRemoved          bool                      `json:",omitempty"`
}

// Post-canonical acceptance evidence in the existing state owner. This contains
// routing and attributed metadata only; it cannot reconstruct a message body.
type messageAcceptedRoute struct {
	Forward                                   *messageForwardRoute `json:",omitempty"`
	Digest                                    string
	Key, Token, Pair, ID, ContextID, Receiver string
	Actor                                     messageActor
	AcceptedAt                                int64
	ExpiresUnixMs                             int64 `json:"ExpiresAt"`
	To, Context                               string
	Kind                                      msg.Kind
	NotifyDelivered                           bool
	Audit                                     *messageAuditEntry `json:",omitempty"`
}

const messageStateLimit = messageRecordLimit

var errMessageStateInvalid = errors.New("messages: invalid state evidence")

func boundedMessageStateError(err error) error {
	if errors.Is(err, cas.ErrTooLarge) {
		return fmt.Errorf("%w: %v", errMessageStateInvalid, err)
	}
	return err
}

func (s *messageStore) retainAcceptedRoute(ctx context.Context, key string, record *messageRecord) error {
	if record.Forward != nil {
		return s.retainForwardRoute(ctx, key, record)
	}
	if record.Alias || record.Control || record.Lost {
		return nil
	}
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil {
		return err
	}
	route := &messageAcceptedRoute{Key: key, Token: record.Token, Pair: record.Pair, ID: record.ID, ContextID: record.ContextID, Receiver: record.Receiver, Actor: record.Actor, AcceptedAt: record.AcceptedAt, ExpiresUnixMs: record.ExpiresUnixMs, To: request.To, Context: request.Context, Kind: request.Kind, NotifyDelivered: request.NotifyDelivered != nil && *request.NotifyDelivered, Audit: record.Audit}
	route.Digest = messageDigest(string(record.Spec))
	err = cas.ChangeLimitContext(ctx, s.statePath(key, record.Token), messageStateLimit, func(raw []byte) ([]byte, error) {
		state, err := readMessageState(raw, record)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errMessageStateInvalid, err)
		}
		if state.Acceptance != nil {
			old := state.Acceptance
			if old.Key != key || old.Token != record.Token || old.ID != record.ID || old.AcceptedAt != record.AcceptedAt {
				return nil, errors.New("messages: acceptance route custody differs")
			}
			return raw, nil
		}
		state.Acceptance = route
		return json.Marshal(state)
	})
	return boundedMessageStateError(err)
}

// Holding ends once, refusal happens once, and a question becomes terminal
// once. Four entries cover every legal visible transition without imposing a
// message/alias quota. Receiver acknowledgment is independent of these events.
const messageStateEventLimit = 4

func (s *messageStore) statePath(key, token string) string {
	return filepath.Join(s.dir, "states", key+"-"+token+".json")
}

func readMessageState(raw []byte, r *messageRecord) (messageState, error) {
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil {
		return messageState{}, err
	}
	// The internal projection always stores a valid generated enum. TaskState
	// is disclosed only for questions; note/reply refusals use Refused.
	state := messageState{Profile: "messages-state@1", Token: r.Token, Held: r.Held, Refused: r.Refused, TaskState: msg.TaskStateSubmitted, Events: []messageStateEvent{}}
	if request.Kind == msg.KindQuestion {
		state.TaskState = msg.TaskStateSubmitted
		if r.Held {
			state.TaskState = msg.TaskStateAuthRequired
		}
	}
	if raw == nil {
		return state, nil
	}
	if err = messageStoredJSON(raw, &state); err != nil || state.Profile != "messages-state@1" || state.Token != r.Token || state.Version > messageStateEventLimit || len(state.Events) != int(state.Version) || state.Observed>>state.Version != 0 || state.ReceiverObserved>>state.Version != 0 {
		return state, errors.New("messages: invalid state record")
	}
	if route := state.Acceptance; route != nil {
		if !agentHex(route.Key, 32) || route.Token != r.Token || route.Pair != r.Pair || route.ID != r.ID || route.ContextID != r.ContextID || route.Receiver != r.Receiver || route.AcceptedAt != r.AcceptedAt || route.ExpiresUnixMs != r.ExpiresUnixMs || route.To != request.To || route.Context != request.Context || route.Kind != request.Kind || !reflect.DeepEqual(route.Actor, r.Actor) {
			return state, errors.New("messages: accepted route differs from canonical custody")
		}
	}
	if (state.ReplyKey == "") != (state.ReplyToken == "") || (state.ReplyKey != "" && (!agentHex(state.ReplyKey, 32) || !agentHex(state.ReplyToken, 24))) {
		return state, errors.New("messages: invalid reply reservation")
	}
	if (state.CancelKey == "") != (state.CancelToken == "") || (state.CancelKey != "" && (!agentHex(state.CancelKey, 32) || !agentHex(state.CancelToken, 24))) {
		return state, errors.New("messages: invalid cancellation reservation")
	}
	if state.AcceptedTerminal != "" && state.AcceptedTerminal != "complete" && state.AcceptedTerminal != "cancel" {
		return state, errors.New("messages: invalid accepted terminal intent")
	}
	if state.AcceptedTerminal != "" && request.Kind != msg.KindQuestion || state.ReplyAuthorized && request.Kind != msg.KindReply {
		return state, errors.New("messages: terminal authorization kind mismatch")
	}
	if intent := state.RemoteReply; intent != nil {
		if r.Actor.Remote == nil || request.Kind != msg.KindQuestion || !agentHex(intent.Fingerprint, 32) || intent.Completed && intent.Failed || intent.Reference != nil && (!validPairingHex(intent.Reference.OwnerPeerKey, 32) || len(intent.Reference.MessageID) < 1 || len(intent.Reference.MessageID) > 128) {
			return state, errors.New("messages: invalid remote reply intent")
		}
		if len(intent.ContextID) > 128 || intent.ContextID != "" && (intent.Reference == nil || !intent.Completed && !intent.Failed) {
			return state, errors.New("messages: invalid remote reply receipt")
		}
	}
	if pin := state.SimpleReply; pin != nil {
		if request.Kind != msg.KindQuestion || !validSimpleReplyPin(pin) {
			return state, errors.New("messages: invalid simplified reply pin")
		}
	}
	for i, event := range state.Events {
		if event.Version != uint64(i+1) || !agentHex(event.Change, 24) {
			return state, errors.New("messages: invalid state history")
		}
	}
	if len(state.Events) > 0 {
		last := state.Events[len(state.Events)-1]
		if last.Held != state.Held || last.Refused != state.Refused || last.TaskState != state.TaskState {
			return state, errors.New("messages: inconsistent current state")
		}
	}
	return state, nil
}

func (s *messageStore) currentState(ctx context.Context, key string, r *messageRecord) (messageState, error) {
	raw, err := cas.ReadLimitContext(ctx, s.statePath(key, r.Token), messageStateLimit)
	if err != nil {
		if errors.Is(err, cas.ErrTooLarge) {
			return messageState{}, fmt.Errorf("%w: %v", errMessageStateInvalid, err)
		}
		return messageState{}, err
	}
	state, err := readMessageState(raw, r)
	if err != nil {
		return state, fmt.Errorf("%w: %v", errMessageStateInvalid, err)
	}
	return state, nil
}

func messageTerminal(state msg.TaskState) bool {
	return state == msg.TaskStateCompleted || state == msg.TaskStateCanceled || state == msg.TaskStateFailed || state == msg.TaskStateRejected
}

func messageNextState(current messageState, r *messageRecord, request msg.SendRequest, operation string, now int64) (messageState, error) {
	if request.Kind == msg.KindQuestion && !messageTerminal(current.TaskState) {
		switch current.AcceptedTerminal {
		case "complete":
			current.TaskState = msg.TaskStateCompleted
		case "cancel":
			current.TaskState = msg.TaskStateCanceled
		}
	}
	next := current
	switch operation {
	case "release":
		if !current.Refused {
			next.Held = false
			if request.Kind == msg.KindReply {
				next.ReplyAuthorized = true
			}
			if next.TaskState == msg.TaskStateAuthRequired {
				next.TaskState = msg.TaskStateSubmitted
				if request.AnswerByUnixMs <= now {
					next.TaskState = msg.TaskStateFailed
				}
			}
		}
	case "refuse":
		if !r.Acked {
			next.Held, next.Refused = false, true
			if request.Kind == msg.KindQuestion && !messageTerminal(current.TaskState) {
				next.TaskState = msg.TaskStateRejected
			}
		}
	case "complete", "cancel", "fail":
		if request.Kind != msg.KindQuestion {
			return current, errors.New("messages: state target is not a question")
		}
		if !messageTerminal(current.TaskState) {
			switch operation {
			case "complete":
				next.TaskState = msg.TaskStateCompleted
			case "cancel":
				next.TaskState = msg.TaskStateCanceled
			case "fail":
				next.TaskState = msg.TaskStateFailed
			}
		}
	default:
		return current, errors.New("messages: unknown state operation")
	}
	return next, nil
}

func messageStateEqual(a, b messageState) bool {
	return a.Held == b.Held && a.Refused == b.Refused && a.TaskState == b.TaskState
}

// transitionState uses the reviewed event reference owner as a publication
// fence. No canonical lock is held while reserving a reference. Its commit
// callback acquires only this bounded state CAS; it never reenters event heads,
// enrollment, policy, Book or a blocking wait.
func (s *messageStore) transitionState(ctx context.Context, key string, r *messageRecord, operation string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	current, err := s.currentState(ctx, key, r)
	if err != nil {
		return err
	}
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil {
		return err
	}
	if request.Kind == msg.KindQuestion && (operation == "fail" || operation == "cancel" || operation == "complete") {
		intent, e := s.questionTerminalIntent(ctx, current)
		if e != nil {
			return e
		}
		if intent != "" {
			operation = intent
		}
	}
	next, err := messageNextState(current, r, *request, operation, s.now().UnixMilli())
	if err != nil || messageStateEqual(current, next) {
		return err
	}
	if current.Version >= messageStateEventLimit {
		return errors.New("messages: illegal repeated state transition")
	}
	change, err := applicationToken()
	if err != nil {
		return err
	}
	ref := messageEventRef{Key: key, Token: r.Token, Change: change, Version: current.Version + 1, ExpiresUnixMs: r.ExpiresUnixMs}
	if err = s.reserveStateEvent(ctx, r, request, ref); err != nil {
		return err
	}
	if err = s.at("state_event_reserved"); err != nil {
		return err
	}
	err = s.publishStateEvent(ctx, r, request, ref, func() error {
		return cas.ChangeLimitContext(ctx, s.statePath(key, r.Token), messageStateLimit, func(raw []byte) ([]byte, error) {
			actual, err := s.readRecord(ctx, key)
			if err != nil || actual == nil || actual.Token != r.Token || actual.ExpiresUnixMs <= s.now().UnixMilli() {
				return nil, errors.New("messages: state acceptance unavailable or expired")
			}
			state, err := readMessageState(raw, actual)
			if err != nil {
				return nil, err
			}
			if state.Version != current.Version {
				return nil, errors.New("messages: state version changed before publication")
			}
			effective := operation
			if request.Kind == msg.KindQuestion && (operation == "fail" || operation == "cancel" || operation == "complete") {
				intent, e := s.questionTerminalIntent(ctx, state)
				if e != nil {
					return nil, e
				}
				if intent != "" {
					effective = intent
				}
			}
			updated, err := messageNextState(state, actual, *request, effective, s.now().UnixMilli())
			if err != nil {
				return nil, err
			}
			if request.Kind == msg.KindReply && effective == "release" && !state.ReplyAuthorized {
				if err = s.authorizeHeldReply(ctx, actual, key); err != nil {
					return nil, err
				}
			}
			if messageStateEqual(state, updated) {
				return raw, nil
			}
			updated.Version = ref.Version
			updated.Events = append(updated.Events, messageStateEvent{Version: ref.Version, Change: change, Held: updated.Held, Refused: updated.Refused, TaskState: updated.TaskState, At: s.now().UnixMilli()})
			if err = s.at("before_state_record"); err != nil {
				return nil, err
			}
			return json.Marshal(updated)
		})
	})
	if err == nil {
		s.advance()
	}
	return err
}

func (s *messageStore) checkEvent(ctx context.Context, ref messageEventRef) (messageEventDisposition, error) {
	return s.checkScopedEvent(ctx, ref, nil)
}
func (s *messageStore) checkScopedEvent(ctx context.Context, ref messageEventRef, actor *messageActor) (messageEventDisposition, error) {
	if ref.DeliveryReport != "" {
		record, err := s.readRecord(ctx, ref.Key)
		if err != nil {
			return messageEventPending, err
		}
		if record != nil && record.Token == ref.Token && record.Delivery != nil && record.Delivery.Report == ref.DeliveryReport {
			return messageEventReady, nil
		}
		return messageEventPending, nil
	}
	if ref.ExpiresUnixMs <= s.now().UnixMilli() {
		return messageEventExpired, nil
	}
	r, err := s.readRecord(ctx, ref.Key)
	if err != nil || r == nil || r.Token != ref.Token {
		return messageEventPending, errors.New("messages: event lost canonical acceptance")
	}
	// Its terminal delivery report and Activity retain the accepted route and
	// question state. A missing body cannot be replayed as an ordinary event.
	if r.Lost || r.Delivery != nil && r.Delivery.State == msg.DeliveryUndelivered && r.Delivery.Reason == msg.UndeliveredReasonLost {
		return messageEventExpired, nil
	}
	state, err := s.currentState(ctx, ref.Key, r)
	if err != nil {
		return messageEventPending, err
	}
	receiver := false
	if actor != nil {
		var e error
		receiver, e = messageEventReceiverFor(r, *actor)
		if e != nil {
			return messageEventPending, e
		}
	}
	if receiver && state.Refused && !r.Acked {
		return messageEventObserved, nil
	}
	for _, event := range state.Events {
		if event.Version == ref.Version && event.Change == ref.Change {
			observed := state.Observed
			if receiver {
				observed = state.ReceiverObserved
			}
			if observed&(1<<(ref.Version-1)) != 0 {
				return messageEventObserved, nil
			}
			return messageEventReady, nil
		}
	}
	return messageEventPending, nil
}

func (s *messageStore) observeEvent(ctx context.Context, ref messageEventRef) error {
	return s.observeScopedEvent(ctx, ref, false)
}
func (s *messageStore) observeScopedEvent(ctx context.Context, ref messageEventRef, receiver bool) error {
	return cas.ChangeLimitContext(ctx, s.statePath(ref.Key, ref.Token), messageStateLimit, func(raw []byte) ([]byte, error) {
		r, err := s.readRecord(ctx, ref.Key)
		if err != nil {
			return nil, err
		}
		if r == nil || r.Token != ref.Token || r.ExpiresUnixMs <= s.now().UnixMilli() {
			return raw, nil
		}
		state, err := readMessageState(raw, r)
		if err != nil {
			return nil, err
		}
		for _, event := range state.Events {
			if event.Version == ref.Version && event.Change == ref.Change {
				if receiver {
					state.ReceiverObserved |= 1 << (ref.Version - 1)
				} else {
					state.Observed |= 1 << (ref.Version - 1)
				}
				return json.Marshal(state)
			}
		}
		return nil, errors.New("messages: unreceived state event")
	})
}
