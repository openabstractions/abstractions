package setup

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestExamplePackageScheduledUpdateAdmission(t *testing.T) {
	ctx := t.Context()
	store := PackageMaintenanceStore{Path: filepath.Join(t.TempDir(), "example-package.json")}
	const pin = "new-connector"
	before := PackageMaintenanceRecord{Seen: "old-release", Kept: "old-connector", Recorded: "old-release", History: []string{"old-connector"}}
	if err := store.Edit(ctx, func(record *PackageMaintenanceRecord) error {
		*record = before
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unexpectedStamp := func() (string, int64) {
		t.Fatal("plan requested a stamp without creating an expectation")
		return "", 0
	}
	unchanged, done, downgrade := before.PlanScheduledUpdate(pin, PackageUpdateObservation{}, unexpectedStamp)
	if done || downgrade || !reflect.DeepEqual(unchanged, before) {
		t.Fatalf("unavailable native read settled a decision: %+v", unchanged)
	}
	observed := PackageUpdateObservation{Known: true, Kept: true, Outdated: true, Application: "example-package",
		Program: filepath.Join(filepath.Dir(store.Path), "connector.exe")}
	next, done, downgrade := before.PlanScheduledUpdate(pin, observed, func() (string, int64) { return "release-2", 23 })
	want := PackageUpdateExpectation{Application: observed.Application, Program: observed.Program, SHA256: pin, Version: "release-2", At: 23}
	if !done || downgrade || next.PendingUpdate == nil || *next.PendingUpdate != want || next.Seen != pin || next.Recorded != before.Recorded {
		t.Fatalf("planned the wrong receipt: %+v", next)
	}
	if err := store.Edit(ctx, func(record *PackageMaintenanceRecord) error {
		return record.AdmitScheduledUpdate(before, next, next.PendingUpdate)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Edit(ctx, func(record *PackageMaintenanceRecord) error {
		return record.AdmitScheduledUpdate(before, unchanged, nil)
	}); err == nil {
		t.Fatal("a stale decision overwrote an admitted expectation")
	}
	read, err := store.Load()
	if err != nil || !reflect.DeepEqual(read, next) {
		t.Fatalf("admitted receipt: %+v (%v)", read, err)
	}
	again, done, downgrade := read.PlanScheduledUpdate(pin, observed, unexpectedStamp)
	if !done || downgrade || !reflect.DeepEqual(again, read) {
		t.Fatalf("replanning replaced an existing expectation: %+v", again)
	}
	active := *read.PendingUpdate
	active.Replacement = &PackageReplacement{Candidate: "candidate-package"}
	read.PendingUpdate = &active
	snapshot := read.ScheduledUpdateSnapshot("different-release")
	deferred, done, downgrade := snapshot.PlanScheduledUpdate("different-release", PackageUpdateObservation{Known: true}, unexpectedStamp)
	if done || downgrade || !reflect.DeepEqual(deferred, read) {
		t.Fatalf("a new release displaced an admitted replacement: %+v", deferred)
	}

}
