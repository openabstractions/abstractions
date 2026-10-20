package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func messageApplicationResource(resource string) (string, bool) {
	name, ok := strings.CutPrefix(resource, "app:")
	return name, ok && providerName.MatchString(name)
}

// configureMessagePermits uses the existing outer roles preparation and one
// quiet provider guard. No registration publication runs in a Rights callback.
func (r *runtimeRights) configureMessagePermits(d *applicationDirectory) error {
	if r == nil || r.policy == nil || d == nil {
		return errors.New("messages: permit owner unavailable")
	}
	if err := r.policy.ConfigurePermitAdmission(ActionApplicationMessage, func(ctx context.Context, subject wire.Subject, selection wire.PermissionSelection) (func() error, error) {
		var resource string
		if subject.Account != r.owner || selection.Permission.ResourceProfile != rights.LegacyExactResourceProfile || selection.Selector.Kind != wire.ResourceSelectorKindOne || json.Unmarshal(selection.Selector.Target, &resource) != nil {
			return nil, closedError("messages: invalid permit target")
		}
		if key, _, ok := parseMessagePeerResource(resource); ok {
			return r.prepareMessagePeerPermit(ctx, subject, key)
		}
		name, ok := messageApplicationResource(resource)
		if !ok {
			return nil, closedError("messages: invalid permit target name")
		}
		// The published facts, or those the outer operation prepared and
		// carries: a registration write may clear the published roles at any
		// moment, and this callback cannot prepare them under its locks.
		facts := d.messagingFactsFor(ctx)
		if facts == nil || facts.owner != r.owner {
			return nil, closedError("messages: target preparation unavailable")
		}
		program := ""
		for _, agent := range facts.agents {
			if agent.name == name {
				program = agent.program
			}
		}
		if program == "" || facts.current(ctx) != nil {
			return nil, closedError("messages: current receiver unavailable")
		}
		return func() error {
			// The captured registration facts staying current is the condition;
			// a republished roles index with the same facts changes no receiver.
			if err := facts.current(ctx); err != nil {
				return err
			}
			return ctx.Err()
		}, nil
	}); err != nil {
		return err
	}
	d.messageBlock = func(ctx context.Context, name string) error {
		if d.agentQuestions == nil || d.agentQuestions.a == nil || d.agentQuestions.a.book == nil {
			return errors.New("messages: question owner unavailable")
		}
		book := d.agentQuestions.a.book
		records, _, err := book.ApplicationHistoryScoped()
		if err != nil {
			return err
		}
		resource := "app:" + name
		for _, entry := range records {
			if !messagePermissionRecord(r.owner, entry.Record, resource) {
				continue
			}
			_, outcome, err := book.RetireApplicationAuthorized(entry.Record.ID, func() error { return ctx.Err() })
			if err != nil {
				return err
			}
			if outcome != "retired" && outcome != "unknown" {
				return errors.New("messages: question retirement unavailable")
			}
		}
		return r.policy.InvalidateProviderPermitsContext(ctx, resource)
	}
	return nil
}

func messagePermissionRecord(owner string, record asks.Record, resource string) bool {
	if permission, ok := asks.PermissionForRecord(owner, record); ok {
		for _, target := range permission.Targets {
			if target.Action == ActionApplicationMessage && target.Resource == resource {
				return true
			}
		}
	}
	if record.TypedPermission != nil {
		if _, captured, ok := grants.AgentMessageLegacyEffect(record.TypedPermission.Effect); ok {
			for _, pair := range captured {
				if "app:"+pair.Receiver.Name == resource || "app:"+pair.Sender.Name == resource {
					return true
				}
			}
			return false
		}
		for _, selection := range record.TypedPermission.Effect.Selections {
			var target string
			if selection.Permission.Action == ActionApplicationMessage && selection.Permission.ResourceProfile == rights.LegacyExactResourceProfile && selection.Selector.Kind == wire.ResourceSelectorKindOne && json.Unmarshal(selection.Selector.Target, &target) == nil && target == resource {
				return true
			}
		}
	}
	return false
}
