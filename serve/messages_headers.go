package main

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	watch "github.com/openabstractions/abstraction-watch/go"
)

type messageObserver struct {
	r   *messagesReceiver
	pin interface {
		nativeInboxPeerCapture
		Close() error
	}
	actor     messageActor
	subject   rights.Subject
	admission applicationAgentAdmission
	// discovery is this observation's one read of each agent's discovered
	// conversations: a listing's two snapshots see the same ones, so a
	// refresh that lands between them never turns the listing into a gap.
	// noDiscovery lists open and retained conversations only.
	discovery   map[string][]applicationAttachSession
	warmed      bool
	noDiscovery bool
	// Receipt titles reuse current snapshots only; they never warm discovery.
	titleApplication string
	// discoverable names the agents this sender may message; discovery
	// runs for no other agent.
	discoverable map[string]bool
}

// discovered returns the agent's discovered conversations once per
// observation. After the listing's one shared wait it reads the current
// snapshot without waiting again.
func (o *messageObserver) discovered(application string) []applicationAttachSession {
	if o.noDiscovery || application == "" || o.discoverable != nil && !o.discoverable[application] {
		return nil
	}
	if sessions, ok := o.discovery[application]; ok {
		return sessions
	}
	if o.discovery == nil {
		o.discovery = map[string][]applicationAttachSession{}
	}
	d := o.r.host.directory
	var sessions []applicationAttachSession
	if o.titleApplication != "" {
		if d.messageAttachCurrent != nil {
			sessions = d.messageAttachValid(d.messageAttachCurrent(application))
		}
	} else if o.warmed && d.messageAttachCurrent != nil {
		sessions = d.messageAttachValid(d.messageAttachCurrent(application))
	} else {
		sessions = d.messageAttachSessionsFor(o.r.ctx, application)
	}
	o.discovery[application] = sessions
	return sessions
}

// An unavailable observation supplies no authority conclusion. A forbidden
// observation records affirmative loss of the captured native admission.
type messageObservationError struct {
	outcome msg.Outcome
	cause   error
}

func (e *messageObservationError) Error() string { return e.cause.Error() }
func (e *messageObservationError) Unwrap() error { return e.cause }

func messageObservationFailure(err error) msg.Outcome {
	var observation *messageObservationError
	if errors.As(err, &observation) {
		return observation.outcome
	}
	return msg.OutcomeUnavailable
}

func messageNativeInspectionUncertain(err error) bool {
	var inspection *nativeInboxInspectionError
	return errors.As(err, &inspection) || errors.Is(err, errNativeInboxEvidenceExpired)
}

func (r *messagesReceiver) messageObserver(contextName string) (*messageObserver, msg.Outcome) {
	if r.call == nil || r.host == nil || r.host.directory == nil {
		return nil, msg.OutcomeForbidden
	}
	pin, err := r.call.RetainProcess()
	if err != nil {
		if messageNativeInspectionUncertain(nativeInboxInspectionFailure(err)) {
			return nil, msg.OutcomeUnavailable
		}
		return nil, msg.OutcomeForbidden
	}
	peer, subject, _, _, err := nativeInboxPeer(pin, r.host.directory.now())
	if err != nil || subject.Account != r.host.directory.owner {
		pin.Close()
		if messageNativeInspectionUncertain(err) {
			return nil, msg.OutcomeUnavailable
		}
		return nil, msg.OutcomeForbidden
	}
	admission, allowed, err := r.host.directory.captureApplicationAgent(r.ctx, peer, "")
	if err != nil {
		pin.Close()
		return nil, msg.OutcomeUnavailable
	}
	if !allowed {
		pin.Close()
		return nil, msg.OutcomeForbidden
	}
	return &messageObserver{r: r, pin: pin, subject: subject, admission: admission, actor: messageActor{Account: subject.Account, Program: admission.Image.Program, ObservedProgram: subject.Program, Application: admission.Enrollment.Application, Context: contextName}}, msg.OutcomePage
}

