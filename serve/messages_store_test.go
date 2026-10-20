package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func messagesStoreFixture(t *testing.T) (*messageStore, *time.Time, messageActor, msg.SendRequest) {
	t.Helper()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s, err := openMessageStore(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	a := messageActor{Account: "account", Program: "native-codex", Application: "codex", Context: "source"}
	r := msg.SendRequest{To: "app:claude", Context: "receiver", Kind: msg.KindNote, Subject: "one line", Body: "untrusted body", IdempotencyKey: "key", ExpiresUnixMs: now.Add(time.Hour).UnixMilli()}
	return s, &now, a, r
}

func messagesReadPair(t *testing.T, s *messageStore, actor messageActor, receiver string) messagePair {
	t.Helper()
	raw, err := cas.ReadLimitContext(context.Background(), s.pairPath(actor, receiver), messageIndexLimit)
	if err != nil {
		t.Fatal(err)
	}
	p, err := readMessagePair(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMessagesFreshTypedNotesPublishAcrossIndependentInboxes(t *testing.T) {
	s, _, actor, request := messagesStoreFixture(t)
	actor.Chat = &messageChatRoute{SourceInstance: "native-instance", SourceProcess: messageDigest("original process"), ReturnContext: "canonical-return"}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ctx = withMessageOrigin(ctx, []string{"waking-question"})
	entered, release := make(chan struct{}), make(chan struct{})
	var stalled sync.Once
	s.boundary = func(point string) error {
		if point == "before_record" {
			first := false
			stalled.Do(func() { first = true })
			if first {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return nil
	}
	a := make(chan msg.SendResult, 1)
	go func() { a <- s.accept(ctx, actor, "native-claude", request, false, "") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first note did not reach publication")
	}
	contender, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	if result := s.accept(contender, actor, "native-claude", request, false, ""); result.Outcome != msg.OutcomeUnknown || contender.Err() == nil {
		stop()
		t.Fatal("same-key contender did not wait for the owner", result, contender.Err())
	}
	stop()
	other := request
	other.To, other.Context, other.IdempotencyKey = "app:opencode", "other-receiver", "other-key"
	b := make(chan msg.SendResult, 1)
	go func() { b <- s.accept(ctx, actor, "native-opencode", other, false, "") }()
	var second msg.SendResult
	select {
	case second = <-b:
	case <-ctx.Done():
		t.Fatal("other inbox could not publish while first was stalled")
	}
	close(release)
	first := <-a
	if first.Outcome != msg.OutcomeSent || second.Outcome != msg.OutcomeSent || first.MessageID == second.MessageID {
		t.Fatal(first, second)
	}
	for i, item := range []struct {
		receiver string
		request  msg.SendRequest
		result   msg.SendResult
	}{{"native-claude", request, first}, {"native-opencode", other, second}} {
		refs, err := s.queueCandidates(ctx, actor.Account, item.receiver, item.request.Context)
		if err != nil || len(refs) != 1 || refs[0].ID != item.result.MessageID {
			t.Fatal(i, refs, err)
		}
	}
	conflict := other
	conflict.IdempotencyKey = request.IdempotencyKey
	if result := s.accept(ctx, actor, "native-opencode", conflict, false, ""); result.Outcome != msg.OutcomeConflict {
		t.Fatal("cross-target key did not retain its original owner", result)
	}
	if result := s.accept(ctx, actor, "native-claude", request, false, ""); result.Outcome != msg.OutcomeSent || result.MessageID != first.MessageID {
		t.Fatal("same request did not recover its original receipt", result)
	}
	key := s.key(actor, request.IdempotencyKey)
	record, err := s.readRecord(ctx, key)
	if err != nil || record == nil || record.Actor.Chat == nil || *record.Actor.Chat != *actor.Chat || len(record.Origin) != 1 || record.Origin[0] != "waking-question" {
		t.Fatal("typed provenance or wake origin was lost", record, err)
	}
	alias := request
	alias.IdempotencyKey = "same-content-new-key"
	if result := s.accept(ctx, actor, "native-claude", alias, false, ""); result.Outcome != msg.OutcomeSent || result.MessageID != first.MessageID {
		t.Fatal("typed content did not replay within its occurrence", result)
	}
	for _, route := range []messageChatRoute{
		{SourceInstance: actor.Chat.SourceInstance, SourceProcess: messageDigest("different process"), ReturnContext: actor.Chat.ReturnContext},
		{SourceInstance: actor.Chat.SourceInstance, SourceProcess: actor.Chat.SourceProcess, ReturnContext: "different-return"},
	} {
		changed := actor
		changed.Chat = &route
		if result := s.accept(ctx, changed, "native-claude", request, false, ""); result.Outcome != msg.OutcomeConflict {
			t.Fatal("canonical replay crossed a typed provenance boundary", result)
		}
		alias.IdempotencyKey += "-distinct"
		if result := s.accept(ctx, changed, "native-claude", alias, false, ""); result.Outcome != msg.OutcomeSent || result.MessageID == first.MessageID {
			t.Fatal("content replay crossed a typed provenance boundary", result)
		}
	}
}

func TestMessagesStoreCrashBoundariesAndRecovery(t *testing.T) {
	for _, point := range []string{"reserved", "before_record", "expiry_linked", "record_published", "index_published"} {
		t.Run(point, func(t *testing.T) {
			s, _, actor, request := messagesStoreFixture(t)
			s.boundary = func(at string) error {
				if at == point {
					return errors.New("inert crash")
				}
				return nil
			}
			first := s.accept(context.Background(), actor, "native-claude", request, false, "")
			if first.Outcome != msg.OutcomeUnknown {
				t.Fatal("crash became success", first)
			}
			canonical, err := s.readRecord(context.Background(), s.key(actor, request.IdempotencyKey))
			if err != nil {
				t.Fatal(err)
			}
			published := point == "record_published" || point == "index_published"
			if (canonical != nil) != published {
				t.Fatal("canonical publication boundary", canonical, point)
			}
			reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
			if err != nil {
				t.Fatal(err)
			}
			retry := reopened.accept(context.Background(), actor, "native-claude", request, false, "")
			if retry.Outcome != msg.OutcomeSent || published && retry.MessageID != canonical.ID {
				t.Fatal("key reconciliation", retry)
			}
			request.IdempotencyKey, request.Body = "next", "different content"
			if next := reopened.accept(context.Background(), actor, "native-claude", request, false, ""); next.Outcome != msg.OutcomeSent {
				t.Fatal(next)
			}
			if p := messagesReadPair(t, reopened, actor, "native-claude"); p.Count != 2 || len(p.Pointers) != 2 {
				t.Fatal("recovery did not precede fresh budget", p)
			}
		})
	}
}

func TestMessagesStoreAliasesAreDurableAndWorkIsPaged(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	first := s.accept(context.Background(), actor, "native-claude", request, true, "")
	if first.Outcome != msg.OutcomeSent {
		t.Fatal(first)
	}
	reads, maxPage := 0, 0
	s.inspect = func(kind string, n int) {
		if kind == "record_read" {
			reads += n
		}
		if kind == "expiry_page_entries" && n > maxPage {
			maxPage = n
		}
	}
	started := time.Now()
	for i := 0; i < 300; i++ {
		request.IdempotencyKey = "alias-" + strconv.Itoa(i)
		reads = 0
		alias := s.accept(context.Background(), actor, "native-claude", request, false, "")
		if alias.Outcome != msg.OutcomeSent || alias.MessageID != first.MessageID || !alias.AwaitingPermission || reads > 3 {
			t.Fatal("alias authority/work", i, alias, reads)
		}
		if retry := s.accept(context.Background(), actor, "native-claude", request, false, ""); retry.MessageID != first.MessageID {
			t.Fatal("alias replay", retry)
		}
		changed := request
		changed.Body = "changed"
		if got := s.accept(context.Background(), actor, "native-claude", changed, false, ""); got.Outcome != msg.OutcomeConflict {
			t.Fatal("alias lost immutable spec", got)
		}
	}
	t.Logf("300 aliases plus replay/conflict: %s; max page entries %d", time.Since(started), maxPage)
	if maxPage > messageExpiryPageEntries {
		t.Fatal("unbounded page", maxPage)
	}
	if p := messagesReadPair(t, s, actor, "native-claude"); p.Count != 1 || len(p.Pointers) != 1 {
		t.Fatal("alias charged budget or grew pair history", p)
	}
	*now = now.Add(2 * time.Hour)
	for i := 0; i < 8; i++ {
		expires := 0
		s.inspect = func(kind string, n int) {
			if kind == "expire_record" {
				expires += n
			}
		}
		if err := s.sweepExpiry(context.Background()); err != nil {
			t.Fatal(err)
		}
		if expires > messageExpiryWork {
			t.Fatal("unbounded expiry work", expires)
		}
	}
	for _, key := range []string{"key", "alias-0", "alias-299"} {
		r, err := s.readRecord(context.Background(), s.key(actor, key))
		if err != nil || r == nil || r.Profile != "messages-expired@1" || len(r.Spec) != 0 {
			t.Fatal("expired body retained", key, r, err)
		}
	}
}

func TestMessagesStoreInboxCapacityAcrossSendersAndKeyBypassesPause(t *testing.T) {
	s, _, actor, request := messagesStoreFixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	sent, exhausted := 0, 0
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, r := actor, request
			a.Program = fmt.Sprintf("sender-%d", i/20)
			r.IdempotencyKey, r.Body = fmt.Sprintf("key-%d", i), fmt.Sprintf("content-%d", i)
			result := s.accept(context.Background(), a, "native-claude", r, false, "")
			mu.Lock()
			defer mu.Unlock()
			switch result.Outcome {
			case msg.OutcomeSent:
				sent++
			case msg.OutcomeExhausted:
				exhausted++
			default:
				t.Errorf("unexpected result %s", result.Outcome.String())
			}
		}(i)
	}
	wg.Wait()
	if sent != 50 || exhausted != 10 {
		t.Fatal("cross-sender inbox capacity", sent, exhausted)
	}

	// A full direction, and a second exhaustion that pauses it, cannot turn an
	// accepted-key retry into exhausted or mint a second acceptance.
	s2, _, a, r := messagesStoreFixture(t)
	first := s2.accept(context.Background(), a, "receiver", r, false, "")
	for i := 1; i < 22; i++ {
		other := r
		other.IdempotencyKey, other.Body = strconv.Itoa(i), strconv.Itoa(i)
		s2.accept(context.Background(), a, "receiver", other, false, "")
	}
	if p := messagesReadPair(t, s2, a, "receiver"); !p.paused(s2.now().UnixMilli()) || p.Count != 20 {
		t.Fatal("direction pause", p)
	}
	if got := s2.accept(context.Background(), a, "receiver", r, false, ""); got.Outcome != msg.OutcomeSent || got.MessageID != first.MessageID {
		t.Fatal("key lookup followed limits", got)
	}
}

func TestMessagesStoreReservationFencesAnotherInstanceAndExpiredReuse(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	reserved, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(finish)
	s.boundary = func(point string) error {
		if point == "reserved" {
			close(reserved)
			<-release
		}
		return nil
	}
	result := make(chan msg.SendResult, 1)
	go func() { result <- s.accept(context.Background(), actor, "receiver", request, false, "") }()
	select {
	case <-reserved:
	case early := <-result:
		t.Fatal("publication ended before reserved boundary", early)
	case <-time.After(3 * time.Second):
		t.Fatal("reservation boundary did not arrive")
	}
	other, err := openMessageStore(filepath.Dir(s.dir), s.now)
	if err != nil {
		t.Fatal(err)
	}
	r2 := request
	r2.IdempotencyKey, r2.Body = "other", "other"
	if got := other.accept(context.Background(), actor, "receiver", r2, false, ""); got.Outcome != msg.OutcomeSent {
		finish()
		t.Fatal(got)
	}
	finish()
	select {
	case got := <-result:
		if got.Outcome != msg.OutcomeUnknown {
			t.Fatal("stale publisher bypassed reservation", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale publisher did not terminate")
	}
	if r, err := other.readRecord(context.Background(), other.key(actor, request.IdempotencyKey)); err != nil || r != nil {
		t.Fatal("unreserved publication", r, err)
	}
	if got := other.accept(context.Background(), actor, "receiver", request, false, ""); got.Outcome != msg.OutcomeSent {
		t.Fatal("canceled reservation prevented explicit retry", got)
	}

	// The old expiry reference must never erase a newer acceptance of a key.
	request.IdempotencyKey, request.ExpiresUnixMs = "short", now.Add(time.Minute).UnixMilli()
	if got := other.accept(context.Background(), actor, "receiver", request, false, ""); got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	*now = now.Add(2 * time.Minute)
	request.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
	newer := other.accept(context.Background(), actor, "receiver", request, false, "")
	if newer.Outcome != msg.OutcomeSent {
		t.Fatal("expired-key reuse", newer)
	}
	if err := other.sweepExpiry(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, err := other.readRecord(context.Background(), other.key(actor, "short"))
	if err != nil || r == nil || r.ID != newer.MessageID || len(r.Spec) == 0 {
		t.Fatal("old cleanup erased new key acceptance", r, err)
	}
	if p := messagesReadPair(t, other, actor, "receiver"); p.Count != 4 {
		t.Fatal("expiry refunded an hourly acceptance", p.Count)
	}
}

func TestMessagesStoreExpiryBeforeCanonicalAndMissingTailRefuse(t *testing.T) {
	t.Run("expiry-between-pointer-and-record", func(t *testing.T) {
		s, now, actor, request := messagesStoreFixture(t)
		s.boundary = func(point string) error {
			if point == "expiry_linked" {
				*now = now.Add(2 * time.Hour)
				return s.sweepExpiry(context.Background())
			}
			return nil
		}
		if got := s.accept(context.Background(), actor, "receiver", request, false, ""); got.Outcome != msg.OutcomeUnknown {
			t.Fatal("expired publication reported sent", got)
		}
		if r, err := s.readRecord(context.Background(), s.key(actor, request.IdempotencyKey)); err != nil || r != nil {
			t.Fatal("body published after its expiry reference was swept", r, err)
		}
	})
	t.Run("missing-referenced-tail", func(t *testing.T) {
		s, _, actor, request := messagesStoreFixture(t)
		first := s.accept(context.Background(), actor, "receiver", request, false, "")
		if first.Outcome != msg.OutcomeSent {
			t.Fatal(first)
		}
		raw, err := cas.ReadLimitContext(context.Background(), filepath.Join(s.dir, "expiry", "head.json"), 4096)
		if err != nil {
			t.Fatal(err)
		}
		h, err := readMessageExpiryHead(raw)
		if err != nil || h.Last == "" {
			t.Fatal(h, err)
		}
		if err = os.Remove(s.expiryPagePath(h.Last)); err != nil {
			t.Fatal(err)
		}
		request.IdempotencyKey, request.Body = "new", "different"
		if got := s.accept(context.Background(), actor, "receiver", request, false, ""); got.Outcome != msg.OutcomeUnknown {
			t.Fatal("missing authoritative expiry page replaced", got)
		}
		if r, err := s.readRecord(context.Background(), s.key(actor, request.IdempotencyKey)); err != nil || r != nil {
			t.Fatal("new acceptance published without cleanup index", r, err)
		}
	})
}

func TestMessagesStoreExpiryAndAnswerDeadlineAreSemantic(t *testing.T) {
	for _, change := range []string{"later-expiry", "earlier-expiry", "answer-deadline"} {
		t.Run(change, func(t *testing.T) {
			s, now, actor, original := messagesStoreFixture(t)
			original.Kind, original.AnswerByUnixMs = msg.KindQuestion, now.Add(30*time.Minute).UnixMilli()
			first := s.accept(context.Background(), actor, "receiver", original, false, "")
			if first.Outcome != msg.OutcomeSent {
				t.Fatal(first)
			}
			changed := original
			switch change {
			case "later-expiry":
				changed.ExpiresUnixMs += int64(time.Minute / time.Millisecond)
			case "earlier-expiry":
				changed.ExpiresUnixMs -= int64(time.Minute / time.Millisecond)
			case "answer-deadline":
				changed.AnswerByUnixMs += int64(time.Minute / time.Millisecond)
			}
			if got := s.accept(context.Background(), actor, "receiver", changed, false, ""); got.Outcome != msg.OutcomeConflict {
				t.Fatal("same-key deadline change accepted", got)
			}
			changed.IdempotencyKey = "new-semantic-request"
			second := s.accept(context.Background(), actor, "receiver", changed, false, "")
			if second.Outcome != msg.OutcomeSent || second.MessageID == first.MessageID {
				t.Fatal("changed deadline quietly coalesced", second)
			}
			alias := original
			alias.IdempotencyKey = "equal-alias"
			if got := s.accept(context.Background(), actor, "receiver", alias, false, ""); got.MessageID != first.MessageID {
				t.Fatal("equal content lost original receipt", got)
			}
			r, err := s.readRecord(context.Background(), s.key(actor, alias.IdempotencyKey))
			if err != nil || r == nil || r.ExpiresUnixMs != original.ExpiresUnixMs || !r.Alias {
				t.Fatal("alias changed retention", r, err)
			}
			stored, err := decodeRetainedMessageRequest(r.Spec)
			if err != nil || stored.AnswerByUnixMs != original.AnswerByUnixMs || stored.ExpiresUnixMs != original.ExpiresUnixMs {
				t.Fatal("alias changed canonical deadline", stored, err)
			}
			if p := messagesReadPair(t, s, actor, "receiver"); p.Count != 2 || len(p.Pointers) != 2 {
				t.Fatal("deadline or alias budget drift", p)
			}
		})
	}
}
