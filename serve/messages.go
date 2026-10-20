package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	watch "github.com/openabstractions/abstraction-watch/go"
)

// messagesReceiver shares the Applications native listener. All actor facts
// come from its retained call and the existing kept-image owner.
type messagesReceiver struct {
	host                             *applicationsHost
	call                             *listen.FramedCall
	ctx                              context.Context
	remote                           *messageRemoteCall
	selectedPeer, selectedGeneration string
}

func (r *messagesReceiver) reportMessageError(stage string, err error) {
	if r.host.report != nil {
		r.host.report(fmt.Errorf("messages: %s: %w", stage, err))
	}
}

func (r *messagesReceiver) InboxOpen(instance, contextName, status string) (msg.InboxChange, error) {
	defer r.host.reportSlowPresence("inbox open", time.Now())
	v := r.host.directory.openNativeInbox(r.ctx, r.call, instance, contextName, status)
	return r.inboxChange(v)
}

func (r *messagesReceiver) InboxOpenChat(instance, sourceContext, status string, binding msg.ChatBinding) (msg.ChatInboxChange, error) {
	if r.remote != nil || r.call == nil {
		return msg.ChatInboxChange{Outcome: msg.OutcomeForbidden}, nil
	}
	defer r.host.reportSlowPresence("chat inbox open", time.Now())
	v := r.host.directory.openNativeInboxClaim(r.ctx, r.call, instance, sourceContext, status, &binding, r.host.messages)
	change, err := r.inboxChange(v)
	return msg.ChatInboxChange{Outcome: change.Outcome, Context: v.context, Revision: change.Revision, Holder: change.Holder, OpenedUnixMs: change.OpenedUnixMs}, err
}

func (r *messagesReceiver) inboxChange(v nativeInboxView) (msg.InboxChange, error) {
	// A caller that left before its inbox opened chose to; that is no fault.
	if v.cause != nil && !(errors.Is(v.cause, context.Canceled) && r.ctx.Err() != nil) {
		// Keep the wire refusal unchanged. The existing diagnostic path records
		// only the error type and cancellation class, never context/status text.
		r.reportMessageError("inbox open", fmt.Errorf("%T (canceled=%t deadline=%t)", v.cause, errors.Is(v.cause, context.Canceled), errors.Is(v.cause, context.DeadlineExceeded)))
	}
	outcomes := map[string]msg.Outcome{"opened": msg.OutcomeOpened, "held": msg.OutcomeHeld, "forbidden": msg.OutcomeForbidden, "invalid": msg.OutcomeInvalid, "unavailable": msg.OutcomeUnavailable, "ended": msg.OutcomeEnded}
	outcome, ok := outcomes[v.outcome]
	if v.outcome == "page" {
		outcome, ok = msg.OutcomePage, true
	}
	if !ok {
		outcome = msg.OutcomeUnavailable
	}
	var openedAt int64
	if !v.opened.IsZero() {
		openedAt = v.opened.UnixMilli()
	}
	return msg.InboxChange{Outcome: outcome, Revision: v.revision, Holder: v.holder, OpenedUnixMs: openedAt}, nil
}

func (r *messagesReceiver) InboxesList(query msg.InboxQuery) (result msg.InboxPage, err error) {
	defer func() { r.recordMessageDiscovery("inboxes_list", result.Outcome, result.Reason) }()
	if r.selectedPeer != "" {
		if query.PeerKey != "" && query.PeerKey != r.selectedPeer {
			return messageEmptyInboxes(msg.OutcomeForbidden), nil
		}
		query.PeerKey = r.selectedPeer
	}
	if r.remote != nil {
		return r.remoteInboxes(query), nil
	}
	if query.PairingEpoch != "" {
		return messageEmptyInboxes(msg.OutcomeForbidden), nil
	}
	if query.PeerKey != "" {
		return r.pairedInboxes(query), nil
	}
	if query.Receiver != "" {
		return messageEmptyInboxes(msg.OutcomeInvalid), nil
	}
	return r.disclosedInboxes(r.inboxesList(query.Cursor)), nil
}

