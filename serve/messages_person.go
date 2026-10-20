package main

import (
	"context"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// ReadForPerson is the native Panel's administrative exception. Agent inbox
// ownership and pair grants authorize none of this operator-only disclosure.
func (r *messagesReceiver) ReadForPerson(id string) (msg.Page, error) {
	empty := func(outcome msg.Outcome) (msg.Page, error) { return messageEmptyPage(outcome), nil }
	if len(id) == 0 || len(id) > 128 {
		return empty(msg.OutcomeInvalid)
	}
	check, release, outcome := r.personOperator()
	if outcome != msg.OutcomePage {
		return empty(outcome)
	}
	defer release()
	if outcome := check(); outcome != msg.OutcomePage {
		return empty(outcome)
	}
	store := r.host.messages
	record, key, err := store.readByID(r.ctx, id)
	if err != nil {
		return empty(msg.OutcomeUnavailable)
	}
	page := messageEmptyPage(msg.OutcomePage)
	if record != nil && record.Forward == nil && record.ExpiresUnixMs > store.now().UnixMilli() {
		state, err := store.currentState(r.ctx, key, record)
		if err != nil {
			return empty(msg.OutcomeUnavailable)
		}
		message, err := messageRecordView(record, state)
		if err != nil {
			return empty(msg.OutcomeUnavailable)
		}
		page.Messages = append(page.Messages, message)
	}
	if outcome := check(); outcome != msg.OutcomePage {
		return empty(outcome)
	}
	if record != nil && record.ExpiresUnixMs <= store.now().UnixMilli() {
		return empty(msg.OutcomePage)
	}
	return page, nil
}

// StopForPerson is the native Panel operator's stop for one woken turn, under
// the same operator check as ReadForPerson (VISION 2026-10-31: Activity can
// stop a running woken turn as well as revoke the direction).
func (r *messagesReceiver) StopForPerson(id string) (msg.StopResult, error) {
	if len(id) == 0 || len(id) > 128 {
		return msg.StopResult{Outcome: msg.OutcomeInvalid}, nil
	}
	check, release, outcome := r.personOperator()
	if outcome != msg.OutcomePage {
		return msg.StopResult{Outcome: outcome}, nil
	}
	defer release()
	if outcome := check(); outcome != msg.OutcomePage {
		return msg.StopResult{Outcome: outcome}, nil
	}
	return r.host.stopMessageStartForPerson(r.ctx, id), nil
}

// personOperator retains the calling process and returns the check that it is
// still the native Panel operator in the runtime account. Remote receiving has
// no call and is forbidden.
func (r *messagesReceiver) personOperator() (func() msg.Outcome, func(), msg.Outcome) {
	if r.host == nil || r.host.directory == nil || r.host.messages == nil {
		return nil, nil, msg.OutcomeUnavailable
	}
	if r.call == nil || r.remote != nil {
		return nil, nil, msg.OutcomeForbidden
	}
	pin, err := r.call.RetainProcess()
	if err != nil {
		return nil, nil, msg.OutcomeForbidden
	}
	d := r.host.directory
	var admitted rights.Subject
	check := func() msg.Outcome {
		if r.ctx.Err() != nil {
			return msg.OutcomeUnavailable
		}
		_, subject, _, _, err := nativeInboxPeer(pin, d.now())
		if err != nil {
			return msg.OutcomeForbidden
		}
		if admitted.Account != "" && subject != admitted {
			return msg.OutcomeForbidden
		}
		admitted = subject
		outcome := d.agentOperator(r.ctx, subject)
		if outcome == wire.ApplicationOutcomeApplied {
			return msg.OutcomePage
		}
		if outcome == wire.ApplicationOutcomeUnavailable {
			return msg.OutcomeUnavailable
		}
		return msg.OutcomeForbidden
	}
	return check, func() { pin.Close() }, msg.OutcomePage
}

// stopMessageStartForPerson durably marks the start one question woke as
// stopped. A running turn's next authority check then refuses and the turn
// is cancelled; a pending start never dispatches. Activity records the stop.
// It withdraws no permit and changes no message.
func (h *applicationsHost) stopMessageStartForPerson(ctx context.Context, id string) msg.StopResult {
	store := h.messages
	record, key, err := store.readByID(ctx, id)
	if err != nil {
		return msg.StopResult{Outcome: msg.OutcomeUnavailable}
	}
	running := func(r *messageRecord) bool {
		return r != nil && r.Start != nil && (r.Start.State == "unknown" || r.Start.State == "ready")
	}
	if !running(record) {
		return msg.StopResult{Outcome: msg.OutcomeConflict, Reason: "not_running"}
	}
	if record.Start.Delivery == messageStartDesktopQueue {
		// The turn runs in the person's Codex app, not in OA's process.
		return msg.StopResult{Outcome: msg.OutcomeConflict, Reason: "runs_in_codex"}
	}
	stopped := false
	if err := store.updateMessageNotice(ctx, key, record.Token, func(current *messageRecord) {
		if running(current) && current.Start.Attempt == record.Start.Attempt {
			current.Start.State, current.Start.AuditDone = messageStartStopped, false
			stopped = true
		}
	}); err != nil {
		return msg.StopResult{Outcome: msg.OutcomeUnavailable}
	}
	if !stopped {
		return msg.StopResult{Outcome: msg.OutcomeConflict, Reason: "not_running"}
	}
	if current, err := store.readRecord(ctx, key); err == nil && current != nil {
		if err := h.flushMessageStartAudit(ctx, key, current); err != nil && h.report != nil {
			h.report(err)
		}
	}
	return msg.StopResult{Outcome: msg.OutcomePage, Reason: "stopped"}
}
