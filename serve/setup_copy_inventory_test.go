package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
)

func TestSetupCopyInventoryProducerRepairAndReadback(t *testing.T) {
	for _, kind := range []setupCopyKind{setupCopyGatewaySource, setupCopyGatewayCache} {
		for _, before := range []setupCopyState{setupCopyStale, setupCopyAbsent} {
			t.Run(string(kind)+"/"+string(before), func(t *testing.T) {
				state := before
				var calls []string
				row := setupCopyRow{Kind: kind, Owner: cacheproducer.ClaudeProducer, Location: "owned-copy",
					Observe: func(context.Context) (setupCopyReading, error) {
						calls = append(calls, "observe")
						return setupCopyReading{State: state}, nil
					}, Repair: func(context.Context) error {
						calls = append(calls, "producer repair")
						state = setupCopyCurrent
						return nil
					}}
				var inventory setupCopyInventory
				got, err := inventory.check(context.Background(), []setupCopyRow{row})
				if err != nil || len(got) != 1 || got[0].Before.State != before || !got[0].Repaired || got[0].After.State != setupCopyCurrent {
					t.Fatalf("repair/readback = %+v, %v", got, err)
				}
				if want := []string{"observe", "producer repair", "observe"}; !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls = %v; want %v", calls, want)
				}
			})
		}
	}
}

func TestSetupCopyInventoryRefusalsDoNotRepair(t *testing.T) {
	for _, state := range []setupCopyState{setupCopyCurrent, setupCopyHeld, setupCopyUnreadable,
		setupCopyForeign, setupCopyRefused, setupCopyHumanNeeded, setupCopyUnknown} {
		t.Run(string(state), func(t *testing.T) {
			var inventory setupCopyInventory
			row := setupCopyRow{Kind: setupCopyGatewaySource, Owner: cacheproducer.ClaudeProducer, Location: "owned-copy",
				Observe: func(context.Context) (setupCopyReading, error) { return setupCopyReading{State: state}, nil },
				Repair:  func(context.Context) error { t.Fatal("repair after refusal"); return nil }}
			got, err := inventory.check(context.Background(), []setupCopyRow{row})
			if err != nil || len(got) != 1 || got[0].Repaired || got[0].After.State != state {
				t.Fatalf("refusal = %+v, %v", got, err)
			}
		})
	}
}

func TestSetupCopyInventoryCannotClaimRepairBeforeCurrentReadback(t *testing.T) {
	for _, repairErr := range []error{nil, errors.New("held before producer admission")} {
		var inventory setupCopyInventory
		row := setupCopyRow{Kind: setupCopyGatewaySource, Owner: cacheproducer.ClaudeProducer, Location: "owned-copy",
			Observe: func(context.Context) (setupCopyReading, error) { return setupCopyReading{State: setupCopyStale}, nil },
			Repair:  func(context.Context) error { return repairErr }}
		got, err := inventory.check(context.Background(), []setupCopyRow{row})
		if err != nil || len(got) != 1 || got[0].Repaired {
			t.Fatalf("unconfirmed repair = %+v, %v", got, err)
		}
	}
}

func TestSetupCopyInventoryValidatesWholeRosterBeforeEffects(t *testing.T) {
	row := setupCopyRow{Kind: setupCopyGatewaySource, Owner: cacheproducer.ClaudeProducer, Location: "owned-copy",
		Observe: func(context.Context) (setupCopyReading, error) {
			t.Fatal("observed invalid roster")
			return setupCopyReading{}, nil
		}}
	for _, bad := range []setupCopyRow{
		{Kind: "new-unlisted-location", Owner: "producer", Location: "new-place", Observe: row.Observe},
		row,
	} {
		var inventory setupCopyInventory
		if _, err := inventory.check(context.Background(), []setupCopyRow{row, bad}); err == nil {
			t.Fatal("unlisted or duplicate placement accepted")
		}
	}
}

func TestSetupCopyInventoryReadErrorAndCancellationPreserveCopies(t *testing.T) {
	var inventory setupCopyInventory
	row := setupCopyRow{Kind: setupCopyGatewaySource, Owner: cacheproducer.ClaudeProducer, Location: "owned-copy",
		Observe: func(context.Context) (setupCopyReading, error) {
			return setupCopyReading{}, errors.New("unreadable receipt")
		},
		Repair: func(context.Context) error { t.Fatal("repair after read error"); return nil }}
	got, err := inventory.check(context.Background(), []setupCopyRow{row})
	if err != nil || len(got) != 1 || got[0].After.State != setupCopyUnreadable {
		t.Fatalf("read failure = %+v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inventory.check(ctx, []setupCopyRow{row}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestSetupCopyInventoryReportOnlyOwnersCannotBeTriggered(t *testing.T) {
	if setupCopyOwners[0].Kind != setupCopyEngine {
		t.Fatal("provider-owned engine startup must remain the first roster row")
	}
	for _, kind := range []setupCopyKind{setupCopyEngine, setupCopyMSI, setupCopyHelper, setupCopyOfficialCache} {
		var inventory setupCopyInventory
		row := setupCopyRow{Kind: kind, Owner: "external producer", Location: "receipt location",
			Observe: func(context.Context) (setupCopyReading, error) {
				return setupCopyReading{State: setupCopyHumanNeeded}, nil
			}}
		results, err := inventory.check(context.Background(), []setupCopyRow{row})
		if err != nil || len(results) != 1 || results[0].After.State != setupCopyHumanNeeded || results[0].Repaired {
			t.Fatalf("report-only owner: %+v, %v", results, err)
		}
		row.Repair = func(context.Context) error { t.Fatal("runtime triggered report-only owner"); return nil }
		if _, err := inventory.check(context.Background(), []setupCopyRow{row}); err == nil {
			t.Fatal("report-only owner received runtime repair authority")
		}
	}
}
