package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	waitwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/responderwait"
	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	jobwire "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type nativeMessages interface {
	Prepare(string, string, wire.PrepareRequest) (wire.PrepareResult, error)
	Reply(string, string, string, string) (wire.SendResult, error)
	InboxOpen(string, string, string) (wire.InboxChange, error)
	InboxOpenChat(string, string, string, wire.ChatBinding) (wire.ChatInboxChange, error)
	InboxesList(wire.InboxQuery) (wire.InboxPage, error)
	InboxesForPerson(string) (wire.InboxPage, error)
	Send(string, string, wire.SendRequest) (wire.SendResult, error)
	SendChat(string, string, wire.ChatSendRequest) (wire.SendResult, error)
	Wait(string, string, wire.View, string, string, int64) (wire.Page, error)
	TurnWake(string, string, string) (wire.TurnWakeResult, error)
}
type messagesClientFactory func(context.Context) (nativeMessages, error)

type messagePermissionClient interface {
	RequestContext(context.Context, askswire.PermissionIntent) (askswire.PermissionObservation, error)
	ObserveContext(context.Context, string, int64) (askswire.PermissionObservation, error)
}

func (b *messagesBackend) ModelsConnect(ctx context.Context, in gateway.ModelsConnectInput) (gateway.ModelsConnectOutput, error) {
	owner, ok := b.Backend.(gateway.ModelsConnectBackend)
	if !ok {
		return gateway.ModelsConnectOutput{Outcome: "unavailable", Reason: "provider_setup_unsupported"}, nil
	}
	return owner.ModelsConnect(ctx, in)
}

func (b *messagesBackend) JobsFollowWork(ctx context.Context, id jobwire.RequestIdentity, after string, waitMS int64) (jobwire.WorkObservation, error) {
	owner, ok := b.Backend.(interface {
		JobsFollowWork(context.Context, jobwire.RequestIdentity, string, int64) (jobwire.WorkObservation, error)
	})
	if !ok {
		return jobwire.WorkObservation{Outcome: jobwire.WorkObservationOutcomeUnsupported, Mechanism: jobwire.WorkObservationMechanismPolled}, nil
	}
	return owner.JobsFollowWork(ctx, id, after, waitMS)
}

// GenerateImage makes the picture through the same native backend, under
// this agent connection's admitted identity.
func (b *messagesBackend) GenerateImage(ctx context.Context, in gateway.ImageInput) (gateway.ImageOutput, error) {
	owner, ok := b.Backend.(gateway.ImageBackend)
	if !ok {
		return gateway.ImageOutput{}, errors.New("gateway: pictures unavailable on this connection")
	}
	return owner.GenerateImage(ctx, in)
}

// ServesPictures forwards the quiet session-start read of the picture policy.
// A backend that cannot say lists the picture tool.
func (b *messagesBackend) ServesPictures(ctx context.Context) (bool, error) {
	owner, ok := b.Backend.(interface {
		ServesPictures(context.Context) (bool, error)
	})
	if !ok {
		return true, nil
	}
	return owner.ServesPictures(ctx)
}

// VideoModel forwards the quiet read of the person's video model, so
// oa_video_generate without model reaches it through this connection (R35:
// without it every call answered "Name a video model").
func (b *messagesBackend) VideoModel(ctx context.Context) (string, string, error) {
	owner, ok := b.Backend.(interface {
		VideoModel(context.Context) (string, string, error)
	})
	if !ok {
		return "", "", errors.New("gateway: this connection reads no video model; name one with model")
	}
	return owner.VideoModel(ctx)
}

// One live association shares this owner between explicit status changes and
// admission renewal. It holds no durable state or identity authority.
type messagesInboxStatus struct {
	routingMu          sync.Mutex
	routingBound       bool
	routingChanged     chan struct{}
	chatReceiver       bool
	chatSource         bool
	chatID             string
	chatSourceInstance func() string
	chatSourceProof    func(context.Context) (nativeMessages, func(), wire.Outcome, error)
	chatSourceContext  string
	chatTitle          string
	chatLaunch         *wire.ChatLaunch
	mu                 sync.Mutex
	confirmed          string
	unknown            bool
	renewalUnknown     bool
	pending            string
	pendingInstance    string
	watchHealth        string
	wakeBusy           bool
	// wakeUnconfirmed is the Codex receiver's own claim, wake_busy or
	// queue_unknown, that a queued wake has gone unconfirmed.
	wakeUnconfirmed string
	renewalPending  string
	admission       *agentToolAdmission

	// instance is the current presence instance. Re-admission in place keeps
	// the context and replaces the instance; every call re-derives it here.
	instanceMu sync.Mutex
	instance   string
}

