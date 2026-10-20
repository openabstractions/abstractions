package setup

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Setup's state read runs an installed Lemonade provider's setup-info, which
// writes the installed.json an upgraded provider lacks. One exit 0 is
// enough, whatever the reply says. A failed run is repeated no sooner than a
// minute later. Without an installed provider it runs no provider verb.
func TestStateReadRunsAnInstalledLemonadesSetupInfoOnce(t *testing.T) {
	reset := func() {
		lemonadeRecord.Lock()
		lemonadeRecord.done, lemonadeRecord.running, lemonadeRecord.failed = false, false, time.Time{}
		lemonadeRecord.Unlock()
	}
	reset()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	savedNow, savedCLI := lemonadeNow, lemonadeCLI
	t.Cleanup(func() { lemonadeNow, lemonadeCLI = savedNow, savedCLI; reset() })
	lemonadeNow = func() time.Time { return now }
	calls := 0
	var reply func() ([]byte, error)
	lemonadeCLI = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) != 2 || args[0] != "setup-info" || args[1] != "--json" {
			return nil, errors.New("unexpected provider command")
		}
		calls++
		return reply()
	}
	record := func() error { return ReconcileInstalledPictureProviderRecords(context.Background()) }

	withLemonadeJournal(t, Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")})
	if err := record(); err != nil || calls != 0 {
		t.Fatalf("no installed provider: %d calls, %v", calls, err)
	}

	f := newUpdateFixture(t, "lemonade-engine", packageEntries("lemonade-engine", "2.0.0"))
	withLemonadeJournal(t, f.journal)
	reply = func() ([]byte, error) { return nil, errors.New("inert provider failure") }
	if err := record(); err == nil || err.Error() != "Lemonade installed models: inert provider failure" || calls != 1 {
		t.Fatalf("failed setup-info: %d calls, %v", calls, err)
	}
	now = now.Add(59 * time.Second)
	if err := record(); err != nil || calls != 1 {
		t.Fatalf("a read within the minute ran the provider again: %d calls, %v", calls, err)
	}
	// An exit 0 whose reply the Panel would refuse, as during an update,
	// still means the provider wrote installed.json.
	now = now.Add(2 * time.Second)
	reply = func() ([]byte, error) { return []byte(`{"version":99}`), nil }
	if err := record(); err != nil || calls != 2 {
		t.Fatalf("setup-info after the minute: %d calls, %v", calls, err)
	}
	now = now.Add(time.Hour)
	if err := record(); err != nil || calls != 2 {
		t.Fatalf("a recorded provider ran again: %d calls, %v", calls, err)
	}
}
