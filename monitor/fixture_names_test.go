package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
)

// fixtureSeq numbers the runtimes this test process listens for.
var fixtureSeq atomic.Uint64

// fixtureName is prefix made unique to this process and fixture: its process
// id and its own count. A name built from time.Now().UnixNano() alone repeats
// across test processes, since Go's wall clock on Windows steps every timer
// tick (0.5 ms to 15.6 ms): two concurrent runs of these tests then listened
// on one runtime's names, one lost its rights endpoint to the other and the
// Panel answered not_ready (TestAnsweredForYouUndoesOnlyTheAssistantsOwnPermit,
// 2026-10-07).
func fixtureName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, os.Getpid(), fixtureSeq.Add(1))
}

func TestFixtureNamesAreUniqueWithinOneClockTick(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		name := fixtureName("panel-first-use")
		if seen[name] {
			t.Fatalf("fixture name %s repeats", name)
		}
		seen[name] = true
	}
}
