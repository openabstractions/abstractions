package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Terminal delivery belongs to the canonical acceptance. Question answer state
// remains independent. The report is body-free and survives body expiry.
type messageDeliveryState struct {
	State    msg.Delivery
	Reason   msg.UndeliveredReason
	At       int64
	Report   string
	Actor    messageActor
	Reported bool
	Fold     *messageDeliveryFold `json:",omitempty"`
}

type messageDeliveryFold struct {
	Page          string
	Before, After messageEventRef
}

var errMessageDeliveryTerminal = errors.New("messages: delivery already terminal")
var errMessageDeliveryExpired = errors.New("messages: delivery acknowledgment expired")

func messageDeliveryActor(record *messageRecord) messageActor {
	actor := record.Actor
	actor.Context = actor.returnContext()
	return actor
}

// A successfully observed enrollment removal suppresses future native reports;
// the existing person Activity audit remains the destination for that outcome.
func (s *messageStore) markSenderRemoved(ctx context.Context, key, token string) error {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	record, err := s.readRecord(ctx, key)
	if err != nil {
		return err
	}
	if record == nil || record.Token != token {
		return errors.New("messages: sender occurrence changed")
	}
	if record.Delivery != nil {
		return nil
	}
	if err = s.retainAcceptedRoute(ctx, key, record); err != nil {
		return err
	}
	// Retain the observed disposition before its canonical projection. The
	// validated post-canonical route preserves it through canonical damage.
	if err = cas.ChangeLimitContext(ctx, s.statePath(key, token), messageStateLimit, func(raw []byte) ([]byte, error) {
		var state messageState
		if messageStoredJSON(raw, &state) != nil || state.Profile != "messages-state@1" || state.Token != token || !acceptedMessageRouteValid(state.Acceptance, key, token) {
			return nil, errMessageStateInvalid
		}
		if state.SenderRemoved {
			return raw, nil
		}
		state.SenderRemoved = true
		return json.Marshal(state)
	}); err != nil {
		return err
	}
	return cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
		var record messageRecord
		if messageStoredJSON(raw, &record) != nil || record.Token != token {
			return nil, errors.New("messages: sender occurrence changed")
		}
		if record.SenderRemoved || record.Delivery != nil {
			return raw, nil
		}
		record.SenderRemoved = true
		return json.Marshal(record)
	})
}

func messageDeliveryReport(record *messageRecord, request *msg.SendRequest, state msg.Delivery, reason msg.UndeliveredReason) string {
	if record.SenderRemoved || record.Actor.Remote != nil {
		return ""
	}
	if state == msg.DeliveryDelivered && (request.NotifyDelivered == nil || !*request.NotifyDelivered) {
		return ""
	}
	h := msg.Header{MessageID: record.ID, From: request.To, FromContext: request.Context, To: "app:" + record.Actor.Application, Context: record.Actor.returnContext(), Kind: msg.KindNote, ContextID: record.ContextID, ExpiresUnixMs: math.MaxInt64, Delivery: new(state), Count: new(int64(1))}
	if record.Forward != nil {
		ref := record.Forward.Reference
		h.Reference = &ref
		h.MessageID = ref.MessageID
	}
	if state == msg.DeliveryUndelivered && reason != "" {
		h.Undelivered = new(reason)
	}
	return string(msg.EncodeHeader(&h))
}

func (s *messageStore) finishDelivery(ctx context.Context, key, token string, state msg.Delivery, reason msg.UndeliveredReason) error {
	return s.finishDeliveryOf(ctx, key, token, state, reason, false)
}

// finishAnsweredDelivery reports a question its receiver answered as
// delivered, also at its expiry: the accepted reply proves it reached the
// receiver before then.
func (s *messageStore) finishAnsweredDelivery(ctx context.Context, key, token string) error {
	return s.finishDeliveryOf(ctx, key, token, msg.DeliveryDelivered, "", true)
}

