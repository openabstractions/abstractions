package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

// writeHostLogLine writes one host log line: the UTC time, this process's id
// and the message. The supervisor's own lines and the runtime's diagnostics,
// which the supervisor routes from the runtime's standard error into the same
// file, share this shape, so every line of host.log carries its time.
func writeHostLogLine(w io.Writer, message string) {
	//unchecked: w is the log sink itself; a failed write here has nowhere else to be reported
	_, _ = fmt.Fprintf(w, "%s [%d] %s\n", time.Now().UTC().Format(time.RFC3339Nano), os.Getpid(), message)
}

// hostLogLines writes each standard log message as one host log line, so a
// package's log.Printf, such as the download archive's, carries the same UTC
// time as the runtime's own lines (R34 live test, D6: the archive's lines
// read local time beside UTC ones).
type hostLogLines struct{ w io.Writer }

func (l hostLogLines) Write(p []byte) (int, error) {
	writeHostLogLine(l.w, strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

// useHostLogLines makes the standard logger write host log lines to its
// current output. It is idempotent.
func useHostLogLines() {
	if _, done := log.Writer().(hostLogLines); done {
		return
	}
	log.SetFlags(0)
	log.SetOutput(hostLogLines{w: log.Writer()})
}

// runtimeReporter returns the runtime's error reporter, which writes each
// error as one timestamped host log line.
func runtimeReporter(w io.Writer) func(error) {
	return func(err error) { writeHostLogLine(w, "runtime: "+err.Error()) }
}
