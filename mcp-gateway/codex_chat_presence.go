package main

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/client"
)

var errChatSourceAdmissionUnavailable = errors.New("actual native source admission unavailable")

func (b *messagesBackend) chatSourceUnavailableError() error {
	if b.status == nil || b.status.admission == nil {
		return errChatSourceAdmissionUnavailable
	}
	a := b.status.admission
	a.mu.Lock()
	cause := a.lost
	a.mu.Unlock()
	return errors.Join(errChatSourceAdmissionUnavailable, cause)
}

func (b *messagesBackend) chatSourceRefusal() error {
	if b.status == nil || b.status.admission == nil {
		return nil
	}
	a := b.status.admission
	a.mu.Lock()
	defer a.mu.Unlock()
	var factory *agentNativeFactoryError
	if a.refused == nil && errors.As(a.lost, &factory) && !chatFactoryAvailability(factory) && !agentVersionSkew(factory) {
		return &agentPresenceTerminalError{cause: a.lost}
	}
	if a.refused == nil && a.lost != nil && (messagesWatchFailure(a.lost, "unavailable") == "invalid" || messagesWatchFailure(a.lost, "unavailable") == "forbidden") {
		return a.lost
	}
	return a.refused
}

// The canonical claim uses the original source's retained admission and its
// transport context. It creates no new source or authority on re-admission.
func (b *messagesBackend) chatSourceProof(ctx context.Context) (nativeMessages, func(), wire.Outcome, error) {
	if ctx.Err() != nil {
		return nil, func() {}, wire.OutcomeUnavailable, ctx.Err()
	}
	call, finish, admitted := b.BeginNativeTool(ctx, "oa_messages_watch")
	if !admitted {
		finish()
		if codexReceivingRetired(b) {
			return nil, func() {}, wire.OutcomeEnded, nil
		}
		if err := b.chatSourceRefusal(); err != nil {
			err = establishedChatError(err)
			var terminal *agentPresenceTerminalError
			if errors.As(err, &terminal) {
				b.status.admission.refuse(err, true)
			}
			return nil, func() {}, wire.OutcomeInvalid, err
		}
		return nil, func() {}, wire.OutcomeUnavailable, b.chatSourceUnavailableError()
	}
	cancel := func() {}
	if b.status != nil && b.status.admission != nil {
		a := b.status.admission
		a.mu.Lock()
		deadline := a.deadline
		a.mu.Unlock()
		call, cancel = context.WithDeadline(call, deadline)
	}
	done := func() { cancel(); finish() }
	client, err := b.native(call)
	if err != nil {
		ownedCanceled := call.Err() != nil && ctx.Err() == nil
		done()
		if ownedCanceled && agentPresenceCompletion(err, true) == nil && !codexReceivingRetired(b) {
			return nil, func() {}, wire.OutcomeUnavailable, errors.Join(errChatSourceAdmissionUnavailable, err)
		}
		if !agentVersionSkew(err) && !chatFactoryAvailability(err) {
			err = &agentPresenceTerminalError{cause: err}
		}
		return nil, func() {}, wire.OutcomeUnavailable, establishedChatError(err)
	}
	return client, done, wire.OutcomeOpened, nil
}

func chatFactoryAvailability(err error) bool {
	if messagesWatchFailure(err, "unavailable") != "unavailable" {
		return false
	}
	var resolution *client.ResolutionError
	return errors.As(err, &resolution) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.DeadlineExceeded)
}

func (b *messagesBackend) bindChat(thread string) error {
	if !codexQueueThread.MatchString(thread) {
		return errors.New("native host chat metadata unavailable")
	}
	return b.bindChatID(thread)
}

func (b *messagesBackend) bindChatID(thread string) error {
	b.chatMu.Lock()
	defer b.chatMu.Unlock()
	if b.chatID != "" && b.chatID != thread {
		return errors.New("canonical chat changed")
	}
	b.chatID = thread
	return nil
}

