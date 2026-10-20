//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type messagesObserveRun struct {
	run                       *selfRegisteredRun
	reader, other             *messagesReceiver
	store                     *messageStore
	now                       *time.Time
	self, otherSubject        rights.Subject
	selfProgram, otherProgram string
}

func messagesObserveFixture(t *testing.T, personConsent ...bool) *messagesObserveRun {
	t.Helper()
	return messagesObserveFixtureNamed(t, "other-agent", "observer-peer.exe", personConsent...)
}

func TestMessagesAgentDirectoryRequiresMessagePermissionWithoutApplicationRead(t *testing.T) {
	f := messagesObserveFixtureNamed(t, "oa-claude-integration", "claude-directory-peer.exe")
	original := f.run.d.decide
	f.run.d.decide = func(ctx context.Context, subject rights.Subject, action, resource string) (bool, error) {
		if action == ActionApplicationRead {
			return false, nil
		}
		return original(ctx, subject, action, resource)
	}
	pin, err := f.other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, f.run.d.now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: "oa-claude-integration", LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "claude-chat"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	if opened, err := f.other.InboxOpen(presence.Instance, "claude-chat", "ready"); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	if page := f.run.d.observe(f.run.ctx, f.self, "", 0); len(page.Applications) != 0 {
		t.Fatal("message permission disclosed the application directory", page)
	}
	page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Inboxes) != 1 || page.Inboxes[0].Program != "app:oa-claude-integration" || page.Inboxes[0].Context != "claude-chat" {
		t.Fatal("authorized Claude conversation missing", page, err)
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:oa-claude-integration", false); err != nil {
		t.Fatal(err)
	}
	if page := f.run.d.observe(f.run.ctx, f.self, "", 0); len(page.Applications) != 0 {
		t.Fatal("revoked caller read applications", page)
	}
	page, err = messagesBaseListing(f.reader, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Inboxes) != 0 {
		t.Fatal("revoked caller discovered conversations", page, err)
	}
}

func TestMessagesInboxDisclosureAcrossNativeHeartbeat(t *testing.T) {
	f := messagesObserveFixture(t)
	var clock atomic.Int64
	clock.Store(f.run.d.now().UnixNano())
	f.run.d.now = func() time.Time { return time.Unix(0, clock.Load()) }
	pin, err := f.other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, f.run.d.now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "receiving"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	instance := presence.Instance
	opened, err := f.other.InboxOpen(instance, "receiving", "ready")
	if err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	observer, outcome := f.reader.messageObserver("")
	if observer == nil {
		t.Fatal(outcome)
	}
	defer observer.pin.Close()
	before, digest, err := observer.inboxSnapshot()
	if err != nil || len(before) != 1 {
		t.Fatal(before, err)
	}
	// The ordinary renewal crosses the two captures of one fresh listing.
	clock.Add(int64(time.Second))
	renewed, err := f.other.InboxOpen(instance, "receiving", "ready")
	if err != nil || renewed.Outcome != msg.OutcomeOpened || renewed.OpenedUnixMs != opened.OpenedUnixMs || renewed.Revision != opened.Revision {
		t.Fatal(opened, renewed, err)
	}
	after, current, err := observer.inboxSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if digest != current || before[0].LastSeenUnixMs == after[0].LastSeenUnixMs {
		t.Fatal("heartbeat changed snapshot identity or lost fresh observation")
	}
	if !messageInboxDisclosureEqual(before, after) {
		t.Fatal("ordinary renewal refused final disclosure")
	}
	if page := messageInboxesPage(after, current, digest+".0"); page.Outcome != msg.OutcomePage || page.Inboxes[0].LastSeenUnixMs != after[0].LastSeenUnixMs {
		t.Fatal("heartbeat cursor lost fresh observation", page)
	}
	if page := f.reader.inboxesList(""); page.Outcome != msg.OutcomePage || page.Inboxes[0].LastSeenUnixMs != after[0].LastSeenUnixMs {
		t.Fatal("fresh listing lost renewal observation", page)
	}
	if changed, err := f.other.InboxOpen(instance, "receiving", "changed"); err != nil || changed.Outcome != msg.OutcomeOpened {
		t.Fatal(changed, err)
	}
	changed, _, err := observer.inboxSnapshot()
	if err != nil || messageInboxDisclosureEqual(before, changed) {
		t.Fatal("changed status accepted", changed, err)
	}
	if page := f.reader.inboxesList(digest + ".0"); page.Outcome != msg.OutcomeGap {
		t.Fatal("status change retained old cursor", page)
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
		t.Fatal(err)
	}
	revoked, _, err := observer.inboxSnapshot()
	if err != nil || messageInboxDisclosureEqual(before, revoked) {
		t.Fatal("revoked disclosure accepted", revoked, err)
	}
}

