package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
)

// setupController is the runtime's private Setup owner. Receipt and cooldown
// policy live with setupRequests; the copy inventory is a projection of producer
// receipts. Neither projection grants installation or enrollment authority.
type setupController struct {
	setupRequests
	copies setupCopyInventory
}

type setupCopyKind string

const (
	setupCopyEngine        setupCopyKind = "managed-engine"
	setupCopyMSI           setupCopyKind = "installed-payload"
	setupCopyHelper        setupCopyKind = "enrolled-helper"
	setupCopyGatewaySource setupCopyKind = "gateway-source"
	setupCopyGatewayCache  setupCopyKind = "gateway-cache"
	setupCopyOfficialCache setupCopyKind = "official-cache"
)

type setupCopyOwner struct {
	Kind                setupCopyKind
	Owner, Evidence     string
	RuntimeMayReconcile bool
}

// This roster names authority and evidence, never expected file hashes. The
// engine is first: its own startup reconciles before its endpoint can serve.
var setupCopyOwners = [...]setupCopyOwner{
	{setupCopyEngine, "provider startup", "provider-owned startup; typed copy status pending", false},
	{setupCopyMSI, "installation", "installation payload owner", false},
	{setupCopyHelper, "Panel Setup", "native enrollment and human Keep", false},
	{setupCopyGatewaySource, cacheproducer.ClaudeProducer, "gateway source receipt", true},
	{setupCopyGatewayCache, cacheproducer.ClaudeProducer, "gateway receipt and native vendor readback", true},
	{setupCopyGatewaySource, cacheproducer.CodexProducer, "gateway source receipt", true},
	{setupCopyGatewayCache, cacheproducer.CodexProducer, "gateway receipt and native vendor readback", true},
	{setupCopyOfficialCache, "official application installer", "official package provenance", false},
}

func (k setupCopyKind) owner(producer string) (setupCopyOwner, bool) {
	for _, owner := range setupCopyOwners {
		if owner.Kind == k && (!owner.RuntimeMayReconcile || owner.Owner == producer) {
			return owner, true
		}
	}
	return setupCopyOwner{}, false
}

type setupCopyState string

const (
	setupCopyCurrent     setupCopyState = "current"
	setupCopyStale       setupCopyState = "stale"
	setupCopyAbsent      setupCopyState = "absent"
	setupCopyHeld        setupCopyState = "held"
	setupCopyUnreadable  setupCopyState = "unreadable"
	setupCopyForeign     setupCopyState = "foreign"
	setupCopyRefused     setupCopyState = "refused"
	setupCopyHumanNeeded setupCopyState = "human-needed"
	setupCopyUnknown     setupCopyState = "unknown"
)

type setupCopyReading struct {
	State  setupCopyState
	Reason string
}

// A producer supplies its receipt-derived location and current expectation to
// observe. Only that producer may supply repair. The inventory stores no hashes,
// desired paths, permission answers or vendor configuration.
type setupCopyRow struct {
	Kind     setupCopyKind
	Owner    string
	Location string
	Observe  func(context.Context) (setupCopyReading, error)
	Repair   func(context.Context) error
}

type setupCopyResult struct {
	Kind            setupCopyKind
	Owner, Location string
	Before, After   setupCopyReading
	Repaired        bool
}

type setupCopyInventory struct {
	mu      sync.Mutex
	results []setupCopyResult
}

// check validates the complete producer projection before observing any row.
// Adding a location with an unlisted kind fails the pass before any repair.
// Stale and absent are positive ownership classifications made by the producer;
// a missing receipt is foreign or unknown, rather than an owned absent copy.
func (i *setupCopyInventory) check(ctx context.Context, rows []setupCopyRow) ([]setupCopyResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := string(row.Kind) + "\x00" + row.Location
		owner, listed := row.Kind.owner(row.Owner)
		if !listed || row.Owner == "" || row.Observe == nil || seen[key] || row.Location == "" && owner.RuntimeMayReconcile {
			return nil, fmt.Errorf("setup copies: unlisted or duplicate placement %q at %q", row.Kind, row.Location)
		}
		if row.Repair != nil && !owner.RuntimeMayReconcile {
			return nil, fmt.Errorf("setup copies: %s requires its %s owner", row.Kind, owner.Owner)
		}
		seen[key] = true
	}
	results := make([]setupCopyResult, 0, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := setupCopyResult{Kind: row.Kind, Owner: row.Owner, Location: row.Location}
		reading, err := row.Observe(ctx)
		if err != nil {
			reading = setupCopyReading{State: setupCopyUnreadable, Reason: err.Error()}
		}
		result.Before, result.After = reading, reading
		if err == nil && (reading.State == setupCopyStale || reading.State == setupCopyAbsent) && row.Repair != nil {
			// Repair performs its own receipt/identity/CAS admission. A read
			// here supplies no authority to overwrite changed files.
			if err := row.Repair(ctx); err != nil {
				result.After = setupCopyReading{State: setupCopyUnknown, Reason: err.Error()}
			} else {
				result.After, err = row.Observe(ctx)
				if err != nil {
					result.After = setupCopyReading{State: setupCopyUnreadable, Reason: err.Error()}
				}
				result.Repaired = err == nil && result.After.State == setupCopyCurrent
			}
		}
		results = append(results, result)
	}
	i.results = results
	return append([]setupCopyResult(nil), results...), nil
}

func (i *setupCopyInventory) snapshot() []setupCopyResult {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]setupCopyResult(nil), i.results...)
}
