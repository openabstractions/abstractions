package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Reconciliation observes existing person decisions; it grants nothing and
// performs no body delivery, acknowledgment, presence or inbox mutation.
// Call only outside native admission, Book and CAS callbacks.
func (h *applicationsHost) reconcileMessageState(ctx context.Context, key string, record *messageRecord) (messageState, bool, error) {
	return h.reconcileMessageStateIn(ctx, key, record, nil)
}

// messageProgressPass is one bounded progress pass's observation of agent
// enrollments. The records of one expiry slice name the same few agents; each
// enrollment read takes the directory lock, the registration CAS and a full
// native image verification. One pass reads each agent once, so a slice of
// live records fits the worker's budget instead of exceeding it on every tick.
// A pass lasts at most that budget; the next pass observes again. An inbox
// read keeps one for the records it reconciles.
type messageProgressPass struct {
	mu     sync.Mutex
	agents map[string]messageProgressAgent
}

type messageProgressAgent struct {
	result applications.AgentEnrollmentResult
	image  applicationAgentImage
}

// messageProgressStarts keeps, per expiry slice, the index where the next pass
// over that slice starts. A slice is named by its first reference; the map is
// cleared when it holds 256 slices, which restarts those slices at their head.
type messageProgressStarts struct {
	mu     sync.Mutex
	starts map[string]int
}

func (s *messageProgressStarts) next(slice string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts[slice]
}

func (s *messageProgressStarts) store(slice string, next int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starts == nil || len(s.starts) >= 256 {
		s.starts = map[string]int{}
	}
	if next == 0 {
		delete(s.starts, slice)
		return
	}
	s.starts[slice] = next
}

func newMessageProgressPass() *messageProgressPass {
	return &messageProgressPass{agents: map[string]messageProgressAgent{}}
}

// agentEnrollment is currentAgentEnrollmentImage, read at most once per pass
// for each application. A nil pass reads every time.
func (h *applicationsHost) agentEnrollment(ctx context.Context, pass *messageProgressPass, name string, capture *applicationAgentImage) applications.AgentEnrollmentResult {
	if pass == nil {
		return h.directory.currentAgentEnrollmentImage(ctx, name, capture)
	}
	pass.mu.Lock()
	v, ok := pass.agents[name]
	pass.mu.Unlock()
	if !ok {
		v.result = h.directory.currentAgentEnrollmentImage(ctx, name, &v.image)
		// An observation cut short by the pass budget is not retained.
		if ctx.Err() == nil {
			pass.mu.Lock()
			pass.agents[name] = v
			pass.mu.Unlock()
		}
	}
	if capture != nil {
		*capture = v.image
	}
	return v.result
}