// The named source interface opts into separate receiving advertisement. It
// retains ordinary native source ownership and grants no authority.
func (s *messagesInboxStatus) receivingPresence() ([]facade.ApplicationInterface, <-chan struct{}) {
	if s == nil {
		return nil, nil
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	if s.routingChanged == nil {
		s.routingChanged = make(chan struct{}, 1)
	}
	return nil, s.routingChanged
}

func (s *messagesInboxStatus) receivingBinding(bound bool) {
	if s == nil {
		return
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	if s.routingChanged == nil {
		s.routingChanged = make(chan struct{}, 1)
	}
	if s.routingBound == bound {
		return
	}
	s.routingBound = bound
	select {
	case s.routingChanged <- struct{}{}:
	default:
	}
}

func (s *messagesInboxStatus) setInstance(instance string) {
	if s == nil {
		return
	}
	s.instanceMu.Lock()
	defer s.instanceMu.Unlock()
	s.instance = instance
}

func (s *messagesInboxStatus) currentInstance() string {
	if s == nil {
		return ""
	}
	s.instanceMu.Lock()
	defer s.instanceMu.Unlock()
	return s.instance
}

// inboxInstance is the connection's current presence instance.
func (b *messagesBackend) inboxInstance() string {
	if current := b.status.currentInstance(); current != "" {
		return current
	}
	return b.instance
}

var errMessagesInboxStatusUnknown = errors.New("messages: inbox status is unknown; explicit Open reconciliation required")

func (s *messagesInboxStatus) open(c nativeMessages, instance, contextName string, requested *string) (wire.InboxChange, error) {
	return s.openContext(context.Background(), c, instance, contextName, requested)
}

func (s *messagesInboxStatus) recordChatOpenOutcome(outcome wire.Outcome) {
	if s != nil && outcome == wire.OutcomeEnded {
		s.admission.refuse(&agentInboxOutcomeError{outcome: outcome}, true)
	}
}

func (s *messagesInboxStatus) openContext(ctx context.Context, c nativeMessages, instance, contextName string, requested *string) (wire.InboxChange, error) {
	if s == nil {
		value := ""
		if requested != nil {
			value = *requested
		}
		return c.InboxOpen(instance, contextName, value)
	}
	if s.chatReceiver && s.chatSourceProof != nil {
		sourceClient, finish, outcome, err := s.chatSourceProof(ctx)
		defer finish()
		if err != nil || outcome != wire.OutcomeOpened {
			if err == nil {
				s.recordChatOpenOutcome(outcome)
			}
			return wire.InboxChange{Outcome: outcome}, err
		}
		c = sourceClient
	}
	for !s.mu.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return wire.InboxChange{}, ctx.Err()
		case <-timer.C:
		}
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return wire.InboxChange{}, err
	}
	// A lost explicit Open blocks changes to its own lease until reconciled.
	// A fresh Announce can reopen the chat with its last confirmed status.
	if s.unknown && !s.renewalUnknown && s.pendingInstance == instance && (requested == nil || *requested != s.pending) {
		return wire.InboxChange{Outcome: wire.OutcomeUnknown}, errMessagesInboxStatusUnknown
	}
	base := s.confirmed
	if requested != nil {
		base = *requested
	}
	value := messagesWatchHealthStatus(base, s.watchHealth)
	// Join every suffix first and truncate the base once, so no later
	// suffix cuts an earlier one away.
	var suffixes []string
	if s.wakeUnconfirmed != "" {
		suffixes = append(suffixes, messagesWakeUnconfirmedText)
	}
	if s.wakeBusy {
		suffixes = append(suffixes, "receiver:busy")
	}
	if len(suffixes) > 0 {
		value = messagesStatusSuffix(value, strings.Join(suffixes, " | "))
	}
	if requested == nil && s.unknown && s.renewalUnknown {
		value = s.renewalPending
	}
	var out wire.InboxChange
	var err error
	s.routingMu.Lock()
	typed := s.chatSource
	claim := wire.ChatBinding{ChatID: s.chatID, ChatTitle: s.chatTitle}
	if s.chatLaunch != nil {
		copy := *s.chatLaunch
		claim.Launch = &copy
	}
	sourceContext := contextName
	if s.chatReceiver {
		sourceContext = s.chatSourceContext
	}
	if s.chatSourceInstance != nil {
		claim.SourceInstance = s.chatSourceInstance()
	}
	s.routingMu.Unlock()
	if typed {
		if client, ok := c.(interface {
			InboxOpenChat(string, string, string, wire.ChatBinding) (wire.ChatInboxChange, error)
		}); ok {
			var opened wire.ChatInboxChange
			opened, err = client.InboxOpenChat(instance, sourceContext, value, claim)
			err = establishedChatError(err)
			var terminal *agentPresenceTerminalError
			if errors.As(err, &terminal) {
				s.admission.refuse(err, true)
			}
			if err == nil && (opened.Outcome == wire.OutcomeOpened || opened.Outcome == wire.OutcomePage) {
				expectedContext := ""
				if s.chatReceiver {
					expectedContext = contextName
				}
				if opened.Context != expectedContext {
					err := &agentPresenceTerminalError{cause: errors.New("native chat context changed; reopen the connection")}
					s.admission.refuse(err, true)
					return wire.InboxChange{Outcome: wire.OutcomeUnavailable}, err
				}
			}
			out = wire.InboxChange{Outcome: opened.Outcome, Revision: opened.Revision, Holder: opened.Holder, OpenedUnixMs: opened.OpenedUnixMs}
			if err == nil {
				s.recordChatOpenOutcome(opened.Outcome)
			}
		} else {
			return wire.InboxChange{}, errors.New("typed chat inbox unavailable; update integration")
		}
	} else {
		out, err = c.InboxOpen(instance, contextName, value)
	}
	if err != nil || out.Outcome == wire.OutcomeUnknown {
		s.unknown = true
		s.renewalUnknown = requested == nil
		if requested == nil {
			s.renewalPending = value
		}
		if requested != nil {
			s.pending = *requested
			s.pendingInstance = instance
		}
	}
	if err == nil && out.Outcome == wire.OutcomeOpened {
		s.confirmed = base
		s.unknown = false
		s.renewalUnknown = false
		s.pending = ""
		s.pendingInstance = ""
		s.renewalPending = ""
	}
	return out, err
}

