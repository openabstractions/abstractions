package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	wire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

const panelOwnedApplication = "openabstractions-panel"

type panelOwnedPresenceCalls struct {
	announce func(context.Context, string, wire.ApplicationPresence) (wire.ApplicationChange, error)
	withdraw func(context.Context, string, string) error
}

func resolvePanelOwnedPresence(ctx context.Context) (panelOwnedPresenceCalls, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return panelOwnedPresenceCalls{}, err
	}
	return panelOwnedPresenceCalls{
		announce: func(ctx context.Context, key string, presence wire.ApplicationPresence) (wire.ApplicationChange, error) {
			return applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).Announce(key, presence)
		},
		withdraw: func(ctx context.Context, application, instance string) error {
			change, err := applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).Withdraw(application, instance)
			if err != nil {
				return err
			}
			if change.Outcome != wire.ApplicationOutcomeApplied {
				return fmt.Errorf("Panel withdrawal: %s", change.Reason)
			}
			return nil
		},
	}, nil
}

type panelOwnedPresence struct {
	calls    panelOwnedPresenceCalls
	instance string
	once     sync.Once
}

// The native directory assigns the handle. A lost first reply is returned as
// uncertainty; this owner never substitutes another handle or constructs a Core.
func announcePanelOwned(ctx context.Context, calls panelOwnedPresenceCalls) (*panelOwnedPresence, error) {
	presence := wire.ApplicationPresence{Application: panelOwnedApplication, LeaseMs: 30000,
		Interfaces: []wire.ApplicationInterface{},
		Contexts:   []wire.ApplicationContext{}}
	key, err := applications.NewRequestKey()
	if err != nil {
		return nil, err
	}
	change, err := calls.announce(ctx, key, presence)
	if err != nil {
		return nil, err
	}
	if change.Outcome != wire.ApplicationOutcomeApplied || change.Instance == "" {
		return nil, errors.New("Panel native announcement unavailable: " + change.Reason)
	}
	return &panelOwnedPresence{calls: calls, instance: change.Instance}, nil
}

func (p *panelOwnedPresence) heartbeat(ctx context.Context, retire func()) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			current := p.renew(callCtx)
			cancel()
			if !current {
				retire()
				return
			}
		}
	}
}

func (p *panelOwnedPresence) renew(ctx context.Context) bool {
	key, err := applications.NewRequestKey()
	if err != nil {
		return false
	}
	change, err := p.calls.announce(ctx, key, wire.ApplicationPresence{Application: panelOwnedApplication, Instance: p.instance, LeaseMs: 30000,
		Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{}})
	return err == nil && change.Outcome == wire.ApplicationOutcomeApplied && change.Instance == p.instance
}

func (p *panelOwnedPresence) withdraw() {
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Expiry retains absence semantics if the runtime is unavailable.
		if err := p.calls.withdraw(ctx, panelOwnedApplication, p.instance); err != nil {
			log.Print("Panel native presence withdrawal is unavailable")
		}
	})
}
