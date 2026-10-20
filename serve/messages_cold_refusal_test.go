package main

import (
	"context"
	"errors"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"os"
	"testing"
	"time"
)

func TestMessagesCanonicalNoticeFollowsStartDecision(t *testing.T) {
	for _, settingsFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "profile-refused", true: "settings-refused"}[settingsFailure], func(t *testing.T) {
			launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Cwd: t.TempDir(), Origin: msg.ChatOriginDesktop, Mode: msg.ChatModePlan}
			h, key, _, calls, _ := messagesStartProgressFixtureFor(t, "chat-notice-order", launch)
			notices, _, _, _ := messagesNoticeFixture(t)
			h.messageNotices = notices.messageNotices
			decided, noticed := false, false
			h.messageStartSettings = func(context.Context, string) (messageStartSettings, error) {
				if settingsFailure {
					decided = true
					return messageStartSettings{}, errors.New("fixture settings unavailable")
				}
				return defaultMessageStartSettings(), nil
			}
			h.directory.messageStartProfile = func(context.Context, wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
				decided = true
				return applicationMessageStartRequest{}, errors.New("fixture profile refused")
			}
			h.messages.boundary = func(at string) error {
				if at == "before_notice_record" {
					noticed = true
					if !decided {
						t.Fatal("canonical notice preceded its start decision")
					}
				}
				return nil
			}
			err := h.progressMessages(t.Context())
			if err != nil || !decided || !noticed || calls.Load() != 0 {
				t.Fatal("decision/refusal fallback", decided, noticed, calls.Load(), err)
			}
			stored, err := h.messages.readRecord(t.Context(), key)
			if err != nil || stored.WaitingNotice == nil || !stored.WaitingNotice.Settled || stored.Start != nil {
				t.Fatal("refusal changed canonical queue", stored, err)
			}
		})
	}
}

func TestMessagesGenericNoticeKeepsExistingDecisionOrder(t *testing.T) {
	h, key, _, calls, _ := messagesStartProgressFixture(t)
	notices, _, _, _ := messagesNoticeFixture(t)
	h.messageNotices = notices.messageNotices
	decided, noticed := false, false
	h.directory.messageStartProfile = func(context.Context, wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		decided = true
		return applicationMessageStartRequest{}, errors.New("fixture profile refused")
	}
	h.messages.boundary = func(at string) error {
		if at == "before_notice_record" {
			noticed = true
			if decided {
				t.Fatal("generic notice moved behind the start decision")
			}
		}
		return nil
	}
	if err := h.progressMessages(t.Context()); err != nil || !decided || !noticed || calls.Load() != 0 {
		t.Fatal("generic decision/notice order", decided, noticed, calls.Load(), err)
	}
	stored, err := h.messages.readRecord(t.Context(), key)
	if err != nil || stored.WaitingNotice == nil || !stored.WaitingNotice.Settled || stored.Start != nil {
		t.Fatal("generic refusal changed queue", stored, err)
	}
}

func TestMessagesColdRefusalUsesDurableNoticeForAddressedChat(t *testing.T) {
	for _, variant := range []string{"unapproved-profile", "unsupported-profile", "missing-launch", "replaced-cwd", "revoked-profile"} {
		t.Run(variant, func(t *testing.T) {
			launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Cwd: t.TempDir(), Origin: msg.ChatOriginDesktop, Mode: msg.ChatModePlan}
			var h *applicationsHost
			var key string
			var record *messageRecord
			h, key, record, calls, valid := messagesStartProgressFixtureFor(t, "chat-missing", launch)
			switch variant {
			case "unapproved-profile":
				h.directory.messageStartProfile = nil
			case "unsupported-profile":
				h, key, record, calls, valid = messagesStartProgressFixtureFor(t, "chat-unsupported")
			case "missing-launch":
				if _, err := h.messages.contextForChat(t.Context(), h.directory.owner, record.Receiver, launch.Session, &msg.ChatBinding{ChatID: launch.Session}); err != nil {
					t.Fatal(err)
				}
			case "replaced-cwd":
				if err := os.Rename(launch.Cwd, launch.Cwd+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(launch.Cwd, 0700); err != nil {
					t.Fatal(err)
				}
			case "revoked-profile":
				*valid = false
			}
			notices, _, _, _ := messagesNoticeFixture(t)
			h.messageNotices = notices.messageNotices
			h.directory.instances["unrelated-chat"] = applicationLease{application: "receiver", owner: rights.Subject{Program: record.Receiver}, deadline: time.Now().Add(time.Hour)}
			for i := 0; i < 2; i++ {
				if err := h.progressMessageStartSettled(context.Background(), key, record); err != nil {
					t.Fatal(err)
				}
			}
			history, _, err := h.messageNotices.book.NotificationHistory()
			if err != nil || len(history) != 1 || calls.Load() != 0 {
				t.Fatal("cold refusal lost or duplicated fallback", variant, len(history), calls.Load(), err)
			}
			stored, err := h.messages.readRecord(t.Context(), key)
			if err != nil || stored.WaitingNotice == nil || !stored.WaitingNotice.Settled || stored.Start != nil {
				t.Fatal("cold refusal changed queue", stored, err)
			}
		})
	}
}

func TestMessagesColdSelectionUsesReceiverOwnerBeforeSenderConsent(t *testing.T) {
	launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Cwd: t.TempDir(), Origin: msg.ChatOriginDesktop, Mode: msg.ChatModePlan}
	h, key, record, calls, _ := messagesStartProgressFixtureFor(t, "chat-owner", launch)
	profile := h.directory.messageStartProfile
	validated := false
	h.directory.messageStartProfile = func(ctx context.Context, d wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		selected, err := profile(ctx, d)
		if err != nil {
			return selected, err
		}
		check := selected.Validate
		selected.Validate = func(ctx context.Context) error { validated = true; return check(ctx) }
		return selected, nil
	}
	// The receiver selection precedes sender consent and must use the receiver's
	// owner even when the supplied sender snapshot names another account.
	snapshot := *record
	snapshot.Actor.Account = "foreign-sender-account"
	if err := h.progressMessageStartSettled(t.Context(), key, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !validated || calls.Load() != 0 {
		t.Fatal("receiver lookup used sender account or granted start", validated, calls.Load())
	}
}