type messagesBackend struct {
	claudeTitleRefresh time.Duration
	gateway.Backend
	application, instance, contextName string
	native                             messagesClientFactory
	status                             *messagesInboxStatus
	requestAccess                      func(context.Context, askswire.PermissionIntent) (askswire.PermissionObservation, error)
	permission                         func(context.Context) (messagePermissionClient, error)
	checkAccess                        func(context.Context, string) (askswire.PermissionObservation, error)
	responder                          func(context.Context) (gateway.AsksResponder, error)
	waitingAsks                        func(context.Context) (waitwire.WaitingPage, error)
	asksAccess                         func(context.Context) (string, error)
	chatMu                             sync.Mutex
	chatContext                        string
	chatID                             string
	chatLaunch                         *wire.ChatLaunch
	chatReceiver                       *messagesBackend
	requireChat                        bool
	chatPresence                       func(context.Context, string, *messagesInboxStatus, func(string), bool) error
	wakeConfirm                        func(context.Context, string) (string, bool, error)
	wakeReserve                        func(context.Context, string, string) (string, error)
	wakePointer                        func(context.Context, string, string) (string, error)
	// wakeHeld reports a Codex pointer queued to the host and not yet read
	// with its ticket (S5 ruling, 2026-11-22).
	wakeHeld func(context.Context) (bool, error)
	// wakeIssued reports a Codex ticket the wake ledger handed to the host
	// for this chat's thread (security review 2026-11-22).
	wakeIssued func(context.Context, string) (bool, error)
	// adapter holds the host adapter's key; a chat receiver shares its
	// launcher's (messages_adapter.go).
	adapter *messagesAdapter
	// asksWake keeps this connection's answerable Asks wake key (ASK-R14).
	asksObservation sync.Mutex
	asksWake        asksWakeState
	asksWaitingWake asksWakeState
	// replyOnly marks a turn another agent's message started (S5 ruling,
	// 2026-11-22); a chat receiver shares its launcher's (messages_reply_only.go).
	replyOnly *replyOnlyTurn
	// decideFullTools decides this program's own full-tools rule on a sender.
	decideFullTools func(context.Context, string) (rightswire.Decision, error)
}

var _ gateway.AgentBackend = (*messagesBackend)(nil)

func (b *messagesBackend) MessagesContext() string {
	b.chatMu.Lock()
	defer b.chatMu.Unlock()
	if b.requireChat && b.chatContext != "" {
		return b.chatContext
	}
	return b.contextName
}

// Called only by the dedicated launcher after native presence and InboxOpen
// both succeeded. The caller cancels and joins this server before withdrawing.
// These retained labels select the owned association; they grant no authority.
func newMessagesBackend(base gateway.Backend, machine *client.Machine, application, instance, contextName string) (gateway.Backend, error) {
	return newMessagesBackendWithStatus(base, machine, application, instance, contextName, nil)
}

