package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	awire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// This immutable request belongs to the original canonical acceptance. Book
// owns replay, notification retention and the OA program's notification budget.
type messageNotice struct {
	Request                         awire.NotificationRequest
	ReceiverProgram, NotificationID string
	Settled                         bool
	Closed                          bool
}

type receiverNoticeReservation struct {
	Profile, Account, Key, Token string
	At, Until                    int64
	Notice                       messageNotice
}

// Retain the immutable Book request before posting. The rolling reservation
// belongs to the canonical local receiver, independent of sender/claim labels.
func (s *messageStore) reserveReceiverNotice(ctx context.Context, account, program, key, token string, notice messageNotice) (messageNotice, bool, error) {
	selected := notice
	owned := false
	now := s.now().UnixMilli()
	err := cas.ChangeLimitContext(ctx, filepath.Join(s.dir, "notices", messageDigest(account+"\x00"+program)+".json"), 8192, func(raw []byte) ([]byte, error) {
		if raw != nil {
			var old receiverNoticeReservation
			if err := messageStoredJSON(raw, &old); err != nil || old.Profile != "messages-receiver-notice@1" || old.Account != account || old.Notice.ReceiverProgram != program || !agentHex(old.Key, 32) || !agentHex(old.Token, 24) || old.At <= 0 || old.Until-old.At != int64(time.Hour/time.Millisecond) || !asks.ValidNotificationRequest(old.Notice.Request) {
				return nil, errors.New("messages: invalid receiver notice reservation")
			}
			if old.Key == key && old.Token == token {
				selected, owned = old.Notice, true
				return raw, nil
			}
			if now < old.Until {
				return raw, nil
			}
		}
		owned = true
		return json.Marshal(receiverNoticeReservation{Profile: "messages-receiver-notice@1", Account: account, Key: key, Token: token, At: now, Until: now + int64(time.Hour/time.Millisecond), Notice: notice})
	})
	return selected, owned, err
}

func (h *applicationsHost) postMessageNotice(ctx context.Context, q awire.NotificationRequest, guard func() error) (awire.NotificationObservation, error) {
	a := h.messageNotices
	if a == nil || a.book == nil || a.rights == nil {
		return awire.NotificationObservation{}, errors.New("messages: notifications unavailable")
	}
	program := a.rights.self().Program
	result, _, err := a.book.PostNotificationFrom(a.scope, program, q, listen.Seen{Why: "runtime message notification"}, asks.Publisher{}, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if guard != nil {
			if err := guard(); err != nil {
				return err
			}
		}
		decision := a.rights.decide(ctx, rights.Subject{Account: a.rights.owner, Program: program}, notificationPostAction, notificationPostResource)
		if decision.Outcome == rights.DecisionOutcomePermitted {
			return nil
		}
		return asks.ErrNotificationSuppressed
	})
	if errors.Is(err, asks.ErrNotificationSuppressed) {
		return awire.NotificationObservation{Outcome: awire.NotificationOutcomeSuppressed}, nil
	}
	return result, err
}

func messageNoticeSettled(outcome awire.NotificationOutcome) bool {
	return outcome == awire.NotificationOutcomePosted || outcome == awire.NotificationOutcomeScheduled || outcome == awire.NotificationOutcomeConflict || outcome == awire.NotificationOutcomeSuppressed || outcome == awire.NotificationOutcomeExpired
}

func (h *applicationsHost) receiverRunning(ctx context.Context, program string) (bool, error) {
	d := h.directory
	if d == nil || !d.lock(ctx) {
		return false, errors.New("messages: presence unavailable")
	}
	defer d.unlock()
	// An unexpired native presence conservatively suppresses the notice. The
	// established presence owner expires/revokes it; this check creates none.
	for _, lease := range d.instances {
		if d.now().Before(lease.deadline) && samePrograms(lease.owner.Program, program) {
			return true, nil
		}
	}
	return false, nil
}

// receiverNoticeRunning is receiverRunning for the waiting notice. A canonical
// chat is reached only while its addressed native inbox is open; another live
// chat does not suppress its notice. For other contexts a Codex integration's
// presence counts only while it watches the addressed context:
// its helper wakes a chat only after Codex names the chat's thread, so an open
// inbox without that watch leaves the question unseen. For those other contexts
// Claude Code's hook wake is not observable here, so its presence suppresses the
// notice, as Prepare still holds back receiver_not_wakeable.
func (h *applicationsHost) receiverNoticeRunning(ctx context.Context, program, contextName string) (bool, error) {
	d := h.directory
	if d == nil || !d.lock(ctx) {
		return false, errors.New("messages: presence unavailable")
	}
	present, codexOnly := false, true
	if messageCanonicalChatContext(contextName) {
		for _, lease := range d.instances {
			if d.now().Before(lease.deadline) && samePrograms(lease.owner.Program, program) && d.messageContextOpenLocked(lease.application, contextName) {
				d.unlock()
				return true, nil
			}
		}
		d.unlock()
		return false, nil
	}
	for _, lease := range d.instances {
		if d.now().Before(lease.deadline) && samePrograms(lease.owner.Program, program) {
			present = true
			codexOnly = codexOnly && messageCodexReceiver(lease.application)
		}
	}
	d.unlock()
	if !present || !codexOnly {
		return present, nil
	}
	return h.messageWatchHeld(program, contextName), nil
}

