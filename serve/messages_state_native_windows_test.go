//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
)

func messagesStateClient(r *messagesReceiver) *msg.MessagesClient {
	return msg.NewMessagesClient(registryCompat1687Exchange(func(frame []byte) ([]byte, error) {
		r.call.Frame = frame
		return r.host.exchangeApplicationFrame(r.ctx, r.call)
	}))
}

func messagesStateOpen(t *testing.T, f *messagesObserveRun, r *messagesReceiver, application, contextName string) (string, *msg.MessagesClient) {
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
	client := messagesStateClient(r)
	if opened, err := client.InboxOpen(presence.Instance, contextName, ""); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	return presence.Instance, client
}

func messagesStateRequest(f *messagesObserveRun, key string, kind msg.Kind) msg.SendRequest {
	request := msg.SendRequest{To: "app:other-agent", Context: "target", Kind: kind, Subject: key, Body: "dummy " + key, IdempotencyKey: key, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	if kind == msg.KindQuestion {
		request.AnswerByUnixMs = f.now.Add(time.Minute).UnixMilli()
	}
	return request
}

func messagesStateHas(page msg.Page, id string, state msg.TaskState) bool {
	for _, h := range page.Headers {
		if h.MessageID == id && h.TaskState != nil && *h.TaskState == state {
			return true
		}
	}
	for _, m := range page.Messages {
		if m.Header.MessageID == id && m.Header.TaskState != nil && *m.Header.TaskState == state {
			return true
		}
	}
	return false
}

func TestMessagesNativeWithdrawalExactOwnerReplayAndConflict(t *testing.T) {
	f := messagesObserveFixture(t)
	sourceInstance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	receiverInstance, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	request := messagesStateRequest(f, "withdraw-question", msg.KindQuestion)
	sent, err := sender.Send(sourceInstance, "source", request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	withdraw := messagesStateRequest(f, "withdraw-control", msg.KindNote)
	withdraw.Withdraw = sent.MessageID
	if got, err := receiver.Send(receiverInstance, "target", withdraw); err != nil || got.Outcome != msg.OutcomeForbidden {
		t.Fatal("receiver withdrew source question", got, err)
	}
	siblingInstance, sibling := messagesStateOpen(t, f, f.reader, "native-agent", "sibling")
	if got, err := sibling.Send(siblingInstance, "sibling", withdraw); err != nil || got.Outcome != msg.OutcomeForbidden {
		t.Fatal("sibling context withdrew source question", got, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	thirdPath := filepath.Join(t.TempDir(), "state-peer.exe")
	if err := os.WriteFile(thirdPath, raw, 0700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, f.run, "third-agent", identity.NormalizeSubjectProgram(thirdPath))
	agent1478Set(t, f.run, "third-agent", "", true)
	q := agent1478Question(t, f.run, "third-agent")
	answer(t, f.run.f, q.ID, "keep")
	agent1478Wait(t, f.run, "third-agent", "enrolled")
	thirdCall, _ := messages1608CallImage(t, thirdPath)
	thirdReceiver := &messagesReceiver{host: f.reader.host, call: thirdCall, ctx: f.run.ctx}
	thirdInstance, third := messagesStateOpen(t, f, thirdReceiver, "third-agent", "source")
	if got, err := third.Send(thirdInstance, "source", withdraw); err != nil || got.Outcome != msg.OutcomeForbidden {
		t.Fatal("third image withdrew source question", got, err)
	}
	canceled, err := sender.Send(sourceInstance, "source", withdraw)
	if err != nil || canceled.Outcome != msg.OutcomeSent {
		t.Fatal(canceled, err)
	}
	replay, err := sender.Send(sourceInstance, "source", withdraw)
	if err != nil || replay != canceled {
		t.Fatal("withdraw replay changed receipt", replay, canceled, err)
	}
	withdraw.Body = "different control"
	if got, err := sender.Send(sourceInstance, "source", withdraw); err != nil || got.Outcome != msg.OutcomeConflict {
		t.Fatal("withdraw key mismatch accepted", got, err)
	}
	for _, side := range []struct {
		c       *msg.MessagesClient
		context string
	}{{sender, "source"}, {receiver, "target"}} {
		page, err := side.c.Wait("", side.context, msg.ViewHeaders, "", "", 0)
		if err != nil || !messagesStateHas(page, sent.MessageID, msg.TaskStateCanceled) {
			t.Error("cancel not visible", page, err)
		}
	}
}

func TestMessagesNativeDeadlineVisibleToBothEndpoints(t *testing.T) {
	f := messagesObserveFixture(t)
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	_, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	request := messagesStateRequest(f, "deadline-question", msg.KindQuestion)
	sent, err := sender.Send(instance, "source", request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	*f.now = f.now.Add(2 * time.Minute)
	for _, side := range []struct {
		c       *msg.MessagesClient
		context string
	}{{sender, "source"}, {receiver, "target"}} {
		page, err := side.c.Wait("", side.context, msg.ViewHeaders, "", "", 0)
		if err != nil || !messagesStateHas(page, sent.MessageID, msg.TaskStateFailed) {
			t.Error("deadline not visible", page, err)
		}
	}
}

func TestMessagesNativeNoteReplyAndSamePrincipalAliasesRefused(t *testing.T) {
	f := messagesObserveFixture(t)
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	receiverInstance, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	note := messagesStateRequest(f, "note", msg.KindNote)
	sent, err := sender.Send(instance, "source", note)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	reply := msg.SendRequest{To: "app:native-agent", Context: "source", Kind: msg.KindReply, Subject: "reply", Body: "dummy", IdempotencyKey: "reply-to-note", ReplyTo: sent.MessageID, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	if got, err := receiver.Send(receiverInstance, "target", reply); err != nil || got.Outcome != msg.OutcomeInvalid {
		t.Fatal("reply to note accepted", got, err)
	}
	agent1478Register(t, f.run, "native-alias", f.selfProgram)
	// One kept native image already owns the class. A second display
	// registration cannot become a separate destination principal.
	for _, target := range []string{"app:native-agent", "app:native-alias"} {
		self := messagesStateRequest(f, "self-"+target, msg.KindNote)
		self.To = target
		self.Context = "sibling"
		if got, err := sender.Send(instance, "source", self); err != nil || got.Outcome != msg.OutcomeForbidden {
			t.Error("same native principal destination accepted", got, err)
		}
	}
}

func TestMessagesNativeReceiverSeesTerminalStateAfterAcknowledgment(t *testing.T) {
	for _, terminal := range []msg.TaskState{msg.TaskStateCanceled, msg.TaskStateFailed} {
		t.Run(terminal.String(), func(t *testing.T) {
			f := messagesObserveFixture(t)
			instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
			receiverInstance, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
			request := messagesStateRequest(f, "post-ack", msg.KindQuestion)
			sent, err := sender.Send(instance, "source", request)
			if err != nil || sent.Outcome != msg.OutcomeSent {
				t.Fatal(sent, err)
			}
			body, err := receiver.Wait(receiverInstance, "target", msg.ViewBody, "", "", 0)
			if err != nil || len(body.Messages) != 1 || body.Cursor == "" {
				t.Fatal(body, err)
			}
			ack, err := receiver.Wait(receiverInstance, "target", msg.ViewBody, body.Cursor, "", 0)
			if err != nil || len(ack.Messages) != 0 {
				t.Fatal("ack", ack, err)
			}
			if terminal == msg.TaskStateCanceled {
				withdraw := messagesStateRequest(f, "post-ack-cancel", msg.KindNote)
				withdraw.Withdraw = sent.MessageID
				if got, err := sender.Send(instance, "source", withdraw); err != nil || got.Outcome != msg.OutcomeSent {
					t.Fatal(got, err)
				}
			} else {
				*f.now = f.now.Add(2 * time.Minute)
			}
			if _, err := sender.Wait("", "source", msg.ViewHeaders, "", "", 0); err != nil {
				t.Fatal(err)
			}
			headers, err := receiver.Wait("", "target", msg.ViewHeaders, "", "", 0)
			if err != nil || !messagesStateHas(headers, sent.MessageID, terminal) {
				t.Error("acknowledged receiver lost terminal header", headers, err)
			}
			changed, err := receiver.Wait(receiverInstance, "target", msg.ViewBody, "", "", 0)
			if err != nil || !messagesStateHas(changed, sent.MessageID, terminal) {
				t.Error("acknowledged receiver lost terminal event", changed, err)
			}
			if changed.Cursor != "" {
				if _, err = receiver.Wait(receiverInstance, "target", msg.ViewBody, changed.Cursor, "", 0); err != nil {
					t.Fatal(err)
				}
			}
			source, err := sender.Wait(instance, "source", msg.ViewBody, "", "", 0)
			if err != nil || !messagesStateHas(source, sent.MessageID, terminal) {
				t.Fatal("receiver ack consumed source state", source, err)
			}
		})
	}
}
