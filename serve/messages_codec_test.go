package main

import (
	"bytes"
	"math"
	"strings"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// This uses all independent schema maxima together, including the two arrays
// that the receiving view rule makes mutually exclusive. Six bytes per input
// UTF-8 byte bounds every unrestricted JSON escape; no_control strings need at
// most two bytes for quotes/backslashes. 64KiB covers the fixed fields,
// their names, enum words, int64 spellings and the generator's fixed indentation.
const messagesWorstHeaderFrame = 2*(128+68+64+68+64+128+2*(64+128)+64+64+64+256+64) + 6*(320+128)
const messagesWorstPageFrame = 10*(messagesWorstHeaderFrame+128*6+8192*6) + 50*messagesWorstHeaderFrame + (256+128)*2 + 65536

type messagesCodecOwner struct {
	page msg.Page
}

func (o *messagesCodecOwner) ObserveDelivery(msg.MessageRef, string) (msg.ObserveDeliveryResult, error) {
	return msg.ObserveDeliveryResult{Outcome: msg.DeliveryObservationOutcomeForbidden}, nil
}

func (o *messagesCodecOwner) InboxOpen(string, string, string) (msg.InboxChange, error) {
	return msg.InboxChange{Outcome: msg.OutcomeOpened}, nil
}
func (o *messagesCodecOwner) InboxesList(msg.InboxQuery) (msg.InboxPage, error) {
	rows := make([]msg.Inbox, 64)
	for i := range rows {
		rows[i] = msg.Inbox{Program: strings.Repeat("x", 68), Context: strings.Repeat("x", 64), Status: strings.Repeat("\x00", 800), ChatTitle: strings.Repeat("\x00", 256), Revision: strings.Repeat("x", 128), LastSeenUnixMs: math.MinInt64, Reach: new(msg.InboxReachStartsOnQuestion)}
	}
	return msg.InboxPage{Outcome: msg.OutcomePage, Cursor: strings.Repeat("x", 256), Inboxes: rows}, nil
}
func (o *messagesCodecOwner) Send(string, string, msg.SendRequest) (msg.SendResult, error) {
	return msg.SendResult{Outcome: msg.OutcomeSent, MessageID: strings.Repeat("x", 128), ContextID: strings.Repeat("x", 128)}, nil
}
func (o *messagesCodecOwner) SendChat(string, string, msg.ChatSendRequest) (msg.SendResult, error) {
	return msg.SendResult{Outcome: msg.OutcomeForbidden}, nil
}
func (o *messagesCodecOwner) Prepare(string, string, msg.PrepareRequest) (msg.PrepareResult, error) {
	return msg.PrepareResult{Outcome: msg.OutcomeForbidden}, nil
}
func (o *messagesCodecOwner) Reply(string, string, string, string) (msg.SendResult, error) {
	return msg.SendResult{Outcome: msg.OutcomeForbidden}, nil
}
func (o *messagesCodecOwner) Wait(string, string, msg.View, string, string, int64) (msg.Page, error) {
	return o.page, nil
}

func (o *messagesCodecOwner) ReadForPerson(string) (msg.Page, error) {
	return messageEmptyPage(msg.OutcomeForbidden), nil
}

func (o *messagesCodecOwner) StopForPerson(string) (msg.StopResult, error) {
	return msg.StopResult{Outcome: msg.OutcomeForbidden}, nil
}

func (o *messagesCodecOwner) TurnWake(string, string, string) (msg.TurnWakeResult, error) {
	return msg.TurnWakeResult{Outcome: msg.OutcomeForbidden}, nil
}

type messagesSizedTransport struct {
	dispatch       *msg.MessagesDispatcher
	request, reply int
}

func (x *messagesSizedTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	x.request = len(frame)
	reply, err := x.dispatch.ExchangeFrame(frame)
	x.reply = len(reply)
	return reply, err
}

func TestMessagesGeneratedWorstCaseFrames(t *testing.T) {
	if messagesWorstPageFrame != 896704 || messagesWorstPageFrame >= 1<<20 {
		t.Fatal("frame arithmetic exceeds existing listener", messagesWorstPageFrame)
	}
	state := msg.TaskStateAuthRequired
	header := msg.Header{MessageID: strings.Repeat("x", 128), From: strings.Repeat("x", 68), FromContext: strings.Repeat("x", 64), To: strings.Repeat("x", 68), Context: strings.Repeat("x", 64), Kind: msg.KindQuestion, Subject: strings.Repeat("\x00", 320), ContextID: strings.Repeat("x", 128), ExpiresUnixMs: math.MinInt64, ReplyTo: strings.Repeat("x", 128), AnswerByUnixMs: math.MinInt64, TaskState: &state, Refused: true}
	header.Reference = &msg.MessageRef{OwnerPeerKey: strings.Repeat(`"`, 64), MessageID: strings.Repeat(`"`, 128)}
	header.ReplyReference = header.Reference
	header.RemoteOrigin = &msg.RemoteOrigin{PeerKey: strings.Repeat(`"`, 64), Claim: msg.OriginClaim{Namespace: strings.Repeat(`"`, 64), Application: strings.Repeat(`"`, 64), ProgramLabel: strings.Repeat(`"`, 256), ReturnContext: strings.Repeat(`"`, 64)}}
	page := msg.Page{Outcome: msg.OutcomePage, Cursor: strings.Repeat("x", 256), Revision: strings.Repeat("x", 128), Messages: make([]msg.Message, 10), Headers: make([]msg.Header, 50)}
	for i := range page.Messages {
		page.Messages[i] = msg.Message{Header: header, IdempotencyKey: strings.Repeat("\x00", 128), Body: strings.Repeat("\x00", 8192)}
	}
	for i := range page.Headers {
		page.Headers[i] = header
	}
	x := &messagesSizedTransport{dispatch: &msg.MessagesDispatcher{Handler: &messagesCodecOwner{page: page}}}
	client := msg.NewMessagesClient(x)
	got, err := client.Wait(strings.Repeat("x", 128), strings.Repeat("x", 64), msg.ViewBody, strings.Repeat("x", 256), strings.Repeat("x", 128), 30000)
	if err != nil || len(got.Messages) != 10 || len(got.Headers) != 50 || x.reply > messagesWorstPageFrame {
		t.Fatal("maximum generated frame", err, x.request, x.reply)
	}
	if _, err = client.InboxesList(msg.InboxQuery{Cursor: strings.Repeat("x", 256)}); err != nil || x.reply >= 1<<20 {
		t.Fatal("maximum inbox frame", err, x.reply)
	}
	request := msg.SendRequest{To: strings.Repeat("x", 68), Context: strings.Repeat("x", 64), Kind: msg.KindQuestion, Subject: strings.Repeat("\x00", 320), Body: strings.Repeat("\x00", 8192), IdempotencyKey: strings.Repeat("\x00", 128), ExpiresUnixMs: math.MinInt64, ReplyTo: strings.Repeat("x", 128), Withdraw: strings.Repeat("x", 128), AnswerByUnixMs: math.MinInt64}
	if _, err = client.Send(strings.Repeat("x", 128), strings.Repeat("x", 64), request); err != nil || x.request >= 1<<20 {
		t.Fatal("maximum send frame", err, x.request)
	}
	if _, err = client.SendChat(strings.Repeat("x", 128), strings.Repeat("x", 64), msg.ChatSendRequest{Request: request, ReturnContext: strings.Repeat(`"`, 64)}); err != nil || x.request >= 1<<20 {
		t.Fatal("maximum chat send frame", err, x.request)
	}
	encoded := msg.EncodeSendRequest(&request)
	if !bytes.Contains(encoded, []byte(`\u0000`)) {
		t.Fatal("escape counterexample did not exercise six-byte escapes")
	}
	request.Body += "x"
	// The generated reader, rather than a native copy of its grammar, owns
	// the exact byte bound. Start with the accepted emitted document.
	tooLarge := bytes.Replace(encoded, []byte(`"body": "`), []byte(`"body": "x`), 1)
	if _, err = msg.DecodeSendRequest(tooLarge); err == nil {
		t.Fatal("generated body bound accepted8193 bytes")
	}
}

func TestMessagesGeneratedHeaderObserverWithoutPresence(t *testing.T) {
	x := &messagesSizedTransport{dispatch: &msg.MessagesDispatcher{Handler: &messagesCodecOwner{page: msg.Page{Outcome: msg.OutcomePage, Messages: []msg.Message{}, Headers: []msg.Header{}}}}}
	// A separate same-program native hook has no presence instance to supply.
	// Receiving code authenticates it and enforces the mutually exclusive view.
	page, err := msg.NewMessagesClient(x).Wait("", "closed-context", msg.ViewHeaders, "", "known", 0)
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal("generated argument grammar forced presence borrowing", page, err)
	}
}

func (p *messagesCodecOwner) InboxOpenChat(i, c, s string, _ msg.ChatBinding) (msg.ChatInboxChange, error) {
	out, err := p.InboxOpen(i, c, s)
	return msg.ChatInboxChange{Outcome: out.Outcome, Revision: out.Revision, Holder: out.Holder, OpenedUnixMs: out.OpenedUnixMs}, err
}

func (p *messagesCodecOwner) InboxesForPerson(string) (msg.InboxPage, error) {
	return p.InboxesList(msg.InboxQuery{})
}
