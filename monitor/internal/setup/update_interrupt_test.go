package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withBuild runs the rest of the test as Panel build name.
func withBuild(t *testing.T, name string) {
	t.Helper()
	previous := PanelBuild
	PanelBuild = func() string { return name }
	t.Cleanup(func() { PanelBuild = previous })
}

// Owner, 2026-11-09: an OpenAbstractions update interrupts the Setup
// install. The approval a build recorded reads as interrupted by the update
// once another build reads it; reading it again changes nothing, and the
// person's Install continues it under the new build.
func TestInstallRecordInterruptedByUpdate(t *testing.T) {
	ctx := context.Background()
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	withBuild(t, "r17")
	approved, err := journal.ApproveInstallRecord(ctx, InstallRecord{PlanID: "plan-a", ModelPlanID: "klein"}, time.Now())
	if err != nil || approved.Build != "r17" {
		t.Fatalf("approve = %+v, %v", approved, err)
	}
	if same, err := journal.NoteInstallUpdate(ctx); err != nil || same.Interrupted != "" {
		t.Fatalf("same build = %+v, %v", same, err)
	}

	withBuild(t, "r18")
	stopped, err := journal.NoteInstallUpdate(ctx)
	if err != nil || stopped.Interrupted != InterruptedByUpdate || stopped.PlanID != "plan-a" || stopped.ModelPlanID != "klein" || stopped.Build != "r17" {
		t.Fatalf("after update = %+v, %v", stopped, err)
	}
	if again, err := journal.LoadInstall(ctx); err != nil || again.Interrupted != InterruptedByUpdate {
		t.Fatalf("journal = %+v, %v", again, err)
	}

	if _, err := journal.ContinueInstall(ctx, "plan-b"); err == nil {
		t.Fatal("continued another plan's approval")
	}
	continued, err := journal.ContinueInstall(ctx, "plan-a")
	if err != nil || continued.Interrupted != "" || continued.Build != "r18" || continued.ModelPlanID != "klein" || continued.ApprovedAt != approved.ApprovedAt {
		t.Fatalf("continue = %+v, %v", continued, err)
	}
	if read, err := journal.NoteInstallUpdate(ctx); err != nil || read.Interrupted != "" {
		t.Fatalf("read after continue = %+v, %v", read, err)
	}
}

// A cancelled approval stays cancelled: an update does not mark it and
// Install does not continue it.
func TestCancelledInstallIsNotInterrupted(t *testing.T) {
	ctx := context.Background()
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	withBuild(t, "r17")
	if _, err := journal.ApproveInstallRecord(ctx, InstallRecord{PlanID: "plan-a"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkInstall(ctx, "plan-a", func(r *InstallRecord) { r.Cancelled = true }); err != nil {
		t.Fatal(err)
	}
	withBuild(t, "r18")
	if record, err := journal.NoteInstallUpdate(ctx); err != nil || record.Interrupted != "" {
		t.Fatalf("cancelled after update = %+v, %v", record, err)
	}
	if _, err := journal.ContinueInstall(ctx, "plan-a"); err == nil {
		t.Fatal("continued a cancelled approval")
	}
}

// The owner's install.json of 2026-11-09 predates the build field: an
// earlier build wrote it, so the update interrupted it.
func TestInstallRecordWithoutBuildIsInterrupted(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	journal := Journal{Path: filepath.Join(dir, "plan.json")}
	raw := `{"plan_id":"c5db","model_plan_id":"fb55","rules":[{"action":"abstraction.inference/complete","resource":"host:pytorch-image"}],"rules_digest":"1b82","approved_at":"2026-11-09T10:29:05Z"}`
	if err := os.WriteFile(filepath.Join(dir, "install.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	withBuild(t, "r19")
	record, err := journal.NoteInstallUpdate(ctx)
	if err != nil || record.Interrupted != InterruptedByUpdate || record.RulesDigest != "1b82" || len(record.Rules) != 1 {
		t.Fatalf("record = %+v, %v", record, err)
	}
}

// Sign-in resumes approved step work only for the build that ran it.
func TestStepPendingOnlyForItsBuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "step.json")
	if err := os.WriteFile(path, []byte(`{"phase":"approved","use":"code","build":"r17"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	step := SetupStep{JournalPath: path}
	withBuild(t, "r17")
	if pending, err := step.Pending(ctx); err != nil || !pending {
		t.Fatalf("same build = %v, %v", pending, err)
	}
	withBuild(t, "r18")
	if pending, err := step.Pending(ctx); err != nil || pending {
		t.Fatalf("after update = %v, %v", pending, err)
	}
}
