//go:build windows && !panel

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	leasewire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/presenter"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/probe"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

type leasedFixtureSurface struct {
	fakeSurface
	id string
}

func (s *leasedFixtureSurface) AskLeased(_ context.Context, occurrence bookOccurrence, start func() bool, shown func()) (string, error) {
	if !start() {
		return "", errPromptNotShown
	}
	s.id = occurrence.ID()
	shown()
	return "", nil
}

func TestPresenterPollShowsOnlyLiveWaitClaim(t *testing.T) {
	book, operator := trayTestBook(t, func(peer *identity.Peer) (string, bool) {
		process, err := peer.Process.AtLeast(identity.ProofKernel)
		return "fixture-desktop", err == nil && process.PID == os.Getpid()
	})
	raise := func(key string) string {
		t.Helper()
		record, outcome, err := book.AskApplicationAs("scope", filepath.Join(t.TempDir(), "opencode.exe"), key, asks.ApplicationCommandOnceKey,
			map[string]string{"program": "OpenCode", "operation": "Open ComfyUI", "operationmore": "", "command": "open",
				"code": key, "until": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}, listen.Seen{}, asks.Publisher{})
		if err != nil || outcome != "pending" {
			t.Fatal(outcome, err)
		}
		return record.ID
	}
	older := raise("12345678901234567890")
	waiting := raise("12345678901234567891")
	_, stop := book.ObserveQuestionWait(waiting)
	defer stop()
	surface := &leasedFixtureSurface{}
	presenter := &questionPresenter{session: unlockedSession(), surface: surface, operator: func(context.Context) (presenterOperator, error) { return operator, nil }}
	for _, id := range []string{waiting, older} {
		result, legacy := pollLeasedQuestion(t.Context(), presenter)
		if legacy || result.Outcome != presentClosed || surface.id != id {
			t.Fatalf("wanted %s, showed %s, result %+v, legacy %v", id, surface.id, result, legacy)
		}
	}
	result, legacy := pollLeasedQuestion(t.Context(), presenter)
	if legacy || result.Outcome != presentDeferred || result.Err != nil {
		t.Fatalf("the seen pending occurrences repeated: %+v, legacy %v", result, legacy)
	}
	for _, id := range []string{waiting, older} {
		record, found, err := book.ApplicationRecord(id)
		if err != nil || !found || !record.Pending() || !asks.AutomaticNativeQuestion(record.Key) {
			t.Fatalf("dismissal changed the pending occurrence: %s, found %v, error %v", id, found, err)
		}
	}
}

type interruptedLeaseSurface struct {
	fakeSurface
	shown chan struct{}
}

func (s *interruptedLeaseSurface) AskLeased(ctx context.Context, occurrence bookOccurrence, start func() bool, shown func()) (string, error) {
	if !start() {
		return "", errPromptNotShown
	}
	shown()
	close(s.shown)
	<-ctx.Done()
	return "allow", nil
}

func TestPresenterLeaseClosesOnSessionRevocation(t *testing.T) {
	var revoked atomic.Bool
	book, operator := trayTestBook(t, func(peer *identity.Peer) (string, bool) {
		process, err := peer.Process.AtLeast(identity.ProofKernel)
		return "fixture-desktop", !revoked.Load() && err == nil && process.PID == os.Getpid()
	})
	id := ask(t, book, "revoked", "opencode.exe", "abstraction.model/lookup", "hf")
	record, _, err := book.ApplicationRecord(id)
	if err != nil {
		t.Fatal(err)
	}
	surface := &interruptedLeaseSurface{fakeSurface: fakeSurface{dismissed: make(chan struct{})}, shown: make(chan struct{})}
	p := &questionPresenter{session: unlockedSession(), surface: surface}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	lease, legacy, err := claimPopup(ctx, operator, "")
	if err != nil || legacy || lease == nil || lease.claim.ID != id {
		t.Fatalf("claim %v, legacy %v, error %v", lease != nil, legacy, err)
	}
	done := make(chan error, 1)
	go func() {
		result := lease.run(ctx, func(ctx context.Context) presentResult {
			option, err := p.askPopup(ctx, bookOccurrence{id: record.ID})
			if option != "" {
				t.Errorf("interrupted surface returned %q", option)
			}
			return presentResult{Outcome: presentDeferred, Err: err}
		})
		done <- result.Err
	}()
	select {
	case <-surface.shown:
		revoked.Store(true)
	case <-ctx.Done():
		t.Fatal("surface was not displayed")
	}
	select {
	case err := <-done:
		if err != errPromptNotShown || ctx.Err() != nil {
			t.Fatalf("dismissal %v, context %v", err, ctx.Err())
		}
	case <-ctx.Done():
		t.Fatal("lease renewal refusal did not close the surface")
	}
}