func newMessagesBackendWithStatus(base gateway.Backend, machine *client.Machine, application, instance, contextName string, status *messagesInboxStatus) (gateway.Backend, error) {
	if machine == nil {
		return nil, errors.New("messages: native machine required")
	}
	supervised := status != nil && status.admission != nil
	b, err := bindMessagesBackendStatus(base, application, instance, contextName, nativeMessagesFactory(machine), supervised)
	if err == nil && status != nil {
		b.(*messagesBackend).status = status
	}
	if err == nil {
		owned := b.(*messagesBackend)
		owned.chatPresence = func(ctx context.Context, contextName string, status *messagesInboxStatus, ready func(string), readmit bool) error {
			return runNativeAgentPresenceAttempt(ctx, machine, application, contextName, ready, status, readmit)
		}
		owned.asksAccess = asksWatchAccess(machine)
		owned.waitingAsks = nativeWaitingAsks(machine)
		owned.responder = func(ctx context.Context) (gateway.AsksResponder, error) {
			return machine.ResolveAsksResponder(ctx, client.Requirements{Scope: client.ScopeLocal})
		}
		owned.permission = func(ctx context.Context) (messagePermissionClient, error) {
			return machine.ResolveAsksPermission(ctx, client.Requirements{Scope: client.ScopeLocal})
		}
		owned.decideFullTools = func(ctx context.Context, resource string) (rightswire.Decision, error) {
			rights, err := machine.ResolveRights(ctx, client.Requirements{Scope: client.ScopeLocal})
			if err != nil {
				return rightswire.Decision{}, err
			}
			return rights.DecideContext(ctx, grants.ApplicationMessageFullToolsAction, resource)
		}
		owned.checkAccess = func(ctx context.Context, receiver string) (askswire.PermissionObservation, error) {
			return owned.checkMessageAccess(ctx, receiver, owned.resolveMessageReceiver, func(ctx context.Context, resource string) (rightswire.Decision, error) {
				rights, err := machine.ResolveRights(ctx, client.Requirements{Scope: client.ScopeLocal})
				if err != nil {
					return rightswire.Decision{}, err
				}
				return rights.DecideContext(ctx, grants.ApplicationMessageAction, resource)
			})
		}
	}
	return b, err
}

func (b *messagesBackend) AsksResponder(ctx context.Context) (gateway.AsksResponder, error) {
	if b.responder == nil {
		return nil, errors.New("asks: native responder unavailable")
	}
	return b.responder(ctx)
}

func (b *messagesBackend) AwaitPermissionQuestions(ctx context.Context, ids []string) (string, error) {
	call, done, admitted := b.BeginNativeTool(ctx, "oa_asks_watch")
	defer done()
	if !admitted {
		return "waiting", nil
	}
	if waiter, ok := b.Backend.(gateway.PermissionQuestionBackend); ok {
		return waiter.AwaitPermissionQuestions(call, ids)
	}
	return "waiting", nil
}

func (b *messagesBackend) checkMessageAccess(ctx context.Context, receiver string,
	resolve func(context.Context, string, string) (string, string, error),
	decide func(context.Context, string) (rightswire.Decision, error),
) (askswire.PermissionObservation, error) {
	resolved, _, err := resolve(ctx, receiver, "")
	if ctx.Err() != nil {
		return askswire.PermissionObservation{}, ctx.Err()
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return askswire.PermissionObservation{}, err
	case errors.Is(err, errMessageReceiverNotListed):
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending}, nil
	case err != nil:
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}, nil
	}
	decision, err := decide(ctx, "app:"+resolved)
	if err != nil {
		return askswire.PermissionObservation{}, err
	}
	return b.confirmMessageAccess(ctx, resolved, decision)
}

