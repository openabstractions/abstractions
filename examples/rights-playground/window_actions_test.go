package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

func awaitWindowCompletion(t *testing.T, notified <-chan struct{}, actions *windowActions) windowCompletion {
	t.Helper()
	select {
	case <-notified:
	case <-time.After(3 * time.Second):
		t.Fatal("worker completion did not arrive")
	}
	result, ok := actions.take()
	if !ok {
		t.Fatal("completion was lost")
	}
	return result
}

func TestWindowOneClickOneCallUntilCompletionConsumed(t *testing.T) {
	notified := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	a := &windowActions{notify: func() { notified <- struct{}{} }, execute: func(context.Context, windowAction) error {
		calls.Add(1)
		<-release
		return nil
	}}
	defer a.close()
	if a.start(windowAction{"read", "gamma"}) || a.start(windowAction{"typo", "alpha"}) {
		t.Fatal("invalid selection reached the adapter")
	}
	if !a.start(windowAction{"read", "alpha"}) || a.start(windowAction{"use", "beta"}) {
		t.Fatal("concurrent click was admitted")
	}
	close(release)
	select {
	case <-notified:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not finish")
	}
	if a.start(windowAction{"read", "alpha"}) {
		t.Fatal("unconsumed result admitted another call")
	}
	result, ok := a.take()
	if !ok || result.Action.Label != "alpha" || result.Err != nil || calls.Load() != 1 {
		t.Fatal("single-click result changed")
	}
	if !a.start(windowAction{"use", "beta"}) {
		t.Fatal("explicit next click was refused")
	}
	awaitWindowCompletion(t, notified, a)
	if calls.Load() != 2 {
		t.Fatal("unexpected automatic retry")
	}
}

func TestWindowCloseCancelsAndSuppressesLateResult(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	var notifications atomic.Int32
	a := &windowActions{notify: func() { notifications.Add(1) }, execute: func(ctx context.Context, _ windowAction) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}}
	if !a.start(windowAction{"use", "alpha"}) {
		t.Fatal("first click refused")
	}
	<-started
	a.close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not cancel the adapter")
	}
	if a.start(windowAction{"use", "alpha"}) {
		t.Fatal("closed app admitted a retry")
	}
	if _, ok := a.take(); ok || notifications.Load() != 0 {
		t.Fatal("closed app received late status")
	}
}

func TestWindowExplicitUseRetryReconcilesOriginalIdentity(t *testing.T) {
	f, _ := fixtureFor("alpha")
	jobs := &uncertainJobs{completedJobs: completedJobs{body: f.artifact}}
	path := filepath.Join(t.TempDir(), "intent.json")
	notified := make(chan struct{}, 2)
	a := &windowActions{notify: func() { notified <- struct{}{} }, execute: func(ctx context.Context, _ windowAction) error {
		return submitUse(ctx, jobs, "http://127.0.0.1:1234", f, useGuarantees(false), path, io.Discard)
	}}
	defer a.close()
	a.start(windowAction{"use", "alpha"})
	first := awaitWindowCompletion(t, notified, a)
	if !errors.Is(first.Err, errAcceptanceUncertain) || len(jobs.submitted) != 1 || len(jobs.reconciled) != 1 {
		t.Fatal("uncertain operation was retried automatically")
	}
	a.start(windowAction{"use", "alpha"})
	second := awaitWindowCompletion(t, notified, a)
	if second.Err != nil || len(jobs.submitted) != 1 || len(jobs.reconciled) != 2 || jobs.reconciled[1] != jobs.submitted[0].Identity {
		t.Fatal("explicit retry duplicated the uncertain effect")
	}
}

func TestWindowStatusUsesTypedOutcomesAndHidesDiagnostics(t *testing.T) {
	for _, test := range []struct {
		err      error
		contains string
	}{
		{nil, "succeeded"},
		{&readRefusalError{Label: "alpha", Outcome: wire.SecretReadOutcomeForbidden}, "was refused"},
		{&useRefusalError{Outcome: api.AcceptanceOutcomeForbidden, Reason: "private-provider-detail"}, "was refused"},
		{&useFailureError{State: api.WorkStateFailed, Cause: api.FailureCauseCredential}, "credential was unavailable or refused"},
		{errors.New("forbidden pending private-provider-detail"), "could not complete"},
		{errAcceptanceUncertain, "reconcile the retained operation"},
	} {
		status := windowResult(windowCompletion{Action: windowAction{"use", "alpha"}, Err: test.err})
		if !strings.Contains(status, test.contains) || strings.Contains(status, "private-provider-detail") || strings.Contains(status, "pending") {
			t.Fatalf("incorrect or unsafe status: %q", status)
		}
	}
}

func TestMenuMistakesDoNotClaimPermissionPending(t *testing.T) {
	var out bytes.Buffer
	if err := menu(connection{}, strings.NewReader("\noops\nread gamma\nquit\n"), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "pending") || strings.Contains(out.String(), "choose in OA") {
		t.Fatal("invalid input acquired permission guidance")
	}
	if strings.Count(out.String(), "type use/read") != 2 || !strings.Contains(out.String(), "accepts only alpha or beta") {
		t.Fatal("input mistakes were not identified")
	}
}

func TestReadRefusalRetainsTypedOutcome(t *testing.T) {
	f, _ := fixtureFor("alpha")
	err := readWithClient(context.Background(), &readerReply{result: wire.SecretReadResult{Outcome: wire.SecretReadOutcomeForbidden}}, f, io.Discard)
	var refused *readRefusalError
	if !errors.As(err, &refused) || refused.Outcome != wire.SecretReadOutcomeForbidden {
		t.Fatal("reader lost its typed refusal")
	}
}
