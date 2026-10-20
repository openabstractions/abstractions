package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestHostLogLineCarriesTimeAndProcess(t *testing.T) {
	var out bytes.Buffer
	writeHostLogLine(&out, "runtime: messages: inbox open")
	line := regexp.MustCompile(`^(\S+) \[(\d+)\] runtime: messages: inbox open\n$`).FindStringSubmatch(out.String())
	if line == nil {
		t.Fatalf("host log line %q", out.String())
	}
	if _, err := time.Parse(time.RFC3339Nano, line[1]); err != nil || !strings.HasSuffix(line[1], "Z") {
		t.Fatal("time", line[1], err)
	}
	if line[2] != fmt.Sprint(os.Getpid()) {
		t.Fatal("process", line[2])
	}
}

func TestHostLogLineRuntimeReport(t *testing.T) {
	var out bytes.Buffer
	runtimeReporter(&out)(errors.New("applications: slow inbox open took 1.5s"))
	line := regexp.MustCompile(`^(\S+) \[(\d+)\] runtime: applications: slow inbox open took 1.5s\n$`).FindStringSubmatch(out.String())
	if line == nil {
		t.Fatalf("report line %q", out.String())
	}
	if _, err := time.Parse(time.RFC3339Nano, line[1]); err != nil {
		t.Fatal("time", line[1], err)
	}
	if line[2] != fmt.Sprint(os.Getpid()) {
		t.Fatal("process", line[2])
	}
}

func TestPresenceSlowCallLines(t *testing.T) {
	var lines []string
	h := &applicationsHost{report: func(err error) { lines = append(lines, err.Error()) }}
	h.reportSlowPresence("inbox open", time.Now().Add(-500*time.Millisecond))
	if len(lines) != 0 {
		t.Fatal("fast call logged", lines)
	}
	h.reportSlowPresence("inbox open", time.Now().Add(-1500*time.Millisecond))
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "applications: slow inbox open took 1.5") {
		t.Fatal(lines)
	}
	// Within the same second further slow calls are counted, not written.
	h.reportSlowPresence("announce", time.Now().Add(-3*time.Second))
	h.reportSlowPresence("announce", time.Now().Add(-3*time.Second))
	if len(lines) != 1 {
		t.Fatal("slow lines not bounded", lines)
	}
	h.slowPresence.mu.Lock()
	h.slowPresence.last = time.Now().Add(-2 * time.Second)
	h.slowPresence.mu.Unlock()
	h.reportSlowPresence("announce", time.Now().Add(-2*time.Second))
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "applications: slow announce took 2") || !strings.HasSuffix(lines[1], "(2 more slow presence calls since the last line)") {
		t.Fatal(lines)
	}
	var none *applicationsHost
	none.reportSlowPresence("announce", time.Now().Add(-time.Hour))
}
