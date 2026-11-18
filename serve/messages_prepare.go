package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/client"
	"sort"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func (r *messagesReceiver) Prepare(instance, sourceContext string, in msg.PrepareRequest) (result msg.PrepareResult, err error) {
	defer func() { r.recordMessageDiscovery("prepare", result.Outcome, result.Reason) }()
	refuse := func(out msg.Outcome, reason string) (msg.PrepareResult, error) {
		return msg.PrepareResult{Outcome: out, Reason: reason}, nil
	}
	if r.remote != nil || r.selectedPeer != "" {
		return refuse(msg.OutcomeForbidden, "")
	}
	actor, subject, senderInfo, ok := r.owner(instance, sourceContext)
	if !ok {
		return refuse(msg.OutcomeForbidden, "")
	}
	if r.host.messages == nil || r.host.messageRights == nil {
		return refuse(msg.OutcomeUnavailable, "")
	}
	if !strings.HasPrefix(in.Receiver, "app:") || !providerName.MatchString(strings.TrimPrefix(in.Receiver, "app:")) || in.Context != "" && !applicationContextName.MatchString(in.Context) {
		return refuse(msg.OutcomeInvalid, "receiver")
	}
	normalizedTitle, titleValid := client.NormalizeChatTitle(in.ContextTitle)
	if !titleValid || normalizedTitle != in.ContextTitle {
		return refuse(msg.OutcomeInvalid, "context_title")
	}
	kind := msg.KindQuestion
	if in.Kind != nil {
		kind = *in.Kind
	}
	if kind != msg.KindQuestion && kind != msg.KindNote {
		return refuse(msg.OutcomeInvalid, "kind")
	}
	if !nativeMessageText(in.Subject, in.Body, "prepare") || in.Body == "" {
		return refuse(msg.OutcomeInvalid, "text")
	}
	ttl := in.TTLMs
	if ttl == 0 {
		ttl = 600000
		// A question that names only its answer time lives at least that
		// long; the default lifetime never refuses it.
		if in.AnswerWithinMs > ttl && in.AnswerWithinMs <= 604800000 {
			ttl = in.AnswerWithinMs
		}
	}
	if ttl < 1000 || ttl > 604800000 || in.AnswerWithinMs < 0 || in.AnswerWithinMs > 0 && in.AnswerWithinMs < 1000 || in.AnswerWithinMs > ttl || kind == msg.KindNote && in.AnswerWithinMs != 0 {
		return refuse(msg.OutcomeInvalid, "lifetime")
	}
	name, err := r.host.directory.resolveMessageApplication(r.ctx, strings.TrimPrefix(in.Receiver, "app:"))
	if err != nil {
		if errors.Is(err, errMessageApplicationUnknown) {
			return refuse(msg.OutcomeUnavailable, "not_found")
		}
		if errors.Is(err, errMessageApplicationAmbiguous) {
			return refuse(msg.OutcomeConflict, "ambiguous_receiver")
		}
		return refuse(msg.OutcomeUnavailable, "receiver_unavailable")
	}
	in.Receiver = "app:" + name
	var receiver applicationAgentImage
	target := r.host.directory.currentAgentEnrollmentImage(r.ctx, strings.TrimPrefix(in.Receiver, "app:"), &receiver)
	if target.Outcome != applications.OutcomePage {
		return refuse(msg.OutcomeUnavailable, "receiver_unavailable")
	}
	if target.Enrollment == nil {
		return refuse(msg.OutcomeUnavailable, "receiver_unavailable")
	}
	if !messageSendTargetEligibleTo(actor, senderInfo, *target.Enrollment, receiver, in.Context) {
		return refuse(msg.OutcomeForbidden, "")
	}
	r.host.messageRights.prepareAgentMessageClass(r.ctx)
	decision := r.host.messageRights.decide(r.ctx, subject, ActionApplicationMessage, in.Receiver)
	if decision.Outcome == rights.DecisionOutcomeNotGranted || decision.Outcome == rights.DecisionOutcomeDenied {
		marker := "access:" + name
		if len(marker) > 64 {
			return refuse(msg.OutcomeUnavailable, "bound:access_target")
		}
		return refuse(msg.OutcomeForbidden, marker)
	}
	if decision.Outcome != rights.DecisionOutcomePermitted {
		return refuse(msg.OutcomeUnavailable, "receiver_unavailable")
	}
	// A named conversation that is open or retained, such as the chat that
	// just messaged this sender, resolves without discovery; discovery runs
	// only for a conversation those rows do not hold.
	// Prepare matches over one whole read of the listing, so it never pages.
	// The sender's own rows, from the same listing, name the chats whose
	// exchanges the last_exchanged hint reads.
	var mine []messageChat
	listWith := func(discover bool) ([]msg.Inbox, bool, msg.Outcome, string) {
		return messagePrepareCandidates(r.ctx, in, func(msg.InboxQuery) (msg.InboxPage, map[string]messageListedName, error) {
			page, names := r.inboxesListWhole(discover)
			mine = messageMineChats(page)
			return page, names, nil
		})
	}
	ambiguous := func(choices []msg.Inbox) msg.PrepareResult {
		r.markChoiceHints(actor, mine, choices)
		return msg.PrepareResult{Outcome: msg.OutcomeConflict, Reason: "ambiguous_receiver", Choices: choices}
	}
	candidates, choose, outcome, reason := listWith(in.Context == "")
	if in.Context != "" && outcome == msg.OutcomePage && len(candidates) == 0 {
		candidates, choose, outcome, reason = listWith(true)
	}
	if outcome != msg.OutcomePage {
		return refuse(outcome, reason)
	}
	// A name that can pass to another chat, such as a counted or last-active
	// name seen earlier, offers its chats to choose from.
	if choose && len(candidates) > 0 {
		return ambiguous(messageAmbiguousChoices(candidates)), nil
	}
	// An explicit context or title keeps its own selection below. A name
	// alone reaches its program's one live chat; with several, the sending
	// agent chooses (architect ruling P3, 2026-11-22).
	if in.Context == "" && in.ContextTitle == "" {
		live, err := r.host.directory.liveReceivingCandidates(r.ctx, candidates)
		if err != nil {
			return refuse(msg.OutcomeUnavailable, "discovery_incomplete")
		}
		switch len(live) {
		case 0:
			return refuse(msg.OutcomeUnavailable, messageNoReachableChat)
		case 1:
			candidates = live
		default:
			return ambiguous(messageBoundedChoices(live)), nil
		}
	}
	if len(candidates) > 1 {
		return ambiguous(messageAmbiguousChoices(candidates)), nil
	}
	if len(candidates) == 0 {
		// Only a sender whose message right to this receiver is granted learns
		// how its last inbox ended; any other sender reads the generic word.
		reason := "receiver_unavailable"
		if r.host.messageRights.decide(r.ctx, subject, ActionApplicationMessage, in.Receiver).Outcome == rights.DecisionOutcomePermitted {
			reason = r.host.directory.receiverAbsence(r.ctx, strings.TrimPrefix(in.Receiver, "app:"), in.Context)
		}
		return refuse(msg.OutcomeUnavailable, reason)
	}
	selected := candidates[0]
	current, _, _, ok := r.owner(instance, sourceContext)
	if !ok || current.Program != actor.Program || current.Application != actor.Application {
		return refuse(msg.OutcomeForbidden, "")
	}
	key, err := applicationToken()
	if err != nil {
		return refuse(msg.OutcomeUnavailable, "")
	}
	now := r.host.messages.now()
	request := msg.SendRequest{To: selected.Program, Context: selected.Context, Kind: kind, Subject: in.Subject, Body: in.Body, IdempotencyKey: key, ExpiresUnixMs: now.Add(time.Duration(ttl) * time.Millisecond).UnixMilli()}
	if kind == msg.KindQuestion {
		answer := in.AnswerWithinMs
		if answer == 0 {
			answer = ttl
		}
		request.AnswerByUnixMs = now.Add(time.Duration(answer) * time.Millisecond).UnixMilli()
	}
	if _, err := msg.DecodeSendRequest(msg.EncodeSendRequest(&request)); err != nil {
		return refuse(msg.OutcomeInvalid, "request")
	}
	// The result names the chat it prepared for (architect ruling 2026-11-12, (a)).
	result = msg.PrepareResult{Outcome: msg.OutcomePage, SendRequest: &request, ChatTitle: selected.ChatTitle}
	// A queued receiver accepts the message, which waits until its session
	// next reads its inbox; the reason tells the sender so.
	if selected.Reach != nil && *selected.Reach == msg.InboxReachQueued {
		result.Reason = msg.InboxReachQueued.String()
	}
	// A Codex chat whose receiver reports an unconfirmed wake tells the
	// sender why the message waits.
	if messageWakeUnconfirmed(strings.TrimPrefix(selected.Program, "app:"), selected.Status) {
		result.Reason = messagePrepareWakeUnconfirmed
	}
	return result, nil
}

