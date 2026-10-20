package main

import (
	"context"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Ends are kept per receiver context. A lease nobody renewed reads lapsed and
// is reported about the receiver's own program; a withdrawal reads withdrawn.
// The most recent end among the receiver's contexts answers, or the named one.
func TestPresenceEndPerContextAndLapseRecord(t *testing.T) {
	clock := time.UnixMilli(1793400000000)
	d := &applicationDirectory{now: func() time.Time { return clock }, instances: map[string]applicationLease{}}
	var mu sync.Mutex
	var lapsed []string
	done := make(chan struct{}, 4)
	d.setPresenceLapsed(func(program, application, contextName string, _ time.Time) {
		mu.Lock()
		lapsed = append(lapsed, program+"|"+application+"|"+contextName)
		mu.Unlock()
		done <- struct{}{}
	})
	ctx := context.Background()
	if got := d.receiverAbsence(ctx, "codex", ""); got != "receiver_unavailable" {
		t.Fatal(got)
	}
	owner := rights.Subject{Account: "a", Program: `C:\oa\oa-codex-integration.exe`}
	d.instances["chat-one"] = applicationLease{application: "codex", owner: owner, deadline: clock.Add(time.Second), value: wire.ApplicationInstance{Contexts: []wire.ApplicationContext{{Name: "native-one"}}}}
	d.instances["chat-two"] = applicationLease{application: "codex", owner: owner, deadline: clock.Add(time.Hour), value: wire.ApplicationInstance{Contexts: []wire.ApplicationContext{{Name: "native-two"}}}}
	clock = clock.Add(2 * time.Second)
	d.mu.Lock()
	d.expireLocked()
	d.mu.Unlock()
	<-done
	if got := d.receiverAbsence(ctx, "codex", ""); got != "receiver_lapsed" {
		t.Fatal(got)
	}
	mu.Lock()
	if len(lapsed) != 1 || lapsed[0] != `C:\oa\oa-codex-integration.exe|codex|native-one` {
		t.Fatal(lapsed)
	}
	mu.Unlock()
	clock = clock.Add(time.Second)
	d.mu.Lock()
	d.recordPresenceEndLocked(d.instances["chat-two"], applicationPresenceWithdrawn)
	delete(d.instances, "chat-two")
	d.mu.Unlock()
	if got := d.receiverAbsence(ctx, "codex", ""); got != "receiver_withdrawn" {
		t.Fatal("most recent end", got)
	}
	if got := d.receiverAbsence(ctx, "codex", "native-one"); got != "receiver_lapsed" {
		t.Fatal("named context", got)
	}
	if got := d.receiverAbsence(ctx, "claude", ""); got != "receiver_unavailable" {
		t.Fatal("other receiver", got)
	}
	for i := 0; i < applicationPresenceEndLimit+10; i++ {
		clock = clock.Add(time.Millisecond)
		d.mu.Lock()
		d.recordPresenceEndLocked(applicationLease{application: "many", value: wire.ApplicationInstance{Contexts: []wire.ApplicationContext{{Name: "c" + time.Duration(i).String()}}}}, applicationPresenceWithdrawn)
		d.mu.Unlock()
	}
	if len(d.presenceEnds) > applicationPresenceEndLimit {
		t.Fatal("unbounded presence ends", len(d.presenceEnds))
	}
}