func (b *messagesBackend) newChatReceiver(ctx context.Context, configure ...func(*messagesBackend) error) (*messagesBackend, func(), error) {
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	b.chatMu.Lock()
	priorContext, chatID, launch := b.chatContext, b.chatID, b.chatLaunch
	b.chatMu.Unlock()
	if chatID == "" || b.chatPresence == nil {
		return nil, nil, errors.New("canonical chat receiving unavailable")
	}
	native, finishSource, sourceOutcome, err := b.chatSourceProof(ctx)
	defer finishSource()
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if sourceOutcome == wire.OutcomeUnavailable && chatFactoryAvailability(err) {
			return nil, nil, errors.Join(errChatSourceAdmissionUnavailable, err)
		}
		if class := messagesWatchFailure(err, "unknown"); class == "invalid" || class == "forbidden" {
			return nil, nil, &agentPresenceTerminalError{cause: err}
		}
		return nil, nil, err
	}
	if sourceOutcome != wire.OutcomeOpened {
		b.status.recordChatOpenOutcome(sourceOutcome)
		return nil, nil, &agentInboxOutcomeError{outcome: sourceOutcome}
	}
	// This lease occurrence is captured once per dispatch. Re-admission keeps
	// b's original native process/source context and independently bound chat.
	sourceInstance := b.inboxInstance()
	allocated, err := native.InboxOpenChat(sourceInstance, b.contextName, "", wire.ChatBinding{ChatID: chatID, SourceInstance: sourceInstance, Launch: launch})
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if messagesWatchFailure(err, "unknown") == "unknown" || errors.Is(err, context.Canceled) && agentPresenceCompletion(err, true) == nil && !codexReceivingRetired(b) {
			return nil, nil, errors.Join(errChatSourceAdmissionUnavailable, err)
		}
		return nil, nil, &agentPresenceTerminalError{cause: err}
	}
	b.status.recordChatOpenOutcome(allocated.Outcome)
	if allocated.Outcome == wire.OutcomeUnavailable || allocated.Outcome == wire.OutcomeUnknown {
		return nil, nil, errors.Join(errChatSourceAdmissionUnavailable, &agentInboxOutcomeError{outcome: allocated.Outcome})
	}
	if allocated.Outcome != wire.OutcomePage && allocated.Outcome != wire.OutcomeOpened {
		return nil, nil, &agentPresenceTerminalError{cause: &agentInboxOutcomeError{outcome: allocated.Outcome}}
	}
	if !agentRoutingContext.MatchString(allocated.Context) {
		return nil, nil, &agentPresenceTerminalError{cause: errors.New("native chat allocation unavailable")}
	}
	contextName := allocated.Context
	if priorContext != "" && priorContext != contextName {
		return nil, nil, &agentPresenceTerminalError{cause: errors.New("native chat owner changed; reopen the connection")}
	}
	b.chatMu.Lock()
	b.chatContext = contextName
	b.chatMu.Unlock()
	status := &messagesInboxStatus{admission: new(agentToolAdmission), chatReceiver: true, routingBound: true, chatSource: true, chatID: chatID, chatSourceInstance: b.inboxInstance, chatSourceContext: b.contextName}
	status.chatLaunch = launch
	status.chatSourceProof = b.chatSourceProof
	b.chatMu.Lock()
	adapter := b.adapter
	b.chatMu.Unlock()
	receiver := &messagesBackend{Backend: b.Backend, application: b.application, contextName: contextName, native: b.native, status: status, chatID: chatID, adapter: adapter, replyOnly: b.replyOnly, decideFullTools: b.decideFullTools}
	for _, prepare := range configure {
		if err := prepare(receiver); err != nil {
			return nil, nil, err
		}
	}
	receiving, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	b.chatMu.Lock()
	b.chatReceiver = receiver
	b.chatMu.Unlock()
	go func() {
		defer close(done)
		superviseAgentPresence(receiving, func(ctx context.Context, ready func(string), readmit bool, _ func()) error {
			return b.chatPresence(ctx, contextName, status, ready, readmit)
		}, status, time.After, time.Now)
	}()
	return receiver, func() {
		cancel()
		<-done
		b.chatMu.Lock()
		if b.chatReceiver == receiver {
			b.chatReceiver = nil
		}
		b.chatMu.Unlock()
	}, nil
}

func (b *messagesBackend) chatBodyBackend(ctx context.Context) (*messagesBackend, bool) {
	b.chatMu.Lock()
	required, receiver := b.requireChat, b.chatReceiver
	b.chatMu.Unlock()
	if !required {
		return b, true
	}
	if receiver == nil {
		return nil, false
	}
	// A tool call carries its admission deadline: a chat receiver that is
	// re-admitting its context, as after a runtime stall, gets the same
	// bounded wait as the connection's own admission.
	until, waits := ctx.Value(agentAdmissionUntilKey{}).(time.Time)
	for chatReceivingPresence(ctx, receiver) != nil {
		if ctx.Err() != nil || !waits || receiver.status == nil || !receiver.status.admission.awaitAdmission(ctx, until) {
			return nil, false
		}
	}
	return receiver, true
}

func (s *messagesInboxStatus) setChatTitle(title string) {
	s.routingMu.Lock()
	s.chatTitle = hostChatTitle(title)
	if s.routingChanged != nil {
		select {
		case s.routingChanged <- struct{}{}:
		default:
		}
	}
	s.routingMu.Unlock()
}
