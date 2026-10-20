package setup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A refused step's component and reason survive a reload of the record, and
// the person's Install on the stopped approval clears them.
func TestInstallFailureSurvivesReloadUntilContinue(t *testing.T) {
	ctx := context.Background()
	j := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	rules := []ApprovedRule{{Action: "abstraction.inference/complete", Resource: "host:lemonade-image"}}
	if _, err := j.ApproveInstall(ctx, "plan-a", "model-a", rules, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RecordInstallFailure(ctx, "plan-b", InstallFailure{Component: "comfyui", Sentence: "x"}); err == nil {
		t.Fatal("a failure for another plan was kept")
	}
	long := strings.Repeat("d", 5000)
	if _, err := j.RecordInstallFailure(ctx, "plan-a", InstallFailure{Component: "comfyui", Sentence: "ComfyUI did not install. The disk is full.", Detail: long}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := (Journal{Path: j.Path}).LoadInstall(ctx)
	if err != nil || reloaded.Failure == nil || reloaded.Failure.Component != "comfyui" || reloaded.Failure.Sentence != "ComfyUI did not install. The disk is full." || len(reloaded.Failure.Detail) != 600 || reloaded.RulesDigest != RulesDigest(rules) {
		t.Fatalf("failure after reload: %+v %v", reloaded.Failure, err)
	}
	continued, err := j.ContinueInstall(ctx, "plan-a")
	if err != nil || continued.Failure != nil {
		t.Fatalf("continue kept the failure: %+v %v", continued.Failure, err)
	}
	if again, _ := j.LoadInstall(ctx); again.Failure != nil {
		t.Fatalf("stored failure after continue: %+v", again.Failure)
	}
}

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

func TestInstallRecordKeepsExplicitCodingApprovalAndLegacyAbsence(t *testing.T) {
	ctx := context.Background()
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	if _, err := j.ApproveInstall(ctx, "apps", "", nil, time.Now(), CodingModelPlan().ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := (Journal{Path: j.Path}).LoadInstall(ctx)
	if err != nil || reopened.CodingModelPlanID != CodingModelPlan().ID {
		t.Fatalf("coding approval lost: %+v %v", reopened, err)
	}
	if _, err := j.ApproveInstall(ctx, "legacy", "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacy, err := j.LoadInstall(ctx)
	if err != nil || legacy.CodingModelPlanID != "" {
		t.Fatalf("legacy approval acquired a model: %+v %v", legacy, err)
	}
}