func (s *messageStore) finishDeliveryOf(ctx context.Context, key, token string, state msg.Delivery, reason msg.UndeliveredReason, answered bool) error {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if state != msg.DeliveryDelivered && state != msg.DeliveryUndelivered {
		return errors.New("messages: nonterminal delivery transition")
	}
	record, err := s.readRecord(ctx, key)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("messages: delivery acceptance missing")
	}
	if record.Token != token {
		return nil
	}
	if record.Alias || record.Control {
		return nil
	}
	if record.Delivery != nil {
		return s.publishDeliveryReport(ctx, key, token, record.Delivery)
	}
	if !record.SenderRemoved {
		raw, err := cas.ReadLimitContext(ctx, s.statePath(key, token), messageStateLimit)
		if err != nil && !errors.Is(err, cas.ErrTooLarge) && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var retained messageState
		if err == nil && messageStoredJSON(raw, &retained) == nil && retained.Profile == "messages-state@1" && retained.Token == token && acceptedMessageRouteValid(retained.Acceptance, key, token) && retained.SenderRemoved {
			record.SenderRemoved = true
		}
	}
	if record.Forward != nil {
		return s.finishForwardDeliveryLocked(ctx, key, record, state, reason)
	}
	if record.Profile != "messages-record@1" {
		return errors.New("messages: delivery source unavailable")
	}
	if err := s.retainAcceptedRoute(ctx, key, record); err != nil {
		if !(state == msg.DeliveryUndelivered && reason == msg.UndeliveredReasonLost && errors.Is(err, errMessageStateInvalid)) {
			return err
		}
	}
	if state == msg.DeliveryDelivered {
		current, err := s.currentState(ctx, key, record)
		if err != nil {
			return err
		}
		if current.Refused || current.Held {
			return nil
		}
	}
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil {
		return err
	}
	if state == msg.DeliveryDelivered && !answered && record.ExpiresUnixMs <= s.now().UnixMilli() {
		return nil
	}
	if record.Acked {
		state, reason = msg.DeliveryDelivered, ""
	}
	next := &messageDeliveryState{State: state, Reason: reason, At: s.now().UnixMilli()}
	next.Report = messageDeliveryReport(record, request, state, reason)
	next.Actor = messageDeliveryActor(record)
	next.Reported = next.Report == ""
	commit := func() error {
		return cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
			var current messageRecord
			if messageStoredJSON(raw, &current) != nil || current.Token != token || current.Profile != "messages-record@1" {
				return nil, errors.New("messages: delivery acceptance changed")
			}
			if current.Delivery != nil {
				return nil, errMessageDeliveryTerminal
			}
			if state == msg.DeliveryDelivered && !answered && current.ExpiresUnixMs <= s.now().UnixMilli() {
				return nil, errMessageDeliveryExpired
			}
			current.Delivery = next
			current.SenderRemoved = current.SenderRemoved || record.SenderRemoved
			current.AuditDone = false
			if state == msg.DeliveryDelivered {
				current.Acked = true
			} else if reason == msg.UndeliveredReasonRefused || reason == msg.UndeliveredReasonReceiverRemoved || reason == msg.UndeliveredReasonPeerRemoved || reason == msg.UndeliveredReasonContextClosed || reason == msg.UndeliveredReasonLost {
				// Delivery safety cannot wait for a TaskState event-log slot.
				current.Refused, current.Held = true, false
			}
			return json.Marshal(current)
		})
	}
	if err := s.at("delivery_before_terminal"); err != nil {
		return err
	}
	err = commit()
	if errors.Is(err, errMessageDeliveryTerminal) || errors.Is(err, errMessageDeliveryExpired) {
		return nil
	}
	if err == nil {
		s.advance()
		if err := s.retainDeliveryEcho(ctx, key, token, next); err != nil {
			if !errors.Is(err, errMessageStateInvalid) {
				return err
			}
		}
		return s.publishDeliveryReport(ctx, key, token, next)
	}
	return err
}

// The canonical terminal decision precedes event publication. Until custody is
// transferred, the same canonical/tombstone owns a body-free pending report.
func (s *messageStore) publishDeliveryReport(ctx context.Context, key, token string, delivery *messageDeliveryState) error {
	if delivery.Reported || delivery.Report == "" {
		return nil
	}
	if err := s.retainDeliveryEcho(ctx, key, token, delivery); err != nil {
		if errors.Is(err, errMessageDeliveryTransferred) {
			return s.markDeliveryReported(ctx, key, token, delivery.Report)
		}
		if !errors.Is(err, errMessageStateInvalid) {
			return err
		}
	}
	ref := messageEventRef{Key: key, Token: token, Change: "delivery-" + token, Version: 1, ExpiresUnixMs: math.MaxInt64, AcceptedAt: delivery.At, DeliveryReport: delivery.Report}
	actor := delivery.Actor
	if err := s.recoverDeliveryEvent(ctx, actor); err != nil {
		return err
	}
	if delivery.Fold != nil {
		done, err := s.coalesceDeliveryReport(ctx, key, token, delivery, ref)
		if err != nil || done {
			return err
		}
	}
	published, err := s.deliveryReportPending(ctx, actor, ref)
	if err != nil {
		return err
	}
	if !published {
		if err := s.reserveMessageEvent(ctx, actor, ref, s.checkEvent); err != nil {
			if errors.Is(err, errMessageEventsFull) {
				done, foldErr := s.coalesceDeliveryReport(ctx, key, token, delivery, ref)
				if foldErr != nil {
					return foldErr
				}
				if done {
					return nil
				}
				// The canonical pending report keeps custody while this actor's
				// existing log is full. Reads and ACKs must remain able to drain it.
				return nil
			}
			return err
		}
		if err := s.publishMessageEvent(ctx, actor, ref, func() error { return nil }); err != nil {
			return err
		}
	}
	return s.markDeliveryReported(ctx, key, token, delivery.Report)
}

