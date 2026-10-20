package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	awire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
)

// Message budgets (architect ruling 2026-11-09, backlog/AB-1s.md). A program
// pair accepts 20 messages an hour. A same-app message is budgeted per
// directed chat pair instead, so a loop between two chats leaves the rest of
// that app's chats working. The second refused send of a day pauses its scope
// for 8 hours, until the person's Resume or the pause's end.
const (
	messageBudgetPerHour = 20
	messagePauseFor      = 8 * time.Hour
	messageChatBudgets   = 64
	messageResumeAction  = "resume"
)

const (
	messageHourMs = int64(time.Hour / time.Millisecond)
	messageDayMs  = int64(24 * time.Hour / time.Millisecond)
)

type messageBudget struct {
	Hour        int64
	Count       int
	Day         int64
	Exhaustions int
	// Paused is the pause written before PausedUntil existed; roll converts
	// it to a pause that ends at the end of its day.
	Paused      bool
	PausedUntil int64 `json:",omitempty"`
	// ResumedAt is the person's Resume: the program pair's hourly recount
	// counts only what was accepted at or after it.
	ResumedAt int64 `json:",omitempty"`
}

type messageChatBudget struct {
	Scope string
	messageBudget
}

func (b *messageBudget) valid() bool {
	return b.Count >= 0 && b.Count <= messageBudgetPerHour && b.Exhaustions >= 0 && b.PausedUntil >= 0 && b.ResumedAt >= 0
}

// resume clears a pause the person resumed and starts a fresh count.
func (b *messageBudget) resume(now int64) {
	b.PausedUntil, b.Exhaustions, b.Count, b.ResumedAt = 0, 0, 0, now
}

// messageChatPairsFull refuses a new chat pair while every kept one is still
// in use this hour or was refused today.
const messageChatPairsFull = "chat_pairs_full"

// roll brings the budget to now: a new hour clears the count, a new day the
// refusals, and an ended pause clears with its refusals.
func (b *messageBudget) roll(now int64) {
	if b.Paused && b.PausedUntil == 0 {
		b.PausedUntil = (b.Day + 1) * messageDayMs
	}
	b.Paused = false
	if b.PausedUntil != 0 && b.PausedUntil <= now {
		b.PausedUntil, b.Exhaustions = 0, 0
	}
	if hour := now / messageHourMs; b.Hour != hour {
		b.Hour, b.Count = hour, 0
	}
	if day := now / messageDayMs; b.Day != day {
		b.Day, b.Exhaustions = day, 0
	}
}

func (b *messageBudget) paused(now int64) bool { return b.PausedUntil > now }

func (b *messageBudget) full(now int64) bool {
	return b.paused(now) || b.Count >= messageBudgetPerHour
}

// refuse counts one refused send and returns when to retry; the second
// refusal of a day pauses the scope.
func (b *messageBudget) refuse(now int64) int64 {
	b.Exhaustions++
	if !b.paused(now) && b.Exhaustions >= 2 {
		b.PausedUntil = now + int64(messagePauseFor/time.Millisecond)
	}
	if b.paused(now) {
		return b.PausedUntil - now
	}
	return (b.Hour+1)*messageHourMs - now
}

// messageBudgetScope is the directed chat pair a same-app message is budgeted
// under, or "" for the program pair.
func messageBudgetScope(actor messageActor, receiver string, request msg.SendRequest) string {
	if actor.Remote != nil || actor.Program != receiver && !samePrograms(actor.Program, receiver) {
		return ""
	}
	return messageDigest(actor.returnContext() + "\x00" + request.Context)[:32]
}

func messageRecordBudgetScope(r *messageRecord) (string, error) {
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil {
		return "", err
	}
	return messageBudgetScope(r.Actor, r.Receiver, *request), nil
}

// budget is scope's budget, rolled to now, created when missing. It is nil
// when the pair keeps its maximum of chat budgets and none may be dropped:
// only a scope whose hour has ended, that was not refused today and is not
// paused can give way, so no active scope ever restarts its count.
func (p *messagePair) budget(scope string, now int64) *messageBudget {
	if scope == "" {
		p.messageBudget.roll(now)
		return &p.messageBudget
	}
	keep := p.Chats[:0]
	for _, c := range p.Chats {
		if c.Scope == scope || c.paused(now) || now/messageHourMs-c.Hour < 24 {
			keep = append(keep, c)
		}
	}
	p.Chats = keep
	for i := range p.Chats {
		if p.Chats[i].Scope == scope {
			p.Chats[i].roll(now)
			return &p.Chats[i].messageBudget
		}
	}
	if len(p.Chats) >= messageChatBudgets {
		oldest := -1
		for i, c := range p.Chats {
			idle := c.Hour < now/messageHourMs && !c.paused(now) && (c.Day != now/messageDayMs || c.Exhaustions == 0)
			if idle && (oldest < 0 || c.Hour < p.Chats[oldest].Hour) {
				oldest = i
			}
		}
		if oldest < 0 {
			return nil
		}
		p.Chats = append(p.Chats[:oldest], p.Chats[oldest+1:]...)
	}
	p.Chats = append(p.Chats, messageChatBudget{Scope: scope})
	b := &p.Chats[len(p.Chats)-1].messageBudget
	b.roll(now)
	return b
}

// peekBudget is scope's budget rolled to now without changing the pair.
func (p messagePair) peekBudget(scope string, now int64) messageBudget {
	b := p.messageBudget
	if scope != "" {
		b = messageBudget{}
		for _, c := range p.Chats {
			if c.Scope == scope {
				b = c.messageBudget
			}
		}
	}
	b.roll(now)
	return b
}

