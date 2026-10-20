//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
	"testing"
	"time"
)

func TestMessagesNativePrepareRetainsApprovedContextAcrossReceiverRestart(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	profile := startprofile.Profile{Name: "claude-v1", Context: "durable-mailbox", Folder: t.TempDir(), StateDir: t.TempDir(), CLIFileID: "inert-native-fixture", Model: "fixture", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 1 << 30, BudgetCents: 1}
	descriptor, _, err := f.run.d.agentSnapshot(f.run.ctx, "other-agent")
	if err != nil {
		t.Fatal(err)
	}
	recipe := &wire.ApplicationActivationRecipe{Arguments: profile.Arguments(), Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 1000}
	if got := f.run.d.setActivation(f.run.ctx, f.run.f.panel, applications.ActivationConfiguration{Expected: descriptor, Activation: recipe}, nil); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	f.run.d.messageStartProfile = messageStartProfileResolver(func(_ context.Context, current wire.ApplicationDescriptor, parsed startprofile.Profile) error {
		if current.Name != "other-agent" || parsed != profile {
			return errors.New("unapproved fixture profile")
		}
		return nil
	})
	starts := 0
	f.run.d.launchMessageStart = func(context.Context, applicationMessageStartRequest, func(context.Context) error) error {
		starts++
		return errors.New("live receiver must not autostart")
	}
	oldCall, stopOld := messages1608CallImage(t, f.otherProgram)
	old := &messagesReceiver{host: f.other.host, call: oldCall, ctx: f.run.ctx}
	ri, oldClient := messagesStateOpen(t, f, old, "other-agent", profile.Context)
	prepare := func(body string) msg.SendRequest {
		t.Helper()
		got, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: body})
		if err != nil || got.Outcome != msg.OutcomePage || got.SendRequest == nil || got.SendRequest.Context != profile.Context {
			t.Fatal(got, err)
		}
		return *got.SendRequest
	}
	first, err := sender.Send(si, "source", prepare("first question"))
	if err != nil || first.Outcome != msg.OutcomeSent {
		t.Fatal(first, err)
	}
	if page, err := oldClient.Wait(ri, profile.Context, msg.ViewBody, "", "", 0); err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	if got, err := oldClient.Reply(ri, profile.Context, first.MessageID, "first answer"); err != nil || got.Outcome != msg.OutcomeSent {
		t.Fatal(got, err)
	}
	pin, err := oldCall.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := f.run.d.withdrawInSession(f.run.ctx, subject, session, "other-agent", ri); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	stopOld() // The old native process actually exits and joins before replacement.
	call, _ := messages1608CallImage(t, f.otherProgram)
	replacement := &messagesReceiver{host: f.other.host, call: call, ctx: f.run.ctx}
	instance, client := messagesStateOpen(t, f, replacement, "other-agent", profile.Context)
	if instance == ri {
		t.Fatal("receiver restart reused the old native instance")
	}
	if got, _ := client.InboxOpen(ri, profile.Context, ""); got.Outcome == msg.OutcomeOpened {
		t.Fatal("replacement reopened stale native owner", got)
	}
	if got, _ := client.Wait(ri, profile.Context, msg.ViewBody, "", "", 0); got.Outcome == msg.OutcomePage {
		t.Fatal("replacement used stale body owner", got)
	}
	second, err := sender.Send(si, "source", prepare("fresh question after restart"))
	if err != nil || second.Outcome != msg.OutcomeSent || second.MessageID == first.MessageID {
		t.Fatal(second, err)
	}
	page, err := client.Wait(instance, profile.Context, msg.ViewBody, "", "", 0)
	found := false
	for _, message := range page.Messages {
		found = found || message.Header.MessageID == second.MessageID && message.Body == "fresh question after restart"
	}
	if err != nil || !found {
		t.Fatal(page, err)
	}
	if got, err := client.Reply(instance, profile.Context, second.MessageID, "replacement answer"); err != nil || got.Outcome != msg.OutcomeSent {
		t.Fatal(got, err)
	}
	if page, err := sender.Wait(si, "source", msg.ViewBody, "", "", 0); err != nil || !messagesReceiverHasReply(page, "replacement answer") {
		t.Fatal(page, err)
	}
	record, _, err := f.store.readByID(f.run.ctx, second.MessageID)
	if err != nil || record == nil || record.Start != nil || starts != 0 {
		t.Fatal("receiver restart created a startup attempt or charge", record, starts, err)
	}
}

