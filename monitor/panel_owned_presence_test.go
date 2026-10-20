package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
)

func TestPanelOwnedPresenceUsesServiceHandle(t *testing.T) {
	calls := 0
	withdraws := 0
	p, err := announcePanelOwned(context.Background(), panelOwnedPresenceCalls{
		announce: func(_ context.Context, key string, in wire.ApplicationPresence) (wire.ApplicationChange, error) {
			calls++
			if !strings.HasPrefix(key, "a3.") || in.Instance != "" || in.Application != panelOwnedApplication || len(in.Interfaces) != 0 {
				t.Fatalf("initial presence %#v", in)
			}
			return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "service-issued"}, nil
		},
		withdraw: func(_ context.Context, app, instance string) error {
			withdraws++
			if app != panelOwnedApplication || instance != "service-issued" {
				t.Fatalf("withdraw %s %s", app, instance)
			}
			return nil
		},
	})
	if err != nil || p.instance != "service-issued" || calls != 1 {
		t.Fatalf("announcement %#v %v", p, err)
	}
	p.withdraw()
	p.withdraw()
	if withdraws != 1 {
		t.Fatalf("withdraws %d", withdraws)
	}
}

func TestPanelOwnedHeartbeatNeverRebinds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change wire.ApplicationChange
		err    error
		want   bool
	}{
		{"current", wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "service-issued"}, nil, true},
		{"different handle", wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "replacement"}, nil, false},
		{"expired", wire.ApplicationChange{Outcome: wire.ApplicationOutcomeStale}, nil, false},
		{"unavailable", wire.ApplicationChange{}, errors.New("unavailable"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := map[string]bool{}
			p := &panelOwnedPresence{instance: "service-issued", calls: panelOwnedPresenceCalls{announce: func(_ context.Context, key string, in wire.ApplicationPresence) (wire.ApplicationChange, error) {
				if !strings.HasPrefix(key, "a3.") || keys[key] || in.Instance != "service-issued" || len(in.Interfaces) != 0 {
					t.Fatalf("heartbeat %#v key%s", in, key)
				}
				keys[key] = true
				return tc.change, tc.err
			}}}
			for n := 0; n < 2; n++ {
				if got := p.renew(context.Background()); got != tc.want {
					t.Fatalf("renew%v want%v", got, tc.want)
				}
			}
			if p.instance != "service-issued" {
				t.Fatal("heartbeat rebound the Core")
			}
		})
	}
}

func TestPanelOwnedPresenceUncertaintyNeverMintsReplacement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change wire.ApplicationChange
		err    error
	}{
		{"lost reply", wire.ApplicationChange{}, errors.New("lost reply")},
		{"refused", wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden, Reason: "policy"}, nil},
		{"missing handle", wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p, err := announcePanelOwned(context.Background(), panelOwnedPresenceCalls{announce: func(context.Context, string, wire.ApplicationPresence) (wire.ApplicationChange, error) {
				calls++
				return tc.change, tc.err
			}})
			if p != nil || err == nil || calls != 1 {
				t.Fatalf("uncertain result %#v %v calls%d", p, err, calls)
			}
		})
	}
}