func (o *messageObserver) check() error {
	unavailable := func(err error) error { return &messageObservationError{outcome: msg.OutcomeUnavailable, cause: err} }
	forbidden := func(err error) error { return &messageObservationError{outcome: msg.OutcomeForbidden, cause: err} }
	d := o.r.host.directory
	if !d.lockRead(o.r.ctx) {
		return unavailable(o.r.ctx.Err())
	}
	closed := d.nativeInboxesClosed
	d.mu.RUnlock()
	if closed {
		return unavailable(errors.New("messages: native receiver closed"))
	}
	peer, subject, _, _, err := inspectNativeInboxPeer(o.pin, o.r.host.directory.now())
	if err != nil || subject.Account != o.actor.Account {
		if messageNativeInspectionUncertain(err) {
			return unavailable(err)
		}
		return forbidden(errors.New("messages: observer native occurrence ended"))
	}
	info, allowed, err := o.r.host.directory.captureApplicationAgent(o.r.ctx, peer, "")
	if err != nil {
		return unavailable(err)
	}
	if !allowed || !sameNativeInboxAdmission(o.admission, info) || info.Image.Program != o.actor.Program {
		return forbidden(errors.New("messages: observer enrollment changed"))
	}
	if err := o.r.ctx.Err(); err != nil {
		return unavailable(err)
	}
	return nil
}

func messageEmptyPage(outcome msg.Outcome) msg.Page {
	return msg.Page{Outcome: outcome, Messages: []msg.Message{}, Headers: []msg.Header{}}
}

// Native incoming queue eligibility has one owner. Callers establish current
// permission, exact receiving context, non-held state and unacknowledged record.
func messageWakeHints(h msg.Header, observed int64) (pending, reply int64) {
	if h.Refused || h.ExpiresUnixMs <= observed {
		return
	}
	switch h.Kind {
	case msg.KindQuestion:
		if h.TaskState != nil && *h.TaskState == msg.TaskStateSubmitted && h.AnswerByUnixMs > observed {
			pending = h.ExpiresUnixMs
			if h.AnswerByUnixMs < pending {
				pending = h.AnswerByUnixMs
			}
		}
	case msg.KindNote:
		// Architect ruling B (2026-11-08): an unacknowledged note wakes an idle
		// chat under the question eligibility. Its text stays a peer request.
		pending = h.ExpiresUnixMs
	case msg.KindReply:
		reply = h.ExpiresUnixMs
	}
	return
}

func (r *messagesReceiver) headersWait(contextName, revision string, waitMs int64) msg.Page {
	if !applicationContextName.MatchString(contextName) || len(revision) > 128 || waitMs < 0 || waitMs > 30000 {
		return messageEmptyPage(msg.OutcomeInvalid)
	}
	if r.host == nil || r.host.messages == nil || r.host.messageRights == nil {
		return messageEmptyPage(msg.OutcomeUnavailable)
	}
	observer, outcome := r.messageObserver(contextName)
	if observer == nil {
		return messageEmptyPage(outcome)
	}
	defer observer.pin.Close()
	store := r.host.messages
	page := messageEmptyPage(msg.OutcomeUnavailable)
	var selected string
	var stopWatching func()
	defer func() {
		if stopWatching != nil {
			stopWatching()
		}
	}()
	var watchErr error
	waitOutcome, err := store.gate.Wait(r.ctx, time.Duration(waitMs)*time.Millisecond, watch.Probe{
		Read: func() (bool, error) {
			next, e := observer.headerSnapshot()
			if e != nil {
				return false, e
			}
			page = next
			selected = next.Revision
			return selected != revision, nil
		},
		Recheck: func() error {
			if watchErr != nil {
				return watchErr
			}
			if selected == revision {
				// Nothing to answer: the caller's authority is the only fact
				// to confirm, and the next read sees any change.
				return observer.check()
			}
			current, e := observer.headerSnapshot()
			if e != nil {
				return e
			}
			if current.Revision != selected {
				return errors.New("messages: headers changed before receiving")
			}
			return nil
		},
		Deadline: func() time.Time {
			// Gate calls Deadline after this RPC holds its positive wait slot.
			// Immediate snapshots, zero waits and rejected slots publish no watch.
			if stopWatching == nil && watchErr == nil {
				stopWatching, watchErr = r.watchMessages(observer.actor)
			}
			return time.Time{} // Gate's bounded wait ends with a fresh read.
		},
	})
	if err != nil || watchErr != nil || r.ctx.Err() != nil || waitOutcome == watch.Busy || waitOutcome == watch.Failed {
		if err != nil && r.ctx.Err() == nil && watchErr == nil {
			return messageEmptyPage(messageObservationFailure(err))
		}
		return messageEmptyPage(msg.OutcomeUnavailable)
	}
	if err := observer.check(); err != nil {
		return messageEmptyPage(messageObservationFailure(err))
	}
	return page
}

