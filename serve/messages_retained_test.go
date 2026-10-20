package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestMessagesRetainedSchemaPreservesAcceptedCustodyAcrossRestart(t *testing.T) {
	for _, kind := range []msg.Kind{msg.KindNote, msg.KindQuestion, msg.KindReply} {
		t.Run(kind.String(), func(t *testing.T) {
			s, now, actor, request := messagesStoreFixture(t)
			request.Kind = kind
			request.Body = `request_key "expires_at" <>& pairing_generation`
			if kind == msg.KindQuestion {
				request.AnswerByUnixMs = now.Add(time.Minute).UnixMilli()
			}
			if kind == msg.KindReply {
				request.ReplyTo = "old-app-counter-1"
			}
			ctx := context.Background()
			first := s.accept(ctx, actor, "native-claude", request, false, "")
			if first.Outcome != msg.OutcomeSent {
				t.Fatal(first)
			}
			key := s.key(actor, request.IdempotencyKey)
			record, err := s.readRecord(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			prior, err := messageRenameRetained(record.Spec, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := msg.DecodeSendRequest(prior); err == nil {
				t.Fatal("old wire admitted publicly")
			}
			record.Spec = prior
			raw, _ := json.Marshal(record)
			if err := os.WriteFile(s.recordPath(key), raw, 0600); err != nil {
				t.Fatal(err)
			}
			stateRaw, err := os.ReadFile(s.statePath(key, record.Token))
			if err != nil {
				t.Fatal(err)
			}
			var state messageState
			if err := json.Unmarshal(stateRaw, &state); err != nil {
				t.Fatal(err)
			}
			state.Acceptance.Digest = messageDigest(string(prior))
			stateRaw, _ = json.Marshal(state)
			if err := os.WriteFile(s.statePath(key, record.Token), stateRaw, 0600); err != nil {
				t.Fatal(err)
			}
			reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
			if err != nil {
				t.Fatal(err)
			}
			if replay := reopened.accept(ctx, actor, "native-claude", request, false, ""); replay.Outcome != msg.OutcomeSent || replay.MessageID != first.MessageID {
				t.Fatal("original receipt", replay)
			}
			changed := request
			changed.Body += " changed"
			if replay := reopened.accept(ctx, actor, "native-claude", changed, false, ""); replay.Outcome != msg.OutcomeConflict {
				t.Fatal("changed payload", replay)
			}
			// The old content index used the preceding canonical field spellings.
			contentRequest := request
			contentRequest.IdempotencyKey = "content"
			contentSpec, err := messageRenameRetained(msg.EncodeSendRequest(&contentRequest), false)
			if err != nil {
				t.Fatal(err)
			}
			pair := messagesReadPair(t, reopened, actor, "native-claude")
			pair.Pointers[0].Content = messageDigest(actor.Context + "\x00" + string(contentSpec))
			pairRaw, _ := json.Marshal(pair)
			if err := os.WriteFile(reopened.pairPath(actor, "native-claude"), pairRaw, 0600); err != nil {
				t.Fatal(err)
			}
			alias := request
			alias.IdempotencyKey = "current-key-old-content"
			if replay := reopened.accept(ctx, actor, "native-claude", alias, false, ""); replay.Outcome != msg.OutcomeSent || replay.MessageID != first.MessageID {
				t.Fatal("old content alias", replay)
			}
			kept, err := reopened.readRecord(ctx, key)
			if err != nil || !bytes.Equal(kept.Spec, prior) {
				t.Fatal("immutable accepted bytes", err)
			}
			decoded, err := decodeRetainedMessageRequest(kept.Spec)
			if err != nil || decoded.Body != request.Body || decoded.AnswerByUnixMs != request.AnswerByUnixMs {
				t.Fatal("semantic request", decoded, err)
			}
			page := reopened.bodyPage(ctx, actor.Account, "native-claude", request.Context, "actual-native-owner", "", map[string]string{key: kept.Token})
			if page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Messages[0].Body != request.Body || page.Messages[0].IdempotencyKey != request.IdempotencyKey {
				t.Fatal("old pending body", page)
			}
			if err := reopened.finishDelivery(ctx, key, kept.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonRefused); err != nil {
				t.Fatal(err)
			}
			refs, err := reopened.messageEventCandidates(ctx, actor, reopened.stateEventCheckFor(actor))
			if err != nil || len(refs) != 1 {
				t.Fatal("delivery report", refs, err)
			}
			*now = now.Add(2 * time.Hour)
			if err := reopened.expireRecord(ctx, messagePointer{Key: key, Token: kept.Token}); err != nil {
				t.Fatal("expiry", err)
			}
			terminal, err := reopened.readRecord(ctx, key)
			if err != nil || terminal == nil || terminal.Delivery == nil || terminal.Delivery.State != msg.DeliveryUndelivered {
				t.Fatal("retained terminal", terminal, err)
			}
		})
	}
}

func TestMessagesRetainedHeaderExactPriorBoundary(t *testing.T) {
	h := msg.Header{MessageID: "old-1", From: "app:from", FromContext: "source", To: "app:to", Context: "target", Kind: msg.KindQuestion, ContextID: "thread", ExpiresUnixMs: 2000, AnswerByUnixMs: 1500, Subject: `expires_at "answer_by" <>&`}
	current := msg.EncodeHeader(&h)
	prior, err := messageRenameRetained(current, false)
	if err != nil {
		t.Fatal(err)
	}
	// An alternate JSON escape and whitespace remain original storage bytes.
	prior = bytes.Replace(prior, []byte(`<>&`), []byte(`\u003c\u003e\u0026`), 1)
	before := append([]byte(nil), prior...)
	got, err := decodeRetainedMessageHeader(prior)
	if err != nil || got.Subject != h.Subject || got.ExpiresUnixMs != h.ExpiresUnixMs || got.AnswerByUnixMs != h.AnswerByUnixMs || !bytes.Equal(prior, before) {
		t.Fatal("value/bytes changed", got, err)
	}
	for name, raw := range map[string][]byte{
		"unknown":   bytes.Replace(prior, []byte(`"expires_at":`), []byte(`"future":0,"expires_at":`), 1),
		"mixed":     bytes.Replace(prior, []byte(`"expires_at"`), []byte(`"expires_unix_ms"`), 1),
		"collision": bytes.Replace(prior, []byte(`"expires_at":`), []byte(`"expires_unix_ms":2000,"expires_at":`), 1),
		"duplicate": bytes.Replace(prior, []byte(`"expires_at":`), []byte(`"expires_at":2000,"expires_at":`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRetainedMessageHeader(raw); err == nil {
				t.Fatal("historical header admitted")
			}
		})
	}
	additive := bytes.Replace(current, []byte(`"expires_unix_ms":`), []byte(`"future":0,"expires_unix_ms":`), 1)
	if _, err := decodeRetainedMessageHeader(additive); err != nil {
		t.Fatal("current additive output policy changed", err)
	}
	_, _, _, request := messagesStoreFixture(t)
	encoded := msg.EncodeSendRequest(&request)
	old, err := messageRenameRetained(encoded, false)
	if err != nil || !messageRequestDigestMatches(messageDigest(string(old)), encoded) {
		t.Fatal("old reply/forward fingerprint", err)
	}
	request.Body += " changed"
	if messageRequestDigestMatches(messageDigest(string(old)), msg.EncodeSendRequest(&request)) {
		t.Fatal("changed fingerprint accepted")
	}
}

func TestMessagesRetainedSchemaIsClosedAndValuePreserving(t *testing.T) {
	_, _, _, request := messagesStoreFixture(t)
	current := msg.EncodeSendRequest(&request)
	prior, err := messageRenameRetained(current, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"mixed":     bytes.Replace(prior, []byte(`"request_key"`), []byte(`"idempotency_key"`), 1),
		"collision": bytes.Replace(prior, []byte(`"request_key":`), []byte(`"idempotency_key":"other", "request_key":`), 1),
		"duplicate": bytes.Replace(prior, []byte(`"request_key":`), []byte(`"request_key":"duplicate", "request_key":`), 1),
		"unknown":   bytes.Replace(prior, []byte(`"request_key":`), []byte(`"unexpected":0, "request_key":`), 1),
		"bound":     []byte(strings.Repeat(" ", messageRecordLimit+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRetainedMessageRequest(raw); err == nil {
				t.Fatal("invalid retained shape admitted")
			}
		})
	}
	h := msg.Header{MessageID: "old-1", From: "app:from", FromContext: "source", To: "app:to", Context: "target", Kind: msg.KindNote, ContextID: "thread", ExpiresUnixMs: request.ExpiresUnixMs}
	oldHeader, err := messageRenameRetained(msg.EncodeHeader(&h), false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeRetainedMessageHeader(oldHeader); err != nil || got.ExpiresUnixMs != h.ExpiresUnixMs {
		t.Fatal(got, err)
	}
	if _, err := msg.DecodeHeader(oldHeader); err == nil {
		t.Fatal("old public header accepted")
	}
}

func TestMessagesRightsRefusalPreservesKnownDecision(t *testing.T) {
	for _, outcome := range []rights.DecisionOutcome{rights.DecisionOutcomeDenied, rights.DecisionOutcomeNotGranted, rights.DecisionOutcomeUnavailable} {
		detail := messageRightsRefusal(rights.Decision{Outcome: outcome})
		if outcome == rights.DecisionOutcomeUnavailable {
			if detail != nil {
				t.Fatal(detail)
			}
			continue
		}
		if detail == nil || detail.Source != rights.RefusalSourceRights || detail.Action != ActionApplicationMessage {
			t.Fatal(detail)
		}
		result := msg.SendResult{Outcome: msg.OutcomeForbidden, Refusal: detail}
		if decoded, err := msg.DecodeSendResult(msg.EncodeSendResult(&result)); err != nil || decoded.Refusal == nil || decoded.Refusal.Decision != detail.Decision {
			t.Fatal(decoded, err)
		}
	}
}