func validMessageChatBudgets(chats []messageChatBudget) bool {
	if len(chats) > messageChatBudgets {
		return false
	}
	seen := map[string]bool{}
	for _, c := range chats {
		if !agentHex(c.Scope, 16) || seen[c.Scope] || !c.valid() {
			return false
		}
		seen[c.Scope] = true
	}
	return true
}

// messagePauseNotice is the one notification of one pause.
func (h *applicationsHost) messagePauseNotice(ctx context.Context, actor messageActor, receiver string, request msg.SendRequest, scope string, until int64) awire.NotificationRequest {
	sender, target := h.messageNoticeSender(ctx, actor), h.directory.messageApplicationTitle(ctx, strings.TrimPrefix(request.To, "app:"))
	if scope != "" {
		app := h.directory.messageApplicationTitle(ctx, actor.Application)
		sender = h.messageChatNoticeTitle(ctx, actor.Account, actor.Application, actor.Program, actor.returnContext(), app)
		target = h.messageChatNoticeTitle(ctx, actor.Account, actor.Application, receiver, request.Context, app)
	}
	return awire.NotificationRequest{
		RequestRef: messagePauseRef(actor, receiver, scope, until),
		Title:      "Messages paused",
		Body:       "“" + messageNoticeName(sender) + "” sent “" + messageNoticeName(target) + "” 20 messages in an hour. " + messagePauseEnds(until-h.messages.now().UnixMilli()),
		Tag:        "message-pair-" + messageDigest(actor.Program+"\x00"+receiver+"\x00"+scope)[:24],
		Delivery:   awire.NotificationDeliveryNow,
		ExpiresMs:  until,
		Actions:    []awire.NotificationAction{{ID: messageResumeAction, Title: "Resume", Activation: awire.NotificationActivationApp}},
	}
}

// messagePauseRef identifies one pause of one budget scope.
func messagePauseRef(actor messageActor, receiver, scope string, until int64) awire.RequestRef {
	digest := messageDigest(actor.Account + "\x00" + actor.Program + "\x00" + receiver + "\x00" + scope)
	return awire.RequestRef{Key: "message-pair-" + digest[:24] + "-" + strconv.FormatInt(until, 10), ReplayUntilMs: until}
}

// messagePauseEnds says when a pause ends: "Resumes in 8 hours." for a new
// one, the hours left for a pause written before the 8-hour rule.
func messagePauseEnds(left int64) string {
	hours := (left + messageHourMs - 1) / messageHourMs
	switch {
	case hours <= 1:
		return "Resumes within an hour."
	default:
		return "Resumes in " + strconv.FormatInt(hours, 10) + " hours."
	}
}

// messageChatNoticeTitle names one chat by its retained title, otherwise as
// "<App> chat (<folder>)", or "<App> chat" when its folder is unknown.
func (h *applicationsHost) messageChatNoticeTitle(ctx context.Context, account, application, program, contextName, app string) string {
	folder := ""
	if chats, err := h.messages.retainedChats(ctx, account, program); err == nil {
		for _, chat := range chats {
			if chat.Context != contextName {
				continue
			}
			if strings.TrimSpace(chat.Title) != "" {
				return chat.Title
			}
			if chat.Launch != nil && chat.Launch.Cwd != "" {
				folder = filepath.Base(filepath.Clean(chat.Launch.Cwd))
			}
		}
	}
	if folder == "" && h.directory != nil {
		for _, s := range h.directory.messageAttachSessionsFor(ctx, application) {
			if s.Context == contextName {
				folder = s.Folder
			}
		}
	}
	if folder = nativeMessageHeader(folder); folder != "" {
		return app + " chat (" + folder + ")"
	}
	return app + " chat"
}

// resumeMessageBudget clears a pause the person resumed through its
// notification. Only the native Act owner records that answer; a peer, a
// model or a setting cannot resume a pause.
func (h *applicationsHost) resumeMessageBudget(ctx context.Context, actor messageActor, receiver string, request msg.SendRequest) error {
	a := h.messageNotices
	if h.messages == nil || a == nil || a.book == nil {
		return nil
	}
	s := h.messages
	path := s.pairPath(actor, receiver)
	raw, err := cas.ReadLimitContext(ctx, path, messageIndexLimit)
	if err != nil || raw == nil {
		return err
	}
	p, err := readMessagePair(raw)
	if err != nil {
		return err
	}
	scope := messageBudgetScope(actor, receiver, request)
	now := s.now().UnixMilli()
	b := p.peekBudget(scope, now)
	if !b.paused(now) {
		return nil
	}
	until := b.PausedUntil
	seen, err := a.book.ObserveNotification(a.scope, messagePauseRef(actor, receiver, scope, until))
	if err != nil || seen.Outcome != awire.NotificationOutcomeActed || seen.Result == nil || seen.Result.Action != messageResumeAction || seen.Result.DecidedBy == "" {
		return err
	}
	return cas.ChangeLimitContext(ctx, path, messageIndexLimit, func(raw []byte) ([]byte, error) {
		p, err := readMessagePair(raw)
		if err != nil {
			return nil, err
		}
		now := s.now().UnixMilli()
		b := p.budget(scope, now)
		if b == nil || b.PausedUntil != until {
			return raw, nil
		}
		b.resume(now)
		return json.Marshal(p)
	})
}