func (b *messagesBackend) confirmMessageAccess(ctx context.Context, receiver string, decision rightswire.Decision) (askswire.PermissionObservation, error) {
	switch decision.Outcome {
	case rightswire.DecisionOutcomeDenied:
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeDenied, PolicyRevision: decision.PolicyRevision}, nil
	case rightswire.DecisionOutcomeNotGranted:
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending}, nil
	case rightswire.DecisionOutcomePermitted:
		if decision.PolicyRevision == "" {
			return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}, nil
		}
		// The enforcing inbox owner rechecks both native parties and current
		// permission. A rule or an Allow answer alone proves no live receiver.
		c, err := b.native(ctx)
		if err != nil {
			return askswire.PermissionObservation{}, err
		}
		cursor := ""
		for {
			page, err := c.InboxesList(wire.InboxQuery{Cursor: cursor})
			if err != nil {
				return askswire.PermissionObservation{}, err
			}
			if page.Outcome == wire.OutcomeGap {
				// The listing changed between the runtime's two reads, as the
				// rule just saved changes it (journey J-MSG, 2026-11-10: the
				// first listing after the Allow answered gap). Pending without
				// a revision: the caller lists again within its bound.
				return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending}, nil
			}
			if page.Outcome != wire.OutcomePage {
				return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}, nil
			}
			// Inbox row presence proves a permitted receiver; needs_permission grants no access.
			// The caller's own row proves nothing: for a same-app direction
			// it has the receiver's program.
			for _, inbox := range page.Inboxes {
				if inbox.Program == "app:"+receiver && (inbox.Mine == nil || !*inbox.Mine) {
					return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePermitted, PolicyRevision: decision.PolicyRevision}, nil
				}
			}
			// A counted chat proves a permitted receiver as a listed row does.
			for _, other := range page.OtherChats {
				if other.Program == "app:"+receiver {
					return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePermitted, PolicyRevision: decision.PolicyRevision}, nil
				}
			}
			if page.Cursor == "" {
				break
			}
			cursor = page.Cursor
		}
		// Saved but the receiver has no live inbox now: pending with the
		// policy revision that witnesses the saved rule.
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending, PolicyRevision: decision.PolicyRevision}, nil
	default:
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}, nil
	}
}

// messageAccessWait bounds how long one call waits for the person's answer.
const messageAccessWait = 4 * time.Second

// messageAccessConfirm bounds how long one call waits, after it observes the
// person's Allow, for the runtime to save that rule and for the confirmation
// to read it. The answer and its rule are separate commits; under load the
// rule lands up to a second after the answer (journey J-MSG, 2026-11-10: an
// Allow near the end of the person's wait read "not saved yet").
var messageAccessConfirm = 10 * time.Second

// messageAccessRenewals bounds fresh questions after changed parties within one call.
const messageAccessRenewals = 3

// MessagesRequestAccess never reports a recorded Allow as an answer: only a
// saved and confirmed permission reads permitted. An Allow whose rule is not
// saved reads unavailable; the runtime releases it, and the same receiver and
// key ask the person afresh.
func (b *messagesBackend) MessagesRequestAccess(ctx context.Context, in gateway.MessagesAccessInput) (askswire.PermissionObservation, error) {
	out, _, err := b.MessagesRequestAccessReport(ctx, in)
	return out, err
}

// MessagesRequestAccessReport also returns the sentence for the person when
// the permission is saved but the receiver's chat is not open: the rule is in
// force and the receiver reads its messages when its chat is next open.
func (b *messagesBackend) MessagesRequestAccessReport(ctx context.Context, in gateway.MessagesAccessInput) (askswire.PermissionObservation, string, error) {
	saved := ""
	out, err := b.requestMessageAccess(ctx, in, &saved)
	if err != nil || out.Outcome == askswire.PermissionOutcomePermitted || out.Outcome == askswire.PermissionOutcomeDenied {
		return out, "", err
	}
	if saved != "" {
		receiver := gateway.MessageAgentTitle(gateway.MessageReceiverName(in.Receiver))
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePermitted, PolicyRevision: saved},
			gateway.MessageAgentTitle(b.application) + " can send messages to " + receiver + ". " + receiver + " reads them when its chat is open.", nil
	}
	if out.Outcome == askswire.PermissionOutcomeAnswered && out.Answer != nil && out.Answer.Option == "allow" {
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}, "", nil
	}
	return out, "", nil
}