func (o *messageObserver) headerSnapshot() (msg.Page, error) {
	if err := o.check(); err != nil {
		return msg.Page{}, err
	}
	r, store := o.r, o.r.host.messages
	refs, err := store.headerQueueCandidates(r.ctx, o.actor.Account, o.actor.Program, o.actor.Context)
	if err != nil {
		return msg.Page{}, err
	}
	headers := map[string]msg.Header{}
	incoming := map[string]bool{}
	var pendingUntil, replyUntil, awaitedReplies int64
	var unread, pendingQuestions, oldestUnreadAt, newestUnreadAt int64
	var wake, oldestWake string
	observed := store.now().UnixMilli()
	for _, ref := range refs {
		record, err := store.readRecord(r.ctx, ref.Key)
		if err != nil {
			return msg.Page{}, err
		}
		if record == nil || record.Token != ref.Token {
			return msg.Page{}, errors.New("messages: headers lost canonical acceptance")
		}
		if record.ExpiresUnixMs <= store.now().UnixMilli() || record.Acked || record.Alias {
			continue
		}
		state, err := store.currentState(r.ctx, ref.Key, record)
		if err != nil {
			return msg.Page{}, err
		}
		visible, err := o.headerPermitted(ref.Key, record, false)
		if err != nil {
			return msg.Page{}, err
		}
		if !visible {
			continue
		}
		state, err = store.currentState(r.ctx, ref.Key, record)
		if err != nil {
			return msg.Page{}, err
		}
		view, err := messageRecordView(record, state)
		if err != nil {
			return msg.Page{}, err
		}
		if state.Refused {
			continue
		}
		unread++
		if oldestUnreadAt == 0 || record.AcceptedAt < oldestUnreadAt {
			oldestUnreadAt = record.AcceptedAt
		}
		// A closed chat compares the newest incoming acceptance with its last
		// activity: one fresh question wakes it beside an older unread note.
		newestUnreadAt = max(newestUnreadAt, record.AcceptedAt)
		headers[view.Header.MessageID] = view.Header
		incoming[view.Header.MessageID] = true
		// Only authorized, unacknowledged incoming queue records contribute.
		// Compute before the bounded header projection; state events stay quiet.
		h := view.Header
		if state.Held {
			continue
		}
		pending, reply := messageWakeHints(h, observed)
		if pending > 0 && h.Kind == msg.KindQuestion {
			pendingQuestions++
		}
		// A message at the hop limit stays unread and keeps its expiry hint;
		// it projects no wake, so no adapter reserves a pointer for it. A
		// message a body page already handed to this inbox wakes nothing more.
		shown := store.wakeShown(r.ctx, messageWakeBinding(o.actor), ref.Key, record.Token)
		if (pending > 0 || reply > 0) && messageChainWakes(record) && !shown {
			key, err := store.wakeForRecord(r.ctx, o.actor, ref.Key, record)
			if err != nil {
				return msg.Page{}, err
			}
			if key != "" && reply > 0 && store.awaitedReply(r.ctx, o.actor, ref.Key, record, h.ReplyTo, observed) {
				awaitedReplies++
			}
			if key > wake {
				wake = key
			}
			if key != "" && (oldestWake == "" || key < oldestWake) {
				oldestWake = key
			}
		}
		if pending > pendingUntil {
			pendingUntil = pending
		}
		if reply > replyUntil {
			replyUntil = reply
		}
	}
	if err := r.host.progressMessages(r.ctx); err != nil {
		return msg.Page{}, err
	}
	events, err := store.messageEventCandidatesLimit(r.ctx, o.actor, store.stateEventCheckFor(o.actor), messageActorEventLimit)
	if err != nil {
		return msg.Page{}, err
	}
	for _, ref := range events {
		if ref.DeliveryReport != "" {
			header, err := decodeRetainedMessageHeader([]byte(ref.DeliveryReport))
			if err != nil {
				return msg.Page{}, err
			}
			headers["delivery-"+ref.Token] = *header
			unread++
			if oldestUnreadAt == 0 || ref.AcceptedAt > 0 && ref.AcceptedAt < oldestUnreadAt {
				oldestUnreadAt = ref.AcceptedAt
			}
			continue
		}
		record, err := store.readRecord(r.ctx, ref.Key)
		if err != nil {
			return msg.Page{}, err
		}
		if record == nil || record.Token != ref.Token {
			return msg.Page{}, errors.New("messages: headers lost canonical event")
		}
		if record.ExpiresUnixMs <= store.now().UnixMilli() {
			continue
		}
		visible, err := o.headerPermitted(ref.Key, record, true)
		if err != nil {
			return msg.Page{}, err
		}
		if !visible {
			continue
		}
		state, err := store.currentState(r.ctx, ref.Key, record)
		if err != nil {
			return msg.Page{}, err
		}
		view, err := messageRecordView(record, state)
		if err != nil {
			return msg.Page{}, err
		}
		headers[view.Header.MessageID] = view.Header
		if record.Receiver == o.actor.Program {
			incoming[view.Header.MessageID] = true
		}
	}
	rows := make([]msg.Header, 0, len(headers))
	for _, header := range headers {
		rows = append(rows, header)
	}
	sort.Slice(rows, func(i, j int) bool {
		priority := func(h msg.Header) int {
			if incoming[h.MessageID] {
				if h.Kind == msg.KindQuestion && h.TaskState != nil && *h.TaskState == msg.TaskStateSubmitted {
					return 0
				}
				return 1
			}
			return 2
		}
		if a, b := priority(rows[i]), priority(rows[j]); a != b {
			return a < b
		}
		if rows[i].MessageID == rows[j].MessageID {
			return rows[i].Delivery == nil && rows[j].Delivery != nil
		}
		return rows[i].MessageID < rows[j].MessageID
	})
	reports, ordinary := []msg.Header{}, []msg.Header{}
	for _, h := range rows {
		if h.Delivery != nil || !incoming[h.MessageID] {
			reports = append(reports, h)
		} else {
			ordinary = append(ordinary, h)
		}
	}
	rows = make([]msg.Header, 0, len(headers))
	for i := 0; i < len(ordinary) || i < len(reports); i++ {
		if i < len(ordinary) {
			rows = append(rows, ordinary[i])
		}
		if i < len(reports) {
			rows = append(rows, reports[i])
		}
	}
	raw, err := json.Marshal(struct {
		Epoch, Account, Program, Context, Image string
		Headers                                 []msg.Header
		PendingUntilUnixMs                      int64 `json:"PendingUntil"`
		ReplyUntilUnixMs                        int64 `json:"ReplyUntil"`
		Wake, OldestWake                        string
		Unread                                  int64
		PendingQuestions, AwaitedReplies        int64
		OldestUnreadUnixMs                      int64 `json:"OldestUnreadAt"`
	}{r.host.directory.epoch, o.actor.Account, o.actor.Program, o.actor.Context, o.admission.Image.Key, rows, pendingUntil, replyUntil, wake, oldestWake, unread, pendingQuestions, awaitedReplies, oldestUnreadAt})
	if err != nil {
		return msg.Page{}, err
	}
	if len(rows) > 50 {
		rows = rows[:50]
	}
	page := messageEmptyPage(msg.OutcomePage)
	page.Revision = messageDigest(string(raw))
	page.Headers = rows
	page.PendingUntilUnixMs, page.ReplyUntilUnixMs = pendingUntil, replyUntil
	page.Wake, page.OldestWake = wake, oldestWake
	page.Unread, page.PendingQuestions, page.AwaitedReplies = new(unread), new(pendingQuestions), new(awaitedReplies)
	if unread > 0 {
		page.OldestUnreadUnixMs = new(oldestUnreadAt)
	}
	if newestUnreadAt > 0 {
		page.NewestUnreadUnixMs = new(newestUnreadAt)
	}
	return page, nil
}

func (o *messageObserver) headerPermitted(key string, record *messageRecord, outgoing bool) (bool, error) {
	r := o.r
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil {
		return false, err
	}
	if record.Actor.Account != o.actor.Account {
		return false, errors.New("messages: headers account mismatch")
	}
	if outgoing {
		receiver, e := messageEventReceiverFor(record, o.actor)
		if e != nil {
			return false, e
		}
		// The source already owns this request. Its canonical OA refusal or
		// completion remains observable after pair consent is revoked.
		if !receiver || record.Acked {
			return true, nil
		}
	} else {
		if !samePrograms(record.Receiver, o.actor.Program) || request.Context != o.actor.Context {
			return false, errors.New("messages: incoming header scope mismatch")
		}
	}
	state, permitted, err := r.host.reconcileMessageState(r.ctx, key, record)
	return permitted && !state.Held && !state.Refused, err
}
