package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

type messageDelivery struct {
	Profile, Owner, LastAck, Cursor string
	Pointers                        []messagePointer
	// Earlier cursors of the live page. A replay that grew the page keeps
	// each earlier cursor able to acknowledge exactly the pointers it showed.
	Earlier []messageEarlierCursor `json:",omitempty"`
}

type messageEarlierCursor struct {
	Cursor string
	Shown  []string
}

// messageDeliveryLimit bounds one inbox's delivery cursor file.
const messageDeliveryLimit = 32 << 10

// A pointer's identity is a fixed-size digest: a delivery report embeds its
// whole header, and earlier cursors retain identities in the bounded file.
func messagePointerIdentity(ref messagePointer) string {
	return messageDigest(ref.Key + "\x00" + ref.Token + "\x00" + strconv.FormatUint(ref.EventVersion, 10) + "\x00" + ref.DeliveryReport)
}

func readMessageDelivery(raw []byte) (messageDelivery, error) {
	d := messageDelivery{Profile: "messages-delivery@1", Pointers: []messagePointer{}}
	if raw == nil {
		return d, nil
	}
	if err := messageStoredJSON(raw, &d); err != nil || d.Profile != "messages-delivery@1" || d.Pointers == nil || len(d.Pointers) > 10 || len(d.Earlier) > 10 {
		return d, errors.New("messages: invalid delivery state")
	}
	for _, ref := range d.Pointers {
		if !agentHex(ref.Key, 32) || ref.Token == "" {
			return d, errors.New("messages: invalid delivery pointer")
		}
	}
	for _, earlier := range d.Earlier {
		if earlier.Cursor == "" || earlier.Cursor == d.Cursor || len(earlier.Shown) == 0 || len(earlier.Shown) > 10 {
			return d, errors.New("messages: invalid earlier delivery cursor")
		}
		for _, id := range earlier.Shown {
			if !agentHex(id, 32) {
				return d, errors.New("messages: invalid earlier delivery identity")
			}
		}
	}
	return d, nil
}

func (s *messageStore) queueCandidates(ctx context.Context, account, receiver, contextName string) ([]messagePointer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var refs []messagePointer
	err := cas.ChangeLimitContext(ctx, s.queuePath(account, receiver, contextName), 64<<10, func(raw []byte) ([]byte, error) {
		q, err := readMessageQueue(raw)
		if err != nil {
			return nil, err
		}
		if err = s.recoverQueue(ctx, &q); err != nil {
			return nil, err
		}
		refs = append([]messagePointer{}, q.Pointers...)
		return json.Marshal(q)
	})
	return refs, err
}

