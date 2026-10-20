package main

import (
	"errors"
	"testing"

	download "github.com/openabstractions/abstraction-download/go"
)

func TestManagedDownloadFreeSpaceReserve(t *testing.T) {
	const gib = 1 << 30
	for _, test := range []struct {
		name        string
		free, total uint64
		additional  int64
		refused     bool
	}{
		{"small volume boundary", 14 * gib, 100 * gib, 4 * gib, false},
		{"small volume below boundary", 14 * gib, 100 * gib, 4*gib + 1, true},
		{"large volume capped", 22 * gib, 500 * gib, 2 * gib, false},
		{"already below reserve", 19 * gib, 500 * gib, 1, true},
		{"no new bytes", 19 * gib, 500 * gib, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkDownloadReserve("work", test.additional, func(path string) (uint64, uint64, error) {
				if path != "work" {
					t.Fatal(path)
				}
				return test.free, test.total, nil
			})
			if test.refused {
				if !errors.Is(err, download.ErrRefused) || !download.Permanent(err) {
					t.Fatal("expected terminal space refusal", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