func (r *messagesReceiver) owner(instance, contextName string) (messageActor, rights.Subject, applicationAgentAdmission, bool) {
	var actor messageActor
	var subject rights.Subject
	var admission applicationAgentAdmission
	ok, err := r.host.directory.withNativeMessageOwnerInfo(r.ctx, r.call, instance, contextName, nil, &admission, func(row *nativeInbox) error {
		subject = row.subject
		actor = messageActor{Account: row.subject.Account, Program: admission.Image.Program, Application: row.application, Context: contextName, ObservedProgram: row.subject.Program}
		return nil
	})
	return actor, subject, admission, ok && err == nil && actor.Program != ""
}

func (r *messagesReceiver) Send(instance, sourceContext string, request msg.SendRequest) (msg.SendResult, error) {
	return r.send(instance, sourceContext, request, nil)
}

func (r *messagesReceiver) SendChat(instance, sourceContext string, request msg.ChatSendRequest) (msg.SendResult, error) {
	if r.remote != nil || !applicationContextName.MatchString(request.ReturnContext) {
		return msg.SendResult{Outcome: msg.OutcomeForbidden}, nil
	}
	if result, handled := r.warmChatNote(instance, sourceContext, request); handled {
		return result, nil
	}
	chat := &messageChatRoute{SourceInstance: instance, ReturnContext: request.ReturnContext}
	owned, err := r.host.directory.withNativeInboxOwner(r.ctx, r.call, instance, sourceContext, func(row *nativeInbox) error {
		if row.admission == nil {
			return errors.New("messages: native source admission missing")
		}
		if !row.process.StartTime.IsZero() || row.process.Generation != 0 {
			chat.SourceProcess = messageSourceProcess(row, sourceContext)
		}
		return nil
	})
	if err != nil {
		return msg.SendResult{Outcome: msg.OutcomeUnavailable}, nil
	}
	if !owned {
		return msg.SendResult{Outcome: msg.OutcomeForbidden}, nil
	}
	return r.send(instance, sourceContext, request.Request, chat)
}

