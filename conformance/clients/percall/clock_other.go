//go:build !windows

package main

import "time"

var epoch = time.Now()

// stamp reads the monotonic clock, CLOCK_MONOTONIC on Linux.
func stamp() time.Duration { return time.Since(epoch) }