func (h *applicationsHost) reconcileMessageStateIn(ctx context.Context, key string, record *messageRecord, pass *messageProgressPass) (messageState, bool, error) {
	if record.Actor.Remote != nil {
		return h.reconcileRemoteMessage(ctx, key, record)
	}
	store := h.messages
	if store == nil || h.messageRights == nil {
		return messageState{}, false, errors.New("messages: state owner unavailable")
	}
	if record.Control {
		err := store.completeWithdraw(ctx, record, key)
		return messageState{}, false, err
	}
	state, err := store.currentState(ctx, key, record)
	if err != nil {
		return state, false, err
	}
	request, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil {
		return state, false, err
	}
	var sourceImage, targetImage applicationAgentImage
	source := h.agentEnrollment(ctx, pass, record.Actor.Application, &sourceImage)
	target := h.agentEnrollment(ctx, pass, strings.TrimPrefix(request.To, "app:"), &targetImage)
	if source.Outcome == applications.OutcomeUnavailable || target.Outcome == applications.OutcomeUnavailable {
		return state, false, errors.Join(errors.New("messages: current agent metadata unavailable"), ctx.Err())
	}
	sourceCurrent := source.Enrollment != nil && source.Enrollment.State == applications.AgentEnrollmentStateEnrolled && samePrograms(sourceImage.Program, record.Actor.Program)
	targetCurrent := target.Enrollment != nil && target.Enrollment.State == applications.AgentEnrollmentStateEnrolled && samePrograms(targetImage.Program, record.Receiver)
	current := sourceCurrent && targetCurrent
	subject := rights.Subject{Account: record.Actor.Account, Program: record.Actor.ObservedProgram}
	if subject.Program == "" {
		subject.Program = record.Actor.Program
	}
	decision := h.messageRights.decide(ctx, subject, ActionApplicationMessage, request.To)
	if decision.Outcome == rights.DecisionOutcomeUnavailable {
		return state, false, errors.Join(errors.New("messages: current permission unavailable"), ctx.Err())
	}
	permitted := current && decision.Outcome == rights.DecisionOutcomePermitted
	invalidReply := false
	if request.Kind == msg.KindReply && state.Held {
		question, qkey, e := store.readByID(ctx, request.ReplyTo)
		if e != nil {
			return state, false, e
		}
		if question == nil {
			invalidReply = true
		} else {
			qstate, e := store.currentState(ctx, qkey, question)
			if e != nil {
				return state, false, e
			}
			qrequest, e := decodeRetainedMessageRequest(question.Spec)
			if e != nil {
				return state, false, e
			}
			intent, e := store.questionTerminalIntent(ctx, qstate)
			if e != nil {
				return state, false, e
			}
			authorized := qstate.AcceptedTerminal == "complete" && qstate.ReplyKey == key && qstate.ReplyToken == record.Token
			invalidReply = !authorized && (intent == "cancel" || (messageTerminal(qstate.TaskState) && qstate.TaskState != msg.TaskStateCompleted) || qrequest.AnswerByUnixMs <= store.now().UnixMilli())
		}
	}
	operation := ""
	if record.Refused || invalidReply || !current || decision.Outcome == rights.DecisionOutcomeDenied || (!state.Held && decision.Outcome == rights.DecisionOutcomeNotGranted) {
		operation = "refuse"
		if invalidReply {
			permitted = false
		}
	} else if state.Held && permitted {
		operation = "release"
	}
	if operation != "" {
		if operation == "refuse" && !record.Alias {
			reason := msg.UndeliveredReasonRefused
			if !targetCurrent {
				reason = msg.UndeliveredReasonReceiverRemoved
			}
			if !sourceCurrent {
				if err = store.markSenderRemoved(ctx, key, record.Token); err != nil {
					return state, false, err
				}
			}
			if err = store.finishDelivery(ctx, key, record.Token, msg.DeliveryUndelivered, reason); err != nil {
				return state, false, err
			}
		}
		if err = store.transitionState(ctx, key, record, operation); err != nil {
			if errors.Is(err, errMessageEventsFull) {
				state.Refused = operation == "refuse"
				return state, false, nil
			}
			return state, false, err
		}
		state, err = store.currentState(ctx, key, record)
		if err != nil {
			return state, false, err
		}
	}
	if request.Kind == msg.KindQuestion && !messageTerminal(state.TaskState) {
		intent, e := store.questionTerminalIntent(ctx, state)
		if e != nil {
			return state, false, e
		}
		if intent != "" {
			if err = store.transitionState(ctx, key, record, intent); err != nil {
				return state, false, err
			}
			state, err = store.currentState(ctx, key, record)
			if err != nil {
				return state, false, err
			}
		}
	}
	if request.Kind == msg.KindQuestion && !messageTerminal(state.TaskState) && request.AnswerByUnixMs <= store.now().UnixMilli() {
		if err = store.transitionState(ctx, key, record, "fail"); err != nil {
			return state, false, err
		}
		state, err = store.currentState(ctx, key, record)
	}
	if err == nil && request.Kind == msg.KindReply && permitted && !state.Held && !state.Refused {
		err = store.completeReply(ctx, record, key)
	}
	if err == nil && state.Refused && !record.Alias && !record.Control {
		err = store.finishDelivery(ctx, key, record.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonRefused)
	}
	return state, permitted, err
}

