//go:build windows

package main

import (
	"errors"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func pairedChatSource(t *testing.T, f *remoteMessagesFixture) (string, *messagesReceiver, string, *msg.MessagesClient, string) {
	t.Helper()
	n := f.nodes[0]
	call, _ := messages1608Call(t)
	b := &messagesReceiver{host: n.reader.host, call: call, ctx: n.run.ctx}
	bi, route := messagesTypedChatAt(t, n, b, "native-agent", "canonical-chat", "Paired chat", "source-b")
	return bi, b, bi, messagesStateClient(b), route
}

func TestMessagesPairedSendChatSealsSourceAndReturnRoute(t *testing.T) {
	f := pairedMessagesFixture(t)
	bi, b, ai, a, route := pairedChatSource(t, f)
	f.permit(t, 0, "app:other-agent")
	request := f.request(0, "chat-key")
	request.Kind = msg.KindQuestion
	request.AnswerByUnixMs = f.nodes[0].now.Add(time.Minute).UnixMilli()
	chat := msg.ChatSendRequest{Request: request, ReturnContext: route}
	client := messagesStateClient(b)
	first, err := client.SendChat(bi, "source-b", chat)
	if err != nil || first.Outcome != msg.OutcomeSent {
		t.Fatal(first, err)
	}
	record, _, err := f.nodes[1].store.readByID(t.Context(), first.MessageID)
	if err != nil || record == nil || record.Actor.Context != "source-b" || record.Actor.Remote == nil || record.Actor.Remote.ReturnContext != route || record.Actor.Program != "remote:"+f.keys[0] {
		t.Fatal(record, err)
	}
	stored, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil || stored.Origin == nil || stored.Origin.ReturnContext != route {
		t.Fatal(stored, err)
	}
	actor, _, _, ok := b.owner(bi, "source-b")
	if !ok {
		t.Fatal("source owner lost")
	}
	if local, err := f.nodes[0].store.readRecord(t.Context(), f.nodes[0].store.key(actor, request.IdempotencyKey)); err != nil || local != nil {
		t.Fatal("paired dispatch created a source outbox", local, err)
	}
	replay, err := client.SendChat(bi, "source-b", chat)
	if err != nil || replay.Outcome != msg.OutcomeSent || replay.MessageID != first.MessageID {
		t.Fatal(replay, err)
	}
	changed := chat
	changed.ReturnContext = "other-chat"
	if out, _ := client.SendChat(bi, "source-b", changed); out.Outcome != msg.OutcomeForbidden {
		t.Fatal("changed return route escaped remote receipt", out)
	}
	changed = chat
	changed.Request.Body = "changed body"
	if out, _ := client.SendChat(bi, "source-b", changed); out.Outcome != msg.OutcomeConflict {
		t.Fatal("changed body escaped remote receipt", out)
	}
	changed = chat
	changed.Request.IdempotencyKey = "foreign-route"
	changed.ReturnContext = "target"
	if out, _ := client.SendChat(bi, "source-b", changed); out.Outcome != msg.OutcomeForbidden {
		t.Fatal("foreign canonical route dispatched", out)
	}
	ri, receiver := messagesStateOpen(t, f.nodes[1], f.nodes[1].other, "other-agent", "target")
	page, err := receiver.Wait(ri, "target", msg.ViewBody, "", "", 0)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Header.FromContext != route || page.Messages[0].Header.RemoteOrigin.Claim.ReturnContext != route {
		t.Fatal(page, err)
	}
	pin, err := f.nodes[1].other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, _, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.nodes[1].reader.host.messageRights.policy.Set(subject, ActionApplicationMessage, messagePeerResource(f.keys[0], "app:native-agent"), true); err != nil {
		t.Fatal(err)
	}
	if err := f.nodes[0].reader.host.messageRights.policy.Set(rights.Subject{Account: f.nodes[0].reader.host.owner, Program: "remote:" + f.keys[1]}, ActionApplicationMessage, "app:native-agent", true); err != nil {
		t.Fatal(err)
	}
	if out, err := receiver.Reply(ri, "target", first.MessageID, "paired canonical answer"); err != nil || out.Outcome != msg.OutcomeSent {
		t.Fatal(out, err)
	}
	if page, err := a.Wait(ai, route, msg.ViewBody, "", "", 0); err != nil || !chatPageHasReply(page, "paired canonical answer") {
		t.Fatal(page, err)
	}
	if page, err := client.Wait(bi, route, msg.ViewBody, "", "", 0); err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal("held source acquired paired reply body", page, err)
	}
}

func TestMessagesPairedSendChatUnknownAndRevocationStayFenced(t *testing.T) {
	f := pairedMessagesFixture(t)
	bi, b, _, _, route := pairedChatSource(t, f)
	f.permit(t, 0, "app:other-agent")
	chat := msg.ChatSendRequest{Request: f.request(0, "chat-unknown"), ReturnContext: route}
	fired := false
	f.nodes[1].store.boundary = func(at string) error {
		if at == "record_published" && !fired {
			fired = true
			return errors.New("owned acceptance reply loss")
		}
		return nil
	}
	first, err := b.SendChat(bi, "source-b", chat)
	f.nodes[1].store.boundary = nil
	if err != nil || !fired || first.Outcome != msg.OutcomeUnknown {
		t.Fatal(first, err, fired)
	}
	changed := chat
	changed.ReturnContext = "other-chat"
	if out, _ := b.SendChat(bi, "source-b", changed); out.Outcome != msg.OutcomeForbidden {
		t.Fatal("uncertain send retargeted", out)
	}
	replay, err := b.SendChat(bi, "source-b", chat)
	if err != nil || replay.Outcome != msg.OutcomeSent {
		t.Fatal(replay, err)
	}
	if err := f.nodes[0].reader.host.messageRights.policy.Set(f.nodes[0].self, ActionApplicationMessage, messagePeerResource(f.keys[1], chat.Request.To), false); err != nil {
		t.Fatal(err)
	}
	if out, _ := b.SendChat(bi, "source-b", chat); out.Outcome != msg.OutcomeForbidden {
		t.Fatal("revoked source pair permission replayed", out)
	}
	if remote, _, err := b.pairedTransport(f.keys[1]); err != nil {
		t.Fatal(err)
	} else if _, err := remote.SendChat(bi, "source-b", chat); err == nil {
		t.Fatal("remote endpoint exposed local SendChat")
	}
}