func TestMessagesNativePrepareUngrantedRegistrationHidesAllInboxMetadata(t *testing.T) {
	f, permission, operator := messagePermissionFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "secret-context")
	for _, request := range []msg.PrepareRequest{{Receiver: "app:other-agent", Body: "ping"}, {Receiver: "app:other-agent", Context: "secret-context", ContextTitle: "secret-title", Body: "ping"}} {
		out, err := sender.Prepare(si, "source", request)
		if err != nil || out.Outcome != msg.OutcomeForbidden || out.Reason != "access:other-agent" || len(out.Choices) != 0 || out.SendRequest != nil {
			t.Fatal(out, err)
		}
		if _, err := msg.DecodePrepareResult(msg.EncodePrepareResult(&out)); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:unregistered-agent", Body: "ping"}); err != nil || out.Outcome != msg.OutcomeUnavailable || out.Reason != "not_found" || len(out.Choices) != 0 || out.SendRequest != nil {
		t.Fatal(out, err)
	}
	observed, err := permission.RequestContext(f.run.ctx, messagePermissionIntent("other-agent", "prepare-first-use"))
	if err != nil || observed.Outcome != askswire.PermissionOutcomePending || observed.Answer == nil {
		t.Fatal(observed, err)
	}
	if same, err := permission.RequestContext(f.run.ctx, messagePermissionIntent("other-agent", "prepare-first-use")); err != nil || same.Answer == nil || same.Answer.ID != observed.Answer.ID {
		t.Fatal("duplicate permission question", same, err)
	}
	if _, err := operator.AnswerPermissionIfPendingContext(f.run.ctx, observed.Answer.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	revision, _, err := f.run.f.a.rights.policy.SelectorSnapshotContext(f.run.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if edit, err := f.run.f.a.applyPermissionAllow(f.run.ctx, f.run.f.panel, revision, observed.Answer.ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	prepared, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: "ping"})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil || prepared.SendRequest.Context != "secret-context" {
		t.Fatal(prepared, err)
	}
	sent, err := sender.Send(si, "source", *prepared.SendRequest)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	if page, err := receiver.Wait(ri, "secret-context", msg.ViewBody, "", "", 0); err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	if reply, err := receiver.Reply(ri, "secret-context", sent.MessageID, "pong"); err != nil || reply.Outcome != msg.OutcomeSent {
		t.Fatal(reply, err)
	}
	if reverse, err := receiver.Prepare(ri, "secret-context", msg.PrepareRequest{Receiver: "app:native-agent", Body: "new direction"}); err != nil || reverse.Outcome != msg.OutcomeForbidden || reverse.Reason != "access:native-agent" {
		t.Fatal(reverse, err)
	}
}

func TestMessagesNativePrepareExplicitSendAndReply(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	for _, lifetime := range []int64{1000, 604800000} {
		out, e := f.reader.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: "bounded time", TTLMs: lifetime, AnswerWithinMs: lifetime})
		if e != nil || out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.ExpiresUnixMs != f.now.UnixMilli()+lifetime || out.SendRequest.AnswerByUnixMs != f.now.UnixMilli()+lifetime {
			t.Fatal("millisecond lifetime", out, e)
		}
	}
	for _, bad := range []msg.PrepareRequest{
		{Receiver: "app:other-agent", Body: "invalid", TTLMs: 999},
		{Receiver: "app:other-agent", Body: "invalid", TTLMs: 1000, AnswerWithinMs: 999},
		{Receiver: "app:other-agent", Body: "invalid", TTLMs: 9223372036854775807},
		{Receiver: "app:other-agent", Body: "invalid", TTLMs: 1000, AnswerWithinMs: 9223372036854775807},
	} {
		if out, e := f.reader.Prepare(si, "source", bad); e != nil || out.Outcome != msg.OutcomeInvalid || out.SendRequest != nil {
			t.Fatal("duration overflow/bound", out, e)
		}
	}
	prepared, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: "question", AnswerWithinMs: 60000})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
		t.Fatal(prepared, err)
	}
	request := *prepared.SendRequest
	if request.Context != "target" || request.Kind != msg.KindQuestion || request.ExpiresUnixMs != f.now.Add(600*time.Second).UnixMilli() || request.AnswerByUnixMs != f.now.Add(60*time.Second).UnixMilli() || request.IdempotencyKey == "" {
		t.Fatal(request)
	}
	if page, e := receiver.Wait(ri, "target", msg.ViewBody, "", "", 0); e != nil || len(page.Messages) != 0 {
		t.Fatal("Prepare dispatched", page, e)
	}
	sent, err := sender.Send(si, "source", request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	record, _, err := f.store.readByID(t.Context(), sent.MessageID)
	if err != nil || !bytes.Equal(record.Spec, msg.EncodeSendRequest(&request)) {
		t.Fatal("prepared arguments changed", record, err)
	}
	answer, err := receiver.Reply(ri, "target", sent.MessageID, "answer")
	if err != nil || answer.Outcome != msg.OutcomeSent || answer.ContextID != sent.ContextID {
		t.Fatal(answer, err)
	}
	*f.now = f.now.Add(61 * time.Second)
	replay, err := receiver.Reply(ri, "target", sent.MessageID, "answer")
	// Receipt survives the answer deadline while the question still exists.
	if err != nil || replay.Outcome != msg.OutcomeSent || replay.MessageID != answer.MessageID {
		t.Fatal(replay, err)
	}
	if changed, _ := receiver.Reply(ri, "target", sent.MessageID, "changed"); changed.Outcome != msg.OutcomeConflict {
		t.Fatal(changed)
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, request.To, false); err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey, request.Body = "explicit-refusal", "new denied request"
	request.AnswerByUnixMs = f.now.Add(time.Minute).UnixMilli()
	denied, err := sender.Send(si, "source", request)
	if err != nil || denied.Outcome != msg.OutcomeForbidden || denied.Refusal == nil || denied.Refusal.Source != rwire.RefusalSourceRights || denied.Refusal.Decision != rwire.RefusalDecisionDenied || denied.Refusal.Ask != rwire.RefusalAskNone {
		t.Fatal("native authority detail", denied, err)
	}
}

