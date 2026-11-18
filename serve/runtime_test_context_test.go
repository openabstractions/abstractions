package main

import (
	"context"
	"testing"
	"time"
)

// runtimeTestContext keeps a retained fixture alive for the invoking test.
// Protocol, authorization and operation deadlines remain with their owners.
func runtimeTestContext(t *testing.T, parent context.Context) (context.Context, context.CancelFunc) {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		return context.WithDeadline(parent, deadline)
	}
	return context.WithCancel(parent)
}

// runtimeTestBudgetMillis keeps fixture operation bounds within both the
// invoking test's remaining lifetime and the operation's existing contract cap.
func runtimeTestBudgetMillis(t *testing.T, maximum time.Duration) int64 {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		maximum = min(maximum, time.Until(deadline))
	}
	if maximum < time.Second {
		t.Fatal("fixture caller has less than one second remaining")
	}
	return maximum.Milliseconds()
}