func TestPresenterProviderCompletionRenewsOnlyItsOwnAllow(t *testing.T) {
	book, op := trayTestBook(t, func(peer *identity.Peer) (string, bool) {
		proc, err := peer.Process.AtLeast(identity.ProofKernel)
		return "fixture", err == nil && proc.PID == os.Getpid()
	})
	permission, err := asks.NewPermissionRequest(asks.PermissionTarget{Account: probe.Self().Account,
		Program: filepath.Join(t.TempDir(), "opencode.exe"), Action: "abstraction.credentials/apply", Resource: "credential:deepseek"})
	if err != nil {
		t.Fatal(err)
	}
	companion := permission.Targets[0]
	companion.Action, companion.Resource = "abstraction.inference/complete", "host:deepseek"
	permission.Targets = append(permission.Targets, companion)
	for _, viaLease := range []bool{true, false} {
		record, _, err := book.AskPermissionFrom("runtime", "runtime", fmt.Sprint("provider-", viaLease), permission, listen.Seen{}, asks.Publisher{})
		if err != nil {
			t.Fatal(err)
		}
		ctx := t.Context()
		lease, legacy, err := claimPopup(ctx, op, "")
		if err != nil || legacy || lease == nil || lease.claim.ID != record.ID {
			t.Fatal(lease, legacy, err)
		}
		wire := op.PresenterContext(ctx)
		if viaLease {
			result, err := wire.BeginProviderCompletion(lease.claim.Token)
			if err != nil || result.Outcome != leasewire.LeaseOutcomeCompletion {
				t.Fatal(result, err)
			}
		} else {
			result, err := op.AnswerQuestionIfPendingContext(ctx, record.ID, "allow")
			if err != nil || result.Outcome != askclient.OperatorDecisionOutcomeAnswered {
				t.Fatal(result, err)
			}
			result2, err := wire.BeginProviderCompletion(lease.claim.Token)
			if err != nil || result2.Outcome != leasewire.LeaseOutcomeStale {
				t.Fatal(result2, err)
			}
		}
		stored, found, err := book.ApplicationRecord(record.ID)
		if err != nil || !found || stored.Option != "allow" {
			t.Fatal(stored.Option, found, err)
		}
		renewed, err := wire.Renew(lease.claim.Token)
		want := leasewire.LeaseOutcomeStale
		if viaLease {
			want = leasewire.LeaseOutcomeHeld
		}
		if err != nil || renewed.Outcome != want {
			t.Fatal(renewed, err)
		}
		if _, err := wire.Release(lease.claim.Token, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPresenterHandedSetupWaitsAndRecoversRetainedAnswer(t *testing.T) {
	book, op := trayTestBook(t, func(peer *identity.Peer) (string, bool) {
		proc, err := peer.Process.AtLeast(identity.ProofKernel)
		return "fixture", err == nil && proc.PID == os.Getpid()
	})
	_, fixture, fake := setupOccurrenceFixture(t)
	first := ask(t, book, "busy", "opencode.exe", "abstraction.model/lookup", "hf")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	lease, _, err := claimPopup(ctx, op, "")
	if err != nil || lease == nil || lease.claim.ID != first {
		t.Fatal(lease, err)
	}
	q, outcome, err := book.AskApplication("runtime", "runtime", "setup", asks.SetupModelsKey, fake.contents[fixture.ID].Content.Slots, listen.Seen{})
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	read, err := op.ReadAttributedQuestionContext(ctx, q.ID)
	if err != nil || read.Question.Record == nil {
		t.Fatal(read, err)
	}
	done := make(chan presentResult, 1)
	go func() {
		done <- awaitHandedPopup(ctx, op, *read.Question.Record, func(context.Context) presentResult {
			return presentResult{Outcome: presentFailed}
		})
	}()
	select {
	case result := <-done:
		t.Fatalf("handoff returned while the lease was held: %+v", result)
	case <-time.After(250 * time.Millisecond):
	}
	answered, err := op.AnswerQuestionIfPendingContext(ctx, q.ID, "setup")
	if err != nil || answered.Outcome != askclient.OperatorDecisionOutcomeAnswered {
		t.Fatal(answered, err)
	}
	if _, err := op.PresenterContext(ctx).Release(lease.claim.Token, false); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Outcome != presentClosed || result.Err != nil {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestPresenterRetriesHoldNoticeWithSameRequest(t *testing.T) {
	book, op := trayTestBook(t, func(peer *identity.Peer) (string, bool) {
		proc, err := peer.Process.AtLeast(identity.ProofKernel)
		return "fixture", err == nil && proc.PID == os.Getpid()
	})
	first := ask(t, book, "shown", "flood.exe", "abstraction.model/lookup", "first")
	ask(t, book, "held", "flood.exe", "abstraction.model/lookup", "second")
	ctx := t.Context()
	lease, _, err := claimPopup(ctx, op, "")
	if err != nil || lease == nil || lease.claim.ID != first {
		t.Fatal(lease, err)
	}
	wire := op.PresenterContext(ctx)
	if v, err := wire.Start(lease.claim.Token); err != nil || v.Outcome != leasewire.LeaseOutcomeHeld {
		t.Fatal(v, err)
	}
	if _, err := wire.Release(lease.claim.Token, true); err != nil {
		t.Fatal(err)
	}
	if held, _, err := claimPopup(ctx, op, ""); err != nil || held != nil {
		t.Fatal(held, err)
	}
	original := postPresenterNotice
	t.Cleanup(func() { postPresenterNotice = original })
	var requests []askclient.NotificationRequest
	postPresenterNotice = func(_ context.Context, n presenterNotice) error {
		requests = append(requests, presenterNoticeRequest(n, time.Now()))
		if len(requests) == 1 {
			return fmt.Errorf("transient notification failure")
		}
		return nil
	}
	for range 3 {
		deliverPopupHold(ctx, op)
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatal(requests)
	}
}

func TestPresenterRefusedStartNeverDisplays(t *testing.T) {
	var revoked atomic.Bool
	book, op := trayTestBook(t, func(peer *identity.Peer) (string, bool) {
		proc, err := peer.Process.AtLeast(identity.ProofKernel)
		return "fixture", !revoked.Load() && err == nil && proc.PID == os.Getpid()
	})
	id := ask(t, book, "start-denied", "opencode.exe", "abstraction.model/lookup", "hf")
	ctx := t.Context()
	lease, _, err := claimPopup(ctx, op, "")
	if err != nil || lease == nil || lease.claim.ID != id {
		t.Fatal(lease, err)
	}
	surface := &leasedFixtureSurface{}
	p := &questionPresenter{surface: surface}
	revoked.Store(true)
	result := lease.run(ctx, func(ctx context.Context) presentResult {
		_, err := p.askPopup(ctx, bookOccurrence{id: id})
		return presentResult{Outcome: presentDeferred, Err: err}
	})
	if result.Err != errPromptNotShown || surface.id != "" {
		t.Fatal(result, surface.id)
	}
}
