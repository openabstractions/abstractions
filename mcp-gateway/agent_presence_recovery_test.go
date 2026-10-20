package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestAgentPresenceTransientRecoveryIsSuspendedAndBounded(t *testing.T) {
	for _, mode := range []string{"recover-announce", "recover-inbox", "recover-inbox-unknown", "revoked", "expiry", "late-positive", "parent-cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := time.Now()
			now := base
			a := new(agentToolAdmission)
			ticks := make(chan time.Time, 1)
			never := make(chan time.Time)
			retry := make(chan time.Time, 1)
			retry <- base
			confirmed, opens, retries := 0, 0, 0
			f := &agentPresenceFixture{announce: func(n int) (wire.ApplicationChange, error) {
				if n == 2 {
					now = base.Add(10 * time.Second)
					if mode == "expiry" {
						now = base.Add(30 * time.Second)
					}
					if mode == "parent-cancel" {
						cancel()
					}
					if mode != "recover-inbox" && mode != "recover-inbox-unknown" {
						return wire.ApplicationChange{}, context.DeadlineExceeded
					}
				}
				if n == 3 {
					if _, _, ok := a.begin(ctx); ok {
						t.Fatal("retry admitted tool effects")
					}
					if mode == "late-positive" {
						now = base.Add(30 * time.Second)
					}
				}
				return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "original"}, nil
			}}
			policy := agentPresencePolicy{admission: a, now: func() time.Time { return now }, after: func(wait time.Duration) <-chan time.Time {
				if wait == agentPresenceRetryDelay {
					retries++
					return retry
				}
				confirmed++
				if confirmed == 2 {
					_, done, ok := a.begin(ctx)
					if !ok {
						t.Fatal("positive original binding did not resume")
					}
					done()
					cancel()
				}
				return never
			}}
			err := maintainAgentPresenceWithPolicy(ctx, f, func(context.Context, string, string) error {
				opens++
				if mode == "recover-inbox" && opens == 2 {
					return &agentInboxOutcomeError{outcome: messages.OutcomeUnavailable}
				}
				if mode == "recover-inbox-unknown" && opens == 2 {
					return &agentInboxOutcomeError{outcome: messages.OutcomeUnknown}
				}
				if mode == "revoked" && opens == 2 {
					return &agentInboxOutcomeError{outcome: messages.OutcomeForbidden}
				}
				return nil
			}, "fixture-agent", "same-context", func(string) { ticks <- base }, ticks, policy)
			if err == nil || len(f.withdrawals) != 1 {
				t.Fatal(err, f.withdrawals)
			}
			for _, p := range f.announcements[1:] {
				if p.Instance != "original" || p.Contexts[0].Name != "same-context" {
					t.Fatal("replaced original association", p)
				}
			}
			switch mode {
			case "recover-announce", "recover-inbox", "recover-inbox-unknown":
				if !errors.Is(err, context.Canceled) || confirmed != 2 || retries != 1 {
					t.Fatal(err, confirmed, retries)
				}
			case "revoked":
				if !strings.Contains(err.Error(), "forbidden") || confirmed != 1 || retries != 1 {
					t.Fatal(err, confirmed, retries)
				}
			case "expiry", "late-positive":
				if !strings.Contains(err.Error(), "lease expired") || confirmed != 1 {
					t.Fatal(err, confirmed)
				}
			case "parent-cancel":
				if !errors.Is(err, context.Canceled) || retries != 0 {
					t.Fatal(err, retries)
				}
			}
		})
	}
}

func TestAgentPresenceRecoveryRPCDeadlineKeepsOriginalLeaseBound(t *testing.T) {
	now := time.Now()
	deadline := now.Add(30 * time.Millisecond)
	ctx, cancel := agentPresenceCall(context.Background(), now, deadline, true)
	defer cancel()
	got, _ := ctx.Deadline()
	if !got.Equal(deadline) {
		t.Fatal(got, deadline)
	}
	ctx2, cancel2 := agentPresenceCall(context.Background(), now, now.Add(time.Minute), true)
	defer cancel2()
	got, _ = ctx2.Deadline()
	if !got.Equal(now.Add(2 * time.Second)) {
		t.Fatal(got)
	}
}

func TestAgentPresenceInitialUnknownNeverAdmitsOrRetries(t *testing.T) {
	a := new(agentToolAdmission)
	ready, timers := 0, 0
	f := &agentPresenceFixture{announce: func(int) (wire.ApplicationChange, error) {
		return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "original"}, nil
	}}
	err := maintainAgentPresenceWithPolicy(context.Background(), f, func(context.Context, string, string) error {
		return &agentInboxOutcomeError{outcome: messages.OutcomeUnknown}
	}, "fixture-agent", "original-context", func(string) { ready++ }, nil, agentPresencePolicy{admission: a, after: func(time.Duration) <-chan time.Time { timers++; return nil }})
	if err == nil || !strings.Contains(err.Error(), "initial inbox open") || ready != 0 || timers != 0 || len(f.withdrawals) != 1 {
		t.Fatal(err, ready, timers, f.withdrawals)
	}
	if _, _, ok := a.begin(context.Background()); ok {
		t.Fatal("initial unknown admitted effects")
	}
}
