//go:build !windows

package inferencefixture_test

import "time"

var epoch = time.Now()

// stamp reads the monotonic clock, CLOCK_MONOTONIC on Linux.
func stamp() time.Duration { return time.Since(epoch) }

const clockName = "time.Since (monotonic)"
