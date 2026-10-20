package setup

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The Install record sits beside the plan journal, survives a reload, and
// takes step marks only for the plan it approved.
func TestInstallRecordMarksOnlyItsOwnPlan(t *testing.T) {
	ctx := context.Background()
	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	if empty, err := journal.LoadInstall(ctx); err != nil || empty.PlanID != "" || len(empty.Rules) != 0 {
		t.Fatalf("no record yet: %+v %v", empty, err)
	}
	rules := []ApprovedRule{{Action: "abstraction.inference/complete", Resource: "host:lemonade-image"}}
	record, err := journal.ApproveInstall(ctx, "plan-a", "model-a", rules, time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if record.ApprovedAt != "2026-10-02T12:00:00Z" || record.RulesDigest != RulesDigest(rules) || len(record.RulesDigest) != 64 {
		t.Fatalf("approval time %q", record.ApprovedAt)
	}
	if _, err := journal.MarkInstall(ctx, "plan-b", func(r *InstallRecord) { r.Granted = true }); err == nil {
		t.Fatal("a step of another plan marked this approval")
	}
	if _, err := journal.MarkInstall(ctx, "plan-a", func(r *InstallRecord) { r.Granted = true }); err != nil {
		t.Fatal(err)
	}
	loaded, err := (Journal{Path: journal.Path}).LoadInstall(ctx)
	if err != nil || loaded.PlanID != "plan-a" || loaded.ModelPlanID != "model-a" || len(loaded.Rules) != 1 || loaded.RulesDigest != record.RulesDigest || !loaded.Granted || loaded.Opened {
		t.Fatalf("reloaded record %+v %v", loaded, err)
	}
	path, _ := journal.InstallRecordPath()
	if filepath.Dir(path) != filepath.Dir(journal.Path) {
		t.Fatalf("record %s is not beside the journal %s", path, journal.Path)
	}
}
