package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Seed the existing published event owner, independently of the incoming queue.
// Each ref is a retained body-free report; new outcomes below use real acceptance.
func seedMessagesFullDeliveryLog(t *testing.T, s *messageStore, actor messageActor, matching bool) []messageEventRef {
	t.Helper()
	dir := s.eventPath(actor)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	refs := make([]messageEventRef, 0, messageActorEventLimit)
	for page := 0; page < messageActorEventPages; page++ {
		name := fmt.Sprintf("%048x", page+1)
		p := messageEventPage{Profile: "messages-events-page@1", Entries: []messageEventEntry{}}
		if page+1 < messageActorEventPages {
			p.Next = fmt.Sprintf("%048x", page+2)
		}
		for i := 0; i < messageEventPageEntries; i++ {
			n := page*messageEventPageEntries + i + 1
			from := fmt.Sprintf("app:receiver%d", n)
			if matching && n == 1 {
				from = "app:claude"
			}
			h := msg.Header{MessageID: fmt.Sprintf("seed-%d", n), From: from, FromContext: "target", To: "app:codex", Context: actor.Context, ContextID: fmt.Sprintf("seed-%d", n), Kind: msg.KindNote, ExpiresUnixMs: math.MaxInt64, Delivery: new(msg.DeliveryUndelivered), Undelivered: new(msg.UndeliveredReasonExpired), Count: new(int64(1))}
			token := fmt.Sprintf("%048x", n)
			ref := messageEventRef{Key: fmt.Sprintf("%064x", n), Token: token, Change: "delivery-" + token, Version: 1, ExpiresUnixMs: math.MaxInt64, AcceptedAt: s.now().UnixMilli() - 1000, DeliveryReport: string(msg.EncodeHeader(&h))}
			p.Entries = append(p.Entries, messageEventEntry{Ref: ref})
			refs = append(refs, ref)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	head := messageEventHead{Profile: "messages-events-head@1", First: fmt.Sprintf("%048x", 1), Last: fmt.Sprintf("%048x", messageActorEventPages)}
	raw, err := json.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "head.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return refs
}

func TestMessagesDeliveryFullLogCoalescesExactlyOnceAcrossRestart(t *testing.T) {
	for _, point := range []string{"delivery_fold_prepared", "delivery_fold_published"} {
		for _, ackBeforeRetry := range []bool{false, true} {
			t.Run(point+fmt.Sprint(ackBeforeRetry), func(t *testing.T) {
				s, now, actor, request := messagesStoreFixture(t)
				ctx := context.Background()
				out := s.accept(ctx, actor, "native-claude", request, false, "")
				if out.Outcome != msg.OutcomeSent {
					t.Fatal(out)
				}
				seedMessagesFullDeliveryLog(t, s, actor, true)
				*now = now.Add(2 * time.Hour)
				s.boundary = func(at string) error {
					if at == point {
						return errors.New("inert crash")
					}
					return nil
				}
				if err := s.sweepExpiry(ctx); err == nil {
					t.Fatal("fold crash not reached")
				}
				record, err := s.readRecord(ctx, s.key(actor, request.IdempotencyKey))
				if err != nil || record.Delivery == nil || record.Delivery.Fold == nil || record.Delivery.Reported {
					t.Fatal(record, err)
				}
				if ackBeforeRetry {
					ref := record.Delivery.Fold.After
					if point == "delivery_fold_prepared" {
						ref = record.Delivery.Fold.Before
					}
					if err := s.observeDeliveryReport(ctx, actor, ref); err != nil {
						t.Fatal(err)
					}
				}
				reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
				if err != nil {
					t.Fatal(err)
				}
				if err := reopened.sweepExpiry(ctx); err != nil {
					t.Fatal(err)
				}
				refs, err := reopened.messageEventCandidatesLimit(ctx, actor, reopened.stateEventCheckFor(actor), messageActorEventLimit)
				want := messageActorEventLimit
				if ackBeforeRetry && point == "delivery_fold_published" {
					want--
				}
				if err != nil || len(refs) != want {
					t.Fatal(len(refs), err)
				}
				total := int64(0)
				for _, ref := range refs {
					h, err := msg.DecodeHeader([]byte(ref.DeliveryReport))
					if err != nil {
						t.Fatal(err)
					}
					total += *h.Count
				}
				wantOutcomes := int64(messageActorEventLimit + 1)
				if ackBeforeRetry {
					wantOutcomes--
					if point == "delivery_fold_published" {
						wantOutcomes--
					}
				}
				if total != wantOutcomes {
					t.Fatal("fold duplicated or lost outcome", total, wantOutcomes)
				}
			})
		}
	}
}

func TestMessagesDeliveryFullUnmatchedLogRetainsPendingUntilSlot(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	refs := seedMessagesFullDeliveryLog(t, s, actor, false)
	*now = now.Add(2 * time.Hour)
	if err := s.sweepExpiry(ctx); err != nil {
		t.Fatal(err)
	}
	record, err := s.readRecord(ctx, s.key(actor, request.IdempotencyKey))
	if err != nil || record.Profile != "messages-expired@1" || record.Delivery == nil || record.Delivery.Reported || len(record.Spec) != 0 {
		t.Fatal(record, err)
	}
	if err := s.observeDeliveryReport(ctx, actor, refs[0]); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.sweepExpiry(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := reopened.messageEventCandidatesLimit(ctx, actor, reopened.stateEventCheckFor(actor), messageActorEventLimit)
	if err != nil || len(got) != messageActorEventLimit {
		t.Fatal(len(got), err)
	}
	record, err = reopened.readRecord(ctx, s.key(actor, request.IdempotencyKey))
	if err != nil || !record.Delivery.Reported {
		t.Fatal(record, err)
	}
}

func TestMessagesDeliveryTerminalRefusalFencesRetainedBodyCursor(t *testing.T) {
	s, _, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	key := s.key(actor, request.IdempotencyKey)
	record, err := s.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	permitted := map[string]string{key: record.Token}
	first := s.bodyPage(ctx, actor.Account, "native-claude", request.Context, "worker", "", permitted)
	if first.Outcome != msg.OutcomePage || len(first.Messages) != 1 {
		t.Fatal(first)
	}
	seedMessagesFullDeliveryLog(t, s, actor, false)
	if err := s.finishDelivery(ctx, key, record.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonReceiverRemoved); err != nil {
		t.Fatal(err)
	}
	current, err := s.readRecord(ctx, key)
	if err != nil || !current.Refused || current.Delivery == nil || current.Delivery.Reported {
		t.Fatal(current, err)
	}
	state, err := s.currentState(ctx, key, current)
	if err != nil || state.Refused {
		t.Fatal("delivery fabricated TaskState projection", state, err)
	}
	second := s.bodyPage(ctx, actor.Account, "native-claude", request.Context, "worker", "", permitted)
	if second.Outcome != msg.OutcomePage || len(second.Messages) != 0 {
		t.Fatal("retained body cursor bypassed canonical refusal", second)
	}
}

func TestMessagesDeliveryAcceptedRouteFollowsCanonicalPublication(t *testing.T) {
	s, _, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	request.Body = "private-body-route-evidence-1103"
	request.IdempotencyKey = "private-request-key-route-evidence-1103"
	s.boundary = func(at string) error {
		if at == "acceptance_route_published" {
			return errors.New("inert crash")
		}
		return nil
	}
	if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeUnknown {
		t.Fatal(out)
	}
	key := s.key(actor, request.IdempotencyKey)
	record, err := s.readRecord(ctx, key)
	if err != nil || record == nil {
		t.Fatal(record, err)
	}
	state, err := s.currentState(ctx, key, record)
	if err != nil || state.Acceptance == nil || state.Acceptance.ID != record.ID {
		t.Fatal(state, err)
	}
	raw, err := json.Marshal(state.Acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), request.Body) || strings.Contains(string(raw), request.IdempotencyKey) {
		t.Fatal("route evidence retained payload", string(raw))
	}
	s.boundary = nil
	if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeSent || out.MessageID != record.ID {
		t.Fatal(out)
	}
}

func TestMessagesDeliveryLostRecordUsesAcceptedRouteAndKeepsReceipt(t *testing.T) {
	for _, damage := range []string{"missing", "malformed"} {
		for _, acked := range []bool{false, true} {
			t.Run(damage+fmt.Sprint(acked), func(t *testing.T) {
				s, _, actor, request := messagesStoreFixture(t)
				ctx := context.Background()
				request.Kind = msg.KindNote
				request.Body = "private-lost-body-1103"
				request.NotifyDelivered = new(true)
				out := s.accept(ctx, actor, "native-claude", request, false, "")
				if out.Outcome != msg.OutcomeSent {
					t.Fatal(out)
				}
				key := s.key(actor, request.IdempotencyKey)
				record, err := s.readRecord(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				ref := messagePointer{Key: key, Token: record.Token, ID: record.ID, ExpiresUnixMs: record.ExpiresUnixMs}
				if acked {
					if err := s.acknowledge(ctx, ref); err != nil {
						t.Fatal(err)
					}
					events, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
					if err != nil || len(events) != 1 {
						t.Fatal(events, err)
					}
					if err := s.observeDeliveryReport(ctx, actor, events[0]); err != nil {
						t.Fatal(err)
					}
				}
				if damage == "missing" {
					err = os.Remove(s.recordPath(key))
				} else {
					err = os.WriteFile(s.recordPath(key), []byte("{broken"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				recovered, err := s.recordForPointer(ctx, ref)
				if err != nil || recovered == nil || !recovered.Lost || recovered.Delivery == nil || recovered.ExpiresUnixMs != record.ExpiresUnixMs {
					t.Fatal(recovered, err)
				}
				want := msg.DeliveryUndelivered
				if acked {
					want = msg.DeliveryDelivered
				}
				if recovered.Delivery.State != want || !acked && recovered.Delivery.Reason != msg.UndeliveredReasonLost {
					t.Fatal(recovered.Delivery)
				}
				if replay := s.accept(ctx, actor, "native-claude", request, false, ""); replay.Outcome != msg.OutcomeSent || replay.MessageID != out.MessageID || replay.ContextID != out.ContextID {
					t.Fatal("lost body changed accepted receipt", replay)
				}
				changed := request
				changed.Body = "changed"
				if conflict := s.accept(ctx, actor, "native-claude", changed, false, ""); conflict.Outcome != msg.OutcomeConflict {
					t.Fatal(conflict)
				}
				raw, err := os.ReadFile(s.recordPath(key))
				if err != nil || strings.Contains(string(raw), request.Body) || strings.Contains(string(raw), request.IdempotencyKey) {
					t.Fatal("recovery retained payload", string(raw), err)
				}
				reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := reopened.recordForPointer(ctx, ref); err != nil {
					t.Fatal(err)
				}
				events, err := reopened.messageEventCandidates(ctx, actor, reopened.stateEventCheckFor(actor))
				n := 1
				if acked {
					n = 0
				}
				if err != nil || len(events) != n {
					t.Fatal(events, err)
				}
			})
		}
	}
}

func TestMessagesDeliveryNeverPromotesReservationOrMissingEvidence(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	s.boundary = func(at string) error {
		if at == "before_record" {
			return errors.New("inert preacceptance crash")
		}
		return nil
	}
	if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeUnknown {
		t.Fatal(out)
	}
	raw, err := os.ReadFile(s.pairPath(actor, "native-claude"))
	if err != nil {
		t.Fatal(err)
	}
	pair, err := readMessagePair(raw)
	if err != nil || len(pair.Pointers) != 1 {
		t.Fatal(pair, err)
	}
	if record, err := s.recordForPointer(ctx, pair.Pointers[0]); err != nil || record != nil {
		t.Fatal("reservation became acceptance", record, err)
	}
	s.boundary = nil
	*now = now.Add(2 * time.Hour)
	if err := s.sweepExpiry(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
	if err != nil || len(events) != 0 {
		t.Fatal("fabricated sender report without accepted route", events, err)
	}
}

func TestMessagesDeliveryRemovedSenderSurvivesCanonicalDamage(t *testing.T) {
	for _, damage := range []string{"missing", "malformed", "projection_pending"} {
		t.Run(damage, func(t *testing.T) {
			s, _, actor, request := messagesStoreFixture(t)
			sink := &messageAuditSink{}
			h := &applicationsHost{messages: s}
			if err := h.configureMessageAudit(sink); err != nil {
				t.Fatal(err)
			}
			defer h.messageAudit.close()
			if out := s.accept(t.Context(), actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeSent {
				t.Fatal(out)
			}
			key := s.key(actor, request.IdempotencyKey)
			r, err := s.readRecord(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.markSenderRemoved(t.Context(), key, r.Token); err != nil {
				t.Fatal(err)
			}
			wantReason := msg.UndeliveredReasonLost
			if damage == "projection_pending" {
				raw, e := json.Marshal(r)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(s.recordPath(key), raw, 0600)
				wantReason = msg.UndeliveredReasonRefused
			} else if damage == "missing" {
				err = os.Remove(s.recordPath(key))
			} else {
				err = os.WriteFile(s.recordPath(key), []byte("{broken"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
			if err != nil {
				t.Fatal(err)
			}
			h.messages = reopened
			if damage == "projection_pending" {
				if err = reopened.finishDelivery(t.Context(), key, r.Token, msg.DeliveryUndelivered, wantReason); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := reopened.recordForPointer(t.Context(), messagePointer{Key: key, Token: r.Token})
			if err != nil || recovered == nil || !recovered.SenderRemoved || recovered.Delivery == nil || recovered.Delivery.Reason != wantReason || recovered.Delivery.Report != "" || !recovered.Delivery.Reported {
				t.Fatal(recovered, err)
			}
			if err = h.flushMessageAudit(t.Context(), key, r.Token); err != nil {
				t.Fatal(err)
			}
			if len(sink.records) != 1 || sink.records[0].Attrs["delivery_state"] != "undelivered" || sink.records[0].Attrs["undelivered_reason"] != string(wantReason) {
				t.Fatal("terminal Activity missing", sink.records)
			}
			refs, err := reopened.messageEventCandidates(t.Context(), actor, reopened.stateEventCheckFor(actor))
			if err != nil || len(refs) != 0 {
				t.Fatal("removed sender gained native report after recovery", refs, err)
			}
		})
	}
}

func TestMessagesDeliveryRemovedLegacySenderRetainsAcceptedRoute(t *testing.T) {
	for _, prior := range []string{"absent", "legacy"} {
		t.Run(prior, func(t *testing.T) {
			s, _, actor, request := messagesStoreFixture(t)
			if out := s.accept(t.Context(), actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeSent {
				t.Fatal(out)
			}
			key := s.key(actor, request.IdempotencyKey)
			r, err := s.readRecord(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			if prior == "absent" {
				err = os.Remove(s.statePath(key, r.Token))
			} else {
				state, e := s.currentState(t.Context(), key, r)
				if e != nil {
					t.Fatal(e)
				}
				state.Acceptance = nil
				raw, e := json.Marshal(state)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(s.statePath(key, r.Token), raw, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.markSenderRemoved(t.Context(), key, r.Token); err != nil {
				t.Fatal("legacy accepted route unavailable", err)
			}
			if err = os.Remove(s.recordPath(key)); err != nil {
				t.Fatal(err)
			}
			reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := reopened.recordForPointer(t.Context(), messagePointer{Key: key, Token: r.Token})
			if err != nil || recovered == nil || !recovered.SenderRemoved || recovered.Delivery == nil || recovered.Delivery.Report != "" || !recovered.Delivery.Reported {
				t.Fatal(recovered, err)
			}
			refs, err := reopened.messageEventCandidates(t.Context(), actor, reopened.stateEventCheckFor(actor))
			if err != nil || len(refs) != 0 {
				t.Fatal(refs, err)
			}
		})
	}
}

func TestMessagesDeliveryLostHeldReceiptAndMissingBothEvidence(t *testing.T) {
	for _, keepRoute := range []bool{false, true} {
		t.Run(fmt.Sprint(keepRoute), func(t *testing.T) {
			s, _, actor, request := messagesStoreFixture(t)
			ctx := context.Background()
			out := s.accept(ctx, actor, "native-claude", request, true, "")
			if out.Outcome != msg.OutcomeSent || !out.AwaitingPermission {
				t.Fatal(out)
			}
			key := s.key(actor, request.IdempotencyKey)
			record, err := s.readRecord(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(s.recordPath(key)); err != nil {
				t.Fatal(err)
			}
			if !keepRoute {
				if err := os.Remove(s.statePath(key, record.Token)); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := s.recordForPointer(ctx, messagePointer{Key: key, Token: record.Token, ExpiresUnixMs: record.ExpiresUnixMs})
			if err != nil {
				t.Fatal(err)
			}
			if keepRoute {
				if recovered == nil || !recovered.Lost {
					t.Fatal(recovered)
				}
				replay := s.accept(ctx, actor, "native-claude", request, true, "")
				if replay.Outcome != msg.OutcomeSent || !replay.AwaitingPermission {
					t.Fatal("loss changed permission facts", replay)
				}
			} else if recovered != nil {
				t.Fatal("missing both sources fabricated acceptance", recovered)
			}
			events, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
			n := 0
			if keepRoute {
				n = 1
			}
			if err != nil || len(events) != n {
				t.Fatal(events, err)
			}
		})
	}
}

func TestMessagesDeliveryLostBodyKeepsQuestionAnswerAxis(t *testing.T) {
	s, _, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	request.Kind = msg.KindQuestion
	request.AnswerByUnixMs = request.ExpiresUnixMs
	out := s.accept(ctx, actor, "native-claude", request, false, "")
	if out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	key := s.key(actor, request.IdempotencyKey)
	original, err := s.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.transitionState(ctx, key, original, "complete"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.recordPath(key)); err != nil {
		t.Fatal(err)
	}
	lost, err := s.recordForPointer(ctx, messagePointer{Key: key, Token: original.Token, ExpiresUnixMs: original.ExpiresUnixMs})
	if err != nil || lost == nil || !lost.Lost {
		t.Fatal(lost, err)
	}
	answer, err := s.currentState(ctx, key, original)
	if err != nil || answer.TaskState != msg.TaskStateCompleted {
		t.Fatal("delivery loss changed answer state", answer, err)
	}
	events, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
	if err != nil || len(events) != 1 || events[0].DeliveryReport == "" {
		t.Fatal("missing body replayed as an ordinary event", events, err)
	}
}

func TestMessagesDeliveryInvalidStateUsesCanonicalAndTransientFailureDoesNot(t *testing.T) {
	for _, damage := range []string{"malformed", "oversized", "canceled"} {
		t.Run(damage, func(t *testing.T) {
			s, now, actor, request := messagesStoreFixture(t)
			ctx := context.Background()
			out := s.accept(ctx, actor, "native-claude", request, false, "")
			if out.Outcome != msg.OutcomeSent {
				t.Fatal(out)
			}
			key := s.key(actor, request.IdempotencyKey)
			record, err := s.readRecord(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			work := ctx
			if damage == "canceled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				work = c
			} else {
				raw := []byte("{broken")
				if damage == "oversized" {
					raw = bytes.Repeat([]byte("x"), messageStateLimit+1)
				}
				if err := os.WriteFile(s.statePath(key, record.Token), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			queue := messageQueue{Profile: "messages-queue@1", Pointers: []messagePointer{{Key: key, Token: record.Token, ExpiresUnixMs: record.ExpiresUnixMs}}}
			err = s.recoverQueue(work, &queue)
			if damage == "canceled" {
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				current, e := s.readRecord(ctx, key)
				if e != nil || current.Delivery != nil {
					t.Fatal("transient failure invented terminal delivery", current, e)
				}
				return
			}
			if err != nil || len(queue.Pointers) != 0 {
				t.Fatal(queue, err)
			}
			current, err := s.readRecord(ctx, key)
			if err != nil || current.Delivery == nil || current.Delivery.Reason != msg.UndeliveredReasonLost {
				t.Fatal(current, err)
			}
			events, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
			if err != nil || len(events) != 1 {
				t.Fatal(events, err)
			}
			*now = now.Add(2 * time.Hour)
			if err := s.sweepExpiry(ctx); err != nil {
				t.Fatal(err)
			}
			current, err = s.readRecord(ctx, key)
			if err != nil || current.Profile != "messages-expired@1" || len(current.Spec) != 0 {
				t.Fatal("damaged state prevented body expiry", current, err)
			}
		})
	}
}

func TestMessagesDeliveryReportSharesBodyPageWithIncomingQueue(t *testing.T) {
	s, _, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	out := s.accept(ctx, actor, "native-claude", request, false, "")
	if out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	key := s.key(actor, request.IdempotencyKey)
	record, err := s.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.finishDelivery(ctx, key, record.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonRefused); err != nil {
		t.Fatal(err)
	}
	permitted := map[string]string{key: record.Token}
	other := messageActor{Account: actor.Account, Program: "native-claude", Application: "claude", Context: "receiver"}
	for i := 0; i < 10; i++ {
		in := msg.SendRequest{To: "app:codex", Context: actor.Context, Kind: msg.KindNote, Body: fmt.Sprintf("incoming%d", i), IdempotencyKey: fmt.Sprintf("incoming%d", i), ExpiresUnixMs: request.ExpiresUnixMs}
		if out := s.accept(ctx, other, actor.Program, in, false, ""); out.Outcome != msg.OutcomeSent {
			t.Fatal(out)
		}
		k := s.key(other, in.IdempotencyKey)
		r, e := s.readRecord(ctx, k)
		if e != nil {
			t.Fatal(e)
		}
		permitted[k] = r.Token
	}
	events, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
	if err != nil {
		t.Fatal(err)
	}
	page, _ := s.deliveryPage(ctx, actor.Account, actor.Program, actor.Context, "worker", "", permitted, events)
	if page.Outcome != msg.OutcomePage || len(page.Messages) != 10 {
		t.Fatal(page)
	}
	reports := 0
	for _, message := range page.Messages {
		if message.Header.Delivery != nil {
			reports++
			if message.Body != "" {
				t.Fatal("report contains body", message)
			}
		}
	}
	if reports != 1 {
		t.Fatal("incoming queue starved delivery report", page)
	}
}

func TestMessagesDeliveryExpiryReportSurvivesRestartAndKeyReuse(t *testing.T) {
	for _, kind := range []msg.Kind{msg.KindNote, msg.KindQuestion, msg.KindReply} {
		t.Run(kind.String(), func(t *testing.T) {
			s, now, actor, request := messagesStoreFixture(t)
			ctx := context.Background()
			request.Kind = kind
			if kind == msg.KindQuestion {
				request.AnswerByUnixMs = request.ExpiresUnixMs
			}
			if kind == msg.KindReply {
				request.ReplyTo = "original-question"
			}
			out := s.accept(ctx, actor, "native-claude", request, false, "")
			if out.Outcome != msg.OutcomeSent {
				t.Fatal(out)
			}
			*now = now.Add(2 * time.Hour)
			if err := s.sweepExpiry(ctx); err != nil {
				t.Fatal(err)
			}
			record, err := s.readRecord(ctx, s.key(actor, request.IdempotencyKey))
			if err != nil || record == nil || record.Profile != "messages-expired@1" || len(record.Spec) != 0 || record.Delivery == nil || record.Delivery.State != msg.DeliveryUndelivered || record.Delivery.Reason != msg.UndeliveredReasonExpired {
				t.Fatal(record, err)
			}
			s, err = openMessageStore(filepath.Dir(s.dir), s.now)
			if err != nil {
				t.Fatal(err)
			}
			request.Body = "new acceptance"
			request.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
			if kind == msg.KindQuestion {
				request.AnswerByUnixMs = request.ExpiresUnixMs
			}
			if fresh := s.accept(ctx, actor, "native-claude", request, false, ""); fresh.Outcome != msg.OutcomeSent || fresh.MessageID == out.MessageID {
				t.Fatal(fresh)
			}
			refs, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
			if err != nil || len(refs) != 1 {
				t.Fatal(refs, err)
			}
			h, err := msg.DecodeHeader([]byte(refs[0].DeliveryReport))
			if err != nil || h.MessageID != out.MessageID || h.Delivery == nil || *h.Delivery != msg.DeliveryUndelivered || h.Undelivered == nil || *h.Undelivered != msg.UndeliveredReasonExpired || h.Count == nil || *h.Count != 1 {
				t.Fatal(h, err)
			}
			permitted := map[string]string{refs[0].Key: refs[0].Token}
			page, _ := s.deliveryPage(ctx, actor.Account, actor.Program, actor.Context, "sender", "", permitted, refs)
			if page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Messages[0].Header.MessageID != out.MessageID || page.Messages[0].Body != "" {
				t.Fatal(page)
			}
			after, _ := s.deliveryPage(ctx, actor.Account, actor.Program, actor.Context, "sender", page.Cursor, permitted, refs)
			if after.Outcome != msg.OutcomePage || len(after.Messages) != 0 {
				t.Fatal("ACK replayed report", after)
			}
			refs, err = s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
			if err != nil || len(refs) != 0 {
				t.Fatal("report survived its ACK", refs, err)
			}
		})
	}
}

func TestMessagesDeliverySuccessRequiresOptInAndCannotBecomeFailure(t *testing.T) {
	for _, notify := range []bool{false, true} {
		s, now, actor, request := messagesStoreFixture(t)
		ctx := context.Background()
		request.NotifyDelivered = new(notify)
		if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeSent {
			t.Fatal(out)
		}
		key := s.key(actor, request.IdempotencyKey)
		record, err := s.readRecord(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.acknowledge(ctx, messagePointer{Key: key, Token: record.Token}); err != nil {
			t.Fatal(err)
		}
		if err := s.finishDelivery(ctx, key, record.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonRefused); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(2 * time.Hour)
		if err := s.sweepExpiry(ctx); err != nil {
			t.Fatal(err)
		}
		record, err = s.readRecord(ctx, key)
		if err != nil || record.Delivery == nil || record.Delivery.State != msg.DeliveryDelivered {
			t.Fatal(record, err)
		}
		refs, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
		want := 0
		if notify {
			want = 1
		}
		if err != nil || len(refs) != want {
			t.Fatal(notify, refs, err)
		}
	}
}

func TestMessagesDeliveryPublicationCrashRecoversOneTerminalReport(t *testing.T) {
	for _, point := range []string{"event_pointer_appended", "event_state_committed"} {
		t.Run(point, func(t *testing.T) {
			s, now, actor, request := messagesStoreFixture(t)
			ctx := context.Background()
			out := s.accept(ctx, actor, "native-claude", request, false, "")
			if out.Outcome != msg.OutcomeSent {
				t.Fatal(out)
			}
			*now = now.Add(2 * time.Hour)
			s.boundary = func(at string) error {
				if at == point {
					return errors.New("inert crash")
				}
				return nil
			}
			if err := s.sweepExpiry(ctx); err == nil {
				t.Fatal("crash seam not reached")
			}
			pending, err := s.readRecord(ctx, s.key(actor, request.IdempotencyKey))
			if err != nil || pending == nil || pending.Profile != "messages-expired@1" || pending.Delivery == nil || pending.Delivery.Reported || len(pending.Spec) != 0 {
				t.Fatal("expiry must purge body while retaining terminal report custody", pending, err)
			}
			replacement := request
			replacement.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
			if reused := s.accept(ctx, actor, "native-claude", replacement, false, ""); reused.Outcome != msg.OutcomeUnknown {
				t.Fatal("request-key reuse erased pending report custody", reused)
			}
			reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
			if err != nil {
				t.Fatal(err)
			}
			if err := reopened.sweepExpiry(ctx); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				refs, err := reopened.messageEventCandidates(ctx, actor, reopened.stateEventCheckFor(actor))
				if err != nil || len(refs) != 1 {
					t.Fatal("restart duplicated or lost terminal report", refs, err)
				}
			}
		})
	}
}

func TestMessagesDeliveryAckCrossingExpiryNeverPublishesSuccess(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	request.NotifyDelivered = new(true)
	if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	key := s.key(actor, request.IdempotencyKey)
	record, err := s.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	advanced := false
	s.boundary = func(at string) error {
		if at == "delivery_before_terminal" && !advanced {
			*now = now.Add(2 * time.Hour)
			advanced = true
		}
		return nil
	}
	if err := s.acknowledge(ctx, messagePointer{Key: key, Token: record.Token}); err != nil {
		t.Fatal(err)
	}
	if err := s.sweepExpiry(ctx); err != nil {
		t.Fatal(err)
	}
	refs, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
	if err != nil || len(refs) != 1 {
		t.Fatal(refs, err)
	}
	header, err := msg.DecodeHeader([]byte(refs[0].DeliveryReport))
	if err != nil || header.Delivery == nil || *header.Delivery != msg.DeliveryUndelivered || header.Undelivered == nil || *header.Undelivered != msg.UndeliveredReasonExpired {
		t.Fatal(header, err)
	}
}

// A completed question was answered, which proves it reached its receiver
// (R19b): its expiry reports it delivered and keeps the answer completed.
func TestMessagesDeliveryExpiryKeepsQuestionAnswerAxis(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	request.Kind = msg.KindQuestion
	request.AnswerByUnixMs = request.ExpiresUnixMs
	if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	key := s.key(actor, request.IdempotencyKey)
	record, err := s.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.transitionState(ctx, key, record, "complete"); err != nil {
		t.Fatal(err)
	}
	original := *record
	*now = now.Add(2 * time.Hour)
	if err := s.sweepExpiry(ctx); err != nil {
		t.Fatal(err)
	}
	record, err = s.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := s.currentState(ctx, key, &original)
	if err != nil || answer.TaskState != msg.TaskStateCompleted || record.Delivery == nil || record.Delivery.State != msg.DeliveryDelivered || record.Delivery.Reason != "" {
		t.Fatal("an answered question expired undelivered, or delivery changed the answer outcome", record, answer, err)
	}
}

// Review of the replay growth: a sender inbox whose page grows by delivery
// reports on every read without after keeps answering pages, and its cursor
// file stays within its bound.
func TestMessagesDeliveryReportGrowthKeepsCursorFileBounded(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	request.Subject = strings.Repeat("s", 120)
	start := *now
	for i := 0; i < 14; i++ {
		request.IdempotencyKey = fmt.Sprintf("report-%02d", i)
		request.ExpiresUnixMs = start.Add(time.Duration(i+1) * time.Minute).UnixMilli()
		if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeSent {
			t.Fatal(out)
		}
	}
	path := filepath.Join(s.dir, "cursors", messageDigest(actor.Account+"\x00"+actor.Program+"\x00"+actor.Context)+".json")
	cursors := map[string]bool{}
	for i := 0; i < 14; i++ {
		*now = start.Add(time.Duration(i+1)*time.Minute + time.Second)
		if err := s.sweepExpiry(ctx); err != nil {
			t.Fatal(err)
		}
		refs, err := s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
		if err != nil {
			t.Fatal(err)
		}
		permitted := map[string]string{}
		for _, ref := range refs {
			permitted[ref.Key] = ref.Token
		}
		page, _ := s.deliveryPage(ctx, actor.Account, actor.Program, actor.Context, "sender", "", permitted, refs)
		if page.Outcome != msg.OutcomePage || len(page.Messages) != min(i+1, 10) {
			t.Fatal("read without after lost its page", i, page.Outcome, len(page.Messages))
		}
		cursors[page.Cursor] = true
		info, err := os.Stat(path)
		if err != nil || info.Size() > messageDeliveryLimit {
			t.Fatal("delivery cursor file outgrew its bound", i, info, err)
		}
	}
	if len(cursors) != 10 {
		t.Fatal("report growth did not mint one cursor per grown page", len(cursors))
	}
}