// messagesObserveFixtureNamed registers the second agent as other, run from
// an image named leaf.
func messagesObserveFixtureNamed(t *testing.T, other, leaf string, personConsent ...bool) *messagesObserveRun {
	t.Helper()
	run, _ := messages1608Fixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	otherImage := filepath.Join(t.TempDir(), leaf)
	if err := os.WriteFile(otherImage, raw, 0700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, run, other, identity.NormalizeSubjectProgram(otherImage))
	agent1478Set(t, run, other, "", true)
	question := agent1478Question(t, run, other)
	answer(t, run.f, question.ID, "keep")
	agent1478Wait(t, run, other, "enrolled")
	call, _ := messages1608Call(t)
	otherCall, _ := messages1608CallImage(t, otherImage)
	pin, err := call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, self, _, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	pin, err = otherCall.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, otherSubject, _, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	policy := run.f.a.rights
	if err := policy.policy.RegisterAction(ActionApplicationMessage); err != nil {
		t.Fatal(err)
	}
	if len(personConsent) != 0 && personConsent[0] {
		policy.applications = run.d
		run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
		if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
			t.Fatal(err)
		}
		if err := policy.configureMessagePermits(run.d); err != nil {
			t.Fatal(err)
		}
	} else {
		for _, rule := range []struct {
			s        rights.Subject
			resource string
		}{{self, "app:" + other}, {otherSubject, "app:native-agent"}} {
			if err := policy.policy.Set(rule.s, ActionApplicationMessage, rule.resource, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Now()
	store, err := openMessageStore(run.state, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	host := &applicationsHost{directory: run.d, messages: store, messageRights: policy}
	return &messagesObserveRun{run: run, reader: &messagesReceiver{host: host, call: call, ctx: run.ctx}, other: &messagesReceiver{host: host, call: otherCall, ctx: run.ctx}, store: store, now: &now, self: self, otherSubject: otherSubject, selfProgram: identity.NormalizeSubjectProgram(exe), otherProgram: identity.NormalizeSubjectProgram(otherImage)}
}

func TestMessagesHeadersUnreadQueueBoundRefusalAndExpiry(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	var first string
	for i := 0; i < 50; i++ {
		if i != 0 && i%20 == 0 {
			*f.now = f.now.Add(time.Hour)
		}
		request := msg.SendRequest{To: "app:native-agent", Context: "bounded-summary", Kind: msg.KindNote, Body: fmt.Sprint(i), IdempotencyKey: fmt.Sprintf("bounded-summary-%d", i), ExpiresUnixMs: f.now.Add(24 * time.Hour).UnixMilli()}
		out := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
		if out.Outcome != msg.OutcomeSent {
			t.Fatal(i, out)
		}
		if i == 0 {
			first = out.MessageID
		}
	}
	page := f.reader.headersWait("bounded-summary", "", 0)
	if page.Unread == nil || *page.Unread != 50 || len(page.Headers) != 50 || page.PendingQuestions == nil || *page.PendingQuestions != 0 {
		t.Fatal(page)
	}
	request := msg.SendRequest{To: "app:native-agent", Context: "bounded-summary", Kind: msg.KindNote, Body: "overflow", IdempotencyKey: "bounded-summary-overflow", ExpiresUnixMs: f.now.Add(24 * time.Hour).UnixMilli()}
	if out := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, ""); out.Outcome != msg.OutcomeExhausted || out.RetryAfterMs != 1000 {
		t.Fatal("51st acceptance", out)
	}
	page = f.reader.headersWait("bounded-summary", "", 0)
	if page.Unread == nil || *page.Unread != 50 {
		t.Fatal("overflow changed count", page)
	}
	record, key, err := f.store.readByID(f.run.ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.acknowledge(f.run.ctx, messagePointer{Key: key, Token: record.Token}); err != nil {
		t.Fatal(err)
	}
	request.Kind, request.IdempotencyKey, request.Body = msg.KindQuestion, "bounded-refused", "question"
	request.AnswerByUnixMs = f.now.Add(time.Hour).UnixMilli()
	out := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
	if out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	record, key, err = f.store.readByID(f.run.ctx, out.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.transitionState(f.run.ctx, key, record, "refuse"); err != nil {
		t.Fatal(err)
	}
	page = f.reader.headersWait("bounded-summary", "", 0)
	if page.Unread == nil || *page.Unread != 49 || page.PendingQuestions == nil || *page.PendingQuestions != 0 {
		t.Fatal("refused question counted", page)
	}
	*f.now = f.now.Add(25 * time.Hour)
	page = f.reader.headersWait("bounded-summary", "", 0)
	if page.Unread == nil || *page.Unread != 0 || page.OldestUnreadUnixMs != nil {
		t.Fatal("expired queue counted", page)
	}
}

func TestMessagesHeadersUnreadNotesExcludeExpiredAndAcknowledgedWithoutConsumption(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	oldest := f.now.UnixMilli()
	for i := 0; i < 5; i++ {
		request := msg.SendRequest{To: "app:native-agent", Context: "summary", Kind: msg.KindNote, Body: fmt.Sprintf("note%d", i), IdempotencyKey: fmt.Sprintf("summary%d", i), ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
		if i == 3 {
			request.ExpiresUnixMs = f.now.Add(time.Millisecond).UnixMilli()
		}
		out := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
		if out.Outcome != msg.OutcomeSent {
			t.Fatal(out)
		}
		if i == 4 {
			record, key, err := f.store.readByID(f.run.ctx, out.MessageID)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.acknowledge(f.run.ctx, messagePointer{Key: key, Token: record.Token}); err != nil {
				t.Fatal(err)
			}
		}
	}
	*f.now = f.now.Add(2 * time.Millisecond)
	page := f.reader.headersWait("summary", "", 0)
	if page.Outcome != msg.OutcomePage || page.Unread == nil || *page.Unread != 3 || page.PendingQuestions == nil || *page.PendingQuestions != 0 || page.OldestUnreadUnixMs == nil || *page.OldestUnreadUnixMs != oldest {
		t.Fatal(page)
	}
	again := f.reader.headersWait("summary", "", 0)
	if again.Unread == nil || *again.Unread != 3 || again.Revision != page.Revision {
		t.Fatal("summary consumed or changed custody", again)
	}
	empty := f.reader.headersWait("empty-summary", "", 0)
	if empty.Unread == nil || *empty.Unread != 0 || empty.PendingQuestions == nil || *empty.PendingQuestions != 0 || empty.OldestUnreadUnixMs != nil {
		t.Fatal("empty was not explicitly reported", empty)
	}
	instance := messages1608Presence(t, f.run, f.reader.call, "", "summary")
	if out, err := f.reader.InboxOpen(instance, "summary", ""); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	body, err := f.reader.Wait(instance, "summary", msg.ViewBody, "", "", 0)
	if err != nil || body.Outcome != msg.OutcomePage || body.Unread != nil || body.PendingQuestions != nil || body.OldestUnreadUnixMs != nil {
		t.Fatal("body exposed summary", body, err)
	}
}

// A closed OpenCode chat wakes for a question sent after its last activity
// also when an older unread note waits beside it: the headers page carries
// the newest incoming acceptance beside the oldest.
func TestMessagesHeadersNewestUnreadIsNewestIncomingAcceptance(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	stale := f.now.UnixMilli()
	send := func(kind msg.Kind, key string) msg.SendResult {
		request := msg.SendRequest{To: "app:native-agent", Context: "newest", Kind: kind, Body: key, IdempotencyKey: key, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
		if kind == msg.KindQuestion {
			request.AnswerByUnixMs = f.now.Add(30 * time.Minute).UnixMilli()
		}
		out := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
		if out.Outcome != msg.OutcomeSent {
			t.Fatal(out)
		}
		return out
	}
	send(msg.KindNote, "stale-note")
	*f.now = f.now.Add(5 * time.Second)
	fresh := f.now.UnixMilli()
	send(msg.KindQuestion, "fresh-question")
	*f.now = f.now.Add(5 * time.Second)
	read := send(msg.KindNote, "read-note")
	record, key, err := f.store.readByID(f.run.ctx, read.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.acknowledge(f.run.ctx, messagePointer{Key: key, Token: record.Token}); err != nil {
		t.Fatal(err)
	}
	page := f.reader.headersWait("newest", "", 0)
	if page.Outcome != msg.OutcomePage || page.Unread == nil || *page.Unread != 2 || page.OldestUnreadUnixMs == nil || *page.OldestUnreadUnixMs != stale || page.NewestUnreadUnixMs == nil || *page.NewestUnreadUnixMs != fresh || page.OldestWake == "" || page.OldestWake >= page.Wake {
		t.Fatal("newest unread is not the fresh question", page)
	}
	empty := f.reader.headersWait("empty-newest", "", 0)
	if empty.Outcome != msg.OutcomePage || empty.NewestUnreadUnixMs != nil || empty.OldestWake != "" {
		t.Fatal("an empty inbox reported a newest unread", empty)
	}
}

func TestMessagesHeadersUnreadIncludesOutgoingDeliveryRecords(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.self.Account, Program: f.selfProgram, ObservedProgram: f.self.Program, Application: "native-agent", Context: "delivery-summary"}
	request := msg.SendRequest{To: "app:other-agent", Context: "receiving", Kind: msg.KindNote, Body: "inert", IdempotencyKey: "delivery-summary", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	out := f.store.accept(f.run.ctx, actor, f.otherProgram, request, false, "")
	if out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	record, key, err := f.store.readByID(f.run.ctx, out.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.finishDelivery(f.run.ctx, key, record.Token, msg.DeliveryUndelivered, msg.UndeliveredReasonRefused); err != nil {
		t.Fatal(err)
	}
	page := f.reader.headersWait("delivery-summary", "", 0)
	if page.Unread == nil || *page.Unread != 1 || page.PendingQuestions == nil || *page.PendingQuestions != 0 || page.OldestUnreadUnixMs == nil || *page.OldestUnreadUnixMs != f.now.UnixMilli() {
		t.Fatal(page)
	}
	refs, err := f.store.messageEventCandidates(f.run.ctx, actor, f.store.stateEventCheckFor(actor))
	if err != nil {
		t.Fatal(refs, err)
	}
	var reports []messageEventRef
	for _, ref := range refs {
		if ref.DeliveryReport != "" {
			reports = append(reports, ref)
		}
	}
	if len(reports) != 1 {
		t.Fatal("delivery report count differs from independent TaskState events", refs)
	}
	if err := f.store.observeDeliveryReport(f.run.ctx, actor, reports[0]); err != nil {
		t.Fatal(err)
	}
	page = f.reader.headersWait("delivery-summary", "", 0)
	if page.Unread == nil || *page.Unread != 0 || page.OldestUnreadUnixMs != nil {
		t.Fatal(page)
	}
}

func TestMessagesHeadersUnreadCountsFullOutgoingLogRecordUnits(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.self.Account, Program: f.selfProgram, ObservedProgram: f.self.Program, Application: "native-agent", Context: "full-summary"}
	seedMessagesFullDeliveryLog(t, f.store, actor, true)
	// One retained event already coalesces nine outcomes. Summary counts the
	// record once; canonical coalescing/restart is exercised by its owner test.
	if err := f.store.changeEventPage(f.run.ctx, f.store.eventPath(actor), fmt.Sprintf("%048x", 1), func(p *messageEventPage) error {
		h, err := msg.DecodeHeader([]byte(p.Entries[0].Ref.DeliveryReport))
		if err != nil {
			return err
		}
		h.Count = new(int64(9))
		p.Entries[0].Ref.DeliveryReport = string(msg.EncodeHeader(h))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page := f.reader.headersWait("full-summary", "", 0)
	if page.Unread == nil || *page.Unread != messageActorEventLimit || page.PendingQuestions == nil || *page.PendingQuestions != 0 || page.OldestUnreadUnixMs == nil || *page.OldestUnreadUnixMs != f.now.UnixMilli()-1000 || len(page.Headers) != 50 {
		t.Fatal(page)
	}
}

func TestMessagesHeadersNativeClosedContextAndCancellation(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	request := msg.SendRequest{To: "app:native-agent", Context: "closed", Kind: msg.KindNote, Subject: "sub\u202eject", Body: "PRIVATE_BODY_NOT_IN_HEADERS", IdempotencyKey: "headers-1", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	sent := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	page := f.reader.headersWait("closed", "", 0)
	if page.Outcome != msg.OutcomePage || len(page.Headers) != 1 || len(page.Messages) != 0 || page.Cursor != "" || page.Revision == "" || page.Headers[0].Subject != "subject" {
		t.Fatal(page)
	}
	if bytes.Contains(msg.EncodePage(&page), []byte(request.Body)) {
		t.Fatal("headers disclosed body")
	}
	if f.run.d.nativeInboxes != nil {
		t.Fatal("header observation created an inbox")
	}
	record, err := f.store.readRecord(f.run.ctx, f.store.key(actor, request.IdempotencyKey))
	if err != nil || record.Acked {
		t.Fatal(record, err)
	}
	ctx, cancel := context.WithCancel(f.run.ctx)
	pending := &messagesReceiver{host: f.reader.host, call: f.reader.call, ctx: ctx}
	done := make(chan msg.Page, 1)
	go func() { done <- pending.headersWait("closed", page.Revision, 1000) }()
	deadline := time.Now().Add(2 * time.Second)
	for !f.reader.host.messageIsWatching(f.run.ctx, f.self.Account, f.selfProgram, "closed") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !f.reader.host.messageIsWatching(f.run.ctx, f.self.Account, f.selfProgram, "closed") {
		cancel()
		t.Fatal("pending admitted native headers wait was not watching")
	}
	cancel()
	select {
	case ended := <-done:
		if len(ended.Headers) != 0 || ended.Outcome != msg.OutcomeUnavailable {
			t.Fatal(ended)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled headers wait did not join")
	}
	if f.reader.host.messageIsWatching(f.run.ctx, f.self.Account, f.selfProgram, "closed") || f.store.gate.Waiting() != 0 {
		t.Fatal("cancelled watcher survived")
	}
	if f.run.d.nativeInboxes != nil {
		t.Fatal("headers took over an inbox")
	}
	files, err := os.ReadDir(filepath.Join(f.store.dir, "cursors"))
	if err != nil || len(files) != 0 {
		t.Fatal("headers created body acknowledgement state", err, files)
	}
	record, err = f.store.readRecord(f.run.ctx, f.store.key(actor, request.IdempotencyKey))
	if err != nil || record.Acked {
		t.Fatal("headers acknowledged canonical record", err)
	}
	if err := f.reader.host.messageRights.policy.Set(f.otherSubject, ActionApplicationMessage, "app:native-agent", false); err != nil {
		t.Fatal(err)
	}
	denied := f.reader.headersWait("closed", "", 0)
	if denied.Outcome != msg.OutcomePage || len(denied.Headers) != 0 || denied.Revision == page.Revision {
		t.Fatal("revoked incoming permission disclosed header", denied)
	}
	if err := f.reader.host.messageRights.policy.Set(f.otherSubject, ActionApplicationMessage, "app:native-agent", true); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(2 * time.Hour)
	expired := f.reader.headersWait("closed", "", 0)
	if expired.Outcome != msg.OutcomePage || len(expired.Headers) != 0 {
		t.Fatal("expired subject survived", expired)
	}
}

func TestMessagesHeadersReplyHintsNativeSaturationAckAndRevocation(t *testing.T) {
	f := messagesObserveFixture(t)
	incoming := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	outgoing := messageActor{Account: f.self.Account, Program: f.selfProgram, ObservedProgram: f.self.Program, Application: "native-agent", Context: "target"}
	// Valid outgoing state events expand the header set beyond its projection.
	for i := 0; i < 50; i++ {
		if i != 0 && i%20 == 0 {
			*f.now = f.now.Add(time.Hour)
		}
		req := msg.SendRequest{To: "app:other-agent", Context: "elsewhere", Kind: msg.KindNote, Subject: "silent note", Body: fmt.Sprint(i), IdempotencyKey: fmt.Sprintf("out-note-%d", i), ExpiresUnixMs: f.now.Add(24 * time.Hour).UnixMilli()}
		if got := f.store.accept(f.run.ctx, outgoing, f.otherProgram, req, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal(got)
		}
		key := f.store.key(outgoing, req.IdempotencyKey)
		record, err := f.store.readRecord(f.run.ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.transitionState(f.run.ctx, key, record, "refuse"); err != nil {
			t.Fatal(err)
		}
	}
	quiet := f.reader.headersWait("target", "", 0)
	if quiet.Outcome != msg.OutcomePage || len(quiet.Headers) != 50 || quiet.PendingUntilUnixMs != 0 || quiet.ReplyUntilUnixMs != 0 {
		t.Fatal("outgoing notes supplied eligibility", quiet)
	}
	*f.now = f.now.Add(time.Hour)
	question := msg.SendRequest{To: "app:other-agent", Context: "source", Kind: msg.KindQuestion, Subject: "question", Body: "question body", IdempotencyKey: "out-question", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
	sent := f.store.accept(f.run.ctx, outgoing, f.otherProgram, question, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	record, key, err := f.store.readByID(f.run.ctx, sent.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	reply := msg.SendRequest{To: "app:native-agent", Context: "target", Kind: msg.KindReply, Subject: "reply", Body: "PRIVATE_REPLY", IdempotencyKey: "incoming-reply", ReplyTo: sent.MessageID, ExpiresUnixMs: question.ExpiresUnixMs}
	accepted := f.store.acceptReply(f.run.ctx, incoming, f.selfProgram, reply, false, sent.ContextID, &messageReplyTarget{key: key, token: record.Token})
	if accepted.Outcome != msg.OutcomeSent {
		t.Fatal(accepted)
	}
	page := f.reader.headersWait("target", quiet.Revision, 0)
	if page.Outcome != msg.OutcomePage || len(page.Headers) != 50 || page.PendingUntilUnixMs != 0 || page.ReplyUntilUnixMs != reply.ExpiresUnixMs || page.Revision == quiet.Revision || len(page.Messages) != 0 || page.Cursor != "" {
		t.Fatal(page)
	}
	if foreign := f.reader.headersWait("foreign", "", 0); foreign.PendingUntilUnixMs != 0 || foreign.ReplyUntilUnixMs != 0 {
		t.Fatal("foreign context hint", foreign)
	}
	if bytes.Contains(msg.EncodePage(&page), []byte(reply.Body)) {
		t.Fatal("reply body leaked")
	}
	// Exercise the existing explicit received-page cursor owner. Headers never ACK.
	permitted := messagesPermitted(t, f.store, incoming, f.selfProgram, "target")
	body := f.store.bodyPage(f.run.ctx, f.self.Account, f.selfProgram, "target", "native-test-owner", "", permitted)
	if body.Outcome != msg.OutcomePage || len(body.Messages) != 1 || body.Messages[0].Header.MessageID != accepted.MessageID || body.PendingUntilUnixMs != 0 || body.ReplyUntilUnixMs != 0 {
		t.Fatal(body)
	}
	if still := f.reader.headersWait("target", "", 0); still.ReplyUntilUnixMs != reply.ExpiresUnixMs {
		t.Fatal("body read implicitly acknowledged", still)
	}
	_ = f.store.bodyPage(f.run.ctx, f.self.Account, f.selfProgram, "target", "native-test-owner", body.Cursor, permitted)
	if acked := f.reader.headersWait("target", "", 0); acked.ReplyUntilUnixMs != 0 {
		t.Fatal("acknowledged reply hint", acked)
	}
	encoded := msg.EncodePage(&body)
	if bytes.Contains(encoded, []byte("pending_until_unix_ms")) || bytes.Contains(encoded, []byte("reply_until_unix_ms")) {
		t.Fatal("body encoded hints")
	}
	question.IdempotencyKey, question.Body = "out-question-second", "second question body"
	sent = f.store.accept(f.run.ctx, outgoing, f.otherProgram, question, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	record, key, err = f.store.readByID(f.run.ctx, sent.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	reply.IdempotencyKey, reply.Body, reply.ReplyTo = "incoming-reply-second", "second reply body", sent.MessageID
	accepted = f.store.acceptReply(f.run.ctx, incoming, f.selfProgram, reply, false, sent.ContextID, &messageReplyTarget{key: key, token: record.Token})
	if accepted.Outcome != msg.OutcomeSent {
		t.Fatal(accepted)
	}
	live := f.reader.headersWait("target", "", 0)
	if live.ReplyUntilUnixMs != reply.ExpiresUnixMs {
		t.Fatal("fresh reply unavailable", live)
	}
	if err := f.reader.host.messageRights.policy.Set(f.otherSubject, ActionApplicationMessage, "app:native-agent", false); err != nil {
		t.Fatal(err)
	}
	denied := f.reader.headersWait("target", live.Revision, 0)
	if denied.ReplyUntilUnixMs != 0 || denied.Revision == live.Revision {
		t.Fatal("revoked reply hint", denied)
	}
	if err := f.reader.host.messageRights.policy.Set(f.otherSubject, ActionApplicationMessage, "app:native-agent", true); err != nil {
		t.Fatal(err)
	}
	question.IdempotencyKey, question.Body = "out-question-third", "third question body"
	sent = f.store.accept(f.run.ctx, outgoing, f.otherProgram, question, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	record, key, err = f.store.readByID(f.run.ctx, sent.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	reply.IdempotencyKey, reply.Body, reply.ReplyTo = "incoming-reply-third", "third reply body", sent.MessageID
	accepted = f.store.acceptReply(f.run.ctx, incoming, f.selfProgram, reply, false, sent.ContextID, &messageReplyTarget{key: key, token: record.Token})
	if accepted.Outcome != msg.OutcomeSent {
		t.Fatal(accepted)
	}
	live = f.reader.headersWait("target", "", 0)
	if live.ReplyUntilUnixMs != reply.ExpiresUnixMs {
		t.Fatal(live)
	}
	*f.now = time.UnixMilli(reply.ExpiresUnixMs)
	expired := f.reader.headersWait("target", live.Revision, 0)
	if expired.ReplyUntilUnixMs != 0 || expired.PendingUntilUnixMs != 0 || expired.Revision == live.Revision {
		t.Fatal("expired reply hint", expired)
	}
}

func TestMessagesHeadersPersonAllowReleasesClosedQuestion(t *testing.T) {
	f := messagesObserveFixture(t, true)
	instance := messages1608Presence(t, f.run, f.reader.call, "", "source")
	if opened, err := f.reader.InboxOpen(instance, "source", ""); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	request := msg.SendRequest{To: "app:other-agent", Context: "closed", Kind: msg.KindQuestion, Subject: "question", Body: "PRIVATE_HELD_BODY", IdempotencyKey: "held-headers", AnswerByUnixMs: f.now.Add(time.Hour).UnixMilli(), ExpiresUnixMs: f.now.Add(2 * time.Hour).UnixMilli()}
	sent, err := f.reader.Send(instance, "source", request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	before := f.other.headersWait("closed", "", 0)
	if before.Outcome != msg.OutcomePage || len(before.Headers) != 0 || before.PendingUntilUnixMs != 0 || before.ReplyUntilUnixMs != 0 {
		t.Fatal("held question visible before consent", before)
	}
	messagesReceiverPersonAllow(t, f.run, request.To)
	after := f.other.headersWait("closed", before.Revision, 0)
	if after.Outcome != msg.OutcomePage || len(after.Headers) != 1 || after.Headers[0].TaskState == nil || *after.Headers[0].TaskState != msg.TaskStateSubmitted || len(after.Messages) != 0 || after.PendingUntilUnixMs != request.AnswerByUnixMs || after.ReplyUntilUnixMs != 0 {
		t.Fatal("headers did not release authorized question", after)
	}
	if after.Unread == nil || *after.Unread != 1 || after.PendingQuestions == nil || *after.PendingQuestions != 1 || after.OldestUnreadUnixMs == nil {
		t.Fatal("queued pending summary", after)
	}
	actor, _, _, ok := f.reader.owner(instance, "source")
	if !ok {
		t.Fatal("source owner lost")
	}
	key := f.store.key(actor, request.IdempotencyKey)
	record, err := f.store.readRecord(f.run.ctx, key)
	if err != nil || record.Acked {
		t.Fatal(record, err)
	}
	state, err := f.store.currentState(f.run.ctx, key, record)
	if err != nil || state.Held {
		t.Fatal(state, err)
	}
	f.run.d.mu.Lock()
	for _, row := range f.run.d.nativeInboxes.rows {
		if row.context == "closed" {
			t.Error("headers opened target inbox")
		}
	}
	f.run.d.mu.Unlock()
	*f.now = time.UnixMilli(request.AnswerByUnixMs)
	expired := f.other.headersWait("closed", after.Revision, 0)
	if expired.PendingUntilUnixMs != 0 || expired.ReplyUntilUnixMs != 0 || expired.Revision == after.Revision {
		t.Fatal("deadline hint or revision retained", expired)
	}
	if expired.PendingQuestions == nil || *expired.PendingQuestions != 0 {
		t.Fatal("expired pending question counted", expired)
	}
}

func TestMessagesHeadersIncomingQuestionsSurviveMixedSaturation(t *testing.T) {
	f := messagesObserveFixture(t)
	incoming := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	outgoing := messageActor{Account: f.self.Account, Program: f.selfProgram, ObservedProgram: f.self.Program, Application: "native-agent", Context: "target"}
	for i := 0; i < 50; i++ {
		if i != 0 && i%20 == 0 {
			*f.now = f.now.Add(time.Hour)
		}
		request := msg.SendRequest{To: "app:native-agent", Context: "target", Kind: msg.KindQuestion, Subject: "incoming", Body: fmt.Sprintf("dummy-%d", i), IdempotencyKey: fmt.Sprintf("incoming-%d", i), AnswerByUnixMs: f.now.Add(12 * time.Hour).UnixMilli(), ExpiresUnixMs: f.now.Add(24 * time.Hour).UnixMilli()}
		if got := f.store.accept(f.run.ctx, incoming, f.selfProgram, request, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal(got)
		}
		request.To, request.Context, request.IdempotencyKey = "app:other-agent", "elsewhere", fmt.Sprintf("outgoing-%d", i)
		if got := f.store.accept(f.run.ctx, outgoing, f.otherProgram, request, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal(got)
		}
		key := f.store.key(outgoing, request.IdempotencyKey)
		record, err := f.store.readRecord(f.run.ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.transitionState(f.run.ctx, key, record, "refuse"); err != nil {
			t.Fatal(err)
		}
	}
	// Outgoing terminal reports publish in bounded progress slices. Observe
	// again if that publication changes the first snapshot's revision.
	var page msg.Page
	for attempt := 0; attempt < 3; attempt++ {
		page = f.reader.headersWait("target", "", 0)
		if page.Outcome == msg.OutcomePage {
			break
		}
	}
	if page.Outcome != msg.OutcomePage || len(page.Headers) != 50 || page.PendingQuestions == nil || *page.PendingQuestions != 50 || page.Unread == nil || *page.Unread != 100 {
		t.Fatal(page)
	}
	incomingVisible := 0
	for _, h := range page.Headers {
		if h.To == "app:native-agent" && h.TaskState != nil && *h.TaskState == msg.TaskStateSubmitted {
			incomingVisible++
		}
	}
	if incomingVisible == 0 {
		t.Fatal("outgoing rows displaced every incoming question cue", page)
	}
}

func TestMessagesHeadersOwnStateSurvivesPairRevocationAndDisplayAlias(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.self.Account, Program: f.selfProgram, ObservedProgram: f.self.Program, Application: "original-display-name", Context: "outgoing"}
	request := msg.SendRequest{To: "app:other-agent", Context: "destination", Kind: msg.KindQuestion, Subject: "own question", Body: "own body", IdempotencyKey: "outgoing-1", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
	sent := f.store.accept(f.run.ctx, actor, f.otherProgram, request, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	key := f.store.key(actor, request.IdempotencyKey)
	record, err := f.store.readRecord(f.run.ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.transitionState(f.run.ctx, key, record, "refuse"); err != nil {
		t.Fatal(err)
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
		t.Fatal(err)
	}
	page := f.reader.headersWait("outgoing", "", 0)
	if page.Outcome != msg.OutcomePage || len(page.Headers) != 1 || !page.Headers[0].Refused || page.Headers[0].From != "app:original-display-name" || len(page.Messages) != 0 {
		t.Fatal(page)
	}
	state, err := f.store.currentState(f.run.ctx, key, record)
	if err != nil || state.Observed != 0 {
		t.Fatal("headers observed outgoing event", state, err)
	}
	again := f.reader.headersWait("outgoing", page.Revision, 0)
	if again.Revision != page.Revision || len(again.Headers) != 1 {
		t.Fatal(again)
	}
}

func TestMessagesInboxesNativePaginationFilteringAndGap(t *testing.T) {
	f := messagesObserveFixture(t)
	pin, err := f.other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	firstInstance := ""
	for start := 0; start < 65; start += 32 {
		contexts := []wire.ApplicationContext{}
		for i := start; i < start+32 && i < 65; i++ {
			contexts = append(contexts, wire.ApplicationContext{Name: fmt.Sprintf("box-%02d", i)})
		}
		presence := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Contexts: contexts, Interfaces: []wire.ApplicationInterface{}})
		if presence.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(presence)
		}
		if start == 0 {
			firstInstance = presence.Instance
		}
		for _, context := range contexts {
			opened, err := f.other.InboxOpen(presence.Instance, context.Name, "ready\u202estatus")
			if err != nil || opened.Outcome != msg.OutcomeOpened {
				t.Fatal(opened, err)
			}
		}
	}
	first := f.reader.inboxesList("")
	if first.Outcome != msg.OutcomePage || len(first.Inboxes) != 64 || first.Cursor == "" {
		t.Fatal(first.Outcome, len(first.Inboxes), first.Cursor)
	}
	for _, inbox := range first.Inboxes {
		if inbox.Program != "app:other-agent" || inbox.Status != "readystatus" || inbox.Watching || inbox.LastSeenUnixMs <= 0 || inbox.Reach == nil || *inbox.Reach != msg.InboxReachOpen {
			t.Fatal(inbox)
		}
	}
	if renewed, err := f.other.InboxOpen(firstInstance, "box-00", "ready\u202estatus"); err != nil || renewed.Outcome != msg.OutcomeOpened {
		t.Fatal(renewed, err)
	}
	second := f.reader.inboxesList(first.Cursor)
	if second.Outcome != msg.OutcomePage || len(second.Inboxes) != 1 || second.Cursor != "" || second.Inboxes[0].Context != "box-64" {
		t.Fatal(second)
	}
	foreign := f.other.inboxesList(first.Cursor)
	if foreign.Outcome != msg.OutcomeGap || len(foreign.Inboxes) != 0 {
		t.Fatal("foreign caller cursor accepted", foreign)
	}
	parts := strings.Split(first.Cursor, ".")
	for _, cursor := range []string{"other." + parts[1] + "." + parts[2], parts[0] + "." + parts[1] + ".0" + parts[2]} {
		if page := f.reader.inboxesList(cursor); page.Outcome != msg.OutcomeGap || len(page.Inboxes) != 0 {
			t.Fatal("foreign epoch/noncanonical cursor accepted", page)
		}
	}
	changed, err := f.other.InboxOpen(firstInstance, "box-00", "changed")
	if err != nil || changed.Outcome != msg.OutcomeOpened {
		t.Fatal(changed, err)
	}
	gap := f.reader.inboxesList(first.Cursor)
	if gap.Outcome != msg.OutcomeGap || len(gap.Inboxes) != 0 {
		t.Fatal("changed snapshot did not gap", gap)
	}
	ownInstance := messages1608Presence(t, f.run, f.reader.call, "", "visible-own")
	if opened, e := f.reader.InboxOpen(ownInstance, "visible-own", ""); e != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, e)
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
		t.Fatal(err)
	}
	hidden := f.reader.inboxesList("")
	if hidden.Outcome != msg.OutcomePage || len(hidden.Inboxes) != 1 || hidden.Inboxes[0].Context != "visible-own" || hidden.Inboxes[0].Mine == nil || !*hidden.Inboxes[0].Mine {
		t.Fatal("permission filtering failed", hidden)
	}
	if hidden.Inboxes[0].Program != "app:native-agent" {
		t.Fatal("own row lost native program", hidden)
	}
	for _, contextName := range []string{"", "visible-own"} {
		prepared, err := f.reader.Prepare(ownInstance, "visible-own", msg.PrepareRequest{Receiver: "app:native-agent", Context: contextName, Body: "self"})
		if err != nil || prepared.SendRequest != nil || len(prepared.Choices) != 0 || prepared.Outcome != msg.OutcomeForbidden {
			t.Fatal("own listing became send destination", prepared, err)
		}
	}
	selfSend, err := f.reader.SendChat(ownInstance, "visible-own", msg.ChatSendRequest{ReturnContext: "visible-own", Request: msg.SendRequest{To: "app:native-agent", Context: "visible-own", Kind: msg.KindNote, Body: "self", IdempotencyKey: "self-send", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}})
	if err != nil || selfSend.Outcome != msg.OutcomeForbidden {
		t.Fatal("self SendChat admitted", selfSend, err)
	}
	policyPath := filepath.Join(filepath.Dir(f.run.f.panel.Program), "decisions.json")
	policyBytes, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.WriteFile(policyPath, policyBytes, 0600); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile(policyPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	unknown := f.reader.inboxesList("")
	if unknown.Outcome != msg.OutcomeUnavailable || len(unknown.Inboxes) != 0 || unknown.Cursor != "" {
		t.Fatal("partial list on unknown rights", unknown)
	}
}

func TestMessagesObserversUnknownStateAndRuntimeCloseReturnNoPartialData(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	request := msg.SendRequest{To: "app:native-agent", Context: "closed", Kind: msg.KindNote, Subject: "private subject", Body: "private body", IdempotencyKey: "unknown-1", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	if sent := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, ""); sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	path := f.store.recordPath(f.store.key(actor, request.IdempotencyKey))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	unknown := f.reader.headersWait("closed", "", 0)
	if unknown.Outcome != msg.OutcomeUnavailable || len(unknown.Headers) != 0 || len(unknown.Messages) != 0 || unknown.Revision != "" {
		t.Fatal("partial headers on corrupt canonical state", unknown)
	}
	if retained, err := os.ReadFile(path); err != nil || !bytes.Equal(retained, []byte("{")) {
		t.Fatal("header observation rewrote corrupt canonical acceptance", string(retained), err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	page := f.reader.headersWait("closed", "", 0)
	if page.Outcome != msg.OutcomePage || len(page.Headers) != 1 {
		t.Fatal(page)
	}
	done := make(chan msg.Page, 1)
	go func() { done <- f.reader.headersWait("closed", page.Revision, 1000) }()
	deadline := time.Now().Add(2 * time.Second)
	for f.store.gate.Waiting() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.store.gate.Waiting() != 1 {
		t.Fatal("native headers wait was not admitted")
	}
	f.run.d.closeNativeInboxes()
	select {
	case ended := <-done:
		if ended.Outcome != msg.OutcomeUnavailable || len(ended.Headers) != 0 {
			t.Fatal(ended)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime close did not end headers wait")
	}
	if f.store.gate.Waiting() != 0 {
		t.Fatal("runtime close leaked wait slot")
	}
}

func TestMessagesHeadersNativeReplyAckThenOwnOutgoingQuestion(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("receiver-reversed-%v", reverse), func(t *testing.T) {
			f := messagesObserveFixture(t)
			sender, peer := f.reader, f.other
			senderApp, peerApp := "native-agent", "other-agent"
			if reverse {
				sender, peer = peer, sender
				senderApp, peerApp = peerApp, senderApp
			}
			announce := func(r *messagesReceiver, application, contextName string) string {
				t.Helper()
				pin, err := r.call.RetainProcess()
				if err != nil {
					t.Fatal(err)
				}
				defer pin.Close()
				_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				presence := f.run.d.announceInSession(f.run.ctx, subject, session, wire.ApplicationPresence{Application: application, LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: contextName}}})
				if presence.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal(presence)
				}
				return presence.Instance
			}
			senderInstance := announce(sender, senderApp, "sender")
			peerInstance := announce(peer, peerApp, "peer")
			for _, owner := range []struct {
				r                 *messagesReceiver
				instance, context string
			}{{sender, senderInstance, "sender"}, {peer, peerInstance, "peer"}} {
				if opened, err := owner.r.InboxOpen(owner.instance, owner.context, ""); err != nil || opened.Outcome != msg.OutcomeOpened {
					t.Fatal(opened, err)
				}
			}
			question := msg.SendRequest{To: "app:" + peerApp, Context: "peer", Kind: msg.KindQuestion, Subject: "first question", Body: "answer this", IdempotencyKey: "first", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
			sent, err := sender.Send(senderInstance, "sender", question)
			if err != nil || sent.Outcome != msg.OutcomeSent {
				t.Fatal(sent, err)
			}
			requestPage, err := peer.Wait(peerInstance, "peer", msg.ViewBody, "", "", 0)
			if err != nil || len(requestPage.Messages) != 1 || requestPage.Messages[0].Header.MessageID != sent.MessageID {
				t.Fatal(requestPage, err)
			}
			replied, err := peer.Reply(peerInstance, "peer", sent.MessageID, "answer")
			if err != nil || replied.Outcome != msg.OutcomeSent {
				t.Fatal(replied, err)
			}
			eligible, err := sender.Wait("", "sender", msg.ViewHeaders, "", "", 0)
			if err != nil || eligible.PendingUntilUnixMs != 0 || eligible.ReplyUntilUnixMs <= f.now.UnixMilli() || eligible.AwaitedReplies == nil || *eligible.AwaitedReplies != 1 || eligible.OldestWake == "" || eligible.OldestWake != eligible.Wake {
				t.Fatal("reply eligibility", eligible, err)
			}
			foreign, err := sender.Wait("", "foreign", msg.ViewHeaders, "", "", 0)
			if err != nil || foreign.AwaitedReplies == nil || *foreign.AwaitedReplies != 0 {
				t.Fatal("foreign context inherited urgency", foreign, err)
			}
			*f.now = time.UnixMilli(question.AnswerByUnixMs)
			ordinary, err := sender.Wait("", "sender", msg.ViewHeaders, "", eligible.Revision, 0)
			if err != nil || ordinary.AwaitedReplies == nil || *ordinary.AwaitedReplies != 0 ||
				ordinary.Wake != eligible.Wake || ordinary.OldestWake != eligible.OldestWake || ordinary.ReplyUntilUnixMs != eligible.ReplyUntilUnixMs ||
				ordinary.Revision == eligible.Revision {
				t.Fatal("answer deadline must clear only urgency", ordinary, err)
			}
			question.AnswerByUnixMs = f.now.Add(time.Minute).UnixMilli()
			body, err := sender.Wait(senderInstance, "sender", msg.ViewBody, "", "", 0)
			if err != nil || body.Outcome != msg.OutcomePage || len(body.Messages) == 0 || body.Cursor == "" || body.AwaitedReplies != nil {
				t.Fatal("reply and completion body page", body, err)
			}
			acked, err := sender.Wait(senderInstance, "sender", msg.ViewBody, body.Cursor, "", 0)
			if err != nil || acked.Outcome != msg.OutcomePage || len(acked.Messages) != 0 {
				t.Fatal("explicit received-page ACK", acked, err)
			}
			quiet, err := sender.Wait("", "sender", msg.ViewHeaders, "", eligible.Revision, 0)
			if err != nil || quiet.PendingUntilUnixMs != 0 || quiet.ReplyUntilUnixMs != 0 {
				t.Fatal("ACK retained wake eligibility", quiet, err)
			}
			question.IdempotencyKey, question.Subject, question.Body = "second", "own outgoing question", "new outgoing work"
			next, err := sender.Send(senderInstance, "sender", question)
			if err != nil || next.Outcome != msg.OutcomeSent {
				t.Fatal(next, err)
			}
			after, err := sender.Wait("", "sender", msg.ViewHeaders, "", quiet.Revision, 0)
			if err != nil || after.PendingUntilUnixMs != 0 || after.ReplyUntilUnixMs != 0 {
				t.Fatal("own outgoing question supplied incoming eligibility", after, err)
			}
			empty, err := sender.Wait(senderInstance, "sender", msg.ViewBody, acked.Cursor, "", 0)
			if err != nil || empty.Outcome != msg.OutcomePage || len(empty.Messages) != 0 {
				t.Fatal("own outgoing question appeared as incoming body", empty, err)
			}
			peerHints, err := peer.Wait("", "peer", msg.ViewHeaders, "", "", 0)
			if err != nil || peerHints.PendingUntilUnixMs != question.AnswerByUnixMs {
				t.Fatal("receiver lost the new incoming question", peerHints, err)
			}
			t.Logf("%s: reply=(%d,%d), ACK=(%d,%d), own-question=(%d,%d), peer-pending=%d, body=%d, own-revision-changed=%v", senderApp, eligible.PendingUntilUnixMs, eligible.ReplyUntilUnixMs, quiet.PendingUntilUnixMs, quiet.ReplyUntilUnixMs, after.PendingUntilUnixMs, after.ReplyUntilUnixMs, peerHints.PendingUntilUnixMs, len(empty.Messages), after.Revision != quiet.Revision)
		})
	}
}