// messageApplicationTitle is the person-facing name of a registered
// application: its registered title, or its name when it has none.
func (d *applicationDirectory) messageApplicationTitle(ctx context.Context, name string) string {
	if d != nil && d.lock(ctx) {
		descriptor, ok := d.descriptors[name]
		d.unlock()
		if title := strings.TrimSpace(descriptor.Title); ok && title != "" {
			return truncateTitle(title)
		}
	}
	return name
}

// messageNoticeSender names the sender in a notice: a local sender by its
// registered title, a paired one by its existing unverified label.
func (h *applicationsHost) messageNoticeSender(ctx context.Context, actor messageActor) string {
	if actor.Remote != nil {
		return messageSenderLabel(actor)
	}
	return h.directory.messageApplicationTitle(ctx, actor.Application)
}

// messageNoticeName is one sanitized name of at most 80 characters, so two of
// them fit the notice body's 240.
func messageNoticeName(name string) string {
	clean := []rune(nativeMessageHeader(name))
	if len(clean) > 80 {
		return string(clean[:79]) + "…"
	}
	return string(clean)
}

func (s *messageStore) updateMessageNotice(ctx context.Context, key, token string, update func(*messageRecord)) error {
	written := false
	err := cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
		written = false
		var current messageRecord
		if raw == nil {
			return nil, errors.New("messages: notice acceptance missing")
		}
		if err := messageStoredJSON(raw, &current); err != nil {
			return nil, err
		}
		if current.Profile != "messages-record@1" || current.Token != token || current.ExpiresUnixMs <= s.now().UnixMilli() {
			return raw, nil
		}
		update(&current)
		written = true
		return json.Marshal(current)
	})
	if err == nil && written {
		s.advance()
	}
	return err
}

