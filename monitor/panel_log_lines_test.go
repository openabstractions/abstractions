package main

import (
	"bytes"
	"context"
	"log"
	"slices"
	"testing"
)

// The background presenter's log lines reach OA's logging service as well
// as standard error, and stop delivers them and restores the logger.
func TestBackgroundLogLinesReachTheLoggingSink(t *testing.T) {
	var stderr bytes.Buffer
	previous, flags := log.Writer(), log.Flags()
	log.SetOutput(&stderr)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(previous); log.SetFlags(flags) })

	var recorded []string
	closed := false
	stop := routeLogLines(func(_ context.Context, line string) { recorded = append(recorded, line) },
		func(context.Context) { closed = true })
	log.Printf("picture engine: the earlier registration stays after %d tries: %v", 5, "unknown pin")
	log.Print("second line")
	stop()
	want := []string{"picture engine: the earlier registration stays after 5 tries: unknown pin", "second line"}
	if !slices.Equal(recorded, want) || !closed {
		t.Fatalf("recorded %q, closed %v", recorded, closed)
	}
	if stderr.String() != want[0]+"\n"+want[1]+"\n" {
		t.Fatalf("standard error lost a line: %q", stderr.String())
	}
	log.Print("after stop")
	if len(recorded) != 2 || log.Writer() != &stderr {
		t.Fatal("the logger was not restored")
	}
}