func (b *messagesBackend) requestMessageAccess(ctx context.Context, in gateway.MessagesAccessInput, saved *string) (askswire.PermissionObservation, error) {
	receiver := gateway.MessageReceiverName(in.Receiver)
	in.Receiver = strings.TrimPrefix(receiver, "app:")
	confirmationReceiver := in.Receiver
	if strings.HasPrefix(receiver, "app:") {
		confirmationReceiver = receiver
	}
	unchecked, err := json.Marshal(map[string]string{"name": in.Receiver})
	if _, invalid := grants.DecodeNamedApplicationTarget(unchecked); err != nil || invalid != nil || !asks.ValidRequestKey(in.IdempotencyKey) {
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeInvalid}, nil
	}
	raw := applications.EncodeNamedApplicationTarget(&applications.NamedApplicationTarget{Name: in.Receiver})
	if b.requestAccess == nil && b.permission == nil {
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}, nil
	}
	key := sha256.Sum256([]byte(b.contextName + "\x00" + in.IdempotencyKey))
	selection := rightswire.PermissionSelection{Permission: rightswire.PermissionKey{Action: grants.ApplicationMessageAction, ResourceProfile: grants.AgentMessageResourceProfile}, Selector: rightswire.ResourceSelector{Kind: rightswire.ResourceSelectorKindOne, Target: raw}}
	intent := askswire.PermissionIntent{Selection: &selection, RequestKey: "message-access:" + hex.EncodeToString(key[:20])}
	if b.permission == nil {
		return b.requestAccess(ctx, intent)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	permission, err := b.permission(ctx)
	if err != nil {
		return askswire.PermissionObservation{}, err
	}
	out, err := permission.RequestContext(ctx, intent) // Admit exactly once.
	// A conflict means this key's recorded question names parties that have
	// since changed, as after an OA connector update; that answer decides
	// nothing about the current parties. Continue under a key derived from
	// the same idempotency_key, so the person is asked afresh and a repeated
	// call reconciles with that same question.
	for renewal := 1; err == nil && out.Outcome == askswire.PermissionOutcomeConflict && renewal <= messageAccessRenewals; renewal++ {
		renewed := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00renewal-%d", b.contextName, in.IdempotencyKey, renewal)))
		intent.RequestKey = "message-access:" + hex.EncodeToString(renewed[:20])
		out, err = permission.RequestContext(ctx, intent)
	}
	if err != nil {
		return out, err
	}
	// Bound observation after the one Request returns. Ending this wait leaves
	// the admitted question and the original request key available to the caller.
	outer := ctx
	ctx, stopWaiting := context.WithTimeout(outer, messageAccessWait)
	defer func() { stopWaiting() }()
	confirming := false
	// Once the person has allowed (or the rule is already saved), the
	// confirmation has its own bound: the person's wait no longer applies.
	confirm := func() {
		if !confirming {
			stopWaiting()
			ctx, stopWaiting = context.WithTimeout(outer, messageAccessConfirm)
		}
		confirming = true
	}
	for {
		if out.Outcome == askswire.PermissionOutcomePermitted {
			// A policy witness alone proves no live receiver. If confirmation
			// cannot finish, there is no pending question to report here.
			out = askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}
			confirm()
		}
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return out, nil
			}
			return out, ctx.Err()
		}
		if confirming {
			if b.checkAccess == nil {
				return out, nil
			}
			current, err := b.checkAccess(ctx, confirmationReceiver)
			if ctx.Err() != nil {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return out, nil
				}
				return out, ctx.Err()
			}
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					return out, nil
				}
				return askswire.PermissionObservation{}, err
			}
			if current.Outcome == askswire.PermissionOutcomePending && current.PolicyRevision != "" {
				*saved = current.PolicyRevision
			}
			if current.Outcome == askswire.PermissionOutcomeUnavailable && out.Outcome == askswire.PermissionOutcomeAnswered {
				return out, nil
			}
			if current.Outcome != askswire.PermissionOutcomePending {
				return current, nil
			}
			// Answer and effect are separate commits. Quietly wait for the
			// enforcing owner, without another Request or a new operation key.
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		switch out.Outcome {
		case askswire.PermissionOutcomePending:
			next, err := gateway.WaitForAnswer(ctx, out, func(ctx context.Context, waitMS int64) (askswire.PermissionObservation, error) {
				return permission.ObserveContext(ctx, intent.RequestKey, waitMS)
			})
			if err != nil {
				return askswire.PermissionObservation{}, err
			}
			if next.Outcome == askswire.PermissionOutcomePending {
				return next, nil
			}
			out = next
		case askswire.PermissionOutcomeAnswered:
			if out.Answer == nil || out.Answer.Option != "allow" || b.checkAccess == nil {
				return out, nil
			}
			confirm()
		default:
			return out, nil
		}
	}
}
func nativeMessagesFactory(machine *client.Machine) messagesClientFactory {
	return func(ctx context.Context) (nativeMessages, error) {
		if machine == nil {
			return nil, errors.New("messages: native machine required")
		}
		binding, err := machine.ResolveService(ctx, "abstraction.facade/messages@1", client.Requirements{Scope: client.ScopeLocal})
		if err != nil {
			return nil, err
		}
		return wire.NewMessagesClient(binding.Transport().WithDefaults(35*time.Second, 1<<20).WithContext(ctx)), nil
	}
}
func bindMessagesBackend(base gateway.Backend, application, instance, contextName string, native messagesClientFactory) (gateway.Backend, error) {
	return bindMessagesBackendStatus(base, application, instance, contextName, native, false)
}

