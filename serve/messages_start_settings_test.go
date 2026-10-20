package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestMessagesStartSettingsRefuseBeforeRightsAndProfile(t *testing.T) {
	for _, reason := range []string{"questions", "receiver", "unavailable", "invalid_rate"} {
		t.Run(reason, func(t *testing.T) {
			h, key, record, calls, _ := messagesStartProgressFixture(t)
			h.messageStartSettings = func(context.Context, string) (messageStartSettings, error) {
				settings := defaultMessageStartSettings()
				switch reason {
				case "questions":
					settings.Questions = false
				case "receiver":
					settings.Receiver = false
				case "unavailable":
					return settings, errors.New("config unavailable")
				case "invalid_rate":
					settings.PerHour = 3
				}
				return settings, nil
			}
			h.messageRights.firstUse = func(rights.Subject, func() asks.Publisher, string, string) string {
				t.Fatal("disabled settings asked for a right")
				return ""
			}
			h.directory.messageStartProfile = func(context.Context, wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
				t.Fatal("disabled settings resolved a launch profile")
				return applicationMessageStartRequest{}, nil
			}
			if err := h.progressMessageStartSettled(context.Background(), key, record); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatal("disabled settings launched")
			}
		})
	}
}

func TestMessagesStartSettingsNotesRequireOptInAndKeepStartConsent(t *testing.T) {
	h, _, original, calls, _ := messagesStartProgressFixture(t)
	request, err := decodeRetainedMessageRequest(original.Spec)
	if err != nil {
		t.Fatal(err)
	}
	request.Kind, request.AnswerByUnixMs, request.IdempotencyKey = msg.KindNote, 0, "note-start"
	if got := h.messages.accept(context.Background(), original.Actor, original.Receiver, *request, false, ""); got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	key := h.messages.key(original.Actor, request.IdempotencyKey)
	record, err := h.messages.readRecord(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.progressMessageStartSettled(context.Background(), key, record); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("default started a note")
	}
	h.messageStartSettings = func(context.Context, string) (messageStartSettings, error) {
		settings := defaultMessageStartSettings()
		settings.Notes = true
		return settings, nil
	}
	allowMessageStartFixture(t, h, key, record)
	if calls.Load() != 0 {
		t.Fatal("note opt-in bypassed native consent")
	}
	if err := h.progressMessageStartSettled(context.Background(), key, record); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("approved opted-in note did not start", calls.Load())
	}
	// Reopening must accept the canonical note's start record as durable state.
	if _, err := openMessageStore(filepath.Dir(h.messages.dir), h.messages.now); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesStartSettingsChangedBeforeDispatchRefuses(t *testing.T) {
	h, key, record, calls, _ := messagesStartProgressFixture(t)
	allowMessageStartFixture(t, h, key, record)
	enabled := true
	h.messageStartSettings = func(context.Context, string) (messageStartSettings, error) {
		settings := defaultMessageStartSettings()
		settings.Questions = enabled
		return settings, nil
	}
	profile := h.directory.messageStartProfile
	h.directory.messageStartProfile = func(ctx context.Context, descriptor wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		target, err := profile(ctx, descriptor)
		check := target.Validate
		target.Validate = func(ctx context.Context) error {
			current, err := h.messages.readRecord(ctx, key)
			if err != nil {
				return err
			}
			if current.Start != nil {
				enabled = false
			}
			return check(ctx)
		}
		return target, err
	}
	if err := h.progressMessageStartSettled(context.Background(), key, record); err != nil {
		t.Fatal(err)
	}
	if enabled || calls.Load() != 0 {
		t.Fatal("changed settings reached launch", enabled, calls.Load())
	}
}

func TestApplicationMessageStartSettingsRateSurvivesRestart(t *testing.T) {
	for _, limit := range []int{1, 2, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			d, _, app, _ := activationFixture(t, time.Second)
			for index := 0; index <= limit; index++ {
				d.mu.Lock()
				ok, err := d.reserveMessageStartLocked(context.Background(), applicationMessageStartReservation{Account: app.Account, Program: app.Program, Attempt: fmt.Sprint(index), Message: fmt.Sprint(index)}, func(map[string]wire.ApplicationDescriptor) error { return nil }, limit)
				d.mu.Unlock()
				if index < limit && (!ok || err != nil) {
					t.Fatal(index, ok, err)
				}
				if index == limit && (ok || err == nil) {
					t.Fatal("exceeded rate", ok, err)
				}
				if index == 0 {
					d, err = openApplications(filepath.Dir(d.dir), d.owner, d.decide)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