func TestMessagesNativeReplyNewInstanceCannotDispatchPinnedIntent(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	sent, err := sender.Send(si, "source", messagesStateRequest(f, "occurrence-unknown", msg.KindQuestion))
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	f.store.boundary = func(at string) error {
		if at == "simple_reply_pinned" {
			return errors.New("owned interruption")
		}
		return nil
	}
	if got, _ := receiver.Reply(ri, "target", sent.MessageID, "answer"); got.Outcome != msg.OutcomeUnknown {
		t.Fatal(got)
	}
	f.store.boundary = nil
	pin, err := f.other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := f.run.d.withdrawInSession(t.Context(), subject, session, "other-agent", ri); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	call, _ := messages1608CallImage(t, f.otherProgram)
	replacement := &messagesReceiver{host: f.other.host, call: call, ctx: f.run.ctx}
	newInstance, newClient := messagesStateOpen(t, f, replacement, "other-agent", "target")
	if newInstance == ri {
		t.Fatal("fixture reused the former occurrence")
	}
	if got, _ := newClient.Reply(newInstance, "target", sent.MessageID, "answer"); got.Outcome != msg.OutcomeUnknown {
		t.Fatal("replacement dispatched former intent", got)
	}
	if got, _ := newClient.Reply(newInstance, "target", sent.MessageID, "different"); got.Outcome != msg.OutcomeConflict {
		t.Fatal(got)
	}
	if page, e := sender.Wait(si, "source", msg.ViewBody, "", "", 0); e != nil || len(page.Messages) != 0 {
		t.Fatal("replacement reply effect", page, e)
	}
}

