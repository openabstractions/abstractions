//go:build windows

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The bridge changes only the transport's native process. All capability frames
// originate in the normal generated clients and traverse the real listener.
func TestMessagesReceiverBridgeChild(t *testing.T) {
	endpoint := os.Getenv("OA_MESSAGES1756_BRIDGE")
	if endpoint == "" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var frame []byte
		if err := decoder.Decode(&frame); err == io.EOF {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		conn, err := listen.Dial(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if err = listen.WriteFrameTo(conn, frame, 1<<20); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		reply, err := listen.ReadFrameFrom(conn, 1<<20)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err = encoder.Encode(reply); err != nil {
			t.Fatal(err)
		}
	}
}

type messagesNativeBridge struct {
	input  *json.Encoder
	output *json.Decoder
}

func messagesReceiverHasReply(page msg.Page, body string) bool {
	reply, completed := false, false
	for _, message := range page.Messages {
		if message.Header.Kind == msg.KindReply && message.Body == body {
			reply = true
		}
		if message.Header.Kind == msg.KindQuestion && message.Header.TaskState != nil && *message.Header.TaskState == msg.TaskStateCompleted {
			completed = true
		}
	}
	return reply && completed
}

func (b *messagesNativeBridge) ExchangeFrame(frame []byte) ([]byte, error) {
	if err := b.input.Encode(frame); err != nil {
		return nil, err
	}
	var reply []byte
	err := b.output.Decode(&reply)
	return reply, err
}
func messagesReceiverBridge(t *testing.T, exe, endpoint string) *messagesNativeBridge {
	t.Helper()
	cmd := exec.Command(exe, "-test.run=^TestMessagesReceiverBridgeChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "OA_MESSAGES1756_BRIDGE="+endpoint)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	})
	return &messagesNativeBridge{input: json.NewEncoder(in), output: json.NewDecoder(out)}
}

func TestMessagesReceiverGeneratedNativeTransport(t *testing.T) {
	t.Run("saved-permits", func(t *testing.T) { runMessagesReceiverGeneratedNativeTransport(t, false) })
	t.Run("person-consent", func(t *testing.T) { runMessagesReceiverGeneratedNativeTransport(t, true) })
}

func messagesReceiverPersonAllow(t *testing.T, run *selfRegisteredRun, resource string) {
	t.Helper()
	records, _, err := run.f.a.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, record := range records {
		if record.Pending() && messagePermissionRecord(run.f.a.rights.owner, record, resource) {
			id = record.ID
			break
		}
	}
	if id == "" {
		t.Fatal("person permission question missing", resource)
	}
	if _, _, err = run.f.a.book.AnswerApplication(id, "allow"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := run.f.a.rights.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.f.a.applyOperatorAllow(run.ctx, run.f.panel, snapshot.Revision, id, func() error { return nil })
	if err != nil || result.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatal("person allow", result, err)
	}
}

func runMessagesReceiverGeneratedNativeTransport(t *testing.T, personConsent bool) {
	run, _ := messages1608Fixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "receiver.exe")
	if err = os.WriteFile(other, raw, 0700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, run, "receiver", identity.NormalizeSubjectProgram(other))
	agent1478Set(t, run, "receiver", "", true)
	q := agent1478Question(t, run, "receiver")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "receiver", "enrolled")
	policy := run.f.a.rights
	if err = policy.policy.RegisterAction(ActionApplicationMessage); err != nil {
		t.Fatal(err)
	}
	if personConsent {
		policy.applications = run.d
		run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
		if err = run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
			t.Fatal(err)
		}
		if err = policy.configureMessagePermits(run.d); err != nil {
			t.Fatal(err)
		}
	} else {
		for _, rule := range []struct{ program, to string }{{identity.NormalizeSubjectProgram(exe), "receiver"}, {identity.NormalizeSubjectProgram(other), "native-agent"}} {
			if err = policy.policy.Set(rights.Subject{Account: policy.owner, Program: rule.program}, ActionApplicationMessage, "app:"+rule.to, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	id, err := applicationToken()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := mustProviderEndpoint(t, "oa-test-messages1756-"+id[:16])
	host, err := listenApplications(endpoint, run.d, policy.owner, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	var messageClock atomic.Int64
	messageClock.Store(time.Now().UnixMilli())
	host.messages, err = openMessageStore(run.state, func() time.Time { return time.UnixMilli(messageClock.Load()) })
	if err != nil {
		t.Fatal(err)
	}
	host.messageRights = policy
	ctx, cancel := context.WithCancel(run.ctx)
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		host.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	senderBridge := messagesReceiverBridge(t, exe, endpoint)
	receiverBridge := messagesReceiverBridge(t, other, endpoint)
	senderApps, receiverApps := wire.NewApplicationsClient(senderBridge), wire.NewApplicationsClient(receiverBridge)
	announce := func(client *wire.ApplicationsClient, name string) string {
		p, e := client.Announce(wire.ApplicationPresence{Application: name, LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: "ctx"}}})
		if e != nil || p.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(p, e)
		}
		return p.Instance
	}
	senderInstance, receiverInstance := announce(senderApps, "native-agent"), announce(receiverApps, "receiver")
	sender, receiver := msg.NewMessagesClient(senderBridge), msg.NewMessagesClient(receiverBridge)
	for _, opening := range []struct {
		client   *msg.MessagesClient
		instance string
	}{{sender, senderInstance}, {receiver, receiverInstance}} {
		v, e := opening.client.InboxOpen(opening.instance, "ctx", "")
		if e != nil || v.Outcome != msg.OutcomeOpened {
			t.Fatal(v, e)
		}
	}
	request := msg.SendRequest{To: "app:receiver", Context: "ctx", Kind: msg.KindQuestion, Subject: "Question", Body: "native transport question", IdempotencyKey: "transport-question", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()}
	sent, e := sender.Send(senderInstance, "ctx", request)
	if e != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, e)
	}
	if personConsent {
		if !sent.AwaitingPermission {
			t.Fatal("held receipt missing", sent)
		}
		held, e := receiver.Wait(receiverInstance, "ctx", msg.ViewBody, "", "", 0)
		if e != nil || len(held.Messages) != 0 {
			t.Fatal("body before consent", held, e)
		}
		messagesReceiverPersonAllow(t, run, "app:receiver")
	}
	headers, e := receiver.Wait("", "ctx", msg.ViewHeaders, "", "", 0)
	if e != nil || headers.Outcome != msg.OutcomePage || len(headers.Messages) != 0 || headers.Cursor != "" || len(headers.Headers) == 0 {
		t.Fatal("header observation disclosed or consumed a body", headers, e)
	}
	page, e := receiver.Wait(receiverInstance, "ctx", msg.ViewBody, "", "", 1000)
	if e != nil || len(page.Messages) != 1 || page.Messages[0].Body != request.Body {
		t.Fatal(page, e)
	}
	reply := msg.SendRequest{To: "app:native-agent", Context: "ctx", Kind: msg.KindReply, Subject: "Reply", Body: "native transport reply", IdempotencyKey: "transport-reply", ReplyTo: sent.MessageID, ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()}
	result, e := receiver.Send(receiverInstance, "ctx", reply)
	if e != nil || result.Outcome != msg.OutcomeSent {
		t.Fatal(result, e)
	}
	if personConsent {
		if !result.AwaitingPermission {
			t.Fatal("reply consent missing", result)
		}
		messagesReceiverPersonAllow(t, run, "app:native-agent")
	} else {
		// R19b: the receiver read the question's body page and replied without
		// acknowledging the page. The reply proves delivery: the question never
		// expires as undelivered.
		answered, _, e := host.messages.readByID(run.ctx, sent.MessageID)
		if e != nil || answered == nil || !answered.Acked || answered.Delivery == nil || answered.Delivery.State != msg.DeliveryDelivered {
			t.Fatal("a replied question stayed undelivered", answered, e)
		}
	}
	answerPage, e := sender.Wait(senderInstance, "ctx", msg.ViewBody, "", "", 1000)
	if e != nil || !messagesReceiverHasReply(answerPage, reply.Body) {
		t.Fatal(answerPage, e)
	}
	ack, e := sender.Wait(senderInstance, "ctx", msg.ViewBody, answerPage.Cursor, "", 0)
	if e != nil || len(ack.Messages) != 0 {
		t.Fatal(ack, e)
	}
	if personConsent {
		return
	}
	// Exercise the same two authenticated native processes after the first
	// exchange, including a pinned body whose lifetime ends before the next one.
	note := msg.SendRequest{To: "app:receiver", Context: "ctx", Kind: msg.KindNote, Body: "ordinary note", IdempotencyKey: "native-note", ExpiresUnixMs: messageClock.Load() + 3600000}
	noted, e := sender.Send(senderInstance, "ctx", note)
	if e != nil || noted.Outcome != msg.OutcomeSent {
		t.Fatal("note", noted, e)
	}
	// A previously disclosed question remains replayable until its explicit ack.
	notePage, e := receiver.Wait(receiverInstance, "ctx", msg.ViewBody, page.Cursor, "", 0)
	foundNote, foundCompletion := false, false
	// An ACK may return the next page. Completion of the earlier question is
	// an independent receiving event and must itself be received and ACKed.
	for pages := 0; e == nil && len(notePage.Messages) > 0 && pages < 4; pages++ {
		for _, message := range notePage.Messages {
			switch message.Header.MessageID {
			case noted.MessageID:
				foundNote = message.Body == note.Body
			case sent.MessageID:
				foundCompletion = message.Header.TaskState != nil && *message.Header.TaskState == msg.TaskStateCompleted
			default:
				t.Fatal("unexpected message after question and note", message)
			}
		}
		if notePage.Cursor == "" {
			t.Fatal("received page missing ACK cursor", notePage)
		}
		notePage, e = receiver.Wait(receiverInstance, "ctx", msg.ViewBody, notePage.Cursor, "", 0)
	}
	if e != nil || !foundNote || !foundCompletion || len(notePage.Messages) != 0 {
		t.Fatal("note and completion ACK", notePage, foundNote, foundCompletion, e)
	}
	old := request
	old.IdempotencyKey, old.Body = "expires-before-next", "old question"
	old.ExpiresUnixMs, old.AnswerByUnixMs = messageClock.Load()+60000, messageClock.Load()+60000
	oldSent, e := sender.Send(senderInstance, "ctx", old)
	if e != nil || oldSent.Outcome != msg.OutcomeSent {
		t.Fatal(oldSent, e)
	}
	oldPage, e := receiver.Wait(receiverInstance, "ctx", msg.ViewBody, "", "", 0)
	if e != nil || len(oldPage.Messages) != 1 || oldPage.Messages[0].Header.MessageID != oldSent.MessageID {
		t.Fatal(oldPage, e)
	}
	messageClock.Add(120000)
	fresh := old
	fresh.IdempotencyKey, fresh.Body = "after-expired-page", "fresh question"
	fresh.ExpiresUnixMs, fresh.AnswerByUnixMs = messageClock.Load()+3600000, messageClock.Load()+3600000
	freshSent, e := sender.Send(senderInstance, "ctx", fresh)
	if e != nil || freshSent.Outcome != msg.OutcomeSent {
		t.Fatal(freshSent, e)
	}
	headers, e = receiver.Wait("", "ctx", msg.ViewHeaders, "", "", 0)
	if e != nil || len(headers.Messages) != 0 || headers.Cursor != "" {
		t.Fatal(headers, e)
	}
	found := false
	for _, h := range headers.Headers {
		found = found || h.MessageID == freshSent.MessageID
	}
	if !found {
		t.Fatal("new question missing from headers", headers)
	}
	freshPage, e := receiver.Wait(receiverInstance, "ctx", msg.ViewBody, "", "", 0)
	if e != nil || len(freshPage.Messages) != 1 || freshPage.Messages[0].Header.MessageID != freshSent.MessageID {
		t.Fatal("first read after expired page", freshPage, e)
	}
	replay, e := receiver.Wait(receiverInstance, "ctx", msg.ViewBody, "", "", 0)
	if e != nil || replay.Cursor != freshPage.Cursor || len(replay.Messages) != 1 {
		t.Fatal("lost page replay", replay, e)
	}
	if retired, e := receiverApps.Withdraw("receiver", receiverInstance); e != nil || retired.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(retired, e)
	}
	closed, e := receiver.Wait(receiverInstance, "ctx", msg.ViewBody, "", "", 0)
	if e != nil || closed.Outcome != msg.OutcomeForbidden {
		t.Fatal("retired receiver", closed, e)
	}
	if retired, e := senderApps.Withdraw("native-agent", senderInstance); e != nil || retired.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(retired, e)
	}
	fresh.IdempotencyKey = "retired-sender"
	denied, e := sender.Send(senderInstance, "ctx", fresh)
	if e != nil || denied.Outcome != msg.OutcomeForbidden || denied.MessageID != "" {
		t.Fatal("retired sender", denied, e)
	}
}

