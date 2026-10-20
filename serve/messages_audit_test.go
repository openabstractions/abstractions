package main

import (
	"bytes"
	"encoding/json"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	logging "github.com/openabstractions/abstraction-logging/go"
)

type messageAuditSink struct{ records []logging.Record }

func (s *messageAuditSink) Write(record logging.Record) error {
	s.records = append(s.records, record)
	return nil
}

func TestMessagesAuditPrivateProjectionRotationAndFailure(t *testing.T) {
	sink := &messageAuditSink{}
	a, err := makeMessageAudit(sink, bytes.NewReader(bytes.Repeat([]byte{1}, 48)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	request := msg.SendRequest{To: "app:receiver", Context: "target", Kind: msg.KindNote, Subject: "SECRET_SUBJECT", Body: "SECRET_BODY\né", IdempotencyKey: "key", ExpiresUnixMs: 2000}
	record := &messageRecord{ID: "sender-1", Actor: messageActor{Account: "account", Program: "native-source", Application: "sender", Context: "source"}, Receiver: "native-target", Spec: msg.EncodeSendRequest(&request), AcceptedAt: 1000, ExpiresUnixMs: 2000}
	entry, err := a.prepare(record)
	if err != nil || len(entry.Digest) != 64 || len(entry.Epoch) != 32 || len(sink.records) != 0 {
		t.Fatal(entry, err)
	}
	if err := a.append(entry); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sink.records)
	if err != nil || bytes.Contains(raw, []byte("SECRET_")) || bytes.Contains(raw, []byte("é")) {
		t.Fatal("plaintext audit", string(raw), err)
	}
	if len(sink.records) != 1 || sink.records[0].Attrs["message_id"] != record.ID || sink.records[0].Attrs["content_hmac"] != entry.Digest {
		t.Fatal(sink.records)
	}
	b, err := makeMessageAudit(sink, bytes.NewReader(bytes.Repeat([]byte{2}, 48)))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	rotated, err := b.prepare(record)
	if err != nil || rotated.Digest == entry.Digest || rotated.Epoch == entry.Epoch {
		t.Fatal("runtime key did not rotate", rotated, err)
	}
	if _, err := makeMessageAudit(sink, bytes.NewReader([]byte{1})); err == nil {
		t.Fatal("entropy failure accepted")
	}
	if _, err := makeMessageAudit(nil, bytes.NewReader(bytes.Repeat([]byte{1}, 48))); err == nil {
		t.Fatal("missing sink accepted")
	}
	a.close()
	if _, err := a.prepare(record); err == nil {
		t.Fatal("closed audit prepared")
	}
	if err := a.append(entry); err == nil {
		t.Fatal("closed audit appended")
	}
	if a.key != [32]byte{} {
		t.Fatal("closed audit retained key field")
	}
}