func TestMessagesNativePrepareFallbackRespectsPermissionAndIgnoresRecipe(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	pin, err := f.other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := f.run.d.announceInSession(t.Context(), subject, session, wire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: "one"}, {Name: "two"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	for _, name := range []string{"one", "two"} {
		if opened, e := f.other.InboxOpen(presence.Instance, name, ""); e != nil || opened.Outcome != msg.OutcomeOpened {
			t.Fatal(opened, e)
		}
	}
	prepare := func() msg.PrepareResult {
		t.Helper()
		got, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: "x"})
		if err != nil || got.Outcome != msg.OutcomePage || got.SendRequest == nil || len(got.Choices) != 0 {
			t.Fatal("program-only prepare", got, err)
		}
		return got
	}
	selected := prepare().SendRequest.Context
	profileContext, profileValid := "one", true
	if selected == "one" {
		profileContext = "two"
	}
	f.run.d.messageStartProfile = func(_ context.Context, descriptor wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		if descriptor.Name != "other-agent" {
			return applicationMessageStartRequest{}, errors.New("foreign recipe")
		}
		return applicationMessageStartRequest{Session: session, Context: profileContext, ProfileFingerprint: "approved-default", Validate: func(context.Context) error {
			if !profileValid {
				return errors.New("stale profile")
			}
			return nil
		}}, nil
	}
	// A registered recipe's context leaves the live fallback unchanged.
	if got := prepare(); got.SendRequest.Context != selected {
		t.Fatal("approved default chose a chat", got)
	}
	if got, _ := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "one", Body: "x"}); got.Outcome != msg.OutcomePage || got.SendRequest == nil || got.SendRequest.Context != "one" {
		t.Fatal("explicit context overridden", got)
	}
	profileValid = false
	if got := prepare(); got.SendRequest.Context != selected {
		t.Fatal("stale default", got)
	}
	profileValid = true
	approved := f.run.d.messageStartProfile
	f.run.d.messageStartProfile = func(ctx context.Context, descriptor wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		if descriptor.Name == "other-agent" {
			return applicationMessageStartRequest{}, errors.New("only a foreign receiver has a profile")
		}
		return approved(ctx, descriptor)
	}
	if got := prepare(); got.SendRequest.Context != selected {
		t.Fatal("foreign default", got)
	}
	f.run.d.messageStartProfile = approved
	if got, _ := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "two", Body: "x"}); got.Outcome != msg.OutcomePage || got.SendRequest == nil || got.SendRequest.Context != "two" {
		t.Fatal(got)
	}
	if err = f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "two", Body: "x"}); got.Outcome != msg.OutcomeForbidden || got.Reason != "access:other-agent" || got.SendRequest != nil {
		t.Fatal("denied discovery", got)
	}
	if got, _ := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: "x"}); got.Outcome != msg.OutcomeForbidden || got.Reason != "access:other-agent" || got.SendRequest != nil {
		t.Fatal("hidden default", got)
	}
}