// Reuse the existing expiry owner's rotating bounded slice. Native/policy
// checks happen after its CAS transaction has released every storage lock.
// Fresh-key aliases require cleanup but never duplicate message state work.
//
// Each pass starts where the previous pass stopped within the slice, so a pass
// cut short by its budget, or by one record's own failure, leaves the later
// records to the next pass instead of starting again at the same head record.
// A short single page returns the same slice on every pass.
func (h *applicationsHost) progressMessages(ctx context.Context) (result error) {
	pass := newMessageProgressPass()
	refs, err := h.messages.expiryProgress(ctx)
	if err != nil {
		return err
	}
	start, slice := 0, ""
	if len(refs) > 0 {
		slice = refs[0].Key
		start = h.messageProgressStarts.next(slice) % len(refs)
		refs = append(append(make([]messagePointer, 0, len(refs)), refs[start:]...), refs[:start]...)
	}
	reached := 0
	defer func() {
		if len(refs) == 0 {
			return
		}
		next := start + reached
		if result != nil && ctx.Err() == nil && reached < len(refs) {
			// A record that failed on its own is passed over next time.
			next++
		}
		h.messageProgressStarts.store(slice, next%len(refs))
	}()
	type forwardObservation struct {
		key    string
		record *messageRecord
	}
	var forwarding []forwardObservation
	for i, ref := range refs {
		reached = i
		if h.messages.inspect != nil {
			h.messages.inspect("progress_record", (start+i)%len(refs))
		}
		// A spent budget ends the pass between records; the next pass starts here.
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := h.messages.recordForPointer(ctx, ref)
		if err != nil {
			return err
		}
		if record != nil && record.Token == ref.Token {
			if record.Forward != nil && !record.Alias {
				if err := h.messages.ensureForwardPublication(ctx, ref.Key, record); err != nil {
					if h.report != nil {
						h.report(errors.New("messages: forwarding custody publication pending"))
					}
					continue
				}
				if record.Delivery == nil && record.Forward.NextObserveAt <= h.messages.now().UnixMilli() {
					forwarding = append(forwarding, forwardObservation{ref.Key, record})
				}
			}
			if record.Delivery != nil && !record.Delivery.Reported {
				if err := h.messages.finishDelivery(ctx, ref.Key, ref.Token, record.Delivery.State, record.Delivery.Reason); err != nil {
					return err
				}
			}
			if err = h.flushMessageStartAudit(ctx, ref.Key, record); err != nil {
				return err
			}
			if err = h.flushMessageAudit(ctx, ref.Key, ref.Token); err != nil {
				if h.report != nil {
					h.report(errors.New("messages: audit append pending"))
				}
			}
		}
		if record == nil || record.Token != ref.Token || record.Forward != nil || record.Lost || record.Alias || record.ExpiresUnixMs <= h.messages.now().UnixMilli() {
			continue
		}
		if _, _, err = h.reconcileMessageStateIn(ctx, ref.Key, record, pass); err != nil {
			if errors.Is(err, errMessageStateInvalid) {
				if err := h.messages.finishDelivery(ctx, ref.Key, ref.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonLost); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if err = h.flushMessageAudit(ctx, ref.Key, ref.Token); err != nil {
			return err
		}
		request, err := decodeRetainedMessageRequest(record.Spec)
		if err != nil {
			return err
		}
		// Canonical refusal notices follow the start decision, owned by its
		// deferred fallback. Other receiving contexts retain their notice order.
		if !messageCanonicalChatContext(request.Context) || record.Start != nil && record.Start.State != "" {
			if err = h.progressMessageNotifications(ctx, ref.Key, record); err != nil {
				return err
			}
		}
		if err = h.progressMessageStart(ctx, ref.Key, record); err != nil {
			return err
		}
	}
	reached = len(refs)
	// Reuse the rotating expiry roster, with at most four concurrent one-second
	// metadata observations. The per-record timestamp supplies fair retry without
	// a new queue, counter, runner or publication of a synthetic message.
	sort.Slice(forwarding, func(i, j int) bool {
		if forwarding[i].record.Forward.NextObserveAt != forwarding[j].record.Forward.NextObserveAt {
			return forwarding[i].record.Forward.NextObserveAt < forwarding[j].record.Forward.NextObserveAt
		}
		return forwarding[i].key < forwarding[j].key
	})
	if len(forwarding) > 4 {
		forwarding = forwarding[:4]
	}
	var observers sync.WaitGroup
	errorsByRecord := make([]error, len(forwarding))
	for i, item := range forwarding {
		claimed, err := h.messages.claimForwardObservation(ctx, item.key, item.record.Token)
		if err != nil {
			errorsByRecord[i] = err
			continue
		}
		if !claimed {
			continue
		}
		observers.Add(1)
		go func() {
			defer observers.Done()
			errorsByRecord[i] = h.observeForwardDelivery(ctx, item.key, item.record)
		}()
	}
	observers.Wait()
	for _, err := range errorsByRecord {
		if err != nil {
			return err
		}
	}
	return nil
}
