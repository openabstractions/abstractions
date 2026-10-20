//go:build windows

package main

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestNativeSendChatTwentyNoteTiming(t *testing.T) {
	f := messagesObserveFixture(t)
	const perDirection = 10
	const count = 2 * perDirection
	type lane struct {
		sender           messagesReceiver
		receiver         *messagesReceiver
		instance, source string
		route, target    string
		targetInstance   string
		to               string
	}
	lanes := [2]lane{
		{sender: *f.reader, receiver: f.other, source: "source-a", target: "target-b", to: "other-agent"},
		{sender: *f.other, receiver: f.reader, source: "source-b", target: "target-a", to: "native-agent"},
	}
	lanes[0].instance, lanes[0].route = messagesTypedChatAt(t, f, &lanes[0].sender, "native-agent", "timing-chat-a", "", lanes[0].source)
	lanes[1].instance, lanes[1].route = messagesTypedChatAt(t, f, &lanes[1].sender, "other-agent", "timing-chat-b", "", lanes[1].source)
	ctx, cancel := context.WithTimeout(f.run.ctx, 30*time.Second)
	defer cancel()
	request := func(l lane, key string) msg.ChatSendRequest {
		return msg.ChatSendRequest{ReturnContext: l.route, Request: msg.SendRequest{
			To: "app:" + l.to, Context: l.target, Kind: msg.KindNote,
			Body: key, IdempotencyKey: key, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(),
		}}
	}
	for i := range lanes {
		l := &lanes[i]
		targetInstance, client := messagesStateOpen(t, f, l.receiver, l.to, l.target)
		l.targetInstance = targetInstance
		l.sender.ctx = ctx
		sent, err := l.sender.SendChat(l.instance, l.source, request(*l, fmt.Sprintf("timing-warm-%d", i)))
		if err != nil || sent.Outcome != msg.OutcomeSent {
			t.Fatal("warm send", sent, err)
		}
		page, err := client.Wait(l.targetInstance, l.target, msg.ViewBody, "", "", 0)
		if err != nil || page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Messages[0].Header.MessageID != sent.MessageID || page.Cursor == "" {
			t.Fatal("warm delivery", page, err)
		}
		if ack, err := client.Wait(l.targetInstance, l.target, msg.ViewBody, page.Cursor, "", 0); err != nil || ack.Outcome != msg.OutcomePage || len(ack.Messages) != 0 {
			t.Fatal("warm acknowledgement", ack, err)
		}
	}
	type result struct {
		index   int
		elapsed time.Duration
		send    msg.SendResult
		err     error
	}
	results := make(chan result, count)
	gate := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(count)
	var start time.Time
	durable := make(chan time.Duration, count)
	f.store.boundary = func(point string) error {
		if point == "index_published" {
			durable <- time.Since(start)
		}
		return nil
	}
	for i := range count {
		go func() {
			l := lanes[i%len(lanes)]
			req := request(l, fmt.Sprintf("timed-note-%02d", i))
			ready.Done()
			<-gate
			out, err := l.sender.SendChat(l.instance, l.source, req)
			results <- result{i, time.Since(start), out, err}
		}()
	}
	ready.Wait()
	start = time.Now()
	close(gate)
	ids := make(map[string]int, count)
	full := make([]time.Duration, 0, count)
	for range count {
		select {
		case item := <-results:
			if item.err != nil || item.send.Outcome != msg.OutcomeSent || item.send.MessageID == "" {
				t.Fatalf("send %d: %+v, err %v", item.index, item.send, item.err)
			}
			if _, exists := ids[item.send.MessageID]; exists {
				t.Fatal("duplicate receipt", item.send.MessageID)
			}
			ids[item.send.MessageID] = item.index
			full = append(full, item.elapsed)
		case <-ctx.Done():
			t.Fatal("bounded SendChat workload did not finish", ctx.Err())
		}
	}
	if len(durable) != count {
		t.Fatalf("got %d durable publications, want %d", len(durable), count)
	}
	published := make([]time.Duration, 0, count)
	for range count {
		published = append(published, <-durable)
	}
	for i, l := range lanes {
		page, err := l.receiver.Wait(l.targetInstance, l.target, msg.ViewBody, "", "", 0)
		if err != nil || page.Outcome != msg.OutcomePage || page.Cursor == "" || len(page.Messages) != perDirection {
			t.Fatalf("target %d: outcome %v, messages %d, error %v", i, page.Outcome, len(page.Messages), err)
		}
		for _, note := range page.Messages {
			index, ok := ids[note.Header.MessageID]
			if !ok || index%len(lanes) != i || note.Header.FromContext != l.route || note.Body != fmt.Sprintf("timed-note-%02d", index) {
				t.Fatal("unexpected native delivery", i, note.Header.MessageID)
			}
			delete(ids, note.Header.MessageID)
		}
		ack, err := l.receiver.Wait(l.targetInstance, l.target, msg.ViewBody, page.Cursor, "", 0)
		if err != nil || ack.Outcome != msg.OutcomePage || len(ack.Messages) != 0 {
			t.Fatal("timed acknowledgement", i, ack, err)
		}
	}
	if len(ids) != 0 {
		t.Fatal("some receipts had no delivery", ids)
	}
	slices.Sort(full)
	slices.Sort(published)
	t.Logf("warm typed SendChat, 20 concurrent notes in two directional program pairs (10 contend per pair): full p95=%s, durable publication p95=%s from batch release", full[18], published[18])
}
