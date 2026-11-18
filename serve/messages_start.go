package main

import (
	"context"
	"encoding/json"
	"errors"
	asks "github.com/openabstractions/abstraction-asks/go"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-identity/remote"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

type messageStartAttempt struct {
	Resource, Attempt, State string
	At                       int64
	AuditDone                bool
	// Folder is the woken conversation's folder label as its launcher
	// reported it at start, for the Activity row; never a path.
	Folder string `json:",omitempty"`
	// Delivery is how the addressed session received the turn, once the
	// launcher reported it: copy, desktop_queue, read_only or resumed.
	Delivery string `json:",omitempty"`
}

// The launcher's delivery words on the owned-start channel (mcp-gateway
// agentDelivery*). A desktop_queue turn runs in the person's Codex app, not
// in OA's process, so OA cannot stop it; a read_only turn is a headless Codex
// resume while the app was closed.
const (
	messageStartDeliveryKey   = "oa_receiver_delivery"
	messageStartDesktopQueue  = "desktop_queue"
	messageStartReadOnly      = "read_only"
	messageStartDeliveredCopy = "copy"
	// messageStartResumed: a closed Claude chat answered in place while no
	// Claude process ran.
	messageStartResumed = "resumed"
)

func validMessageStartDelivery(mode string) bool {
	return mode == messageStartDesktopQueue || mode == messageStartReadOnly || mode == messageStartDeliveredCopy || mode == messageStartResumed
}

// messageStartFolderLimit bounds the retained label; the launcher sends the
// folder's last segment only.
const messageStartFolderLimit = 256

// Start states a person or the hop limit sets. Neither runs a turn, and the
// question waits for an open inbox.
const (
	messageStartStopped  = "stopped"
	messageStartHopLimit = "hop_limit"
)

type messageStartCharge struct {
	Key, Token string
	At         int64
}

func validMessageStart(start *messageStartAttempt, kind msg.Kind) bool {
	_, resource := grants.MessageStartResource(start.Resource)
	return (kind == msg.KindQuestion || kind == msg.KindNote) && resource && agentHex(start.Attempt, 32) && start.At > 0 && len(start.State) <= 64 && len(start.Folder) <= messageStartFolderLimit && utf8.ValidString(start.Folder) && (start.Delivery == "" || validMessageStartDelivery(start.Delivery))
}

// messageStartWording is the consent sentence a start permit is granted
// under: the question's words with the receiver's display name as a slot the
// popup fills in, and the catalogue's disclosure, both owned by grants. The
// resource binds it with the receiver's program, enrollment and recipe, so a
// permit granted under an earlier sentence no longer matches and the person
// is asked again once (VISION 2026-10-31), while renaming the app's title
// asks nothing. One permit per direction covers attach and the recipe's own
// conversation alike.
func messageStartWording() string {
	verb, object := grants.MessageStartWords(grants.MessageStartDisplaySlot)
	definition, _ := grants.PermissionDefinition(grants.ApplicationMessageStartAction)
	return verb + "\x00" + object + "\x00" + grants.MessageStartBody(grants.MessageStartDisplaySlot) + "\x00" + definition.Disclosure
}

// messageStartPreviousWording is the consent sentence permits were granted
// under before the 2026-11-10 popup wording.
func messageStartPreviousWording() string {
	verb, object := grants.MessageStartPreviousWords(grants.MessageStartDisplaySlot)
	definition, _ := grants.PermissionDefinition(grants.ApplicationMessageStartAction)
	return verb + "\x00" + object + "\x00" + definition.Disclosure
}

// messageStartTarget is the receiver a permit names: its program, enrollment,
// registration and recipe kind and limits. It leaves out the recipe's saved
// command identity, so a vendor self-update that re-pins the command asks
// nothing again; every start still revalidates that command.
func messageStartTarget(target applicationMessageStartRequest) []any {
	return []any{target.Application, target.Program, target.ProgramKey, target.EnrollmentRevision, target.PermitRegistration, target.PermitRecipe, target.Session, target.Context}
}

// messageStartLegacyTarget is the receiver as permits named it before the
// saved command identity was left out.
func messageStartLegacyTarget(target applicationMessageStartRequest) []any {
	return []any{target.Application, target.Program, target.ProgramKey, target.EnrollmentRevision, target.RegistrationBinding, target.ProfileFingerprint, target.Session, target.Context}
}

func messageStartResource(source applications.CapturedAgentApplication, target applicationMessageStartRequest) string {
	return messageStartResourceWorded(source, target, messageStartWording())
}

func messageStartResourceWorded(source applications.CapturedAgentApplication, target applicationMessageStartRequest, wording string) string {
	raw, _ := json.Marshal(append(append([]any{source}, messageStartTarget(target)...), wording))
	return "app:" + target.Application + "/start:" + messageDigest(string(raw))
}

// messageStartLegacyResource is the resource of a permit granted before the
// sentence was bound. It is read only so that a person's "Never allow" under
// the earlier sentence stays in force; it never admits a start.
func messageStartLegacyResource(source applications.CapturedAgentApplication, target applicationMessageStartRequest) string {
	raw, _ := json.Marshal(append([]any{source}, messageStartLegacyTarget(target)...))
	return "app:" + target.Application + "/start:" + messageDigest(string(raw))
}

// messagePairedStartResource admits a paired computer's sender by its
// runtime's pinned subject and current pairing generation, never by the
// program name it claims (VISION 2026-10-31, C-69). A new pairing asks again.
func messagePairedStartResource(peerKey, generation string, target applicationMessageStartRequest) string {
	raw, _ := json.Marshal(append(append([]any{"paired", peerKey, generation}, messageStartTarget(target)...), messageStartWording()))
	return "app:" + target.Application + "/start:" + messageDigest(string(raw))
}

// messageStartConsent names who may start a receiver and under which
// resource. current re-proves that sender before dispatch.
type messageStartConsent struct {
	subject          rights.Subject
	resource, legacy string
	// previous is the resource under the previous sentence; like legacy it
	// is read only for a person's Never allow.
	previous string
	current  func(context.Context) error
}

func (h *applicationsHost) messageStartConsent(ctx context.Context, r *messageRecord, target applicationMessageStartRequest) (messageStartConsent, bool) {
	if p := r.Actor.Remote; p != nil {
		// The subject is the receiver-derived pinned key; Application and
		// ProgramLabel are the paired runtime's claims and decide nothing.
		if r.Actor.Program != remote.SubjectPrefix+p.PeerKey || !remote.ValidSubjectProgram(r.Actor.Program) {
			return messageStartConsent{}, false
		}
		current := func(checkCtx context.Context) error {
			return messageGeneration(checkCtx, h.messagePairing, p.PeerKey, p.Generation)
		}
		if current(ctx) != nil {
			return messageStartConsent{}, false
		}
		return messageStartConsent{subject: rights.Subject{Account: r.Actor.Account, Program: r.Actor.Program}, resource: messagePairedStartResource(p.PeerKey, p.Generation, target), current: current}, true
	}
	var sourceImage applicationAgentImage
	source := h.directory.currentAgentEnrollmentImage(ctx, r.Actor.Application, &sourceImage)
	if source.Enrollment == nil || source.Enrollment.State != applications.AgentEnrollmentStateEnrolled || !samePrograms(sourceImage.Program, r.Actor.Program) {
		return messageStartConsent{}, false
	}
	captured := capturedMessageAgent(*source.Enrollment, sourceImage)
	subject := rights.Subject{Account: r.Actor.Account, Program: r.Actor.ObservedProgram}
	if subject.Program == "" {
		subject.Program = r.Actor.Program
	}
	return messageStartConsent{subject: subject, resource: messageStartResource(captured, target), legacy: messageStartLegacyResource(captured, target),
		previous: messageStartResourceWorded(captured, target, messageStartPreviousWording()), current: func(checkCtx context.Context) error {
			var image applicationAgentImage
			native := h.directory.currentAgentEnrollmentImage(checkCtx, r.Actor.Application, &image)
			if native.Enrollment == nil || capturedMessageAgent(*native.Enrollment, image) != captured {
				return errors.New("messages: startup sender changed")
			}
			return nil
		}}, true
}

// messageStartDecision decides a start permit for consent. A start runs only
// when it is permitted under the current sentence. While it is not granted,
// earlierDenied reports a "Never allow" given under an earlier sentence, which
// stays in force: nobody is asked again. A permit given under an earlier
// sentence does not carry over and is asked again once.
func (h *applicationsHost) messageStartDecision(ctx context.Context, consent messageStartConsent) (decision rights.Decision, earlierDenied bool) {
	decision = h.messageRights.decide(ctx, consent.subject, grants.ApplicationMessageStartAction, consent.resource)
	if decision.Outcome != rights.DecisionOutcomeNotGranted {
		return decision, false
	}
	for _, earlier := range []string{consent.legacy, consent.previous} {
		if earlier != "" && h.messageRights.decide(ctx, consent.subject, grants.ApplicationMessageStartAction, earlier).Outcome == rights.DecisionOutcomeDenied {
			return decision, true
		}
	}
	return decision, false
}

// messageStartSubject is the subject a start permit is decided for, as
// messageStartConsent names it.
func messageStartSubject(actor messageActor) string {
	if actor.Remote == nil && actor.ObservedProgram != "" {
		return actor.ObservedProgram
	}
	return actor.Program
}

func (s *messageStore) currentMessageStartCharge(ctx context.Context, key string, r *messageRecord) error {
	raw, err := cas.ReadLimitContext(ctx, s.pairPath(r.Actor, r.Receiver), messageIndexLimit)
	if err != nil {
		return err
	}
	p, err := readMessagePair(raw)
	if err != nil {
		return err
	}
	scope, err := messageRecordBudgetScope(r)
	if err != nil {
		return err
	}
	if budget := p.peekBudget(scope, s.now().UnixMilli()); !budget.paused(s.now().UnixMilli()) {
		for _, charge := range p.Starts {
			if charge.Key == key && charge.Token == r.Token {
				return nil
			}
		}
	}
	return errors.New("messages: startup pair admission ended")
}

// Admission already charges the question. This second idempotent charge pays
// for its receiver start before any application dispatch can occur.
func (s *messageStore) chargeMessageStart(ctx context.Context, key string, r *messageRecord) (bool, error) {
	allowed := false
	now := s.now().UnixMilli()
	scope, err := messageRecordBudgetScope(r)
	if err != nil {
		return false, err
	}
	err = cas.ChangeLimitContext(ctx, s.pairPath(r.Actor, r.Receiver), messageIndexLimit, func(raw []byte) ([]byte, error) {
		p, err := readMessagePair(raw)
		if err != nil {
			return nil, err
		}
		for _, charge := range p.Starts {
			if charge.Key == key && charge.Token == r.Token {
				allowed = true
				return raw, nil
			}
		}
		if err = s.recoverPair(ctx, &p, now); err != nil {
			return nil, err
		}
		canonical := false
		for _, ref := range p.Pointers {
			canonical = canonical || ref.Key == key && ref.Token == r.Token && !ref.Pending && !ref.Alias
		}
		if !canonical {
			return nil, errors.New("messages: startup canonical charge unavailable")
		}
		budget := p.budget(scope, now)
		if budget == nil || budget.full(now) || len(p.Starts) >= 20 {
			return raw, nil
		}
		p.Starts = append(p.Starts, messageStartCharge{key, r.Token, now})
		budget.Count++
		if scope != "" {
			p.Count = min(p.Count+1, messageBudgetPerHour)
		}
		allowed = true
		return json.Marshal(p)
	})
	return allowed, err
}

func (h *applicationsHost) messageStartEligible(ctx context.Context, key string, r *messageRecord) error {
	current, err := h.messages.readRecord(ctx, key)
	if err != nil {
		return err
	}
	if current == nil || current.Token != r.Token || current.Alias || current.Control || current.Acked || current.ExpiresUnixMs <= h.messages.now().UnixMilli() {
		return errors.New("messages: startup acceptance ended")
	}
	request, err := decodeRetainedMessageRequest(current.Spec)
	if err != nil {
		return err
	}
	if current.Actor.Remote != nil && !messageCanonicalChatContext(request.Context) {
		return errors.New("messages: a paired sender wakes existing conversations only")
	}
	settings, err := h.readMessageStartSettings(ctx, strings.TrimPrefix(request.To, "app:"))
	if err != nil {
		return err
	}
	if !settings.permits(request.Kind) || request.Kind == msg.KindQuestion && request.AnswerByUnixMs <= h.messages.now().UnixMilli() {
		return errors.New("messages: startup requires pending question")
	}
	state, permitted, err := h.reconcileMessageState(ctx, key, current)
	if err != nil {
		return err
	}
	if !permitted || state.Held || state.Refused || messageTerminal(state.TaskState) {
		return errors.New("messages: startup question unavailable")
	}
	return ctx.Err()
}

func (h *applicationsHost) progressMessageStart(ctx context.Context, key string, r *messageRecord) (err error) {
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil {
		return err
	}
	// A paired sender may wake an existing conversation only; the recipe's own
	// headless start stays with native senders on this computer.
	if r.Actor.Remote != nil && !messageCanonicalChatContext(request.Context) || r.Alias || r.Control {
		return nil
	}
	if r.Start != nil && r.Start.State != "" {
		return h.flushMessageStartAudit(ctx, key, r)
	}
	canonical := messageCanonicalChatContext(request.Context)
	coldClaimed := false
	if canonical {
		// Canonical notices follow this decision, including a disabled recipe
		// or ineligible start. An unrelated live chat is no wake.
		defer func() {
			if !coldClaimed {
				err = errors.Join(err, h.progressMessageNotifications(ctx, key, r))
			}
		}()
	}
	application := strings.TrimPrefix(request.To, "app:")
	// Where starts are limited to existing chats, only a retained chat is
	// woken; the question to any other context waits for its inbox.
	if h.directory != nil && h.directory.messageStartChatsOnly && !canonical {
		return nil
	}
	// An attach context starts its own session even while other sessions of
	// the same agent run; only an open inbox for that session suppresses it.
	attach := request.Context
	if !canonical {
		attach = ""
	}
	reached := func() (bool, error) {
		if attach == "" {
			return h.receiverRunning(ctx, r.Receiver)
		}
		return h.directory.messageContextOpen(ctx, application, attach)
	}
	// A running receiver or an open session needs no start. The in-memory
	// presence check precedes the eligibility check, whose fresh consent read
	// verifies both agents' images, so a pass over many pending questions to a
	// running receiver performs none. A presence error waits for eligibility.
	if running, err := reached(); err == nil && running {
		return nil
	}
	if h.messageStartEligible(ctx, key, r) != nil {
		return nil
	}
	settings, err := h.readMessageStartSettings(ctx, application)
	if err != nil || !settings.permits(request.Kind) {
		return err
	}
	// Presence is read again after the eligibility read, as before.
	if running, err := reached(); err != nil || running {
		return err
	}
	target, err := h.directory.prepareMessageStart(ctx, application)
	if err != nil {
		return nil
	} // A missing approved profile leaves the queue intact.
	session, known := applicationAttachSession{}, attach == ""
	if canonical {
		if target.Descriptor.Activation == nil {
			return nil
		}
		profile, err := startprofile.Parse(target.Descriptor.Activation.Arguments)
		if err != nil {
			return nil
		}
		target.Cold, err = h.messages.coldResumeForChat(ctx, h.directory.owner, r.Receiver, attach)
		if err != nil {
			return nil
		}
		folder, startable := messageChatStart(profile.Name, target.Cold, h.directory.appsRunning(ctx))
		if !startable {
			// A closed Codex app, a running Claude, or a chat without the
			// facts its recipe needs: the question waits for the person.
			return nil
		}
		session, known = applicationAttachSession{Context: attach, Folder: folder}, true
	}
	if !known || attach == "" && target.Context != request.Context || !samePrograms(target.Program, r.Receiver) {
		return nil
	}
	if target.Validate(ctx) != nil {
		return nil
	}
	if target.Cold != nil && h.messages.checkColdResume(ctx, h.directory.owner, r.Receiver, target.Cold) != nil {
		return nil
	}
	consent, admitted := h.messageStartConsent(ctx, r, target)
	if !admitted {
		return nil
	}
	resource, subject := consent.resource, consent.subject
	if !messageChainWakes(r) {
		// A turn this far down a chain of woken turns wakes nobody; Activity
		// shows the refusal with the chain, and the question waits for an
		// open inbox. No person is asked for a start that would not run.
		attempt := &messageStartAttempt{Resource: resource, Attempt: messageDigest(key + "\x00" + r.Token + "\x00receiver-start"), At: h.messages.now().UnixMilli(), Folder: session.Folder, State: messageStartHopLimit}
		if err := h.messages.updateMessageNotice(ctx, key, r.Token, func(current *messageRecord) {
			if current.Start == nil {
				current.Start = attempt
			}
		}); err != nil {
			return err
		}
		current, err := h.messages.readRecord(ctx, key)
		if err != nil || current == nil {
			return err
		}
		return h.flushMessageStartAudit(ctx, key, current)
	}
	decision, earlierDenied := h.messageStartDecision(ctx, consent)
	if decision.Outcome == rights.DecisionOutcomeNotGranted {
		if earlierDenied {
			return nil
		}
		if r.Actor.Remote != nil {
			// Attach from a paired computer stays off until the person here
			// allows it, in a question naming that computer.
			if _, err := h.pairedPermissionQuestion(subject, grants.ApplicationMessageStartAction, resource, r.Actor.Remote.PeerKey, r.Actor.Remote.PeerLabel, r.Actor.Remote.ProgramLabel, nil); err != nil && h.report != nil {
				h.report(err)
			}
			return nil
		}
		if h.messageRights.firstUse != nil {
			h.messageRights.firstUse(subject, func() asks.Publisher { return asks.Publisher{} }, grants.ApplicationMessageStartAction, resource)
		}
		return nil
	}
	if decision.Outcome != rights.DecisionOutcomePermitted {
		return nil
	}
	attempt := &messageStartAttempt{Resource: resource, Attempt: messageDigest(key + "\x00" + r.Token + "\x00receiver-start"), At: h.messages.now().UnixMilli(), Folder: session.Folder}
	if err = h.messages.updateMessageNotice(ctx, key, r.Token, func(current *messageRecord) {
		if current.Start == nil {
			current.Start = attempt
		}
	}); err != nil {
		return err
	}
	current, err := h.messages.readRecord(ctx, key)
	if err != nil || current == nil || current.Start == nil {
		return err
	}
	if current.Start.Resource != resource || current.Start.State != "" {
		return nil
	}
	charged, err := h.messages.chargeMessageStart(ctx, key, current)
	if err != nil || !charged {
		return err
	}
	// Retain uncertainty before handing an effect to Applications. A competing
	// progress worker or a restarted process observes this same canonical claim.
	claimed := false
	if err = h.messages.updateMessageNotice(ctx, key, r.Token, func(record *messageRecord) {
		if record.Start != nil && record.Start.Attempt == current.Start.Attempt && record.Start.State == "" {
			record.Start.State = "unknown"
			claimed = true
		}
	}); err != nil || !claimed {
		return err
	}
	coldClaimed = true
	// From the claim on, the start is a runtime-owned task under its own bound
	// (architect reviews 41 and 43). The claim already excludes a second start,
	// so the claiming pass returns at once: the host's ticker keeps handling
	// expiry, notifications and other starts, and a Wait or listing call is
	// never held for the receiver's readiness. Host shutdown ends the task.
	settle, settled := messageStartSettle(h.ctx, ctx, target)
	h.messageStarts.begin()
	go func() {
		defer h.messageStarts.end()
		defer settled()
		if err := func(ctx context.Context) error {
			current := current
			var err error
			claimedRecord, err := h.messages.readRecord(ctx, key)
			if err != nil || claimedRecord == nil {
				return err
			}
			if err = h.flushMessageStartAudit(ctx, key, claimedRecord); err != nil {
				return err
			}
			profileCheck := target.Validate
			// The consent resource above names the recipe, never the session, so one
			// permit per direction covers every session of the agent.
			target.Kind, target.Attempt, target.Message, target.Target = request.Kind.String(), current.Start.Attempt, key+":"+r.Token, attach
			target.PerHour = settings.PerHour
			if target.Cold != nil {
				if _, err := target.Cold.Thread(); err == nil {
					// A Codex chat's pointer carries a ticket reserved under
					// this message's wake key (architect ruling 1, 2026-11-22).
					if target.Wake, err = h.messageCodexWakeKey(ctx, key, current, request); err != nil {
						return err
					}
				}
			}
			target.Origin = messageWakeChain(current)
			target.Validate = func(checkCtx context.Context) error {
				fresh, err := h.messages.readRecord(checkCtx, key)
				if err != nil {
					return err
				}
				if fresh == nil || fresh.Token != r.Token || fresh.Start == nil || fresh.Start.Attempt != target.Attempt || fresh.Start.Resource != resource || (fresh.Start.State != "unknown" && fresh.Start.State != "ready") {
					return errors.New("messages: canonical startup attempt changed")
				}
				if err := profileCheck(checkCtx); err != nil {
					return err
				}
				if target.Cold != nil {
					if err := h.messages.checkColdResume(checkCtx, h.directory.owner, r.Receiver, target.Cold); err != nil {
						return err
					}
				}
				if err := h.messageStartEligible(checkCtx, key, current); err != nil {
					return err
				}
				policy, err := h.readMessageStartSettings(checkCtx, application)
				if err != nil || policy.PerHour < target.PerHour {
					return errors.New("messages: startup settings changed")
				}
				if err := h.messages.currentMessageStartCharge(checkCtx, key, current); err != nil {
					return err
				}
				if err := consent.current(checkCtx); err != nil {
					return err
				}
				if h.messageRights.decide(checkCtx, subject, grants.ApplicationMessageStartAction, resource).Outcome != rights.DecisionOutcomePermitted {
					return errors.New("messages: startup consent ended")
				}
				return nil
			}
			target.Continue = func(checkCtx context.Context) error {
				// A read or rights outage is no stop; only the person's decisions are.
				if fresh, err := h.messages.readRecord(checkCtx, key); err == nil && fresh != nil && fresh.Start != nil && fresh.Start.Attempt == target.Attempt && fresh.Start.State == messageStartStopped {
					return errors.New("messages: woken turn stopped by the person")
				}
				switch h.messageRights.decide(checkCtx, subject, grants.ApplicationMessageStartAction, resource).Outcome {
				case rights.DecisionOutcomeNotGranted, rights.DecisionOutcomeDenied:
					return errors.New("messages: start permit withdrawn")
				}
				return nil
			}
			// The launcher reports delivery when the turn ends, after this progress
			// pass may have returned; the record write needs no caller deadline.
			delivered := context.WithoutCancel(ctx)
			target.Delivered = func(mode string) {
				if !validMessageStartDelivery(mode) {
					return
				}
				recorded := false
				if err := h.messages.updateMessageNotice(delivered, key, r.Token, func(record *messageRecord) {
					if record.Start != nil && record.Start.Attempt == target.Attempt && record.Start.Delivery == "" {
						record.Start.Delivery, record.Start.AuditDone = mode, false
						recorded = true
					}
				}); err != nil || !recorded {
					return
				}
				if fresh, err := h.messages.readRecord(delivered, key); err == nil && fresh != nil {
					if err := h.flushMessageStartAudit(delivered, key, fresh); err != nil && h.report != nil {
						h.report(err)
					}
				}
			}
			result := h.directory.startMessageReceiver(ctx, target)
			if err = h.messages.updateMessageNotice(ctx, key, r.Token, func(record *messageRecord) {
				if record.Start != nil && record.Start.Attempt == target.Attempt && record.Start.State == "unknown" {
					record.Start.State = result.State
					record.Start.AuditDone = false
				}
			}); err != nil {
				return err
			}
			current, err = h.messages.readRecord(ctx, key)
			if err != nil || current == nil {
				return err
			}
			return h.flushMessageStartAudit(ctx, key, current)
		}(settle); err != nil && h.report != nil {
			h.report(err)
		}
	}()
	return nil
}

// messageStartTasks counts claimed starts running as their own tasks. Unlike
// a WaitGroup, a new task may begin while a caller waits for idle.
type messageStartTasks struct {
	mu      sync.Mutex
	running int
	idle    *sync.Cond
}

func (t *messageStartTasks) begin() {
	t.mu.Lock()
	t.running++
	t.mu.Unlock()
}

func (t *messageStartTasks) end() {
	t.mu.Lock()
	t.running--
	if t.idle != nil {
		t.idle.Broadcast()
	}
	t.mu.Unlock()
}

// wait returns once no claimed start task runs.
func (t *messageStartTasks) wait() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.idle == nil {
		t.idle = sync.NewCond(&t.mu)
	}
	for t.running > 0 {
		t.idle.Wait()
	}
}

