package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type loginFixture struct {
	mu      sync.Mutex
	enabled bool
	changed chan struct{}
}

func (f *loginFixture) read(ctx context.Context) (applicationLoginState, error) {
	if err := ctx.Err(); err != nil {
		return applicationLoginState{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return applicationLoginState{enabled: f.enabled, changed: f.changed}, nil
}

func (f *loginFixture) set(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = enabled
	close(f.changed)
	f.changed = make(chan struct{})
}

func receiveLogin(t *testing.T, calls <-chan bool, want bool) {
	t.Helper()
	select {
	case value := <-calls:
		if value != want {
			t.Fatalf("platform login = %v, want %v", value, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("login reconciliation did not finish")
	}
}

func TestRuntimeLogonInitialStateAndSerializedLatestState(t *testing.T) {
	f := &loginFixture{changed: make(chan struct{})}
	entered := make(chan bool, 4)
	release := make(chan struct{})
	var mu sync.Mutex
	active, maximum := 0, 0
	apply := func(ctx context.Context, enabled bool) error {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		entered <- enabled
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	stop := watchRuntimeLogon(context.Background(), f.read, apply, func(err error) { t.Errorf("reconcile: %v", err) })
	defer stop()
	receiveLogin(t, entered, false) // Initial empty state is reconciled too.
	f.set(true)
	f.set(false)
	f.set(true) // All changes happened during the blocked initial operation.
	close(release)
	receiveLogin(t, entered, true)
	f.set(false)
	receiveLogin(t, entered, false)
	stop()
	mu.Lock()
	defer mu.Unlock()
	if maximum != 1 || active != 0 {
		t.Fatalf("platform concurrency max=%d active=%d", maximum, active)
	}
	select {
	case extra := <-entered:
		t.Fatalf("unexpected stale call %v", extra)
	default:
	}
}

func TestRuntimeLogonFailureIsReportedAndNotApplied(t *testing.T) {
	f := &loginFixture{changed: make(chan struct{})}
	calls := make(chan bool, 4)
	reports := make(chan error, 4)
	failure := errors.New("platform refused")
	var mu sync.Mutex
	first := true
	stop := watchRuntimeLogon(context.Background(), f.read, func(_ context.Context, enabled bool) error {
		calls <- enabled
		mu.Lock()
		defer mu.Unlock()
		if first {
			first = false
			return failure
		}
		return nil
	}, func(err error) { reports <- err })
	defer stop()
	receiveLogin(t, calls, false)
	select {
	case err := <-reports:
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failure not reported")
	}
	f.set(false) // A new notification retries the failed desired state.
	receiveLogin(t, calls, false)
	f.set(true)
	receiveLogin(t, calls, true)
}

func TestRuntimeLogonPartialFailureRestoresPreviousDesiredState(t *testing.T) {
	f := &loginFixture{changed: make(chan struct{})}
	calls := make(chan bool, 4)
	reports := make(chan error, 1)
	failure := errors.New("presenter entry failed after runtime entry changed")
	var mu sync.Mutex
	platformEnabled := false
	stop := watchRuntimeLogon(context.Background(), f.read, func(_ context.Context, enabled bool) error {
		mu.Lock()
		platformEnabled = enabled // The first platform entry changed durably.
		mu.Unlock()
		calls <- enabled
		if enabled {
			return failure // Updating the second entry failed.
		}
		return nil
	}, func(err error) { reports <- err })
	defer stop()
	receiveLogin(t, calls, false)
	f.set(true)
	receiveLogin(t, calls, true)
	select {
	case err := <-reports:
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("partial platform failure was not reported")
	}
	f.set(false)
	receiveLogin(t, calls, false) // Must run even though false succeeded earlier.
	stop()
	mu.Lock()
	defer mu.Unlock()
	if platformEnabled {
		t.Fatal("last removal retained partial login state")
	}
}

func TestRuntimeLogonCancellationJoinsInFlightOperation(t *testing.T) {
	f := &loginFixture{changed: make(chan struct{})}
	entered := make(chan bool, 1)
	left := make(chan struct{})
	stop := watchRuntimeLogon(context.Background(), f.read, func(ctx context.Context, enabled bool) error {
		entered <- enabled
		<-ctx.Done()
		close(left)
		return ctx.Err()
	}, func(err error) { t.Errorf("shutdown reported a platform failure: %v", err) })
	receiveLogin(t, entered, false)
	stop()
	select {
	case <-left:
	default:
		t.Fatal("stop returned before operation exited")
	}
}

func TestRuntimeLogonSnapshotFailureNeverCallsPlatform(t *testing.T) {
	failure := errors.New("registry snapshot unavailable")
	report := make(chan error, 1)
	stop := watchRuntimeLogon(context.Background(), func(context.Context) (applicationLoginState, error) { return applicationLoginState{}, failure }, func(context.Context, bool) error { t.Error("platform called without facts"); return nil }, func(err error) { report <- err })
	defer stop()
	select {
	case err := <-report:
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot failure not reported")
	}
}

func TestRuntimeLogonUnqualifiedBackendRefuses(t *testing.T) {
	if err := runtimeLogonUnavailable("unqualified test platform"); !errors.Is(err, errRuntimeLogonPrerequisite) {
		t.Fatalf("missing prerequisite classification: %v", err)
	}
}