// Header observation must preserve uncertainty about canonical acceptance.
// Queue recovery can replace a damaged body with a lost-delivery tombstone;
// observing headers must not turn that damage into an authoritative empty page.
func (s *messageStore) headerQueueCandidates(ctx context.Context, account, receiver, contextName string) ([]messagePointer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := cas.ReadLimitContext(ctx, s.queuePath(account, receiver, contextName), 64<<10)
	if err != nil {
		return nil, err
	}
	q, err := readMessageQueue(raw)
	if err != nil {
		return nil, err
	}
	refs := make([]messagePointer, 0, len(q.Pointers))
	for _, ref := range q.Pointers {
		if ref.ExpiresUnixMs <= s.now().UnixMilli() {
			continue
		}
		record, err := s.readRecord(ctx, ref.Key)
		if err != nil {
			return nil, err
		}
		if record == nil && ref.Pending {
			continue
		}
		if record == nil || record.Token != ref.Token {
			return nil, errors.New("messages: headers lost canonical acceptance")
		}
		if record.Lost {
			continue
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func messageRecordView(r *messageRecord, state messageState) (msg.Message, error) {
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil {
		return msg.Message{}, err
	}
	h := msg.Header{MessageID: r.ID, From: "app:" + r.Actor.Application, FromContext: r.Actor.returnContext(), To: request.To, Context: request.Context, Kind: request.Kind, Subject: nativeMessageHeader(request.Subject), ContextID: r.ContextID, ExpiresUnixMs: r.ExpiresUnixMs, ReplyTo: request.ReplyTo, AnswerByUnixMs: request.AnswerByUnixMs, Refused: state.Refused}
	if remote := r.Actor.Remote; remote != nil {
		h.Reference = &msg.MessageRef{OwnerPeerKey: remote.LocalKey, MessageID: r.ID}
		h.RemoteOrigin = &msg.RemoteOrigin{PeerKey: remote.PeerKey, Claim: msg.OriginClaim{Namespace: remote.Namespace, Application: r.Actor.Application, ProgramLabel: remote.ProgramLabel, ReturnContext: remote.ReturnContext}}
		h.ReplyReference = request.ReplyReference
	}
	if request.Kind == msg.KindQuestion {
		task := state.TaskState
		h.TaskState = &task
	}
	return msg.Message{Header: h, IdempotencyKey: request.IdempotencyKey, Body: request.Body}, nil
}

func (s *messageStore) acknowledge(ctx context.Context, ref messagePointer) error {
	return s.finishDelivery(ctx, ref.Key, ref.Token, msg.DeliveryDelivered, "")
}

// bodyPage receives the bounded set of records permitted at this operation's
// receiving decision point. Native ownership and current class are held by the
// outer receiver. No policy, enrollment or blocking wait runs under these locks.
// The persisted page exists before it can be replied, and only its cursor can
// acknowledge those exact records for this native occurrence.
func (s *messageStore) bodyPage(ctx context.Context, account, receiver, contextName, owner, after string, permitted map[string]string) msg.Page {
	page, _ := s.deliveryPage(ctx, account, receiver, contextName, owner, after, permitted, nil)
	return page
}

// deliveryPage also returns the incoming bodies the persisted page holds,
// which the receiving Wait marks shown once it answers with the page.
func (s *messageStore) deliveryPage(ctx context.Context, account, receiver, contextName, owner, after string, permitted map[string]string, outgoing []messageEventRef) (msg.Page, []messagePointer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := msg.Page{Outcome: msg.OutcomeUnknown, Messages: []msg.Message{}, Headers: []msg.Header{}}
	var handed []messagePointer // incoming bodies on the persisted page
	path := filepath.Join(s.dir, "cursors", messageDigest(account+"\x00"+receiver+"\x00"+contextName)+".json")
	err := cas.ChangeLimitContext(ctx, path, messageDeliveryLimit, func(raw []byte) ([]byte, error) {
		d, err := readMessageDelivery(raw)
		if err != nil {
			return nil, err
		}
		if d.Owner != owner {
			if after != "" {
				page.Outcome = msg.OutcomeGap
				if raw == nil {
					return json.Marshal(d)
				}
				return raw, nil
			}
			d.Owner, d.LastAck, d.Cursor, d.Pointers, d.Earlier = owner, "", "", []messagePointer{}, nil
		}
		if after != "" {
			// The live cursor acknowledges the whole page. An earlier cursor of
			// a page that grew on replay acknowledges only what it showed.
			matched, shown := after == d.Cursor, d.Pointers
			for _, earlier := range d.Earlier {
				if after != earlier.Cursor {
					continue
				}
				matched, shown = true, []messagePointer{}
				ids := map[string]bool{}
				for _, id := range earlier.Shown {
					ids[id] = true
				}
				for _, ref := range d.Pointers {
					if ids[messagePointerIdentity(ref)] {
						shown = append(shown, ref)
					}
				}
			}
			if matched {
				// Expiry retires a page's acknowledgment position. Check the
				// retained pointers before touching records: collection may have
				// replaced their bodies with tombstones already.
				for _, ref := range shown {
					if ref.ExpiresUnixMs <= s.now().UnixMilli() {
						page.Outcome = msg.OutcomeGap
						return raw, nil
					}
				}
				for _, ref := range shown {
					if ref.DeliveryReport != "" {
						if err := s.observeDeliveryReport(ctx, messageActor{Account: account, Program: receiver, Context: contextName}, messageEventRef{Key: ref.Key, Token: ref.Token, Change: ref.EventChange, Version: ref.EventVersion, ExpiresUnixMs: ref.ExpiresUnixMs, AcceptedAt: ref.AcceptedAt, DeliveryReport: ref.DeliveryReport}); err != nil {
							return nil, err
						}
						continue
					}
					if ref.EventVersion > 0 {
						record, e := s.readRecord(ctx, ref.Key)
						if e != nil {
							return nil, e
						}
						if record == nil || record.Token != ref.Token {
							return nil, errors.New("messages: acknowledged event lost acceptance")
						}
						incoming, e := messageEventReceiverFor(record, messageActor{Account: account, Program: receiver, Context: contextName})
						if e != nil {
							return nil, e
						}
						err = s.observeScopedEvent(ctx, messageEventRef{Key: ref.Key, Token: ref.Token, Change: ref.EventChange, Version: ref.EventVersion, ExpiresUnixMs: ref.ExpiresUnixMs}, incoming)
					} else {
						err = s.acknowledge(ctx, ref)
					}
					if err != nil {
						return nil, err
					}
				}
				if err = s.at("records_acknowledged"); err != nil {
					return nil, err
				}
				d.LastAck, d.Cursor, d.Pointers, d.Earlier = after, "", []messagePointer{}, nil
			} else if after != d.LastAck {
				page.Outcome = msg.OutcomeGap
				return raw, nil
			}
		}
		// A replayed page must not hide a newer eligible body. The live page's
		// records come first; newer ones extend it under a fresh cursor while
		// the replayed cursor still acknowledges exactly what it showed.
		for scan := 0; scan < 2; scan++ {
			refs := d.Pointers
			{
				queueRaw, err := cas.ReadLimitContext(ctx, s.queuePath(account, receiver, contextName), 64<<10)
				if err != nil {
					return nil, err
				}
				q, err := readMessageQueue(queueRaw)
				if err != nil {
					return nil, err
				}
				events := []messagePointer{}
				incomingKeys := map[string]bool{}
				for _, ref := range q.Pointers {
					incomingKeys[ref.Key+"\x00"+ref.Token] = true
				}
				for _, event := range outgoing {
					ref := messagePointer{Key: event.Key, Token: event.Token, ExpiresUnixMs: event.ExpiresUnixMs, AcceptedAt: event.AcceptedAt, EventVersion: event.Version, EventChange: event.Change, DeliveryReport: event.DeliveryReport}
					// The incoming pointer owns body ACK when the same question
					// also has a receiving state event.
					if event.DeliveryReport == "" && incomingKeys[event.Key+"\x00"+event.Token] {
						continue
					}
					events = append(events, ref)
				}
				// Each read makes progress on incoming bodies and outgoing reports,
				// including when an incoming queue is continuously replenished.
				fresh := make([]messagePointer, 0, len(q.Pointers)+len(outgoing))
				for i := 0; i < len(q.Pointers) || i < len(events); i++ {
					if i < len(q.Pointers) {
						fresh = append(fresh, q.Pointers[i])
					}
					if i < len(events) {
						fresh = append(fresh, events[i])
					}
				}
				if d.Cursor == "" {
					refs = fresh
				} else {
					live := map[string]bool{}
					for _, ref := range d.Pointers {
						live[messagePointerIdentity(ref)] = true
					}
					refs = append([]messagePointer{}, d.Pointers...)
					for _, ref := range fresh {
						if !live[messagePointerIdentity(ref)] {
							refs = append(refs, ref)
						}
					}
				}
			}
			selected := make([]messagePointer, 0, 10)
			seenMessages := map[string]bool{}
			for _, ref := range refs {
				if permitted[ref.Key] != ref.Token {
					continue
				}
				if ref.DeliveryReport != "" {
					live, err := s.deliveryReportPending(ctx, messageActor{Account: account, Program: receiver, Context: contextName}, messageEventRef{Key: ref.Key, Token: ref.Token, Change: ref.EventChange, Version: ref.EventVersion, ExpiresUnixMs: ref.ExpiresUnixMs, AcceptedAt: ref.AcceptedAt, DeliveryReport: ref.DeliveryReport})
					if err != nil {
						return nil, err
					}
					if !live {
						continue
					}
					h, err := decodeRetainedMessageHeader([]byte(ref.DeliveryReport))
					if err != nil {
						return nil, err
					}
					identity := "delivery-" + ref.Token
					if !seenMessages[identity] {
						page.Messages = append(page.Messages, msg.Message{Header: *h, IdempotencyKey: ref.EventChange})
						seenMessages[identity] = true
					}
					selected = append(selected, ref)
					if len(selected) == 10 {
						break
					}
					continue
				}
				r, err := s.readRecord(ctx, ref.Key)
				if err != nil {
					return nil, err
				}
				if r == nil || r.Token != ref.Token || r.ExpiresUnixMs <= s.now().UnixMilli() {
					continue
				}
				state, err := s.currentState(ctx, ref.Key, r)
				if err != nil {
					return nil, err
				}
				request, err := decodeRetainedMessageRequest(r.Spec)
				if err != nil || r.Actor.Account != account {
					return nil, errors.New("messages: delivery routing mismatch")
				}
				if ref.EventVersion > 0 {
					actor := messageActor{Account: account, Program: receiver, Context: contextName}
					ready, err := s.checkScopedEvent(ctx, messageEventRef{Key: ref.Key, Token: ref.Token, Change: ref.EventChange, Version: ref.EventVersion, ExpiresUnixMs: ref.ExpiresUnixMs}, &actor)
					if err != nil {
						return nil, err
					}
					if ready != messageEventReady {
						continue
					}
				} else if r.Acked || r.Refused || state.Refused || state.Held {
					continue
				} else if r.Receiver != receiver || request.Context != contextName {
					return nil, errors.New("messages: body routing mismatch")
				}
				view, err := messageRecordView(r, state)
				if err != nil {
					return nil, err
				}
				if !seenMessages[r.ID] {
					page.Messages = append(page.Messages, view)
					seenMessages[r.ID] = true
				}
				selected = append(selected, ref)
				if len(selected) == 10 {
					break
				}
			}
			if len(selected) == 0 {
				if d.Cursor != "" {
					d.Cursor, d.Pointers, d.Earlier = "", []messagePointer{}, nil
					continue
				}
				d.Cursor, d.Pointers, d.Earlier = "", []messagePointer{}, nil
			} else {
				if d.Cursor != "" {
					shown := map[string]bool{}
					ids := make([]string, 0, len(d.Pointers))
					for _, ref := range d.Pointers {
						shown[messagePointerIdentity(ref)] = true
						ids = append(ids, messagePointerIdentity(ref))
					}
					grew := false
					for _, ref := range selected {
						grew = grew || !shown[messagePointerIdentity(ref)]
					}
					if grew {
						// The replayed cursor stays valid for what it showed. The
						// oldest earlier cursor retires past the page bound and
						// answers gap, which reopens and replays.
						d.Earlier = append(d.Earlier, messageEarlierCursor{Cursor: d.Cursor, Shown: ids})
						if len(d.Earlier) > 10 {
							d.Earlier = d.Earlier[len(d.Earlier)-10:]
						}
						d.Cursor = ""
					}
				}
				if d.Cursor == "" {
					d.Cursor, err = applicationToken()
					if err != nil {
						return nil, err
					}
				}
				d.Pointers = selected
			}
			handed = handed[:0]
			for _, ref := range selected {
				if ref.EventVersion == 0 && ref.DeliveryReport == "" {
					handed = append(handed, ref)
				}
			}
			page.Outcome, page.Cursor = msg.OutcomePage, d.Cursor
			if err = s.at("before_delivery_page"); err != nil {
				return nil, err
			}
			// The oldest earlier cursors retire first when the file would
			// exceed its bound; a retired cursor answers gap.
			out, err := json.Marshal(d)
			for err == nil && len(out) > messageDeliveryLimit && len(d.Earlier) > 0 {
				d.Earlier = d.Earlier[1:]
				out, err = json.Marshal(d)
			}
			return out, err
		}
		return nil, errors.New("messages: delivery refresh did not settle")
	})
	if err != nil || s.at("delivery_page_persisted") != nil {
		return msg.Page{Outcome: msg.OutcomeUnknown, Messages: []msg.Message{}, Headers: []msg.Header{}}, nil
	}
	if after != "" {
		s.gate.Advance()
	}
	return page, handed
}

// markHandedShown records that a body Wait answered its native owner with
// these incoming bodies (architect ruling 2026-11-09). The receiving Wait
// calls it only after its final recheck, so a page it drops wakes again.
func (s *messageStore) markHandedShown(ctx context.Context, account, receiver, contextName string, handed []messagePointer) {
	binding := messageWakeBinding(messageActor{Account: account, Program: receiver, Context: contextName})
	for _, ref := range handed {
		if err := s.markWakeShown(ctx, binding, ref.Key, ref.Token); err != nil {
			return // Unmarked, the message can wake its chat once more.
		}
	}
}