// A supervised connection binds before its first admission: its instance is
// the presence owner's current one, and its admission gate refuses every tool
// until that owner confirms an opened inbox.
func bindMessagesBackendStatus(base gateway.Backend, application, instance, contextName string, native messagesClientFactory, supervised bool) (gateway.Backend, error) {
	if base == nil || application == "" || instance == "" && !supervised || contextName == "" || native == nil {
		return nil, errors.New("messages: opened native association required")
	}
	return &messagesBackend{Backend: base, application: application, instance: instance, contextName: contextName, native: native, status: new(messagesInboxStatus), replyOnly: new(replyOnlyTurn)}, nil
}
func (b *messagesBackend) MessagesInboxOpen(ctx context.Context, in gateway.MessagesOpenInput) (wire.InboxChange, error) {
	if receiver, ok := b.chatBodyBackend(ctx); !ok {
		return wire.InboxChange{Outcome: wire.OutcomeUnavailable}, nil
	} else if receiver != b {
		return receiver.MessagesInboxOpen(ctx, in)
	}
	if in.Context == "" {
		in.Context = b.contextName
	}
	if in.Context != b.contextName {
		return wire.InboxChange{Outcome: wire.OutcomeInvalid}, nil
	}
	var generation uint64
	if b.status != nil {
		generation = b.status.admission.version()
	}
	c, err := b.native(ctx)
	if err != nil {
		if b.status != nil {
			b.status.admission.setReady(false)
		}
		return wire.InboxChange{Outcome: wire.OutcomeUnavailable}, nil
	}
	out, err := b.status.openContext(ctx, c, b.inboxInstance(), b.contextName, &in.Status)
	if b.status != nil && b.status.admission != nil {
		if err != nil || out.Outcome == wire.OutcomeUnknown || out.Outcome == wire.OutcomeUnavailable {
			b.status.admission.setReady(false)
		} else if out.Outcome == wire.OutcomeOpened && ctx.Err() == nil {
			b.status.admission.mu.Lock()
			deadline := b.status.admission.deadline
			b.status.admission.mu.Unlock()
			b.status.admission.confirmVersion(deadline, generation)
		} else if out.Outcome == wire.OutcomeForbidden || out.Outcome == wire.OutcomeHeld {
			b.status.admission.lose(fmt.Errorf("agent explicit inbox open: %w", &agentInboxOutcomeError{outcome: out.Outcome}))
		}
	}
	if err != nil {
		// InboxChange carries no reason; the cause goes to this server's log.
		if !errors.Is(err, errMessagesInboxStatusUnknown) {
			messageReplyCause("InboxOpen", err)
		}
		return wire.InboxChange{Outcome: wire.OutcomeUnknown}, nil
	}
	return out, nil
}
func (b *messagesBackend) MessagesInboxesList(ctx context.Context, in gateway.MessagesListInput) (wire.InboxPage, error) {
	c, err := b.native(ctx)
	if err != nil {
		return wire.InboxPage{Outcome: wire.OutcomeUnavailable}, nil
	}
	out, err := c.InboxesList(wire.InboxQuery{Cursor: in.Cursor, PeerKey: in.PeerKey, Receiver: in.Receiver})
	if err != nil {
		return wire.InboxPage{Outcome: wire.OutcomeUnknown, Inboxes: []wire.Inbox{}, Reason: messageReplyCause("InboxesList", err)}, nil
	}
	return out, nil
}
func (b *messagesBackend) MessagesSend(ctx context.Context, in gateway.MessagesSendInput) (wire.SendResult, error) {
	// Validate the plain adapter object with the generated wire grammar before
	// resolving a native sender. A local codec refusal proves no dispatch.
	raw, err := json.Marshal(in)
	if err != nil {
		return wire.SendResult{Outcome: wire.OutcomeInvalid}, nil
	}
	request, err := wire.DecodeSendRequest(raw)
	if err != nil {
		return wire.SendResult{Outcome: wire.OutcomeInvalid, Reason: messageSendShapeReason(in)}, nil
	}
	ctx, cancel, ok := messagesEffectContext(ctx)
	defer cancel()
	if !ok {
		return wire.SendResult{Outcome: wire.OutcomeUnavailable}, nil
	}
	c, err := b.native(ctx)
	if err != nil {
		return wire.SendResult{Outcome: wire.OutcomeUnavailable}, nil
	}
	// Exactly one call. Transport errors never prove absence of acceptance.
	var out wire.SendResult
	b.chatMu.Lock()
	required, returnContext, chatID := b.requireChat, b.chatContext, b.chatID
	b.chatMu.Unlock()
	if required && (returnContext != "" || chatID != "") {
		chat, ok := c.(interface {
			SendChat(string, string, wire.ChatSendRequest) (wire.SendResult, error)
		})
		if !ok || returnContext == "" {
			return wire.SendResult{Outcome: wire.OutcomeUnavailable, Reason: "chat_route_unavailable"}, nil
		}
		out, err = b.messagesReadmitted(func(instance string) (wire.SendResult, error) {
			return chat.SendChat(instance, b.contextName, wire.ChatSendRequest{Request: *request, ReturnContext: returnContext})
		})
	} else {
		out, err = b.messagesReadmitted(func(instance string) (wire.SendResult, error) {
			return c.Send(instance, b.contextName, *request)
		})
	}
	if err != nil {
		if required {
			cause := messageReplyCause("SendChat", err)
			if cause == "dispatch_unknown_method" || cause == "dispatch_unknown_version" || cause == "dispatch_unknown_service" {
				return wire.SendResult{Outcome: wire.OutcomeUnavailable, Reason: cause}, nil
			}
			return wire.SendResult{Outcome: wire.OutcomeUnknown, Reason: cause}, nil
		}
		return wire.SendResult{Outcome: wire.OutcomeUnknown, Reason: messageReplyCause("Send", err)}, nil
	}
	return out, nil
}
func (b *messagesBackend) MessagesWait(ctx context.Context, in gateway.MessagesWaitInput) (wire.Page, error) {
	if receiver, ok := b.chatBodyBackend(ctx); !ok {
		return wire.Page{Outcome: wire.OutcomeUnavailable, Messages: []wire.Message{}, Headers: []wire.Header{}}, nil
	} else if receiver != b {
		return receiver.MessagesWait(ctx, in)
	}
	if in.WaitMs < 0 || in.WaitMs > 30000 || in.WakeTicket != "" && !validCodexWakeTicket(in.WakeTicket) {
		return wire.Page{Outcome: wire.OutcomeInvalid}, nil
	}
	c, err := b.native(ctx)
	if err != nil {
		return wire.Page{Outcome: wire.OutcomeUnavailable}, nil
	}
	out, err := c.Wait(b.inboxInstance(), b.contextName, wire.ViewBody, in.After, "", in.WaitMs)
	if err != nil {
		// Page carries no reason; the cause goes to this server's log.
		messageReplyCause("Wait", err)
		return wire.Page{Outcome: wire.OutcomeUnknown}, nil
	}
	if out.Outcome == wire.OutcomePage {
		// The chat owner's confirmation applies the host's wake ticket rules.
		b.chatMu.Lock()
		confirm := b.wakeConfirm
		b.chatMu.Unlock()
		incoming := b.incomingMessages(out.Messages)
		woken := false
		if confirm != nil {
			if wake, confirmed, err := confirm(ctx, in.WakeTicket); err == nil && confirmed {
				// The turn this read belongs to was woken by that key; every
				// message it sends carries the key's chain (AB-1s ruling
				// 2026-11-09). A refusal still returns the page.
				if wake != "" {
					b.turnWake(c, wake)
					woken = true
				}
				b.publishWakeConfirmed(ctx)
			}
		}
		// S5 ruling 2026-11-22: a read that a wake started marks the turn
		// reply-only; a read inside a marked turn adds what it read. A Codex
		// read without its ticket while the queued pointer is still held is
		// the pointer's turn as well.
		if !woken && in.WakeTicket == "" && b.replyOnly != nil && !b.replyOnly.active() {
			b.chatMu.Lock()
			held := b.wakeHeld
			b.chatMu.Unlock()
			if held != nil {
				woken, _ = held(ctx)
			}
		}
		// A read with a Codex ticket this ledger handed to the host started
		// that pointer's turn, even when its slot was replaced after the lease
		// (security review 2026-11-22, item 2).
		if !woken && in.WakeTicket != "" {
			b.chatMu.Lock()
			issued := b.wakeIssued
			b.chatMu.Unlock()
			if issued != nil {
				woken, _ = issued(ctx, in.WakeTicket)
			}
		}
		if woken {
			b.replyOnly.mark(nil, incoming)
		} else {
			b.replyOnly.read(incoming)
		}
	}
	return out, nil
}

// turnWake states which wake started this inbox's current turn, or ends the
// turn's chain with an empty key. A refusal or failure is logged only.
func (b *messagesBackend) turnWake(c nativeMessages, wake string) {
	out, err := c.TurnWake(b.inboxInstance(), b.contextName, wake)
	if err != nil {
		messageReplyCause("TurnWake", err)
		return
	}
	if out.Outcome != wire.OutcomePage {
		fmt.Fprintf(messagesCauseLog, "OA_MESSAGES TurnWake %s %s\n", out.Outcome, out.Reason)
	}
}
