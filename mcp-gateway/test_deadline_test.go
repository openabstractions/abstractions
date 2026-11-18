package main

import (
	"context"
	"testing"
	"time"
)

// testBound is the time a test may wait on a step whose speed depends on host
// load. It is a twentieth short of the test's own deadline, so the test still
// names the step it was waiting on; without a deadline (-timeout=0) it is ten
// minutes.
func testBound(t testing.TB) time.Duration {
	t.Helper()
	if d, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if end, ok := d.Deadline(); ok {
			return time.Until(end) * 19 / 20
		}
	}
	return 10 * time.Minute
}

// testCaller is a caller context carrying testBound as its deadline.
func testCaller(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testBound(t))
	t.Cleanup(cancel)
	return ctx
}
