package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeChild struct {
	pid  int
	exit chan error
}

func (c *fakeChild) PID() int     { return c.pid }
func (c *fakeChild) Wait() error  { return <-c.exit }
func (c *fakeChild) Close() error { return nil }

// fakeClock advances only when the plan sleeps, so backoff and healthy-run
// arithmetic is exact.
type fakeClock struct {
	mu    sync.Mutex
	at    time.Time
	slept []time.Duration
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}
func (c *fakeClock) sleep(ctx context.Context, d time.Duration) bool {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	c.at = c.at.Add(d)
	c.mu.Unlock()
	return ctx.Err() == nil
}

func TestHostRestartsAFailedChildWithBoundedBackoff(t *testing.T) {
	clock := &fakeClock{at: time.Unix(1_800_000_000, 0)}
	starts := 0
	plan := hostPlan{
		start: func(context.Context) (runtimeChild, error) {
			starts++
			child := &fakeChild{pid: 100 + starts, exit: make(chan error, 1)}
			child.exit <- fmt.Errorf("killed %d", starts)
			return child, nil
		},
		now: clock.now, sleep: clock.sleep,
	}
	err := plan.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "failed 4 times") || !strings.Contains(err.Error(), "killed 4") {
		t.Fatalf("exhausted backoff: %v", err)
	}
	if starts != 4 || !reflect.DeepEqual(clock.slept, []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second}) {
		t.Fatalf("starts=%d delays=%v", starts, clock.slept)
	}
}

func TestHostCountsStartupFailuresAndCleanExitsAsFailures(t *testing.T) {
	clock := &fakeClock{at: time.Unix(1_800_000_000, 0)}
	attempt := 0
	plan := hostPlan{
		start: func(context.Context) (runtimeChild, error) {
			attempt++
			if attempt%2 == 1 {
				return nil, errors.New("exited before readiness")
			}
			child := &fakeChild{pid: attempt, exit: make(chan error, 1)}
			child.exit <- nil
			return child, nil
		},
		delays: []time.Duration{time.Second}, now: clock.now, sleep: clock.sleep,
	}
	err := plan.run(context.Background())
	if !errors.Is(err, errRuntimeChildExited) || attempt != 2 {
		t.Fatalf("attempts=%d err=%v", attempt, err)
	}
}

func TestHostResetsBackoffAfterAHealthyRun(t *testing.T) {
	clock := &fakeClock{at: time.Unix(1_800_000_000, 0)}
	starts := 0
	plan := hostPlan{
		start: func(context.Context) (runtimeChild, error) {
			starts++
			child := &fakeChild{pid: starts, exit: make(chan error, 1)}
			if starts == 3 {
				clock.advance(hostHealthyRun)
			}
			child.exit <- errors.New("crash")
			return child, nil
		},
		now: clock.now, sleep: clock.sleep,
	}
	if err := plan.run(context.Background()); err == nil {
		t.Fatal("host ran for ever")
	}
	// Two quick failures, one after a healthy run that resets the count, then
	// the full budget again.
	want := []time.Duration{2 * time.Second, 10 * time.Second, 2 * time.Second, 10 * time.Second, 30 * time.Second}
	if starts != 6 || !reflect.DeepEqual(clock.slept, want) {
		t.Fatalf("starts=%d delays=%v", starts, clock.slept)
	}
}

func TestHostLeavesAServedEndpointAlone(t *testing.T) {
	started := false
	plan := hostPlan{
		start:  func(context.Context) (runtimeChild, error) { started = true; return nil, errors.New("unreachable") },
		served: func(context.Context) bool { return true },
	}
	if err := plan.run(context.Background()); err != nil || started {
		t.Fatalf("err=%v started=%v", err, started)
	}
}

// A second host started while the first one's child owns the endpoint fails
// its launch, then finds the endpoint served and exits cleanly.
func TestHostYieldsWhenAnotherRuntimeTakesTheEndpoint(t *testing.T) {
	clock := &fakeClock{at: time.Unix(1_800_000_000, 0)}
	probes := 0
	plan := hostPlan{
		start: func(context.Context) (runtimeChild, error) {
			return nil, errors.New("listen: pipe busy")
		},
		served: func(context.Context) bool { probes++; return probes > 1 },
		now:    clock.now, sleep: clock.sleep,
	}
	if err := plan.run(context.Background()); err != nil || probes != 2 {
		t.Fatalf("err=%v probes=%d", err, probes)
	}
}

func TestHostGuardRefusesBeforeLaunch(t *testing.T) {
	refused := &exitError{code: 3, err: errors.New("an upgrade of this installation is in progress")}
	plan := hostPlan{
		guard: func() error { return refused },
		start: func(context.Context) (runtimeChild, error) { t.Fatal("launched during an upgrade"); return nil, nil },
	}
	if err := plan.run(context.Background()); !errors.Is(err, refused) {
		t.Fatal(err)
	}
}

func TestHostStopEndsSupervisionCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	child := &fakeChild{pid: 7, exit: make(chan error, 1)}
	plan := hostPlan{
		start: func(ctx context.Context) (runtimeChild, error) {
			go func() { <-ctx.Done(); child.exit <- errors.New("forced") }()
			return child, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- plan.run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not end the host")
	}
}