func (h *applicationsHost) progressMessageNotifications(ctx context.Context, key string, r *messageRecord) error {
	if h.messageNotices == nil || r.Alias || r.Control || r.ExpiresUnixMs <= h.messages.now().UnixMilli() {
		return nil
	}
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil {
		return err
	}
	day := h.messages.now().UnixMilli()/int64(24*time.Hour/time.Millisecond) + 1
	if r.PairNoticeDay != day {
		posted, err := h.progressPairNotification(ctx, r.Actor, r.Receiver, *request)
		if err != nil {
			return err
		}
		if posted > 0 {
			if err = h.messages.updateMessageNotice(ctx, key, r.Token, func(record *messageRecord) { record.PairNoticeDay = posted }); err != nil {
				return err
			}
		}
	}
	if request.Kind != msg.KindQuestion {
		return nil
	}
	state, err := h.messages.currentState(ctx, key, r)
	if err != nil {
		return err
	}
	if state.Refused || messageTerminal(state.TaskState) || request.AnswerByUnixMs <= h.messages.now().UnixMilli() {
		return h.closeMessageNotice(ctx, key, r.Token)
	}
	running, err := h.receiverNoticeRunning(ctx, r.Receiver, request.Context)
	if err != nil || running {
		if err != nil {
			return err
		}
		return h.closeMessageNotice(ctx, key, r.Token)
	}
	current, err := h.messages.readRecord(ctx, key)
	if err != nil || current == nil || current.Token != r.Token || current.Profile != "messages-record@1" || current.ExpiresUnixMs <= h.messages.now().UnixMilli() {
		return err
	}
	if current.WaitingNotice == nil {
		now := h.messages.now().UnixMilli()
		until := min(request.AnswerByUnixMs, request.ExpiresUnixMs, now+int64(time.Hour/time.Millisecond))
		q := awire.NotificationRequest{RequestRef: awire.RequestRef{Key: "message-wait-" + r.Token, ReplayUntilMs: now + int64(time.Hour/time.Millisecond)}, Title: "Waiting for an answer", Body: "“" + messageNoticeName(h.messageNoticeSender(ctx, r.Actor)) + "” is waiting for an answer from “" + messageNoticeName(h.directory.messageApplicationTitle(ctx, strings.TrimPrefix(request.To, "app:"))) + "”.", Tag: "message-" + r.Token, Delivery: awire.NotificationDeliveryNow, ExpiresMs: until, Actions: []awire.NotificationAction{}}
		notice := &messageNotice{Request: q, ReceiverProgram: r.Receiver}
		if err = h.messages.at("before_notice_record"); err != nil {
			return err
		}
		if err = h.messages.updateMessageNotice(ctx, key, r.Token, func(record *messageRecord) {
			if record.WaitingNotice == nil {
				record.WaitingNotice = notice
			}
		}); err != nil {
			return err
		}
		current, err = h.messages.readRecord(ctx, key)
		if err != nil || current == nil || current.Profile != "messages-record@1" || current.Token != r.Token || current.ExpiresUnixMs <= h.messages.now().UnixMilli() || current.WaitingNotice == nil {
			return err
		}
	}
	if current.WaitingNotice.Settled {
		return nil
	}
	selected, owned, err := h.messages.reserveReceiverNotice(ctx, h.messageNotices.rights.owner, r.Receiver, key, r.Token, *current.WaitingNotice)
	if err != nil {
		return err
	}
	if !owned {
		return h.messages.updateMessageNotice(ctx, key, r.Token, func(record *messageRecord) { record.WaitingNotice.Settled = true; record.WaitingNotice.Closed = true })
	}
	if err = h.messages.at("notification_reserved"); err != nil {
		return err
	}
	result, err := h.postMessageNotice(ctx, selected.Request, func() error {
		current, e := h.messages.readRecord(ctx, key)
		if e != nil {
			return e
		}
		if current == nil || current.Token != r.Token || current.ExpiresUnixMs <= h.messages.now().UnixMilli() {
			return asks.ErrNotificationSuppressed
		}
		state, e := h.messages.currentState(ctx, key, current)
		if e != nil {
			return e
		}
		if state.Refused || messageTerminal(state.TaskState) || request.AnswerByUnixMs <= h.messages.now().UnixMilli() {
			return asks.ErrNotificationSuppressed
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = h.messages.at("notification_posted"); err != nil {
		return err
	}
	if !messageNoticeSettled(result.Outcome) {
		return errors.New("messages: waiting notification pending")
	}
	return h.messages.updateMessageNotice(ctx, key, r.Token, func(record *messageRecord) {
		record.WaitingNotice.Settled = true
		record.WaitingNotice.NotificationID = result.ID
		record.WaitingNotice.Closed = result.ID == ""
	})
}

func (h *applicationsHost) closeMessageNotice(ctx context.Context, key, token string) error {
	r, err := h.messages.readRecord(ctx, key)
	if err != nil || r == nil || r.Token != token || r.WaitingNotice == nil || r.WaitingNotice.Closed {
		return err
	}
	a := h.messageNotices
	program := a.rights.self().Program
	result, err := a.book.WithdrawOwnNotification(a.scope, r.WaitingNotice.Request.RequestRef, program, func(poster string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !samePrograms(poster, program) {
			return errors.New("messages: notification owner changed")
		}
		return nil
	})
	if err != nil {
		return err
	}
	switch result.Outcome {
	case awire.NotificationDecisionOutcomeWithdrawn, awire.NotificationDecisionOutcomeExpired, awire.NotificationDecisionOutcomeGone, awire.NotificationDecisionOutcomeDismissed, awire.NotificationDecisionOutcomeActed:
		return h.messages.updateMessageNotice(ctx, key, token, func(record *messageRecord) { record.WaitingNotice.Closed = true })
	}
	return errors.New("messages: waiting notice withdrawal pending")
}

// progressPairNotification posts the one notice of a paused budget scope
// (architect ruling 2026-11-09): what paused, when it resumes, and Resume.
func (h *applicationsHost) progressPairNotification(ctx context.Context, actor messageActor, receiver string, request msg.SendRequest) (int64, error) {
	if h.messageNotices == nil {
		return 0, nil
	}
	raw, err := cas.ReadLimitContext(ctx, h.messages.pairPath(actor, receiver), messageIndexLimit)
	if err != nil {
		return 0, err
	}
	pair, err := readMessagePair(raw)
	if err != nil {
		return 0, err
	}
	now := h.messages.now().UnixMilli()
	scope := messageBudgetScope(actor, receiver, request)
	budget := pair.peekBudget(scope, now)
	if !budget.paused(now) {
		return 0, nil
	}
	day := now / int64(24*time.Hour/time.Millisecond)
	q := h.messagePauseNotice(ctx, actor, receiver, request, scope, budget.PausedUntil)
	result, err := h.postMessageNotice(ctx, q, nil)
	if err != nil {
		return 0, err
	}
	if !messageNoticeSettled(result.Outcome) {
		return 0, errors.New("messages: pair notification pending")
	}
	return day + 1, nil
}
