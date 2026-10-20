//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNASInsufficientCapacityStopsBeforeRuntime(t *testing.T) {
	check := nasCheckWriteSpace
	t.Cleanup(func() { nasCheckWriteSpace = check })
	work := filepath.Join(t.TempDir(), "j-nas")
	refused := errors.New("the archive's volume would fall below its free-space reserve (53216890880 bytes free, 53687091200 kept free)")
	nasCheckWriteSpace = func(folder string, bytes int64) error {
		if folder != work || bytes != 7*144_811_072+(16<<20) {
			t.Fatalf("space check used %q and %d bytes", folder, bytes)
		}
		if info, err := os.Stat(folder); err != nil || !info.IsDir() {
			t.Fatalf("work folder missing at preflight: %v", err)
		}
		return refused
	}

	r := result{Journey: "J-NAS"}
	e, err := startJourneyEnv(t.Context(), r.Journey, &options{work: work}, &r, nil)
	if e != nil || !errors.Is(err, refused) || len(r.Checks) != 1 || r.Checks[0].Pass ||
		r.Checks[0].Name != "archive fixture capacity" || !strings.Contains(r.Checks[0].Detail, refused.Error()) {
		t.Fatalf("preflight did not report the capacity refusal: %+v, %v", r.Checks, err)
	}
	if _, err := os.Stat(filepath.Join(work, "runtime.stderr.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime start was attempted: %v", err)
	}
}
