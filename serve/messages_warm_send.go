package main

import (
	"context"

	cas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type messageRegistrationEntry struct {
	enrollment applications.AgentEnrollment
	mark       selfMark
}

// This two-party snapshot carries registration facts only. The call's retained
// process, native images and person answers are checked anew for each Send.
type messageRegistrationSnapshot struct {
	facts   messagePublicationFacts
	entries map[string]messageRegistrationEntry
}

func (d *applicationDirectory) prepareMessageRegistrationSnapshot(legacy map[string]wire.ApplicationDescriptor, marks map[string]selfMark, sender string, related []applications.AgentEnrollment, facts messagePublicationFacts) {
	if len(related) != 1 || sender == related[0].Application {
		return
	}
	entries := make(map[string]messageRegistrationEntry, 2)
	for _, name := range []string{sender, related[0].Application} {
		entry, binding, ok := d.agentEntryLocked(legacy, name)
		if !ok {
			return
		}
		mark := marks[name]
		entries[name] = messageRegistrationEntry{agentEnrollmentView(name, entry.Program, binding, d.owner, mark), mark}
	}
	// Keep the most recently refreshed pairs without changing hot-read state.
	// The snapshots retain no authorization; sends revalidate their inputs.
	slot := len(d.messageRegistrations) - 1
	for i, old := range d.messageRegistrations {
		if old == nil || old.contains(sender, related[0].Application) {
			slot = i
			break
		}
	}
	copy(d.messageRegistrations[1:slot+1], d.messageRegistrations[:slot])
	d.messageRegistrations[0] = &messageRegistrationSnapshot{facts: facts, entries: entries}
}

func (s *messageRegistrationSnapshot) contains(sender, target string) bool {
	_, hasSender := s.entries[sender]
	_, hasTarget := s.entries[target]
	return hasSender && hasTarget
}

func (e messageRegistrationEntry) currentImage(ctx context.Context, d *applicationDirectory, sender bool) (applicationAgentImage, bool) {
	m := e.mark
	if e.enrollment.State != applications.AgentEnrollmentStateEnrolled || m.Agent == nil || m.Agent.Activation != nil || d.agentQuestions == nil {
		return applicationAgentImage{}, false
	}
	verify := d.verifyAgentImage
	if sender {
		verify = d.admittedAgentImage
	}
	image, err := verify(ctx, e.enrollment.Program, m.Agent.Binding)
	if err != nil || image.Key != m.ProgramKey {
		return applicationAgentImage{}, false
	}
	record, found, err := d.agentQuestions.a.book.ApplicationRecord(m.Question)
	return image, err == nil && found && agentPersonAnswer(m, record)
}

// A cache miss leaves the operation to SendChat's established path before
// budget updates, discovery, first-use questions or store publication.
func (r *messagesReceiver) warmChatNote(instance, sourceContext string, in msg.ChatSendRequest) (msg.SendResult, bool) {
	request := in.Request
	to, ok := messageApplicationResource(request.To)
	if !ok || r.selectedPeer != "" || request.PeerKey != "" || request.Origin != nil || request.PairingEpoch != "" ||
		request.Kind != msg.KindNote || request.ReplyTo != "" || request.Withdraw != "" ||
		request.ReplyReference != nil || request.WithdrawReference != nil || messageAttachContext(request.Context) {
		return msg.SendResult{}, false
	}
	d, store, policy := r.host.directory, r.host.messages, r.host.messageRights
	if store == nil || policy == nil || policy.policy == nil || instance == "" || len(instance) > 128 ||
		messageSendInvalidReason(request, store.now().UnixMilli()) != "" {
		return msg.SendResult{}, false
	}
	pin, err := r.call.RetainProcess()
	if err != nil {
		return msg.SendResult{}, false
	}
	defer pin.Close()
	_, subject, session, process, err := nativeInboxPeer(pin, d.now())
	if err != nil || subject.Account != d.owner || !d.lockRead(r.ctx) {
		return msg.SendResult{}, false
	}
	lease, live := d.instances[instance]
	var snapshot *messageRegistrationSnapshot
	if live && lease.application != to {
		for _, candidate := range d.messageRegistrations {
			if candidate != nil && candidate.contains(lease.application, to) {
				snapshot = candidate
				break
			}
		}
	}
	hasV3 := d.v3 != nil
	d.mu.RUnlock()
	if snapshot == nil {
		return msg.SendResult{}, false
	}
	source := snapshot.entries[lease.application]
	target := snapshot.entries[to]
	if !applicationRegistrationPrograms(source.mark.Program, subject.Program, source.mark.ProgramKey, identity.ProgramKey(subject.Program)) {
		return msg.SendResult{}, false
	}
	sender, ok := source.currentImage(r.ctx, d, true)
	if !ok {
		return msg.SendResult{}, false
	}
	receiver, ok := target.currentImage(r.ctx, d, false)
	if !ok {
		return msg.SendResult{}, false
	}
	admission := applicationAgentAdmission{Enrollment: source.enrollment, Image: sender}
	route := &messageChatRoute{SourceInstance: instance, ReturnContext: in.ReturnContext}
	actor := messageActor{Account: subject.Account, Program: sender.Program, Application: lease.application, Context: sourceContext, ObservedProgram: subject.Program, Chat: route}
	if !messageSendTargetEligible(actor, admission, target.enrollment, receiver) {
		return msg.SendResult{}, false
	}
	checkOwner := func() bool {
		row, err := d.nativeMessageOwnerLocked(pin, instance, sourceContext, lease.application, subject, session, process, &admission)
		if err != nil || row == nil || row.admission == nil || (row.process.StartTime.IsZero() && row.process.Generation == 0) {
			return false
		}
		route.SourceProcess = messageSourceProcess(row, sourceContext)
		return d.currentChatRouteLocked(actor) && d.messageContextLiveLocked(to, request.Context)
	}
	owned := false
	current, err := d.publishMessageWithReadCustody(r.ctx, &snapshot.facts, func() error {
		owned = checkOwner()
		return nil
	})
	if err != nil || !current || !owned {
		return msg.SendResult{}, false
	}
	// Only inspect the existing record here. The slow replay path owns repair.
	if old, err := store.readRecord(r.ctx, store.key(actor, request.IdempotencyKey)); err != nil || old != nil {
		return msg.SendResult{}, false
	}
	roles := d.roles.Load()
	if policy.applications != nil && (roles == nil || roles.messaging == nil) && (hasV3 || d.agentClass.Load() != nil) {
		// Default-policy eligibility is prepared by the existing slow path.
		return msg.SendResult{}, false
	}
	if notices := r.host.messageNotices; notices != nil && notices.book != nil {
		path := store.pairPath(actor, receiver.Program)
		raw, err := cas.ReadLimitContext(r.ctx, path, messageIndexLimit)
		if err != nil {
			return msg.SendResult{}, false
		}
		if raw != nil {
			pair, err := readMessagePair(raw)
			now := store.now().UnixMilli()
			budget := pair.peekBudget(messageBudgetScope(actor, receiver.Program, request), now)
			if err != nil || budget.paused(now) {
				return msg.SendResult{}, false
			}
		}
	}
	var result msg.SendResult
	var handled bool
	// Validate the current published role facts outside the acceptance callback
	// and run the existing Rights owner without a registration lock.
	if roles != nil && roles.messaging != nil {
		current, err := d.publishMessageWithReadCustody(r.ctx, &snapshot.facts, func() error {
			return roles.messaging.current(r.ctx)
		})
		if err != nil || !current {
			return msg.SendResult{}, false
		}
	}
	if policy.decide(r.ctx, subject, ActionApplicationMessage, request.To).Outcome != rights.DecisionOutcomePermitted {
		return msg.SendResult{}, false
	}
	current, err = d.publishMessageWithReadCustody(r.ctx, &snapshot.facts, func() error {
		if d.roles.Load() != roles || !checkOwner() {
			return nil
		}
		origin := withMessageOrigin(r.ctx, d.messageWakes.origin(actor.Application, actor.returnContext(), d.now()))
		result, handled = store.acceptFreshNote(origin, actor, receiver.Program, request, "", store.key(actor, request.IdempotencyKey))
		return nil
	})
	if err != nil || !current || !handled {
		return msg.SendResult{}, false
	}
	if result.Outcome == msg.OutcomeSent {
		sourceChat := messageChat{Program: "app:" + actor.Application, Context: actor.returnContext()}
		targetChat := messageChat{Program: request.To, Context: request.Context}
		if err := store.noteThread(r.ctx, actor.Account, sourceChat, targetChat, store.now().UnixMilli()); err != nil {
			r.reportMessageError("thread index", err)
		}
	}
	if result.Outcome == msg.OutcomeExhausted {
		if _, err := r.host.progressPairNotification(r.ctx, actor, receiver.Program, request); err != nil {
			r.reportMessageError("pair notification pending", err)
		}
	}
	return r.projectSendReceipt(result), true
}
