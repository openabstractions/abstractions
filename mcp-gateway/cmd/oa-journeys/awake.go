//go:build windows

package main

import (
	"sync"

	"github.com/openabstractions/abstraction-resource/go/platform"
)

var keepJourneyAwake = platform.KeepAwake

// holdAwake requests both system and display wakefulness for the journey.
func holdAwake(logf func(string, ...any)) (release func()) {
	release, err := keepJourneyAwake("OpenAbstractions journeys", "isolated journey run", true)
	if err != nil {
		logf("display hold failed; the screen may time out during the run")
		return func() {}
	}
	logf("keep-awake request accepted for the run")
	return sync.OnceFunc(release)
}
