package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

type storageInventoryFixture struct {
	pages []content.InventoryPage
	calls int
}

func (f *storageInventoryFixture) List(ctx context.Context, continuation string, limit int64) (content.InventoryPage, error) {
	if err := ctx.Err(); err != nil {
		return content.InventoryPage{}, err
	}
	if f.calls >= len(f.pages) {
		return content.InventoryPage{}, errors.New("unexpected inventory read")
	}
	page := f.pages[f.calls]
	f.calls++
	return page, nil
}
func (f *storageInventoryFixture) Unheld(context.Context, string, int64) (content.InventoryPage, error) {
	return content.InventoryPage{Outcome: content.InventoryOutcomeUnavailable}, nil
}

func TestStorageInventoryCountsStrongestHoldAcrossSharedManifestEntries(t *testing.T) {
	entry := content.Entry{Locator: "shared", Size: 100}
	manifest := func(purpose string, lifetime content.Lifetime) content.ManifestHolders {
		return content.ManifestHolders{Manifest: content.Manifest{Store: "store", Entries: []content.Entry{entry}}, Holds: []content.Hold{{Purpose: purpose, Lifetime: lifetime}}}
	}
	f := &storageInventoryFixture{pages: []content.InventoryPage{
		{Outcome: content.InventoryOutcomePage, Manifests: []content.ManifestHolders{manifest("cache", content.LifetimeUntilReleased), manifest("work", content.LifetimeLease)}, Continuation: "next"},
		{Outcome: content.InventoryOutcomePage, Complete: true, Manifests: []content.ManifestHolders{manifest("new-purpose", content.LifetimeUntilReleased)}, Objects: []content.ObjectHolders{{Store: "store", Object: content.Object{Locator: "unknown", Size: 7}}}},
	}}
	got, err := readStorageInventory(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Classes[0].Bytes != 100 || got.Classes[1].Bytes != 0 || got.Classes[2].Bytes != 0 || got.Unclassified != 7 {
		t.Fatalf("shared bytes or missing hold misclassified: %+v", got)
	}
}

func TestStorageHoldClassUsesOpenPurposeLifetimeAndStrongestHold(t *testing.T) {
	for _, tc := range []struct {
		holds []content.Hold
		want  string
	}{
		{nil, ""},
		{[]content.Hold{{Purpose: "cache", Lifetime: content.LifetimeLease}}, "cache"},
		{[]content.Hold{{Purpose: "future-purpose", Lifetime: content.LifetimeLease}}, "tmp"},
		{[]content.Hold{{Purpose: "future-purpose", Lifetime: content.LifetimeWhilePresent}}, "data"},
		{[]content.Hold{{Purpose: "cache"}, {Lifetime: content.LifetimeLease}}, "tmp"},
		{[]content.Hold{{Purpose: "pinned"}, {Purpose: "cache"}}, "data"},
	} {
		if got := storageHoldClass(tc.holds); got != tc.want {
			t.Errorf("class of %+v = %q, want %q", tc.holds, got, tc.want)
		}
	}
}

func TestStorageInventoryRefusalAndCancellationNeverReportZeroUsage(t *testing.T) {
	f := &storageInventoryFixture{pages: []content.InventoryPage{{Outcome: content.InventoryOutcomeForbidden}}}
	if _, err := readStorageInventory(context.Background(), f); err == nil {
		t.Fatal("inventory refusal became empty usage")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readStorageInventory(ctx, &storageInventoryFixture{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestStorageLogsPreservesKnownUnlimitedAndUnavailable(t *testing.T) {
	old := storageReadLogUsage
	t.Cleanup(func() { storageReadLogUsage = old })
	for _, result := range []logwire.HistoryUsageResult{
		{Outcome: logwire.HistoryUsageOutcomeAvailable, Usage: &logwire.HistoryUsage{RetainedBytes: 7}},
		{Outcome: logwire.HistoryUsageOutcomeAvailable, Usage: &logwire.HistoryUsage{RetainedBytes: 7, MaxUse: new(int64), MaxRetentionMs: new(int64)}},
		{Outcome: logwire.HistoryUsageOutcomeUnavailable},
	} {
		storageReadLogUsage = func(context.Context) (logwire.HistoryUsageResult, error) { return result, nil }
		w := httptest.NewRecorder()
		new(servicePanel).storageLogs(w, httptest.NewRequest("GET", "/card/storage/logs", nil))
		if w.Code != 200 {
			t.Fatalf("usage response: %d %s", w.Code, w.Body.String())
		}
		var native string
		if err := json.Unmarshal(w.Body.Bytes(), &native); err != nil {
			t.Fatal(err)
		}
		decoded, err := logwire.DecodeHistoryUsageResult([]byte(native))
		if err != nil || !reflect.DeepEqual(*decoded, result) {
			t.Fatal("native usage changed", decoded, result, err)
		}
	}
}

func TestStorageInventoryReportsLargeItemsAndVerifiedMatches(t *testing.T) {
	const size = 2_000_000_000
	entry := content.Entry{Locator: "opaque-a", Size: size, Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Evidence: content.EvidenceHashed}
	manifest := content.ManifestHolders{Manifest: content.Manifest{
		Store: "models", Entries: []content.Entry{entry, entry},
		Names: []content.Name{{Name: "Model weights"}},
	}}
	object := func(locator string, evidence content.Evidence, partial bool) content.ObjectHolders {
		return content.ObjectHolders{Store: "pictures", Object: content.Object{
			Locator: locator, Size: size, Digest: entry.Digest, Evidence: evidence, Partial: partial,
		}}
	}
	manifest.Holds = []content.Hold{
		{Holder: content.Holder{Program: "ComfyUI"}, Attestation: content.AttestationObserved, Basis: &content.Basis{Index: "opaque-index", Key: "entry"}},
		{Holder: content.Holder{Program: "no evidence"}, Attestation: content.AttestationObserved},
		{Holder: content.Holder{Program: "declared"}, Attestation: content.AttestationDeclared},
	}
	inventory := &storageInventoryFixture{pages: []content.InventoryPage{
		{Outcome: content.InventoryOutcomePage, Manifests: []content.ManifestHolders{manifest}, Continuation: "next"},
		{Outcome: content.InventoryOutcomePage, Complete: true, Objects: []content.ObjectHolders{
			object("opaque-b", content.EvidenceHashed, false),
			object("opaque-c", content.EvidenceNamed, false),
			object("opaque-d", content.EvidenceHashed, true),
		}},
	}}
	view, err := readStorageInventory(context.Background(), inventory)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Large) != 4 || view.Large[0].Name != "Model weights" || view.Unclassified != 3*size || view.Classes[0].Bytes != size {
		t.Fatalf("large items: %+v", view)
	}
	if !reflect.DeepEqual(view.Large[0].ListedBy, []string{"ComfyUI"}) || len(view.Large[1].ListedBy) != 0 {
		t.Fatalf("observed holder evidence: %+v", view.Large)
	}
	if len(view.Matches) != 1 || view.Matches[0].Locations != 2 || view.Matches[0].Bytes != size {
		t.Fatalf("content matches: %+v", view.Matches)
	}
}

func TestStorageInventoryReportsFreeSpaceOnlyForKnownDrive(t *testing.T) {
	oldInventory, oldDisk := storageResolveInventory, setupInstallDisk
	t.Cleanup(func() { storageResolveInventory, setupInstallDisk = oldInventory, oldDisk })
	storageResolveInventory = func(context.Context) (storageInventory, error) {
		return &storageInventoryFixture{pages: []content.InventoryPage{
			{Outcome: content.InventoryOutcomePage, Complete: true},
		}}, nil
	}
	for _, tc := range []struct {
		volume string
		err    error
		want   *setupDisk
	}{
		{"C:", nil, &setupDisk{Volume: "C:", FreeBytes: 42 << 30}},
		{"C:", errors.New("unavailable"), nil},
		{"", nil, nil},
		{"local path", nil, nil},
	} {
		setupInstallDisk = func() (string, uint64, error) { return tc.volume, 42 << 30, tc.err }
		w := httptest.NewRecorder()
		new(servicePanel).storageInventory(w, httptest.NewRequest("GET", "/card/storage", nil))
		var view storageInventoryView
		if err := json.Unmarshal(w.Body.Bytes(), &view); w.Code != 200 || err != nil || !reflect.DeepEqual(view.Disk, tc.want) {
			t.Fatalf("volume %q: %d %s (%v)", tc.volume, w.Code, w.Body.String(), err)
		}
	}
}