func (s *messageStore) recoverDeliveryEvent(ctx context.Context, actor messageActor) error {
	dir := s.eventPath(actor)
	// A previously interrupted delivery reservation belongs to this same event
	// owner; eventMu excludes the current process's live publication lifetime.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return cas.ChangeLimitContext(ctx, filepath.Join(dir, "head.json"), 4096, func(raw []byte) ([]byte, error) {
		head, err := readMessageEventHead(raw)
		if err != nil {
			return nil, err
		}
		if head.Pending == nil || head.Pending.Ref.DeliveryReport == "" {
			if raw == nil {
				return json.Marshal(head)
			}
			return raw, nil
		}
		if err := s.recoverEvent(ctx, dir, &head, s.checkEvent); err != nil {
			return nil, err
		}
		return json.Marshal(head)
	})
}

// A published outgoing report owns its body-free evidence after the acceptance's
// body expires or its request key is reused. ACK removes only this exact report.
func (s *messageStore) observeDeliveryReport(ctx context.Context, actor messageActor, ref messageEventRef) error {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	dir := s.eventPath(actor)
	return cas.ChangeLimitContext(ctx, filepath.Join(dir, "head.json"), 4096, func(raw []byte) ([]byte, error) {
		head, err := readMessageEventHead(raw)
		if err != nil {
			return nil, err
		}
		page := head.First
		for scans := 0; page != "" && scans < messageActorEventPages; scans++ {
			next := ""
			found := false
			err := s.changeEventPage(ctx, dir, page, func(p *messageEventPage) error {
				next = p.Next
				for i, entry := range p.Entries {
					if entry.Ref == ref && !entry.Pending {
						// Confirm the transfer before deleting its event evidence. A
						// crash after publication cannot resurrect an ACKed report.
						if err := s.markDeliveryReported(ctx, ref.Key, ref.Token, ref.DeliveryReport); err != nil {
							return err
						}
						p.Entries = append(p.Entries[:i], p.Entries[i+1:]...)
						found = true
						break
					}
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if found {
				return raw, nil
			}
			page = next
		}
		return raw, nil
	})
}

func (s *messageStore) markDeliveryReported(ctx context.Context, key, token, report string) error {
	if record, err := s.readRecord(ctx, key); err == nil && record != nil && record.Token == token && record.Delivery != nil && deliveryReportMatches(record.Delivery, report) {
		copy := *record.Delivery
		copy.Reported = true
		copy.Fold = nil
		if err := s.retainDeliveryEcho(ctx, key, token, &copy); err != nil {
			if !errors.Is(err, errMessageStateInvalid) {
				return err
			}
		}
	}
	err := cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, cas.ErrNoValue
		}
		var profile struct{ Profile string }
		if err := json.Unmarshal(raw, &profile); err != nil {
			return nil, err
		}
		if profile.Profile == "messages-expired@1" {
			var record messageExpiredRecord
			if err := messageStoredJSON(raw, &record); err != nil {
				return nil, err
			}
			if record.Token != token || record.Delivery == nil || !deliveryReportMatches(record.Delivery, report) {
				return raw, nil
			}
			record.Delivery.Reported = true
			record.Delivery.Fold = nil
			return json.Marshal(record)
		}
		var record messageRecord
		if err := messageStoredJSON(raw, &record); err != nil {
			return nil, err
		}
		if record.Token != token || record.Delivery == nil || !deliveryReportMatches(record.Delivery, report) {
			return raw, nil
		}
		record.Delivery.Reported = true
		record.Delivery.Fold = nil
		return json.Marshal(record)
	})
	if errors.Is(err, cas.ErrNoValue) {
		return nil
	}
	return err
}

func deliveryReportMatches(delivery *messageDeliveryState, report string) bool {
	return delivery.Report == report || delivery.Fold != nil && delivery.Fold.After.DeliveryReport == report
}

func (s *messageStore) setDeliveryFold(ctx context.Context, key, token string, fold *messageDeliveryFold) error {
	return cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
		var profile struct{ Profile string }
		if err := json.Unmarshal(raw, &profile); err != nil {
			return nil, err
		}
		if profile.Profile == "messages-expired@1" {
			var r messageExpiredRecord
			if err := messageStoredJSON(raw, &r); err != nil {
				return nil, err
			}
			if r.Token != token || r.Delivery == nil || r.Delivery.Reported {
				return nil, errMessageDeliveryTerminal
			}
			r.Delivery.Fold = fold
			return json.Marshal(r)
		}
		var r messageRecord
		if err := messageStoredJSON(raw, &r); err != nil {
			return nil, err
		}
		if r.Token != token || r.Delivery == nil || r.Delivery.Reported {
			return nil, errMessageDeliveryTerminal
		}
		r.Delivery.Fold = fold
		return json.Marshal(r)
	})
}