func TestMessagesNativeReplyUnknownBindingSurvivesStoreRestart(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	request := messagesStateRequest(f, "simple-unknown", msg.KindQuestion)
	sent, err := sender.Send(si, "source", request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	f.store.boundary = func(at string) error {
		if at == "simple_reply_pinned" {
			return errors.New("owned interruption before dispatch")
		}
		return nil
	}
	first, err := receiver.Reply(ri, "target", sent.MessageID, "first")
	if err != nil || first.Outcome != msg.OutcomeUnknown {
		t.Fatal(first, err)
	}
	reopened, err := openMessageStore(f.run.state, func() time.Time { return *f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.reader.host.messages = reopened
	if changed, _ := receiver.Reply(ri, "target", sent.MessageID, "different"); changed.Outcome != msg.OutcomeConflict {
		t.Fatal(changed)
	}
	done, err := receiver.Reply(ri, "target", sent.MessageID, "first")
	if err != nil || done.Outcome != msg.OutcomeSent {
		t.Fatal(done, err)
	}
	again, err := receiver.Reply(ri, "target", sent.MessageID, "first")
	if err != nil || again.Outcome != msg.OutcomeSent || again.MessageID != done.MessageID {
		t.Fatal(again, err)
	}
}

func TestMessagesNativeConvenienceRefusesForeignAndInvalid(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	replyKind := msg.KindReply
	for _, request := range []msg.PrepareRequest{{Receiver: "OpenCode", Body: "x"}, {Receiver: "app:other-agent", Body: "x", Kind: &replyKind}, {Receiver: "app:other-agent", Body: "x", TTLMs: 1000, AnswerWithinMs: 2000}, {Receiver: "app:other-agent", Body: ""}} {
		got, err := f.reader.Prepare(si, "source", request)
		if err != nil || got.Outcome == msg.OutcomePage {
			t.Fatal(got, err)
		}
	}
	if got, _ := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "absent", Body: "x"}); got.Outcome != msg.OutcomeUnavailable {
		t.Fatal(got)
	}
	sent, err := sender.Send(si, "source", messagesStateRequest(f, "simple-note", msg.KindNote))
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	if got, _ := receiver.Reply(ri, "target", sent.MessageID, "answer"); got.Outcome != msg.OutcomeForbidden {
		t.Fatal(got)
	}
	question, err := sender.Send(si, "source", messagesStateRequest(f, "simple-foreign", msg.KindQuestion))
	if err != nil || question.Outcome != msg.OutcomeSent {
		t.Fatal(question, err)
	}
	if got, _ := sender.Reply(si, "source", question.MessageID, "answer"); got.Outcome != msg.OutcomeForbidden {
		t.Fatal(got)
	}
	if got, _ := receiver.Reply(ri, "wrong", question.MessageID, "answer"); got.Outcome != msg.OutcomeForbidden {
		t.Fatal(got)
	}
	withdraw := messagesStateRequest(f, "simple-withdraw", msg.KindNote)
	withdraw.Withdraw = question.MessageID
	if got, e := sender.Send(si, "source", withdraw); e != nil || got.Outcome != msg.OutcomeSent {
		t.Fatal(got, e)
	}
	if got, _ := receiver.Reply(ri, "target", question.MessageID, "withdrawn answer"); got.Outcome != msg.OutcomeForbidden {
		t.Fatal("withdrawn question", got)
	}
	late, err := sender.Send(si, "source", messagesStateRequest(f, "simple-late", msg.KindQuestion))
	if err != nil || late.Outcome != msg.OutcomeSent {
		t.Fatal(late, err)
	}
	*f.now = f.now.Add(2 * time.Minute)
	if got, _ := receiver.Reply(ri, "target", late.MessageID, "late"); got.Outcome != msg.OutcomeForbidden {
		t.Fatal(got)
	}
}

func TestMessagesNativeReplyLostAcceptedResponseReconcilesAfterDeadline(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	sent, err := sender.Send(si, "source", messagesStateRequest(f, "simple-lost-accepted", msg.KindQuestion))
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	f.store.boundary = func(at string) error {
		if at == "record_published" {
			return errors.New("owned lost accepted reply response")
		}
		return nil
	}
	if got, e := receiver.Reply(ri, "target", sent.MessageID, "accepted answer"); e != nil || got.Outcome != msg.OutcomeUnknown {
		t.Fatal(got, e)
	}
	reopened, err := openMessageStore(f.run.state, func() time.Time { return *f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.reader.host.messages = reopened
	*f.now = f.now.Add(2 * time.Minute)
	done, err := receiver.Reply(ri, "target", sent.MessageID, "accepted answer")
	if err != nil || done.Outcome != msg.OutcomeSent || done.ContextID != sent.ContextID {
		t.Fatal("accepted custody was lost at deadline", done, err)
	}
	if changed, _ := receiver.Reply(ri, "target", sent.MessageID, "changed answer"); changed.Outcome != msg.OutcomeConflict {
		t.Fatal(changed)
	}
	again, err := receiver.Reply(ri, "target", sent.MessageID, "accepted answer")
	if err != nil || again.Outcome != msg.OutcomeSent || again.MessageID != done.MessageID {
		t.Fatal(again, err)
	}
}

func TestMessagesNativeReplyLostReceiptSurvivesQuestionExpiry(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	request := messagesStateRequest(f, "simple-lost-expired", msg.KindQuestion)
	request.ExpiresUnixMs = f.now.Add(3 * time.Minute).UnixMilli()
	sent, err := sender.Send(si, "source", request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	f.store.boundary = func(at string) error {
		if at == "index_published" {
			return errors.New("reply response lost after durable commit")
		}
		return nil
	}
	reply, err := receiver.Reply(ri, "target", sent.MessageID, "accepted answer")
	if err != nil || reply.Outcome != msg.OutcomeUnknown {
		t.Fatal(reply, err)
	}
	f.store.boundary = nil
	*f.now = f.now.Add(4 * time.Minute)
	if err := f.store.sweepExpiry(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	page, err := sender.Wait(si, "source", msg.ViewBody, "", "", 0)
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal(page, err)
	}
	var replyID string
	for _, entry := range page.Messages {
		if entry.Body == "accepted answer" && entry.Header.Kind == msg.KindReply {
			if replyID != "" {
				t.Fatal("duplicate delivered reply")
			}
			replyID = entry.Header.MessageID
		}
	}
	if replyID == "" {
		t.Fatal("accepted reply was absent", page)
	}
	f.store.boundary = func(at string) error {
		t.Fatalf("receipt retry reached write boundary %q", at)
		return nil
	}
	got, err := receiver.Reply(ri, "target", sent.MessageID, "accepted answer")
	if err != nil || got.Outcome != msg.OutcomeSent || got.MessageID != replyID {
		t.Fatal(got, err)
	}
	if got, _ := receiver.Reply(ri, "target", sent.MessageID, "changed answer"); got.Outcome != msg.OutcomeConflict {
		t.Fatal(got)
	}
	if got, _ := sender.Reply(si, "source", sent.MessageID, "accepted answer"); got.Outcome != msg.OutcomeForbidden {
		t.Fatal("another inbox received the reply receipt", got)
	}
}