// messageNoReachableChat answers a program-only send when none of the
// program's chats has a live inbox. It names no chat.
const messageNoReachableChat = "no_reachable_chat"

// messageAmbiguousChoices bounds what a title's ambiguous_receiver discloses:
// at most 8 chats, most recently seen first, titles cut to 60 characters.
func messageAmbiguousChoices(rows []msg.Inbox) []msg.Inbox {
	choices := append([]msg.Inbox(nil), rows...)
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].LastSeenUnixMs > choices[j].LastSeenUnixMs })
	return messageBoundedChoices(choices)
}

// messageBoundedChoices keeps the first 8 rows in their order, with titles
// cut to 60 characters.
func messageBoundedChoices(rows []msg.Inbox) []msg.Inbox {
	choices := append([]msg.Inbox(nil), rows...)
	if len(choices) > 8 {
		choices = choices[:8]
	}
	for i := range choices {
		if title := []rune(choices[i].ChatTitle); len(title) > 60 {
			choices[i].ChatTitle = string(title[:60])
		}
	}
	return choices
}

// Prepare owns this cursor, so a changed read-only snapshot restarts the whole
// collection. Partial candidates never cross attempts. Explicit cursor readers
// retain InboxesList's strict gap contract and all its disclosure rechecks.
// choose reports that the title named a listed name that can move between
// chats; its candidates are then offered as choices, never selected.
func messagePrepareCandidates(ctx context.Context, in msg.PrepareRequest, list func(msg.InboxQuery) (msg.InboxPage, map[string]messageListedName, error)) (candidates []msg.Inbox, choose bool, outcome msg.Outcome, reason string) {
	for attempt := 0; attempt < 3; attempt++ {
		var rows []msg.Inbox
		names := map[string]messageListedName{}
		cursor := ""
		seen := map[string]bool{}
		for {
			if ctx.Err() != nil {
				return nil, false, msg.OutcomeUnavailable, "discovery_incomplete"
			}
			page, listed, err := list(msg.InboxQuery{Cursor: cursor})
			if ctx.Err() != nil {
				return nil, false, msg.OutcomeUnavailable, "discovery_incomplete"
			}
			if err != nil {
				return nil, false, msg.OutcomeUnavailable, "discovery_incomplete"
			}
			if page.Outcome == msg.OutcomeGap {
				break
			}
			if page.Outcome != msg.OutcomePage {
				return nil, false, page.Outcome, ""
			}
			for key, name := range listed {
				names[key] = name
			}
			for _, inbox := range page.Inboxes {
				if inbox.Program == in.Receiver && (in.Context == "" || inbox.Context == in.Context) {
					rows = append(rows, inbox)
				}
			}
			if page.Cursor == "" {
				candidates, choose = messageListedNameMatches(rows, names, in.ContextTitle)
				// A context names one chat itself; the title then only confirms it.
				return candidates, choose && in.Context == "", msg.OutcomePage, ""
			}
			if seen[page.Cursor] {
				return nil, false, msg.OutcomeUnavailable, "discovery_incomplete"
			}
			seen[page.Cursor] = true
			cursor = page.Cursor
		}
	}
	return nil, false, msg.OutcomeUnavailable, "discovery_incomplete"
}

