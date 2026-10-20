package main

import (
	"context"
	"errors"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The background presenter delegates the batch question and answer authority
// to Applications. It supplies the same positive connection inventory as Panel.
type agentHelperNotices struct{ batch func(context.Context) error }

type agentConnectionUpdatePending struct {
	Intent          string
	RestartRequired bool
	Err             error
}

func (e *agentConnectionUpdatePending) Error() string {
	return "update_pending: Close the assistant connections to finish: " + e.Err.Error()
}
func (e *agentConnectionUpdatePending) Unwrap() error { return e.Err }

type agentHelperParty struct {
	party setup.AgentParty
	name  string
}

var agentHelperParties = func() []agentHelperParty {
	order := product.AgentConnectionUpdateOrder()
	parties := make([]agentHelperParty, 0, len(order))
	for _, party := range order {
		parties = append(parties, agentHelperParty{setup.AgentParty(party), product.AgentConnectionTitle(party)})
	}
	return parties
}()

func (n agentHelperNotices) raise(ctx context.Context) error { return n.batch(ctx) }

func defaultAgentHelperNotices() agentHelperNotices {
	return agentHelperNotices{batch: runAgentConnectionUpdateBatch}
}

// agentRestartRetryPause is the retry cadence while an approved update waits
// for running apps, so a quick reopen rarely catches the previous connector.
const agentRestartRetryPause = 10 * time.Second

func agentHelperRetryPause(err error, pause time.Duration) time.Duration {
	var pending *agentConnectionUpdatePending
	if errors.As(err, &pending) && pending.RestartRequired && agentRestartRetryPause < pause {
		return agentRestartRetryPause
	}
	return pause
}

func raiseAgentHelperUpdatesAtLogin(ctx context.Context, n agentHelperNotices, pause time.Duration) error {
	var err error
	for attempt := 0; attempt < 4; {
		if err = n.raise(ctx); err == nil {
			return nil
		}
		var pending *agentConnectionUpdatePending
		if !errors.As(err, &pending) {
			attempt++
		}
		if attempt == 4 {
			break
		}
		timer := time.NewTimer(agentHelperRetryPause(err, pause))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}
