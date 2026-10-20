package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	logging "github.com/openabstractions/abstraction-logging/go"
)

func TestMessagesAuditCanonicalCrashReplayExpiry(t *testing.T) {
	ctx := context.Background()
	for _, boundary := range []string{"record_published", "audit_appended"} {
		t.Run(boundary, func(t *testing.T) {
			s, now, actor, request := messagesStoreFixture(t)
			sink := &messageAuditSink{}
			h := &applicationsHost{messages: s}
			if err := h.configureMessageAudit(sink); err != nil {
				t.Fatal(err)
			}
			defer h.messageAudit.close()
			s.boundary = func(at string) error {
				if at == boundary {
					return errors.New("inert crash")
				}
				return nil
			}
			result := s.accept(ctx, actor, "native-claude", request, false, "")
			key := s.key(actor, request.IdempotencyKey)
			r, err := s.readRecord(ctx, key)
			if err != nil || r == nil || r.Audit == nil || r.AuditDone {
				t.Fatal(r, err)
			}
			original := *r.Audit
			if boundary == "record_published" && result.Outcome != msg.OutcomeUnknown {
				t.Fatal(result)
			}
			if boundary == "audit_appended" {
				if err = h.flushMessageAudit(ctx, key, r.Token); err == nil {
					t.Fatal("missing crash")
				}
			}
			// Reopen with a different process key. Recovery must use the persisted
			// metadata, even after logical expiry has removed all message text.
			h.messageAudit.close()
			if err = h.configureMessageAudit(sink); err != nil {
				t.Fatal(err)
			}
			s.boundary = nil
			*now = now.Add(2 * time.Hour)
			refs, err := s.expiryProgress(ctx)
			if err != nil || len(refs) != 1 {
				t.Fatal(refs, err)
			}
			r, err = s.readRecord(ctx, key)
			if err != nil || r.Profile != "messages-expired@1" || len(r.Spec) != 0 || r.Audit == nil {
				t.Fatal(r, err)
			}
			if r.Audit.DeliveryState != "undelivered" || r.Audit.UndeliveredReason != "expired" || r.Audit.DeliveryAt != now.UnixMilli() {
				t.Fatal("expiry lost the delivery axis", r.Audit)
			}
			provenance := *r.Audit
			provenance.DeliveryState, provenance.UndeliveredReason, provenance.DeliveryAt = "", "", 0
			if provenance != original {
				t.Fatal("delivery changed acceptance provenance", provenance, original)
			}
			raw, err := os.ReadFile(s.recordPath(key))
			if err != nil || bytes.Contains(raw, []byte(request.Body)) || bytes.Contains(raw, []byte(request.Subject)) {
				t.Fatal("expired plaintext", err)
			}
			if err = h.flushMessageAudit(ctx, key, r.Token); err != nil {
				t.Fatal(err)
			}
			if err = h.flushMessageAudit(ctx, key, r.Token); err != nil {
				t.Fatal(err)
			}
			want := 1
			if boundary == "audit_appended" {
				want = 2
			}
			if len(sink.records) != want {
				t.Fatal("replay appended new row", len(sink.records))
			}
			for _, logged := range sink.records {
				if logged.Attrs["message_id"] != original.ID || logged.Attrs["content_hmac"] != original.Digest || logged.Attrs["audit_epoch"] != original.Epoch {
					t.Fatal(logged)
				}
			}
			if _, err = s.expiryProgress(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMessagesAuditExistingFileSinkAndFailedAppend(t *testing.T) {
	ctx := context.Background()
	s, _, actor, request := messagesStoreFixture(t)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	sink, err := logging.OpenFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &applicationsHost{messages: s}
	if err = h.configureMessageAudit(sink); err != nil {
		t.Fatal(err)
	}
	defer h.messageAudit.close()
	result := s.accept(ctx, actor, "native-claude", request, false, "")
	if result.Outcome != msg.OutcomeSent {
		t.Fatal(result)
	}
	key := s.key(actor, request.IdempotencyKey)
	r, _ := s.readRecord(ctx, key)
	if err = sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err = h.flushMessageAudit(ctx, key, r.Token); err == nil {
		t.Fatal("closed sink reported append")
	}
	r, _ = s.readRecord(ctx, key)
	if r.AuditDone {
		t.Fatal("lost pending metadata")
	}
	reopened, err := logging.OpenFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	h.messageAudit.close()
	if err = h.configureMessageAudit(reopened); err != nil {
		t.Fatal(err)
	}
	if err = h.flushMessageAudit(ctx, key, r.Token); err != nil {
		t.Fatal(err)
	}
	if err = h.flushMessageAudit(ctx, key, r.Token); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || bytes.Count(raw, []byte("Application message")) != 1 || bytes.Contains(raw, []byte(request.Subject)) || bytes.Contains(raw, []byte(request.Body)) {
		t.Fatal(string(raw), err)
	}
}

func TestMessagesAuditOriginalOnlyAndPendingReuse(t *testing.T) {
	ctx := context.Background()
	s, now, actor, request := messagesStoreFixture(t)
	sink := &messageAuditSink{}
	h := &applicationsHost{messages: s}
	if err := h.configureMessageAudit(sink); err != nil {
		t.Fatal(err)
	}
	defer h.messageAudit.close()
	first := s.accept(ctx, actor, "native-claude", request, false, "")
	if first.Outcome != msg.OutcomeSent {
		t.Fatal(first)
	}
	key := s.key(actor, request.IdempotencyKey)
	r, _ := s.readRecord(ctx, key)
	alias := request
	alias.IdempotencyKey = "alias"
	if result := s.accept(ctx, actor, "native-claude", alias, false, ""); result.MessageID != first.MessageID || result.Outcome != msg.OutcomeSent {
		t.Fatal(result)
	}
	ar, err := s.readRecord(ctx, s.key(actor, alias.IdempotencyKey))
	if err != nil || !ar.Alias || ar.Audit != nil {
		t.Fatal(ar, err)
	}
	*now = now.Add(2 * time.Hour)
	request.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
	if result := s.accept(ctx, actor, "native-claude", request, false, ""); result.Outcome != msg.OutcomeUnknown {
		t.Fatal("pending audit overwritten", result)
	}
	if err = h.flushMessageAudit(ctx, key, r.Token); err != nil {
		t.Fatal(err)
	}
	if result := s.accept(ctx, actor, "native-claude", request, false, ""); result.Outcome != msg.OutcomeSent {
		t.Fatal(result)
	}
	raw, _ := json.Marshal(sink.records)
	if bytes.Contains(raw, []byte(request.Body)) || bytes.Contains(raw, []byte(request.Subject)) {
		t.Fatal("plaintext logged")
	}
}

func TestMessagesAuditWorkerRecoversAfterProgressError(t *testing.T) {
	s, _, actor, request := messagesStoreFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := make(chan error, 4)
	h := &applicationsHost{messages: s, ctx: ctx, report: func(err error) { reports <- err }}
	sink := &messageAuditSink{}
	if err := h.configureMessageAudit(sink); err != nil {
		t.Fatal(err)
	}
	defer h.messageAudit.close()
	head := filepath.Join(s.dir, "expiry", "head.json")
	if err := os.WriteFile(head, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() { defer close(done); h.runMessageProgress(ticks) }()
	defer func() { cancel(); <-done }()
	step := func() {
		t.Helper()
		select {
		case ticks <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("worker stopped")
		}
		select {
		case <-reports:
		case <-time.After(5 * time.Second):
			t.Fatal("progress did not finish")
		}
	}
	step()
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	result := s.accept(ctx, actor, "native-claude", request, false, "")
	if result.Outcome != msg.OutcomeSent {
		t.Fatal(result)
	}
	// The second tick appends pending metadata, then reports the fixture's
	// intentionally absent native policy owner. That error must not stop it.
	step()
	r, err := s.readRecord(ctx, s.key(actor, request.IdempotencyKey))
	if err != nil || !r.AuditDone || len(sink.records) != 1 {
		t.Fatal(r, err)
	}
	// The same failure is reported once, so the next pass ends unannounced.
	// The worker accepts a tick only after it finished the previous pass.
	for range 2 {
		select {
		case ticks <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("worker stopped")
		}
	}
	select {
	case err := <-reports:
		t.Fatal("unchanged failure reported again", err)
	default:
	}
	if len(sink.records) != 1 {
		t.Fatal("confirmed audit replayed")
	}
}