func TestMessagesReceiverNativeRequestReply(t *testing.T) {
	run, _ := messages1608Fixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "receiver.exe")
	if err = os.WriteFile(other, raw, 0700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, run, "receiver", identity.NormalizeSubjectProgram(other))
	agent1478Set(t, run, "receiver", "", true)
	q := agent1478Question(t, run, "receiver")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "receiver", "enrolled")
	senderCall, _ := messages1608Call(t)
	receiverCall, _ := messages1608CallImage(t, other)
	senderInstance := messages1608Presence(t, run, senderCall, "", "ctx")
	pin, err := receiverCall.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	presence := run.d.announceInSession(run.ctx, subject, session, wire.ApplicationPresence{Application: "receiver", LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: "ctx"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	store, err := openMessageStore(run.state, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	policy := run.f.a.rights
	if err = policy.policy.RegisterAction(ActionApplicationMessage); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []struct{ program, to string }{{identity.NormalizeSubjectProgram(exe), "receiver"}, {subject.Program, "native-agent"}} {
		if err = policy.policy.Set(rights.Subject{Account: subject.Account, Program: rule.program}, ActionApplicationMessage, "app:"+rule.to, true); err != nil {
			t.Fatal(err)
		}
	}
	host := &applicationsHost{directory: run.d, messages: store, messageRights: policy}
	sender := &messagesReceiver{host: host, call: senderCall, ctx: run.ctx}
	receiver := &messagesReceiver{host: host, call: receiverCall, ctx: run.ctx}
	for _, opening := range []struct {
		r        *messagesReceiver
		instance string
	}{{sender, senderInstance}, {receiver, presence.Instance}} {
		opened, e := opening.r.InboxOpen(opening.instance, "ctx", "")
		if e != nil || opened.Outcome != msg.OutcomeOpened {
			t.Fatal(opened, e)
		}
	}
	senderClient := msg.NewMessagesClient(registryCompat1687Exchange(func(frame []byte) ([]byte, error) {
		senderCall.Frame = frame
		return host.exchangeApplicationFrame(run.ctx, senderCall)
	}))
	receiverClient := msg.NewMessagesClient(registryCompat1687Exchange(func(frame []byte) ([]byte, error) {
		receiverCall.Frame = frame
		return host.exchangeApplicationFrame(run.ctx, receiverCall)
	}))
	request := msg.SendRequest{To: "app:receiver", Context: "ctx", Kind: msg.KindQuestion, Subject: "Question", Body: "actual native question", IdempotencyKey: "request-1", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()}
	sent, e := senderClient.Send(senderInstance, "ctx", request)
	if e != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal("send", sent, e)
	}
	page, e := receiverClient.Wait(presence.Instance, "ctx", msg.ViewBody, "", "", 0)
	if e != nil || len(page.Messages) != 1 || page.Messages[0].Body != request.Body {
		t.Fatal("wait", page, e)
	}
	if page.Messages[0].Header.TaskState == nil || *page.Messages[0].Header.TaskState != msg.TaskStateSubmitted {
		t.Fatal("pending question state", page)
	}
	replay, e := receiverClient.Wait(presence.Instance, "ctx", msg.ViewBody, "", "", 0)
	if e != nil || replay.Cursor != page.Cursor {
		t.Fatal("lost reply replay", replay, e)
	}
	reply := msg.SendRequest{To: "app:native-agent", Context: "ctx", Kind: msg.KindReply, Subject: "Reply", Body: "actual native answer", IdempotencyKey: "reply-1", ReplyTo: sent.MessageID, ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()}
	replied, e := receiverClient.Send(presence.Instance, "ctx", reply)
	if e != nil || replied.Outcome != msg.OutcomeSent {
		t.Fatal("reply", replied, e)
	}
	received, e := senderClient.Wait(senderInstance, "ctx", msg.ViewBody, "", "", 0)
	if e != nil || !messagesReceiverHasReply(received, reply.Body) {
		t.Fatal("sender reply", received, e)
	}
	ack, e := senderClient.Wait(senderInstance, "ctx", msg.ViewBody, received.Cursor, "", 0)
	if e != nil || len(ack.Messages) != 0 {
		t.Fatal("ack", ack, e)
	}
	wrong, e := receiverClient.Wait(senderInstance, "ctx", msg.ViewBody, "", "", 0)
	if e != nil || wrong.Outcome != msg.OutcomeForbidden {
		t.Fatal("borrowed instance", wrong, e)
	}
	request.To = "app:native-agent"
	request.IdempotencyKey = "self"
	self, e := senderClient.Send(senderInstance, "ctx", request)
	if e != nil || self.Outcome != msg.OutcomeForbidden {
		t.Fatal("same principal", self, e)
	}
}

func TestMessagesReceiverGeneratedHeldAndExplicitStatusClear(t *testing.T) {
	run, _ := messages1608Fixture(t)
	call, _ := messages1608Call(t)
	instance := messages1608Presence(t, run, call, "", "ctx")
	host := &applicationsHost{directory: run.d}
	client := msg.NewMessagesClient(registryCompat1687Exchange(func(frame []byte) ([]byte, error) {
		call.Frame = frame
		return (&msg.MessagesDispatcher{Handler: &messagesReceiver{host: host, call: call, ctx: run.ctx}}).ExchangeFrame(frame)
	}))
	first, err := client.InboxOpen(instance, "ctx", "busy")
	if err != nil || first.Outcome != msg.OutcomeOpened {
		t.Fatal(first, err)
	}
	renewed, err := client.InboxOpen(instance, "ctx", "")
	if err != nil || renewed.Revision == first.Revision || messages1608Row(t, run.d).status != "" {
		t.Fatal("explicit empty status did not replace metadata", renewed, err)
	}
	other, _ := messages1608Call(t)
	otherClient := msg.NewMessagesClient(registryCompat1687Exchange(func(frame []byte) ([]byte, error) {
		other.Frame = frame
		return (&msg.MessagesDispatcher{Handler: &messagesReceiver{host: host, call: other, ctx: run.ctx}}).ExchangeFrame(frame)
	}))
	held, err := otherClient.InboxOpen(instance, "ctx", "")
	if err != nil || held.Outcome != msg.OutcomeHeld || held.Holder != "app:native-agent" {
		t.Fatal("held projection", held, err)
	}
}
