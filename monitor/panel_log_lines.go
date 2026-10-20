package main

import (
	"context"
	"io"
	"log"
	"strings"
	"time"

	logging "github.com/openabstractions/abstraction-logging/go"
)

// backgroundLogMessage names the records that carry the background
// presenter's log lines.
const backgroundLogMessage = "background presenter"

// routeBackgroundLog also sends every line the standard logger writes to
// OA's logging service through sink, as a warning. The sign-in starts the
// background presenter with no console, so standard error alone loses why
// one of its tasks gave up. Lines queue without blocking the caller; a full
// queue drops the newest. stop restores the standard logger and delivers
// what is queued.
func routeBackgroundLog(sink *panelLog) (stop func()) {
	return routeLogLines(func(ctx context.Context, line string) {
		//unchecked: a failure to log is shown in the Panel's sink state; standard error keeps the line
		_ = sink.record(ctx, logging.LevelWarn, backgroundLogMessage, map[string]string{"panel.log": line})
	}, sink.close)
}

func routeLogLines(record func(context.Context, string), closeSink func(context.Context)) func() {
	lines := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for line := range lines {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			record(ctx, line)
			cancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		closeSink(ctx)
	}()
	previous := log.Writer()
	// The queue comes first: a presenter without a console may fail every
	// write to standard error, and MultiWriter stops at the first failure.
	log.SetOutput(io.MultiWriter(logLines(lines), previous))
	return func() {
		// SetOutput waits for a write in progress, so none follows the close.
		log.SetOutput(previous)
		close(lines)
		<-done
	}
}

// logLines queues each log line without its line end.
type logLines chan<- string

func (c logLines) Write(p []byte) (int, error) {
	select {
	case c <- strings.TrimRight(string(p), "\r\n"):
	default:
	}
	return len(p), nil
}
