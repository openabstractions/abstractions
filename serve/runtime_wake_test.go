package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	job "github.com/openabstractions/abstraction-job/go"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// standInWake counts the platform wake requests the lease book holds.
type standInWake struct {
	mu       sync.Mutex
	taken    int
	released int
	why      string
}

type standInHeld struct {
	w    *standInWake
	done chan struct{}
}

func (h standInHeld) Done() <-chan struct{} { return h.done }
func (h standInHeld) Release() error {
	h.w.mu.Lock()
	h.w.released++
	h.w.mu.Unlock()
	return nil
}

func (w *standInWake) Hold(who, why string) (job.Held, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.taken++
	w.why = who + ": " + why
	return standInHeld{w: w, done: make(chan struct{})}, nil
}

func (w *standInWake) counts() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.taken, w.released
}

// The runtime's lease book keeps one platform wake request while any
// acquired wake lease lives, and releases it with the last (resource
// CONTRACT.md RES-A1, RES-G5). Hold is left to the fixture, which permits:
// the rights decision is RES-L1's, tested on its own.
func TestTheRuntimeLeaseBookHoldsOnePlatformWakeRequest(t *testing.T) {
	state := t.TempDir()
	composed := composeResources(state, router.New(), nil, func(error) {})
	t.Cleanup(composed.close)
	if composed.book == nil {
		t.Fatal("no lease book")
	}
	wake := &standInWake{}
	composed.path = filepath.Join(t.TempDir(), "resources")
	options := leaseBookOptions(composed, router.New(), nil, wake)
	if options.KeepAwake == nil {
		t.Fatal("the runtime's lease book takes no platform wake request")
	}
	options.Hold = nil
	book, err := resourceservice.OpenBook(options)
	if err != nil {
		t.Fatal(err)
	}
	gpu := resourceservice.Subject{Program: `C:\OA\modelhostd.exe`, Account: "S-1-5-21-test"}
	// The request keeps the display on, so it is held only while the wake
	// lease's holder holds a card lease (RES-A1, RES-G5).
	if card := book.Acquire(context.Background(), gpu, &identity.Peer{}, "card:0", 1<<20, 0); card.Lease == nil {
		t.Fatalf("card lease read %s", card.Outcome)
	}
	first := book.Acquire(context.Background(), gpu, &identity.Peer{}, resourceservice.ResourceAwake, 0, 0)
	second := book.Acquire(context.Background(), gpu, &identity.Peer{}, resourceservice.ResourceAwake, 0, 0)
	if first.Lease == nil || second.Lease == nil {
		t.Fatalf("wake leases read %s and %s", first.Outcome, second.Outcome)
	}
	if taken, released := wake.counts(); taken != 1 || released != 0 {
		t.Fatalf("two wake leases: %d platform requests taken, %d released", taken, released)
	}
	book.Release(gpu, first.Lease.ID)
	if _, released := wake.counts(); released != 0 {
		t.Fatal("the platform request ended while a wake lease lived")
	}
	book.Release(gpu, second.Lease.ID)
	if taken, released := wake.counts(); taken != 1 || released != 1 {
		t.Fatalf("after the last release: %d taken, %d released", taken, released)
	}
}
