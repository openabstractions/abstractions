package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// ownerCompactJournalID is the plan id in the owner's
// model-pytorch-compact.json on 2026-11-09 (R18).
const ownerCompactJournalID = "fb55c30b3a2033b9d15f2c3b892110684189c0381cd33110532dc585736ef707"

// ownerStaleCompactJournal writes the owner's journal shape: every compact
// Klein file verified with its public job identity, provider_configuring
// set by a preparation that ended at 12:50:50, provider_configured false and
// no failure. Only the account's paths and SID differ.
func ownerStaleCompactJournal(t *testing.T, dir string) (ModelPlan, string) {
	t.Helper()
	plan := PyTorchModelPlan()
	if plan.ID != ownerCompactJournalID {
		t.Fatalf("compact plan id %s differs from the owner's journal", plan.ID)
	}
	stage := filepath.Join(dir, "OA", "m", "pt")
	binding := map[string]any{"provider": "openabstractions.job/1792751411854-9b632b849b17315dedb5", "contract": "abstraction.job/acceptance@1",
		"endpoint": `\\.\pipe\openabstractions-user-S-1-5-21-0-0-0-1001-job-acceptance-v1`, "logical_owner": "openabstractions.job/1792751411854-9b632b849b17315dedb5",
		"required_guarantees": []string{"abstraction.job/caller-exit@1", "abstraction.job/service-restart@1", "abstraction.job/reconciliation@1"}, "result_retention_ms": 86400000}
	operations := map[string]any{}
	for k, v := range binding {
		operations[k] = v
	}
	operations["contract"] = "abstraction.job/operations@1"
	effects := map[string]any{}
	for i, a := range plan.Assets {
		path := filepath.Join(stage, filepath.FromSlash(a.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(a.ID), 0600); err != nil {
			t.Fatal(err)
		}
		effects[a.ID] = map[string]any{"phase": "verified", "staged_path": path, "work": map[string]any{
			"key": "model-" + strings.Repeat("0", 31) + string(rune('a'+i)), "history_epoch": "1792751411854-9e600d15b399cba80266", "attempt": 0,
			"binding": binding, "operations_binding": operations}}
	}
	raw, err := json.Marshal(map[string]any{"plan_id": plan.ID, "approved": true, "effects": effects, "provider_configured": false, "provider_configuring": true})
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, "setup", "model-pytorch-compact.json")
	if err := os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return plan, journal
}

func staleCompactController(plan *ModelPlan, journal string, staged *int, probe func(context.Context, string) (bool, error)) ModelController {
	return ModelController{Plan: plan, JournalPath: journal, OperationLock: filepath.Join(filepath.Dir(journal), "model-pytorch.json.provider-op"),
		StageDir: filepath.Join(filepath.Dir(journal), "stage"), ProviderRoot: filepath.Join(filepath.Dir(journal), "provider"),
		StageModel: func(_ context.Context, _, _ string, files map[string]string, _ func(int64, int64) error) error {
			if len(files) != len(plan.Assets) {
				return errors.New("a verified file is missing")
			}
			*staged++
			return nil
		},
		Probe: probe}
}

// A stale configuring marker with no live operation is resolved by the next
// Configure: an unfinished preparation runs again from the verified files,
// a finished one is recorded. The owner's R18 Setup page showed Preparing
// without end on this journal.
func TestConfigureResumesOwnersStalePreparation(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		probe  func(context.Context, string) (bool, error)
		staged int
	}{
		{"unfinished", func(context.Context, string) (bool, error) { return false, nil }, 1},
		{"partial engine state", func(context.Context, string) (bool, error) { return false, errors.New("engine check failed") }, 1},
		{"finished", func(context.Context, string) (bool, error) { return true, nil }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, journal := ownerStaleCompactJournal(t, t.TempDir())
			staged := 0
			c := staleCompactController(&plan, journal, &staged, tc.probe)
			status, err := c.Configure(ctx, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !status.ProviderConfigured || status.ProviderConfiguring || status.ProviderFailure != "" || staged != tc.staged {
				t.Fatalf("status %+v staged %d", status, staged)
			}
		})
	}
}

// A preparation that still holds the provider operation lock keeps its
// marker: Configure waits for the lock and changes nothing.
func TestConfigureWaitsForLivePreparation(t *testing.T) {
	ctx := context.Background()
	plan, journal := ownerStaleCompactJournal(t, t.TempDir())
	staged := 0
	c := staleCompactController(&plan, journal, &staged, func(context.Context, string) (bool, error) { return false, nil })
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- cas.ChangeLimitContext(ctx, c.OperationLock, 64, func([]byte) ([]byte, error) {
			close(held)
			<-release
			return []byte("complete"), nil
		})
	}()
	<-held
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := c.Configure(deadline, false, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("configure entered a live preparation: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state, err := c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.ProviderConfiguring || staged != 0 {
		t.Fatalf("live preparation marker changed: %+v staged %d", state, staged)
	}
}
