package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The runtime's standard logger writes host log lines: a package's
// log.Printf, such as the download archive's "download archive: served", carries
// the same UTC time and process id as the runtime's own lines (R34 live test
// D6: archive lines read local time beside UTC ones).
func TestStandardLogLinesUseTheHostLogShape(t *testing.T) {
	savedOutput, savedFlags := log.Writer(), log.Flags()
	t.Cleanup(func() {
		log.SetOutput(savedOutput)
		log.SetFlags(savedFlags)
	})
	var out strings.Builder
	log.SetOutput(&out)
	useHostLogLines()
	useHostLogLines()
	log.Printf("download archive: served %s from %s", strings.Repeat("ab", 32), "somewhere")
	line := out.String()
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("one line expected: %q", line)
	}
	stamp, rest, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || !strings.HasSuffix(stamp, "Z") || time.Since(at) > time.Minute || time.Since(at) < -time.Minute {
		t.Fatalf("line %q starts with no current UTC time", line)
	}
	if want := "[" + strconv.Itoa(os.Getpid()) + "] download archive: served "; !strings.HasPrefix(rest, want) {
		t.Fatalf("line %q lacks %q", line, want)
	}
}