// The fixed hint vocabulary of ambiguous_receiver choices (architect ruling
// P3, 2026-11-22). Hints are shown to the sending agent and never acted on.
// last_typed needs the person-typed signal (P16's in-process origin), which
// is not established yet, so no row carries it.
const (
	messageHintLastExchanged = "last_exchanged"
	messageHintLastTyped     = "last_typed"
)

// messageMineChats names the caller's own rows of one listing.
func messageMineChats(page msg.InboxPage) []messageChat {
	var mine []messageChat
	for _, row := range page.Inboxes {
		if row.Mine != nil && *row.Mine {
			mine = append(mine, messageChat{Program: row.Program, Context: row.Context})
		}
	}
	return mine
}

// markChoiceHints marks last_exchanged on the one choice the sending chat
// last sent a message to or received one from. Prepare reaches this only
// after the sender's message right to the program is permitted.
func (r *messagesReceiver) markChoiceHints(actor messageActor, mine []messageChat, choices []msg.Inbox) {
	if r.host.messages == nil || len(choices) == 0 {
		return
	}
	senders := append([]messageChat{{Program: "app:" + actor.Application, Context: actor.returnContext()}}, mine...)
	read := map[messageChat]bool{}
	best, at := -1, int64(0)
	for _, sender := range senders {
		if read[sender] {
			continue
		}
		read[sender] = true
		peers, err := r.host.messages.threads(r.ctx, actor.Account, sender)
		if err != nil {
			// The hint is display data; a damaged index shows none.
			r.reportMessageError("thread index read", err)
			continue
		}
		for _, peer := range peers {
			for i, choice := range choices {
				if choice.Program == peer.Program && choice.Context == peer.Context && (best < 0 || peer.At > at) {
					best, at = i, peer.At
				}
			}
		}
	}
	if best >= 0 {
		choices[best].Hints = append(choices[best].Hints, messageHintLastExchanged)
	}
}