// messageStartAdmissionBound covers a claimed start's admission rechecks:
// enrollment images, consent, registration and the hourly reservation.
const messageStartAdmissionBound = 10 * time.Second

// messageStartSettle is the context a claimed start runs and records its
// outcome under: the receiver's readiness timeout plus the admission bound,
// independent of the claiming pass's deadline, and ended by the host's
// shutdown.
func messageStartSettle(lifetime, caller context.Context, target applicationMessageStartRequest) (context.Context, context.CancelFunc) {
	bound := messageStartAdmissionBound
	if activation := target.Descriptor.Activation; activation != nil && activation.ReadinessTimeoutMs > 0 {
		bound += time.Duration(activation.ReadinessTimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(caller), bound)
	if lifetime == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(lifetime, cancel)
	return ctx, func() { stop(); cancel() }
}

func (h *applicationsHost) flushMessageStartAudit(ctx context.Context, key string, r *messageRecord) error {
	if r.Start == nil || r.Start.State == "" || r.Start.AuditDone || r.Audit == nil {
		return nil
	}
	h.messageAuditMu.Lock()
	defer h.messageAuditMu.Unlock()
	current, err := h.messages.readRecord(ctx, key)
	if err != nil {
		return err
	}
	if current == nil || current.Token != r.Token || current.Start == nil || current.Start.State == "" || current.Start.AuditDone {
		return nil
	}
	r = current
	if h.messageAudit == nil {
		return errors.New("messages: startup activity unavailable")
	}
	entry := *r.Audit
	entry.ID += ":start"
	entry.Outcome, entry.UpdatedAt = "receiver_start_"+r.Start.State, r.Start.At
	// The row names the woken conversation by its folder label and carries
	// the exact start permit, so the person can revoke this direction from it
	// through the Panel's existing rights edit.
	entry.ToFolder, entry.Delivery = r.Start.Folder, r.Start.Delivery
	entry.PermitProgram, entry.PermitAction, entry.PermitResource = messageStartSubject(r.Actor), grants.ApplicationMessageStartAction, r.Start.Resource
	if err := h.messageAudit.append(&entry); err != nil {
		return err
	}
	return h.messages.updateMessageNotice(ctx, key, r.Token, func(current *messageRecord) {
		if current.Start != nil && current.Start.State == r.Start.State {
			current.Start.AuditDone = true
		}
	})
}
