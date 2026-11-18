package main

import (
	"context"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Architect ruling 1, 2026-11-22: a retained Codex chat whose helper has gone
// is wakeable only while the start setting is on, the sender holds the start
// right toward it and Codex runs. receiver_watching follows that; otherwise
// the send carries codex_closed. The woken chat's start names the message's
// wake key in that chat's inbox.
func TestMessagesClosedCodexChatWakeableOnlyWithSettingAndRight(t *testing.T) {
	h, key, r, calls, _ := messagesStartProgressFixtureWith(t, "", "codex-v1", &msg.ChatBinding{ChatID: codexChatThread})
	h.directory.messageCodexRunning = func(context.Context) (bool, error) { return true, nil }
	request, err := decodeRetainedMessageRequest(r.Spec)
	if err != nil {
		t.Fatal(err)
	}
	wakeable := func() (bool, bool) { return h.messageCodexChatWakeable(t.Context(), r, request) }
	if codex, ok := wakeable(); !codex || ok {
		t.Fatal("a chat without the start right is wakeable", codex, ok)
	}
	resource := allowMessageStartFixture(t, h, key, r)
	if codex, ok := wakeable(); !codex || !ok {
		t.Fatal("a chat with the setting and the right is not wakeable", codex, ok)
	}
	h.directory.messageCodexRunning = func(context.Context) (bool, error) { return false, nil }
	if codex, ok := wakeable(); !codex || ok {
		t.Fatal("a chat is wakeable while Codex is closed", codex, ok)
	}
	h.directory.messageCodexRunning = func(context.Context) (bool, error) { return true, nil }
	h.messageStartSettings = func(context.Context, string) (messageStartSettings, error) {
		off := defaultMessageStartSettings()
		off.Questions = false
		return off, nil
	}
	if codex, ok := wakeable(); !codex || ok {
		t.Fatal("a chat is wakeable with the start setting off", codex, ok)
	}
	h.messageStartSettings = nil
	if err := h.messageRights.policy.Set(rights.Subject{Account: r.Actor.Account, Program: messageStartSubject(r.Actor)}, grants.ApplicationMessageStartAction, resource, false); err != nil {
		t.Fatal(err)
	}
	if codex, ok := wakeable(); !codex || ok {
		t.Fatal("a chat is wakeable after the right was refused", codex, ok)
	}
	if err := h.messageRights.policy.Set(rights.Subject{Account: r.Actor.Account, Program: messageStartSubject(r.Actor)}, grants.ApplicationMessageStartAction, resource, true); err != nil {
		t.Fatal(err)
	}

	var selected applicationMessageStartRequest
	h.directory.launchMessageStart = func(_ context.Context, request applicationMessageStartRequest, _ func(context.Context) error) error {
		calls.Add(1)
		selected = request
		return nil
	}
	if err := h.progressMessageStartSettled(t.Context(), key, r); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("the wakeable chat was not started", calls.Load())
	}
	// The key is the one the receiving chat's headers project for this message.
	want, err := h.messages.wakeForRecord(t.Context(), messageActor{Account: r.Actor.Account, Program: r.Receiver, Context: request.Context}, key, r)
	if err != nil || want == "" || selected.Wake != want {
		t.Fatal("the start did not carry the message's wake key", selected.Wake, want, err)
	}
}

// Security review 2026-11-22, item 3: receiver_watching follows the start's
// own decision over the current and earlier sentences' permits. A permit
// under an earlier sentence does not make the chat wakeable, and a "Never
// allow" under it keeps the chat closed without asking anyone.
func TestMessagesClosedCodexChatWakeableAgreesWithEarlierSentencePermits(t *testing.T) {
	for _, c := range []struct {
		name     string
		previous bool // the earlier sentence, else the pre-sentence resource
		allow    bool
		asks     int
	}{
		{"earlier sentence allowed", true, true, 1},
		{"earlier sentence never allowed", true, false, 0},
		{"pre-sentence never allowed", false, false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, key, r, calls, _ := messagesStartProgressFixtureWith(t, "", "codex-v1", &msg.ChatBinding{ChatID: codexChatThread})
			h.directory.messageCodexRunning = func(context.Context) (bool, error) { return true, nil }
			request, err := decodeRetainedMessageRequest(r.Spec)
			if err != nil {
				t.Fatal(err)
			}
			target, err := h.directory.prepareMessageStart(t.Context(), "receiver")
			if err != nil {
				t.Fatal(err)
			}
			if target.Cold, err = h.messages.coldResumeForChat(t.Context(), h.directory.owner, r.Receiver, request.Context); err != nil {
				t.Fatal(err)
			}
			consent, admitted := h.messageStartConsent(t.Context(), r, target)
			if !admitted || consent.previous == "" || consent.legacy == "" {
				t.Fatal("start consent unavailable", admitted)
			}
			resource := consent.legacy
			if c.previous {
				resource = consent.previous
			}
			if err := h.messageRights.policy.Set(consent.subject, grants.ApplicationMessageStartAction, resource, c.allow); err != nil {
				t.Fatal(err)
			}
			asked := 0
			h.messageRights.firstUse = func(rights.Subject, func() asks.Publisher, string, string) string {
				asked++
				return "pending"
			}
			if codex, ok := h.messageCodexChatWakeable(t.Context(), r, request); !codex || ok {
				t.Fatal("an earlier-sentence permit made the chat wakeable", codex, ok)
			}
			if err := h.progressMessageStartSettled(t.Context(), key, r); err != nil || calls.Load() != 0 || asked != c.asks {
				t.Fatal("the start disagreed", calls.Load(), asked, err)
			}
		})
	}
}
