package main

import (
	"context"
	"strings"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// Architect ruling 1, 2026-11-22 (decisions/messaging-wake-0-8-1-proposed-rulings.md):
// a Codex chat whose helper has gone is woken by the runtime's `codex queue
// --thread <id> --message <pointer> --resume` only while the Messages start
// setting is on, the sender holds the start right toward that chat, the Codex
// binary is the one Setup selected and verified, and the woken turn is
// reply-only. receiver_watching is true while the chat is wakeable this way.
// Without the setting or the right, the send says plainly that Codex is closed.

// messageCodexClosedReason marks a sent message to a closed Codex chat that
// OA will not wake; the gateway says "Codex is closed; messages wait until it
// opens."
const messageCodexClosedReason = "codex_closed"

// messageCodexChatWakeable reports whether the message's receiver is a
// retained Codex chat with no open inbox (codex) and whether the runtime's
// start would wake it now (wakeable). It runs progressMessageStart's checks
// without claiming, charging or asking anyone.
func (h *applicationsHost) messageCodexChatWakeable(ctx context.Context, r *messageRecord, request *msg.SendRequest) (codex, wakeable bool) {
	application := strings.TrimPrefix(request.To, "app:")
	if h.directory == nil || !messageCanonicalChatContext(request.Context) || r.Actor.Remote != nil || r.Alias || r.Control {
		return false, false
	}
	if open, err := h.directory.messageContextOpen(ctx, application, request.Context); err != nil || open {
		return false, false
	}
	// The receiver is Codex by its integration's name or its codex-v1 recipe.
	codex = messageCodexReceiver(application)
	target, err := h.directory.prepareMessageStart(ctx, application)
	if err != nil || target.Descriptor.Activation == nil {
		return codex, false
	}
	profile, err := startprofile.Parse(target.Descriptor.Activation.Arguments)
	if err != nil || profile.Name != "codex-v1" {
		return codex, false
	}
	codex = true
	if !messageChainWakes(r) {
		return true, false
	}
	settings, err := h.readMessageStartSettings(ctx, application)
	if err != nil || !settings.permits(request.Kind) {
		return true, false
	}
	target.Cold, err = h.messages.coldResumeForChat(ctx, h.directory.owner, r.Receiver, request.Context)
	if err != nil {
		return true, false
	}
	if _, startable := messageChatStart(profile.Name, target.Cold, h.directory.appsRunning(ctx)); !startable {
		return true, false
	}
	if !samePrograms(target.Program, r.Receiver) || target.Validate == nil || target.Validate(ctx) != nil {
		return true, false
	}
	consent, admitted := h.messageStartConsent(ctx, r, target)
	if !admitted {
		return true, false
	}
	// The same decision the start makes, over the current and earlier
	// sentences' permits.
	decision, _ := h.messageStartDecision(ctx, consent)
	return true, decision.Outcome == rights.DecisionOutcomePermitted
}

// messageCodexWakeKey is the wake key of this message in its receiving chat's
// inbox, as that chat's headers project it. The launcher reserves its pointer
// under this key in the existing wake ledger, one key per new message.
func (h *applicationsHost) messageCodexWakeKey(ctx context.Context, key string, r *messageRecord, request *msg.SendRequest) (string, error) {
	return h.messages.wakeForRecord(ctx, messageStateReceiver(r, request), key, r)
}
