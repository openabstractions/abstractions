package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

func TestAgentPresenceReportsFailedPhaseAndPreservesCause(t *testing.T) {
	for _, phase := range []string{"initial announce", "initial inbox open", "renewal announce", "renewal inbox open"} {
		t.Run(phase, func(t *testing.T) {
			cause := context.DeadlineExceeded
			f := &agentPresenceFixture{announce: func(n int) (wire.ApplicationChange, error) {
				if phase == "initial announce" || phase == "renewal announce" && n == 2 {
					return wire.ApplicationChange{}, cause
				}
				return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "owned"}, nil
			}}
			ticks := make(chan time.Time, 1)
			ticks <- time.Time{}
			opened := 0
			err := maintainAgentPresence(context.Background(), f, func(context.Context, string, string) error {
				opened++
				if phase == "initial inbox open" || phase == "renewal inbox open" && opened == 2 {
					return cause
				}
				return nil
			}, "fixture-agent", "child", nil, ticks)
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), "agent presence "+phase+":") {
				t.Fatalf("phase/cause lost: %v", err)
			}
		})
	}
}

type agentPresenceFixture struct {
	announcements []wire.ApplicationPresence
	withdrawals   []string
	announce      func(int) (wire.ApplicationChange, error)
	withdrawErr   error
}

func TestAgentPresenceStopsOwnedServerBeforeExactWithdrawal(t *testing.T) {
	for _, mode := range []string{"cancel", "loss"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &agentPresenceFixture{announce: func(n int) (wire.ApplicationChange, error) {
				if n > 1 {
					return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
				}
				return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "original"}, nil
			}}
			ticks := make(chan time.Time, 1)
			if mode == "loss" {
				ticks <- time.Time{}
			}
			stops := 0
			err := maintainAgentPresence(ctx, f, func(context.Context, string, string) error { return nil }, "fixture-agent", "native-child", func(string) {
				if mode == "cancel" {
					cancel()
				}
			}, ticks, func() {
				if len(f.withdrawals) != 0 {
					t.Fatal("withdrawal preceded owned server stop")
				}
				stops++
			})
			if err == nil || stops != 1 || !reflect.DeepEqual(f.withdrawals, []string{"fixture-agent:original"}) {
				t.Fatal(err, stops, f.withdrawals)
			}
		})
	}
}

func (f *agentPresenceFixture) Announce(ctx context.Context, p wire.ApplicationPresence) (wire.ApplicationChange, error) {
	if err := ctx.Err(); err != nil {
		return wire.ApplicationChange{}, err
	}
	f.announcements = append(f.announcements, p)
	return f.announce(len(f.announcements))
}
func (f *agentPresenceFixture) Withdraw(ctx context.Context, app, instance string) (wire.ApplicationChange, error) {
	if err := ctx.Err(); err != nil {
		return wire.ApplicationChange{}, err
	}
	f.withdrawals = append(f.withdrawals, app+":"+instance)
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied}, f.withdrawErr
}

func TestAgentPresenceOwnsExactInstanceThroughRenewalAndWithdrawal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &agentPresenceFixture{announce: func(n int) (wire.ApplicationChange, error) {
		return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "native-instance"}, nil
	}}
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	opened, ready := 0, 0
	err := maintainAgentPresence(ctx, f, func(ctx context.Context, instance, name string) error {
		opened++
		if instance != "native-instance" || name != "child" {
			t.Fatal("foreign inbox association")
		}
		if opened == 2 {
			cancel()
		}
		return nil
	}, "fixture-agent", "child", func(instance string) {
		ready++
		if instance != "native-instance" {
			t.Fatal(instance)
		}
	}, ticks)
	if !errors.Is(err, context.Canceled) || opened != 2 || ready != 1 || len(f.announcements) != 2 {
		t.Fatal(err, opened, ready, f.announcements)
	}
	if f.announcements[0].Instance != "" || f.announcements[1].Instance != "native-instance" || !reflect.DeepEqual(f.withdrawals, []string{"fixture-agent:native-instance"}) {
		t.Fatal(f)
	}
}

func TestAgentPresenceRefusalAndLossDoNotRetryOrBorrowAnInstance(t *testing.T) {
	for _, mode := range []string{"announce", "inbox", "renew", "renew-inbox"} {
		t.Run(mode, func(t *testing.T) {
			f := &agentPresenceFixture{announce: func(n int) (wire.ApplicationChange, error) {
				if mode == "announce" {
					return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
				}
				instance := "owned"
				if n > 1 && mode == "renew" {
					instance = "foreign"
				}
				return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: instance}, nil
			}}
			ticks := make(chan time.Time, 1)
			ticks <- time.Time{}
			ready, opened := 0, 0
			err := maintainAgentPresence(context.Background(), f, func(context.Context, string, string) error {
				opened++
				if mode == "inbox" {
					return errors.New("inbox refused")
				}
				if mode == "renew-inbox" && opened == 2 {
					return errors.New("inbox admission revoked")
				}
				return nil
			}, "fixture-agent", "child", func(string) { ready++ }, ticks)
			if err == nil {
				t.Fatal("loss accepted")
			}
			if mode == "announce" {
				if opened != 0 || ready != 0 || len(f.withdrawals) != 0 {
					t.Fatal(f, opened, ready)
				}
			} else {
				wantOpened := 1
				if mode == "renew-inbox" {
					wantOpened = 2
				}
				if opened != wantOpened || !reflect.DeepEqual(f.withdrawals, []string{"fixture-agent:owned"}) {
					t.Fatal(f, opened)
				}
				if mode == "inbox" && ready != 0 {
					t.Fatal("inbox refusal advertised ready")
				}
			}
			want := 1
			if mode == "renew" || mode == "renew-inbox" {
				want = 2
			}
			if len(f.announcements) != want {
				t.Fatal("automatic retry", f)
			}
		})
	}
}
