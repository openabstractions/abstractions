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

func TestMessagesSendContentionAcrossInboxes(t *testing.T) {
	r := newRoutingRun(t)
	inboxes := make([]string, 2)
	instances := make([]string, 2)
	for i := range inboxes {
		instances[i], inboxes[i] = r.open(fmt.Sprintf("contention-%d", i), "Contention fixture")
	}
	request := func(i int) msg.SendRequest {
		return msg.SendRequest{To: "app:" + routingClaude, Context: inboxes[i%2], Kind: msg.KindNote, Body: fmt.Sprintf("note %d", i), IdempotencyKey: fmt.Sprintf("contention-%d", i), ExpiresUnixMs: r.f.now.Add(time.Hour).UnixMilli()}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var storage []time.Duration
	var reserved time.Time
	r.f.store.boundary = func(point string) error {
		switch point {
		case "id_reserved":
			reserved = time.Now()
		case "before_record":
			select {
			case <-entered:
			default:
				close(entered)
				<-release
			}
		case "index_published":
			storage = append(storage, time.Since(reserved))
		}
		return nil
	}
	type sample struct {
		index   int
		elapsed time.Duration
		out     msg.SendResult
		err     error
	}
	results := make(chan sample, 20)
	send := func(i int) {
		start := time.Now()
		out, err := r.f.reader.Send(r.si, "source", request(i))
		results <- sample{i, time.Since(start), out, err}
	}
	go send(0)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("send never reached the canonical publication boundary")
	}
	d := r.f.run.d
	if d.mu.TryLock() {
		d.mu.Unlock()
		t.Fatal("canonical publication did not hold the directory fence")
	}
	if r.f.store.mu.TryLock() {
		r.f.store.mu.Unlock()
		t.Fatal("canonical publication did not hold the store fence")
	}
	// An independent native owner also requires this directory fence. Bound
	// the probe without relying on an elapsed time for the assertion.
	probe, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	owned, err := d.withNativeInboxOwner(probe, r.f.other.call, instances[1], inboxes[1], nil)
	cancel()
	if owned || err == nil {
		t.Fatal("independent inbox obtained directory custody during publication", owned, err)
	}
	for i := 1; i < 20; i++ {
		go send(i)
	}
	unblock()
	seen := make(map[string]int)
	finish := func(s sample) {
		if s.err != nil || s.out.Outcome != msg.OutcomeSent || s.out.MessageID == "" {
			t.Fatalf("send %d: %+v, %v", s.index, s.out, s.err)
		}
		if _, ok := seen[s.out.MessageID]; ok {
			t.Fatalf("send %d repeated receipt %s", s.index, s.out.MessageID)
		}
		seen[s.out.MessageID] = s.index
	}
	var elapsed []time.Duration
	for range 20 {
		s := <-results
		finish(s)
		if s.index != 0 {
			elapsed = append(elapsed, s.elapsed)
		}
	}
	var delivered = make(map[string]bool)
	for i, inbox := range inboxes {
		page, err := r.f.other.Wait(instances[i], inbox, msg.ViewBody, "", "", 0)
		if err != nil || page.Outcome != msg.OutcomePage || len(page.Messages) != 10 {
			t.Fatalf("inbox %d: outcome %v, %d messages, err %v", i, page.Outcome, len(page.Messages), err)
		}
		for _, body := range page.Messages {
			id := body.Header.MessageID
			index, ok := seen[id]
			if !ok || index%2 != i || delivered[id] {
				t.Fatalf("unexpected message %s in inbox %d", id, i)
			}
			delivered[id] = true
		}
	}
	if len(storage) != 20 {
		t.Fatalf("expected 20 durable acceptances, got %d", len(storage))
	}
	storage = storage[1:] // Exclude the injected stall.
	slices.Sort(storage)
	slices.Sort(elapsed)
	t.Logf("send p95=%s, reservation-to-index p95=%s, both exclude the stalled fixture send", elapsed[18], storage[18])
}
