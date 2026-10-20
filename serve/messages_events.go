package main

import (
	"context"
	"encoding/json"
	"errors"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"os"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// Event references are recoverable hints. Only the caller's canonical check
// establishes a published state version or its individual acknowledgement.
type messageEventRef struct {
	Key, Token, Change string
	Version            uint64
	ExpiresUnixMs      int64  `json:"ExpiresAt"`
	AcceptedAt         int64  `json:",omitempty"`
	DeliveryReport     string `json:",omitempty"`
}
type messageEventDisposition uint8

const (
	messageEventPending messageEventDisposition = iota
	messageEventReady
	messageEventObserved
	messageEventExpired
)

type messageEventCheck func(context.Context, messageEventRef) (messageEventDisposition, error)

const messageEventPageEntries = 128
const messageEventWork = 256
const messageEventPageLimit = 1 << 20
const messageActorEventPages = 8
const messageActorEventLimit = messageActorEventPages * messageEventPageEntries

var errMessageEventsProgress = errors.New("messages: event reference cleanup pending")
var errMessageEventsFull = errors.New("messages: actor event log full")

type messageEventEntry struct {
	Ref               messageEventRef
	Pending, Canceled bool
}
type messageEventPage struct {
	Profile, Next string
	Entries       []messageEventEntry
}
type messageEventReservation struct {
	Ref  messageEventRef
	Page string
}
type messageEventHead struct {
	Profile, First, Last string
	Pending              *messageEventReservation
}

func (s *messageStore) eventPath(actor messageActor) string {
	return filepath.Join(s.dir, "events", messageDigest(actor.Account+"\x00"+actor.Program+"\x00"+actor.Context))
}
func messageEventValid(ref messageEventRef) bool {
	if ref.DeliveryReport != "" {
		h, err := decodeRetainedMessageHeader([]byte(ref.DeliveryReport))
		if err != nil || len(ref.DeliveryReport) > 4096 || h.Delivery == nil || *h.Delivery == msg.DeliveryQueued || h.Count == nil || *h.Count < 1 || ref.Change != "delivery-"+ref.Token {
			return false
		}
	}
	return agentHex(ref.Key, 32) && ref.Token != "" && len(ref.Token) <= 128 && ref.Change != "" && len(ref.Change) <= 128 && ref.Version > 0 && ref.ExpiresUnixMs > 0
}
func readMessageEventHead(raw []byte) (messageEventHead, error) {
	h := messageEventHead{Profile: "messages-events-head@1"}
	if raw == nil {
		return h, nil
	}
	if err := messageStoredJSON(raw, &h); err != nil || h.Profile != "messages-events-head@1" || (h.First == "") != (h.Last == "") {
		return h, errors.New("messages: invalid event head")
	}
	for _, p := range []string{h.First, h.Last} {
		if p != "" && !agentHex(p, 24) {
			return h, errors.New("messages: invalid event page identity")
		}
	}
	if h.Pending != nil && (!messageEventValid(h.Pending.Ref) || !agentHex(h.Pending.Page, 24)) {
		return h, errors.New("messages: invalid event reservation")
	}
	return h, nil
}
func readMessageEventPage(raw []byte) (messageEventPage, error) {
	p := messageEventPage{}
	if raw == nil {
		return p, errors.New("messages: referenced event page missing")
	}
	if err := messageStoredJSON(raw, &p); err != nil || p.Profile != "messages-events-page@1" || p.Entries == nil || len(p.Entries) > messageEventPageEntries || p.Next != "" && !agentHex(p.Next, 24) {
		return p, errors.New("messages: invalid event page")
	}
	for _, e := range p.Entries {
		if !messageEventValid(e.Ref) {
			return p, errors.New("messages: invalid event reference")
		}
	}
	return p, nil
}
func (s *messageStore) eventPage(ctx context.Context, dir, page string) (messageEventPage, error) {
	raw, err := cas.ReadLimitContext(ctx, filepath.Join(dir, page+".json"), messageEventPageLimit)
	if err != nil {
		return messageEventPage{}, err
	}
	return readMessageEventPage(raw)
}
func eventEntryIndex(p messageEventPage, ref messageEventRef) int {
	for i, e := range p.Entries {
		if e.Ref == ref {
			return i
		}
	}
	return -1
}
func (s *messageStore) changeEventPage(ctx context.Context, dir, page string, fn func(*messageEventPage) error) error {
	return cas.ChangeLimitContext(ctx, filepath.Join(dir, page+".json"), messageEventPageLimit, func(raw []byte) ([]byte, error) {
		p, err := readMessageEventPage(raw)
		if err != nil {
			return nil, err
		}
		if err = fn(&p); err != nil {
			return nil, err
		}
		return json.Marshal(p)
	})
}

// Every page mutation is serialized by this context head. Canonical callbacks
// must be short CAS operations and must not acquire this head recursively.
func (s *messageStore) recoverEvent(ctx context.Context, dir string, h *messageEventHead, check messageEventCheck) error {
	if h.Pending == nil {
		return nil
	}
	reservation := *h.Pending
	returnErr := s.changeEventPage(ctx, dir, reservation.Page, func(p *messageEventPage) error {
		i := eventEntryIndex(*p, reservation.Ref)
		state, err := s.eventDisposition(ctx, reservation.Ref, check)
		if err != nil {
			return err
		}
		// Page reconciliation may have committed before the head replacement
		// failed. Repeat it from canonical evidence, including a pruned pending
		// hint; never turn that interruption into a permanently stuck head.
		if i < 0 {
			if state != messageEventReady {
				return nil
			}
			if len(p.Entries) == messageEventPageEntries {
				return errors.New("messages: event recovery page full")
			}
			p.Entries = append(p.Entries, messageEventEntry{Ref: reservation.Ref})
			return nil
		}
		switch state {
		case messageEventPending:
			p.Entries[i].Canceled = true
		case messageEventReady:
			p.Entries[i].Pending = false
			p.Entries[i].Canceled = false
		case messageEventObserved, messageEventExpired:
			p.Entries[i].Canceled = true
		default:
			return errors.New("messages: invalid event disposition")
		}
		return nil
	})
	if returnErr == nil {
		h.Pending = nil
	}
	return returnErr
}
func (s *messageStore) eventDisposition(ctx context.Context, ref messageEventRef, check messageEventCheck) (messageEventDisposition, error) {
	if err := ctx.Err(); err != nil {
		return messageEventPending, err
	}
	if ref.ExpiresUnixMs <= s.now().UnixMilli() {
		return messageEventExpired, nil
	}
	if check == nil {
		return messageEventPending, errors.New("messages: event canonical check unavailable")
	}
	state, err := check(ctx, ref)
	if err == nil && state > messageEventExpired {
		err = errors.New("messages: invalid event disposition")
	}
	return state, err
}

func (s *messageStore) reserveMessageEvent(ctx context.Context, actor messageActor, ref messageEventRef, check messageEventCheck) error {
	if !messageEventValid(ref) || ref.ExpiresUnixMs <= s.now().UnixMilli() {
		return errors.New("messages: invalid event reservation")
	}
	dir := s.eventPath(actor)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	complete := false
	err := cas.ChangeLimitContext(ctx, filepath.Join(dir, "head.json"), 4096, func(raw []byte) ([]byte, error) {
		h, err := readMessageEventHead(raw)
		if err != nil {
			return nil, err
		}
		// A repeated reserve before publication is an idempotent lost-reply retry.
		if h.Pending != nil && h.Pending.Ref == ref {
			complete = true
			return raw, nil
		}
		if h.Pending != nil && h.Pending.Ref.Change == ref.Change {
			return nil, errors.New("messages: event change identity differs")
		}
		if err = s.recoverEvent(ctx, dir, &h, check); err != nil {
			return nil, err
		}
		pageName, pages, available := h.First, 0, ""
		for pageName != "" && pages < messageActorEventPages {
			pages++
			p, err := s.eventPage(ctx, dir, pageName)
			if err != nil {
				return nil, err
			}
			if i := eventEntryIndex(p, ref); i >= 0 {
				if p.Entries[i].Canceled {
					return nil, errors.New("messages: event operation canceled")
				}
				if p.Entries[i].Pending {
					h.Pending = &messageEventReservation{ref, pageName}
				}
				complete = true
				return json.Marshal(h)
			}
			if len(p.Entries) < messageEventPageEntries && available == "" {
				available = pageName
			}
			if p.Next == "" {
				h.Last = pageName
			}
			pageName = p.Next
		}
		if pageName != "" {
			return nil, errMessageEventsFull
		}
		if available != "" {
			if err := s.changeEventPage(ctx, dir, available, func(p *messageEventPage) error {
				p.Entries = append(p.Entries, messageEventEntry{Ref: ref, Pending: true})
				return nil
			}); err != nil {
				return nil, err
			}
			h.Pending = &messageEventReservation{ref, available}
			complete = true
			if err := s.at("event_pointer_appended"); err != nil {
				return nil, err
			}
			return json.Marshal(h)
		}
		if pages >= messageActorEventPages {
			return nil, errMessageEventsFull
		}
		page, err := applicationToken()
		if err != nil {
			return nil, err
		}
		p := messageEventPage{Profile: "messages-events-page@1", Entries: []messageEventEntry{{Ref: ref, Pending: true}}}
		if err = cas.ChangeLimitContext(ctx, filepath.Join(dir, page+".json"), messageEventPageLimit, func(raw []byte) ([]byte, error) {
			if raw != nil {
				return nil, errors.New("messages: event page collision")
			}
			return json.Marshal(p)
		}); err != nil {
			return nil, err
		}
		if h.Last != "" {
			if err = s.changeEventPage(ctx, dir, h.Last, func(p *messageEventPage) error { p.Next = page; return nil }); err != nil {
				return nil, err
			}
		} else {
			h.First = page
		}
		h.Last = page
		h.Pending = &messageEventReservation{ref, page}
		complete = true
		if err = s.at("event_pointer_appended"); err != nil {
			return nil, err
		}
		return json.Marshal(h)
	})
	if err == nil && !complete {
		return errMessageEventsProgress
	}
	return err
}

func (s *messageStore) publishMessageEvent(ctx context.Context, actor messageActor, ref messageEventRef, commit func() error) error {
	dir := s.eventPath(actor)
	return cas.ChangeLimitContext(ctx, filepath.Join(dir, "head.json"), 4096, func(raw []byte) ([]byte, error) {
		h, err := readMessageEventHead(raw)
		if err != nil {
			return nil, err
		}
		if h.Pending == nil || h.Pending.Ref != ref {
			return nil, errors.New("messages: event reservation reconciled before publication")
		}
		if ref.ExpiresUnixMs <= s.now().UnixMilli() {
			return nil, errors.New("messages: event expired before publication")
		}
		err = s.changeEventPage(ctx, dir, h.Pending.Page, func(p *messageEventPage) error {
			i := eventEntryIndex(*p, ref)
			if i < 0 || !p.Entries[i].Pending || p.Entries[i].Canceled {
				return errors.New("messages: event pointer not pending")
			}
			if commit == nil {
				return errors.New("messages: event commit unavailable")
			}
			if err := commit(); err != nil {
				return err
			}
			if err := s.at("event_state_committed"); err != nil {
				return err
			}
			p.Entries[i].Pending = false
			return nil
		})
		if err != nil {
			return nil, err
		}
		h.Pending = nil
		return json.Marshal(h)
	})
}

func (s *messageStore) messageEventCandidates(ctx context.Context, actor messageActor, check messageEventCheck) ([]messageEventRef, error) {
	return s.messageEventCandidatesLimit(ctx, actor, check, 50)
}

func (s *messageStore) messageEventCandidatesLimit(ctx context.Context, actor messageActor, check messageEventCheck, limit int) ([]messageEventRef, error) {
	// A live transition's reserved pointer is not abandoned recovery work.
	// Share the finite-operation owner through its canonical publication.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	dir := s.eventPath(actor)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	out := []messageEventRef{}
	bounded := false
	err := cas.ChangeLimitContext(ctx, filepath.Join(dir, "head.json"), 4096, func(raw []byte) ([]byte, error) {
		h, err := readMessageEventHead(raw)
		if err != nil {
			return nil, err
		}
		work := 0
		if h.Pending != nil {
			work++
		}
		if err = s.recoverEvent(ctx, dir, &h, check); err != nil {
			return nil, err
		}
		page := h.First
		pages := 0
		pageLimit, workLimit := 2, messageEventWork
		if limit > 50 {
			pageLimit = messageActorEventPages
			workLimit = messageActorEventLimit + 1
		}
		for page != "" && pages < pageLimit && work < workLimit && len(out) < limit {
			pages++
			next := ""
			empty := false
			err = s.changeEventPage(ctx, dir, page, func(p *messageEventPage) error {
				next = p.Next
				kept := make([]messageEventEntry, 0, len(p.Entries))
				for _, entry := range p.Entries {
					if work >= workLimit || len(out) >= limit {
						kept = append(kept, entry)
						bounded = true
						continue
					}
					work++
					if entry.Canceled {
						continue
					}
					state, err := messageEventReady, error(nil)
					if entry.Ref.DeliveryReport == "" || entry.Pending {
						state, err = s.eventDisposition(ctx, entry.Ref, check)
					}
					if err != nil {
						return err
					}
					switch state {
					case messageEventObserved, messageEventExpired:
						continue
					case messageEventPending:
						if !entry.Pending {
							return errors.New("messages: published event lost canonical state")
						}
						continue
					case messageEventReady:
						entry.Pending = false
						out = append(out, entry.Ref)
					}
					kept = append(kept, entry)
				}
				p.Entries = kept
				empty = len(kept) == 0
				return nil
			})
			if err != nil {
				return nil, err
			}
			if err = s.at("event_candidates_page_written"); err != nil {
				return nil, err
			}
			if s.inspect != nil {
				s.inspect("event_references", work)
			}
			if empty && page == h.First {
				h.First = next
				if next == "" {
					h.Last = ""
				}
			}
			page = next
		}
		if page != "" {
			bounded = true
		}
		return json.Marshal(h)
	})
	if err != nil {
		return nil, err
	}
	if bounded && len(out) == 0 {
		return nil, errMessageEventsProgress
	}
	return out, nil
}
