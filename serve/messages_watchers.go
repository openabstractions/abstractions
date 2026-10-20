package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

type nativeMessageWatch struct {
	actor     messageActor
	pin       *identity.Binding
	ctx       context.Context
	admission applicationAgentAdmission
}

// A watch exists only for the live admitted RPC. It grants no inbox ownership,
// lease, acknowledgment or future wake/rearm promise.
func (r *messagesReceiver) watchMessages(actor messageActor) (func(), error) {
	pin, err := r.call.RetainProcess()
	if err != nil {
		return nil, err
	}
	peer, subject, _, _, err := nativeInboxPeer(pin, r.host.directory.now())
	if err != nil || subject.Account != actor.Account {
		pin.Close()
		return nil, errors.New("messages: watch native owner unavailable")
	}
	info, allowed, err := r.host.directory.captureApplicationAgent(r.ctx, peer, actor.Application)
	if err != nil || !allowed || !samePrograms(info.Image.Program, actor.Program) {
		pin.Close()
		return nil, errors.New("messages: watch current class unavailable")
	}
	watch := &nativeMessageWatch{actor: actor, pin: pin, ctx: r.ctx, admission: info}
	r.host.messageWatchMu.Lock()
	if r.host.messageWatches == nil {
		r.host.messageWatches = map[*nativeMessageWatch]struct{}{}
	}
	if len(r.host.messageWatches) >= 64 {
		r.host.messageWatchMu.Unlock()
		pin.Close()
		return nil, errors.New("messages: watch capacity unavailable")
	}
	r.host.messageWatches[watch] = struct{}{}
	r.host.messageWatchMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h := r.host
			h.messageWatchMu.Lock()
			delete(h.messageWatches, watch)
			now := h.messageWatchClock()
			if h.messageWatchEnds == nil {
				h.messageWatchEnds = map[string]time.Time{}
			}
			for key, ended := range h.messageWatchEnds {
				if now.Sub(ended) >= messageWatchGrace {
					delete(h.messageWatchEnds, key)
				}
			}
			if len(h.messageWatchEnds) < 256 {
				h.messageWatchEnds[actor.Program+"\x00"+actor.Context] = now
			}
			h.messageWatchMu.Unlock()
			pin.Close()
		})
	}, nil
}

// messageWatchGrace counts a receiver as watching for a minute after its last
// watch ended. A Codex waker re-arms its watch right after each page, and runs
// the Codex queue command between two watches when it wakes the chat.
const messageWatchGrace = time.Minute

func (h *applicationsHost) messageWatchClock() time.Time {
	if h.directory != nil && h.directory.now != nil {
		return h.directory.now()
	}
	return time.Now()
}

// messageWatchHeld reports whether program holds a live watch on contextName,
// or ended one within messageWatchGrace. It only suppresses person notices
// and reports reach; it grants nothing and rechecks no enrollment.
func (h *applicationsHost) messageWatchHeld(program, contextName string) bool {
	h.messageWatchMu.Lock()
	defer h.messageWatchMu.Unlock()
	for w := range h.messageWatches {
		if w.ctx.Err() == nil && samePrograms(w.actor.Program, program) && w.actor.Context == contextName {
			return true
		}
	}
	for key, ended := range h.messageWatchEnds {
		watched, watchedContext, _ := strings.Cut(key, "\x00")
		if watchedContext == contextName && samePrograms(watched, program) && h.messageWatchClock().Sub(ended) < messageWatchGrace {
			return true
		}
	}
	return false
}

// messageCodexReceiver reports whether application is a Codex integration. Its
// helper wakes a chat only after Codex names the chat's thread, so its open
// inbox alone does not show a receiver that will see a message.
func messageCodexReceiver(application string) bool {
	return nativecli.AgentApplicationBase(application) == "oa-codex-integration"
}

// messageInboxReach is open for an inbox a running receiver reads. A Codex
// integration's inbox without a held watch is queued: the message waits until
// the person next uses that chat. So is one whose receiver reports its wake
// unconfirmed. Claude Code's hook wake is not observable here, so its inboxes
// stay open.
func (h *applicationsHost) messageInboxReach(application, program, contextName, status string, watching bool) msg.InboxReach {
	if messageWakeUnconfirmed(application, status) || messageCodexReceiver(application) && !watching && !h.messageWatchHeld(program, contextName) {
		return msg.InboxReachQueued
	}
	return msg.InboxReachOpen
}

// messageWakeUnconfirmedStatus is the presence status part a Codex receiver
// publishes while a wake it queued, or the earlier pointer holding its thread,
// is unconfirmed (the gateway's messagesWakeUnconfirmedText). It is display
// metadata: it changes reach and the sender's Prepare reason, never routing.
const messageWakeUnconfirmedStatus = "queued; Codex did not confirm the wake"

// messagePrepareWakeUnconfirmed is Prepare's reason for such an inbox.
const messagePrepareWakeUnconfirmed = "wake_unconfirmed"

func messageWakeUnconfirmed(application, status string) bool {
	return messageCodexReceiver(application) && strings.Contains(status, messageWakeUnconfirmedStatus)
}

func (h *applicationsHost) messageIsWatching(ctx context.Context, account, program, contextName string) bool {
	h.messageWatchMu.Lock()
	watches := make([]*nativeMessageWatch, 0, len(h.messageWatches))
	for w := range h.messageWatches {
		if w.actor.Account == account && w.actor.Program == program && w.actor.Context == contextName {
			watches = append(watches, w)
		}
	}
	h.messageWatchMu.Unlock()
	for _, w := range watches {
		if w.ctx.Err() != nil {
			continue
		}
		peer, subject, _, _, err := nativeInboxPeer(w.pin, h.directory.now())
		if err != nil || subject.Account != account {
			continue
		}
		info, allowed, err := h.directory.captureApplicationAgent(ctx, peer, w.actor.Application)
		if err == nil && allowed && samePrograms(info.Image.Program, program) && sameNativeInboxAdmission(w.admission, info) {
			h.messageWatchMu.Lock()
			_, pending := h.messageWatches[w]
			current := pending && w.ctx.Err() == nil
			h.messageWatchMu.Unlock()
			if current {
				return true
			}
		}
	}
	return false
}
