package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Owner R31, 2026-11-12: nothing removed expired receipts, and every edit
// validated each kept result file, so each Announce took about 2 s with 860
// receipts kept. Admission removes expired receipts once a minute.
func TestApplicationV3ReceiptsCollectExpired(t *testing.T) {
	dir := t.TempDir()
	s := receiptStore(t, dir)
	now := receiptTestTime
	s.now = func() time.Time { return now }
	for n := 1; n <= 3; n++ {
		r := receiptRequest(n)
		r.Scope.Method = "Announce"
		_, token := receiptAdmit(t, s, r)
		if token == nil {
			t.Fatal("no admission")
		}
		if err := s.finish(context.Background(), *token, []byte(`{"outcome":"applied"}`)); err != nil {
			t.Fatal(err)
		}
	}
	results := func() int {
		entries, err := os.ReadDir(filepath.Join(dir, "applications-v3", "receipts", "results"))
		if err != nil {
			t.Fatal(err)
		}
		kept := 0
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".json" {
				kept++
			}
		}
		return kept
	}
	if results() != 3 {
		t.Fatalf("kept results %d", results())
	}
	// Eleven minutes on, the three Announce receipts have expired.
	now = now.Add(11 * time.Minute)
	r := receiptRequest(9)
	r.Key = receiptKey(9, now)
	if _, token := receiptAdmit(t, s, r); token == nil {
		t.Fatal("no admission after expiry")
	}
	if got := results(); got != 0 {
		t.Fatalf("expired results kept: %d", got)
	}
}