func messageSourceProcess(row *nativeInbox, sourceContext string) string {
	return messageDigest(fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%d\x00%s", row.admission.Image.Program, row.admission.Image.Key, row.process.PID, row.process.StartTime.UTC().Format(time.RFC3339Nano), row.process.Generation, sourceContext))
}

func (r *messagesReceiver) send(instance, sourceContext string, request msg.SendRequest, chat *messageChatRoute) (msg.SendResult, error) {
	if r.selectedPeer != "" {
		if request.PeerKey != "" && request.PeerKey != r.selectedPeer {
			return msg.SendResult{Outcome: msg.OutcomeForbidden}, nil
		}
		request.PeerKey = r.selectedPeer
	}
	if r.remote != nil {
		return r.receiveRemoteMessage(instance, sourceContext, request), nil
	}
	if request.PeerKey != "" && (request.Withdraw != "" || request.WithdrawReference != nil) {
		return unsupportedRemoteWithdrawal(), nil
	}
	if request.Origin != nil || request.PairingEpoch != "" {
		return msg.SendResult{Outcome: msg.OutcomeForbidden}, nil
	}
	if request.PeerKey != "" {
		if chat != nil {
			return r.sendPairedMessageWithChat(instance, sourceContext, request, false, false, chat), nil
		}
		return r.sendPairedMessage(instance, sourceContext, request), nil
	}
	if request.ReplyReference != nil || request.WithdrawReference != nil {
		return msg.SendResult{Outcome: msg.OutcomeInvalid}, nil
	}
	result := msg.SendResult{Outcome: msg.OutcomeForbidden}
	actor, subject, senderInfo, ok := r.owner(instance, sourceContext)
	if !ok {
		return result, nil
	}
	actor.Chat = chat
	store := r.host.messages
	if store == nil || r.host.messageRights == nil {
		result.Outcome = msg.OutcomeUnavailable
		return result, nil
	}
	if replay, found := store.lookupReceipt(r.ctx, actor, request); found {
		if replay.Outcome == msg.OutcomeSent && request.Withdraw != "" {
			if record, key, e := store.readByID(r.ctx, replay.MessageID); e == nil && record != nil {
				if e = store.completeWithdraw(r.ctx, record, key); e != nil {
					r.reportMessageError("cancellation recovery", e)
				}
			}
		}
		return r.projectSendReceipt(replay), nil
	}
	if chat != nil && !r.host.directory.currentChatRoute(r.ctx, actor) {
		return result, nil
	}
	now := store.now().UnixMilli()
	if reason := messageSendInvalidReason(request, now); reason != "" {
		result.Outcome, result.Reason = msg.OutcomeInvalid, reason
		return result, nil
	}
	if request.Withdraw != "" {
		return r.withdrawMessage(instance, sourceContext, actor, request)
	}
	// attach-* contexts are retired (architect ruling 1, AB-1s): every
	// conversation is named by its chat-* context. A chat-* context that is
	// neither retained nor open must be a discovered conversation in the
	// sender's project, or the Send refuses at once instead of queueing.
	if messageAttachContext(request.Context) {
		result.Outcome, result.Reason = msg.OutcomeUnavailable, "session_unknown"
		return result, nil
	}
	var receiver applicationAgentImage
	targetResult := r.host.directory.currentAgentEnrollmentImage(r.ctx, strings.TrimPrefix(request.To, "app:"), &receiver)
	if targetResult.Enrollment == nil {
		result.Outcome = msg.OutcomeUnavailable
		if request.Kind == msg.KindReply && targetResult.Outcome == applications.OutcomeUnknown {
			// A reply to an asker whose app is gone says so at once.
			result.Reason = string(msg.UndeliveredReasonReceiverRemoved)
		}
		return result, nil
	}
	target := *targetResult.Enrollment
	var err error
	if !messageSendTargetEligibleTo(actor, senderInfo, target, receiver, request.Context) {
		return result, nil
	}
	contextID := ""
	var question *messageRecord
	questionKey := ""
	if request.Kind == msg.KindReply {
		question, questionKey, err = store.readByID(r.ctx, request.ReplyTo)
		if err != nil {
			result.Outcome = msg.OutcomeUnavailable
			return result, nil
		}
		if question == nil || question.Actor.Account != actor.Account || !samePrograms(question.Receiver, actor.Program) {
			return result, nil
		}
		original, e := decodeRetainedMessageRequest(question.Spec)
		if e != nil {
			result.Outcome = msg.OutcomeUnavailable
			return result, nil
		}
		if original.Context != sourceContext || request.To != "app:"+question.Actor.Application || request.Context != question.Actor.returnContext() || !samePrograms(receiver.Program, question.Actor.Program) {
			return result, nil
		}
		if original.Kind != msg.KindQuestion {
			result.Outcome = msg.OutcomeInvalid
			return result, nil
		}
		contextID = question.ContextID
	} else if request.ReplyTo != "" {
		result.Outcome = msg.OutcomeInvalid
		return result, nil
	}
	held := false
	r.host.messageRights.prepareAgentMessageClass(r.ctx)
	decision := r.host.messageRights.decide(r.ctx, subject, ActionApplicationMessage, request.To)
	if decision.Outcome == rights.DecisionOutcomeNotGranted {
		peer, e := r.call.Peer()
		if e != nil || r.host.messageRights.firstUse == nil {
			result.Outcome = msg.OutcomeUnavailable
			result.Refusal = messageRightsRefusal(decision)
			return result, nil
		}
		questionID := r.host.messageRights.firstUse(subject, requesterPublisher(peer), ActionApplicationMessage, request.To)
		if questionID == "" {
			decision = r.host.messageRights.decide(r.ctx, subject, ActionApplicationMessage, request.To)
		} else {
			held = true
		}
	}
	if !held && decision.Outcome != rights.DecisionOutcomePermitted {
		if decision.Outcome == rights.DecisionOutcomeUnavailable || decision.Outcome == rights.DecisionOutcomeNotGranted {
			result.Outcome = msg.OutcomeUnavailable
		}
		result.Refusal = messageRightsRefusal(decision)
		return result, nil
	}
	// Resolved only once the direction's message permit admits the Send, or
	// holds it for the person: an unpermitted sender never starts a discovery.
	var discovered *applicationAttachSession
	if request.Kind != msg.KindReply && messageCanonicalChatContext(request.Context) {
		// A Send held for the person's permit starts no discovery.
		known, found, err := r.host.messageChatKnown(r.ctx, actor, strings.TrimPrefix(request.To, "app:"), request.Context, !held)
		if err != nil {
			result.Outcome = msg.OutcomeUnavailable
			return result, nil
		}
		if !known && found == nil {
			result.Outcome, result.Reason = msg.OutcomeUnavailable, "session_unknown"
			return result, nil
		}
		discovered = found
	}
	// A same-app message reaches only chats of the sender's own project.
	if !messageSendTargetEligible(actor, senderInfo, target, receiver) && !r.host.messageSameAppInProject(r.ctx, actor, receiver.Program, request.Context) {
		result.Outcome, result.Reason = msg.OutcomeUnavailable, "session_unknown"
		return result, nil
	}
	if err := r.host.resumeMessageBudget(r.ctx, actor, receiver.Program, request); err != nil {
		r.reportMessageError("message resume", err)
	}
	allowed, err := r.host.directory.withNativeMessageOwner(r.ctx, r.call, instance, sourceContext, []applications.AgentEnrollment{target}, func(_ *nativeInbox) error {
		if chat != nil && !r.host.directory.currentChatRouteLocked(actor) {
			result.Outcome = msg.OutcomeForbidden
			return nil
		}
		var replyTarget *messageReplyTarget
		if question != nil {
			replyTarget = &messageReplyTarget{key: questionKey, token: question.Token}
		}
		// A woken turn sends with its origin chain (VISION 2026-10-31). The
		// row belongs to the woken inbox: a chat sends from its source
		// context and returns to the canonical chat context it was woken on.
		origin := withMessageOrigin(r.ctx, r.host.directory.messageWakes.origin(actor.Application, actor.returnContext(), r.host.directory.now()))
		result = store.acceptReply(origin, actor, receiver.Program, request, held, contextID, replyTarget)
		return nil
	})
	if err != nil {
		result.Outcome = msg.OutcomeUnknown
	} else if !allowed {
		result.Outcome = msg.OutcomeForbidden
	}
	if result.Outcome == msg.OutcomeSent && !result.AwaitingPermission && question != nil {
		// Acceptance remains canonical even if the outgoing state projection needs
		// recovery. Never relabel an accepted reply as an absent request.
		if err = store.transitionState(r.ctx, questionKey, question, "complete"); err != nil && r.host.report != nil {
			r.host.report(fmt.Errorf("messages: reply state projection unavailable"))
		}
		// A reply proves its question reached the receiver, also when the
		// receiver never acknowledged the body page it read (owner, R19b: a
		// question answered with a reply expired as undelivered).
		if err = store.finishDelivery(r.ctx, questionKey, question.Token, msg.DeliveryDelivered, ""); err != nil && r.host.report != nil {
			r.host.report(fmt.Errorf("messages: replied question delivery unavailable"))
		}
	}
	// A question to a discovered conversation promotes it to a retained
	// chat, whose existing start path then decides; a note writes nothing.
	if result.Outcome == msg.OutcomeSent && discovered != nil && request.Kind == msg.KindQuestion {
		if err := r.host.promoteDiscoveredChat(r.ctx, receiver.Program, *discovered); err != nil {
			r.reportMessageError("discovered chat promotion", err)
		}
	}
	// The two chats now share a thread: each lists the other with its title.
	if result.Outcome == msg.OutcomeSent && request.Withdraw == "" {
		sender := messageChat{Program: "app:" + actor.Application, Context: actor.returnContext()}
		if err := store.noteThread(r.ctx, actor.Account, sender, messageChat{Program: request.To, Context: request.Context}, store.now().UnixMilli()); err != nil {
			r.reportMessageError("thread index", err)
		}
	}
	if result.Outcome == msg.OutcomeExhausted {
		if _, err = r.host.progressPairNotification(r.ctx, actor, receiver.Program, request); err != nil {
			r.reportMessageError("pair notification pending", err)
		}
	}
	return r.projectSendReceipt(result), nil
}

// messageSameAppReceivers admits other conversations of the sender's own
// app as receivers (owner, 2026-11-09). TurnWake bounds open-chat loops, a
// same-app message is budgeted per chat pair with a resumable pause, and the
// person grants app:X → app:X through request access (backlog/AB-1s.md,
// architect ruling 2026-11-09: same-app messaging on).
var messageSameAppReceivers = true

// messageSendTargetEligibleTo also admits another conversation of the
// sender's own app (owner, 2026-11-09): a chat-* context other than the
// sender's own. An empty context admits the app for listing, whose rows leave
// out the sender's own conversation.
func messageSendTargetEligibleTo(actor messageActor, sender applicationAgentAdmission, target applications.AgentEnrollment, receiver applicationAgentImage, contextName string) bool {
	if messageSendTargetEligible(actor, sender, target, receiver) {
		return true
	}
	if !messageSameAppReceivers || target.State != applications.AgentEnrollmentStateEnrolled || receiver.Program == "" || target.Application != sender.Enrollment.Application {
		return false
	}
	// Only a bound conversation can tell its own chat from another's.
	if !messageCanonicalChatContext(actor.returnContext()) {
		return false
	}
	return contextName == "" || messageCanonicalChatContext(contextName) && !messageOwnContext(actor, contextName)
}

// messageOwnContext reports the sender's own conversation.
func messageOwnContext(actor messageActor, contextName string) bool {
	return contextName == actor.returnContext() || contextName == actor.Context
}

// Prepare and Send share the native image/actor exclusion. Listing one's own
// inbox supplies observation only and never makes it a send destination.
func messageSendTargetEligible(actor messageActor, sender applicationAgentAdmission, target applications.AgentEnrollment, receiver applicationAgentImage) bool {
	return target.State == applications.AgentEnrollmentStateEnrolled && receiver.Program != "" && receiver.Key != sender.Image.Key && !samePrograms(actor.Program, receiver.Program)
}

func (r *messagesReceiver) Wait(instance, contextName string, view msg.View, after, revision string, waitMs int64) (msg.Page, error) {
	if waitMs < 0 || waitMs > 30000 {
		return messageEmptyPage(msg.OutcomeInvalid), nil
	}
	if view == msg.ViewHeaders {
		if instance != "" || after != "" {
			return messageEmptyPage(msg.OutcomeInvalid), nil
		}
		return r.headersWait(contextName, revision, waitMs), nil
	}
	if view != msg.ViewBody || instance == "" || revision != "" {
		return messageEmptyPage(msg.OutcomeInvalid), nil
	}
	if r.host.messages == nil {
		return messageEmptyPage(msg.OutcomeUnavailable), nil
	}
	page := messageEmptyPage(msg.OutcomeUnavailable)
	var handed messageHandedPage
	var stopWatching func()
	var watchErr error
	defer func() {
		if stopWatching != nil {
			stopWatching()
		}
	}()
	outcome, err := r.host.messages.gate.Wait(r.ctx, time.Duration(waitMs)*time.Millisecond, watch.Probe{
		Read: func() (bool, error) {
			var e error
			page, e = r.bodySnapshot(instance, contextName, after, &handed)
			return len(page.Messages) > 0 || page.Outcome != msg.OutcomePage, e
		},
		Recheck: func() error {
			if watchErr != nil {
				return watchErr
			}
			if messageWaitRecheckFault != nil {
				if err := messageWaitRecheckFault(); err != nil {
					return err
				}
			}
			if page.Outcome != msg.OutcomePage {
				return nil
			}
			_, _, _, ok := r.owner(instance, contextName)
			if !ok {
				return errors.New("messages: native inbox ownership ended")
			}
			return nil
		},
		Deadline: func() time.Time {
			if stopWatching == nil && watchErr == nil {
				actor, _, _, ok := r.owner(instance, contextName)
				if !ok {
					watchErr = errors.New("messages: native inbox ownership ended")
				} else {
					stopWatching, watchErr = r.watchMessages(actor)
				}
			}
			return time.Time{} // Gate's bounded wait ends with a fresh read.
		},
	})
	if err != nil || watchErr != nil || outcome == watch.Busy || outcome == watch.Failed {
		return messageEmptyPage(msg.OutcomeUnavailable), nil
	}
	// Read state belongs to the service (architect ruling 2026-11-09): the
	// bodies this answered page hands its native owner are shown for waking,
	// whether or not its model later passes after. ACK stays with after.
	if page.Outcome == msg.OutcomePage && len(handed.refs) > 0 {
		r.host.messages.markHandedShown(r.ctx, handed.account, handed.program, handed.context, handed.refs)
	}
	return page, nil
}

// messageHandedPage is the incoming bodies one body snapshot persisted for
// its native owner; Wait marks them shown once it answers with that page.
type messageHandedPage struct {
	account, program, context string
	refs                      []messagePointer
}

// messageWaitRecheckFault is an inert test seam: a failing final recheck.
var messageWaitRecheckFault func() error

func (r *messagesReceiver) bodySnapshot(instance, contextName, after string, handed *messageHandedPage) (msg.Page, error) {
	*handed = messageHandedPage{}
	page := messageEmptyPage(msg.OutcomeUnavailable)
	actor, _, _, ok := r.owner(instance, contextName)
	if !ok {
		page.Outcome = msg.OutcomeForbidden
		return page, nil
	}
	store := r.host.messages
	if store == nil || r.host.messageRights == nil {
		return page, nil
	}
	if err := r.host.progressMessages(r.ctx); err != nil {
		r.reportMessageError("bounded state progress", err)
		return page, nil
	}
	refs, err := store.queueCandidates(r.ctx, actor.Account, actor.Program, contextName)
	if err != nil {
		r.reportMessageError("incoming queue", err)
		return page, nil
	}
	permitted := map[string]string{}
	// One snapshot reads each sender's and receiver's enrollment once.
	pass := newMessageProgressPass()
	for _, ref := range refs {
		record, e := store.readRecord(r.ctx, ref.Key)
		if e != nil {
			return page, nil
		}
		if record == nil || record.Token != ref.Token {
			continue
		}
		_, allowed, e := r.host.reconcileMessageStateIn(r.ctx, ref.Key, record, pass)
		if e != nil {
			r.reportMessageError("incoming state", e)
			return page, nil
		}
		if allowed {
			permitted[ref.Key] = ref.Token
		}
	}
	outgoing, e := store.messageEventCandidates(r.ctx, actor, store.stateEventCheckFor(actor))
	if e != nil {
		r.reportMessageError("outgoing events", e)
		return page, nil
	}
	for _, event := range outgoing {
		if event.DeliveryReport != "" {
			permitted[event.Key] = event.Token
			continue
		}
		record, e := store.readRecord(r.ctx, event.Key)
		if e != nil {
			return page, nil
		}
		if record == nil || record.Token != event.Token {
			return page, nil
		}
		incoming, e := messageEventReceiverFor(record, actor)
		if e != nil {
			return page, nil
		}
		allowed := !incoming || record.Acked
		if incoming && !record.Acked {
			_, allowed, e = r.host.reconcileMessageStateIn(r.ctx, event.Key, record, pass)
			if e != nil {
				return page, nil
			}
		}
		if allowed {
			permitted[event.Key] = event.Token
		}
	}
	owned, e := r.host.directory.withNativeMessageOwner(r.ctx, r.call, instance, contextName, nil, func(row *nativeInbox) error {
		owner := fmt.Sprintf("%s/%s/%d/%s/%d", row.session, row.instance, row.process.PID, row.process.StartTime.UTC().Format(time.RFC3339Nano), row.process.Generation)
		var refs []messagePointer
		page, refs = store.deliveryPage(r.ctx, actor.Account, actor.Program, contextName, owner, after, permitted, outgoing)
		*handed = messageHandedPage{account: actor.Account, program: actor.Program, context: contextName, refs: refs}
		return nil
	})
	if e != nil {
		page.Outcome = msg.OutcomeUnknown
		return page, nil
	}
	if !owned {
		page.Outcome = msg.OutcomeForbidden
		return page, nil
	}
	return page, nil
}