// A fold journals its exact before/after reference on the source canonical
// acceptance. ACK confirms transfer before removing the aggregate. If the old
// aggregate was ACKed before the fold reached its page, the source stays pending
// and can use the newly freed slot as an individual report.
func (s *messageStore) coalesceDeliveryReport(ctx context.Context, key, token string, delivery *messageDeliveryState, ref messageEventRef) (bool, error) {
	dir := s.eventPath(delivery.Actor)
	done := false
	err := cas.ChangeLimitContext(ctx, filepath.Join(dir, "head.json"), 4096, func(raw []byte) ([]byte, error) {
		head, err := readMessageEventHead(raw)
		if err != nil {
			return nil, err
		}
		fold := delivery.Fold
		if fold == nil {
			incoming, err := decodeRetainedMessageHeader([]byte(delivery.Report))
			if err != nil {
				return nil, err
			}
			page := head.First
			for scans := 0; page != "" && scans < messageActorEventPages && fold == nil; scans++ {
				p, err := s.eventPage(ctx, dir, page)
				if err != nil {
					return nil, err
				}
				for _, entry := range p.Entries {
					if entry.Pending || entry.Canceled || entry.Ref.DeliveryReport == "" {
						continue
					}
					h, err := decodeRetainedMessageHeader([]byte(entry.Ref.DeliveryReport))
					if err != nil {
						return nil, err
					}
					if h.From != incoming.From || h.Delivery == nil || incoming.Delivery == nil || *h.Delivery != *incoming.Delivery || h.Count == nil || *h.Count == math.MaxInt64 {
						continue
					}
					if (h.Reference == nil) != (incoming.Reference == nil) || h.Reference != nil && h.Reference.OwnerPeerKey != incoming.Reference.OwnerPeerKey {
						continue
					}
					h.Count = new(*h.Count + 1)
					if (h.Undelivered == nil) != (incoming.Undelivered == nil) || h.Undelivered != nil && *h.Undelivered != *incoming.Undelivered {
						h.Undelivered = nil
					}
					after := ref
					after.DeliveryReport = string(msg.EncodeHeader(h))
					if entry.Ref.AcceptedAt > 0 && entry.Ref.AcceptedAt < after.AcceptedAt {
						after.AcceptedAt = entry.Ref.AcceptedAt
					}
					fold = &messageDeliveryFold{Page: page, Before: entry.Ref, After: after}
					break
				}
				page = p.Next
			}
			if fold == nil {
				return raw, nil
			}
			if err := s.setDeliveryFold(ctx, key, token, fold); err != nil {
				return nil, err
			}
			if err := s.at("delivery_fold_prepared"); err != nil {
				return nil, err
			}
		}
		if !agentHex(fold.Page, 24) {
			return nil, errors.New("messages: invalid delivery fold page")
		}
		copy := *delivery
		copy.Fold = fold
		if err := s.retainDeliveryEcho(ctx, key, token, &copy); err != nil {
			if !errors.Is(err, errMessageStateInvalid) {
				return nil, err
			}
		}
		found := false
		if err := s.changeEventPage(ctx, dir, fold.Page, func(p *messageEventPage) error {
			for i, entry := range p.Entries {
				if entry.Ref == fold.After && !entry.Pending && !entry.Canceled {
					found = true
					return nil
				}
				if entry.Ref == fold.Before && !entry.Pending && !entry.Canceled {
					p.Entries[i].Ref = fold.After
					found = true
					return nil
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if !found {
			if err := s.setDeliveryFold(ctx, key, token, nil); err != nil {
				return nil, err
			}
			return raw, nil
		}
		if err := s.at("delivery_fold_published"); err != nil {
			return nil, err
		}
		if err := s.markDeliveryReported(ctx, key, token, delivery.Report); err != nil {
			return nil, err
		}
		done = true
		return raw, nil
	})
	return done, err
}

func (s *messageStore) deliveryReportPending(ctx context.Context, actor messageActor, ref messageEventRef) (bool, error) {
	dir := s.eventPath(actor)
	raw, err := cas.ReadLimitContext(ctx, filepath.Join(dir, "head.json"), 4096)
	if err != nil {
		return false, err
	}
	head, err := readMessageEventHead(raw)
	if err != nil {
		return false, err
	}
	page := head.First
	for scans := 0; page != "" && scans < messageActorEventPages; scans++ {
		p, err := s.eventPage(ctx, dir, page)
		if err != nil {
			return false, err
		}
		for _, entry := range p.Entries {
			if entry.Ref == ref && !entry.Pending && !entry.Canceled {
				return true, nil
			}
		}
		page = p.Next
	}
	return false, nil
}
