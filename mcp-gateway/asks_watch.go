package main

import (
	"context"
	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	rights "github.com/openabstractions/abstraction-rights/go"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func (b *messagesBackend) AsksWatch(ctx context.Context, in gateway.AsksWatchInput) (gateway.AsksWatchResult, error) {
	if in.WaitMs != 0 || len(in.Revision) > 128 {
		return gateway.AsksWatchResult{Outcome: "invalid", Items: []gateway.AsksWatchItem{}}, nil
	}
	responder, err := b.AsksResponder(ctx)
	if err != nil {
		return gateway.AsksWatchResult{Outcome: "unavailable", Items: []gateway.AsksWatchItem{}}, nil
	}
	return gateway.ObserveAsks(ctx, in, responder, b.asksAccess)
}

func asksWatchAccess(machine *client.Machine) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		reader, err := machine.ResolveRights(ctx, client.Requirements{Scope: client.ScopeLocal})
		if err != nil {
			return "", err
		}
		decision, err := reader.DecideContext(ctx, asks.AnswerAction, rights.MetaScopeApps)
		return decision.Outcome.String(), err
	}
}
