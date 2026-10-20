//go:build windows

package main

import (
	"errors"
	"testing"
)

func TestHoldAwakeSetsAndReleases(t *testing.T) {
	old := keepJourneyAwake
	t.Cleanup(func() { keepJourneyAwake = old })
	var calls, releases int
	keepJourneyAwake = func(who, why string, display bool) (func(), error) {
		if who == "" || why == "" || !display {
			t.Fatalf("wake request = %q, %q, display=%v", who, why, display)
		}
		calls++
		return func() { releases++ }, nil
	}
	var logged []string
	release := holdAwake(func(line string, _ ...any) { logged = append(logged, line) })
	release()
	release()
	if calls != 1 || releases != 1 {
		t.Fatalf("holds=%d releases=%d", calls, releases)
	}
	if len(logged) != 1 || logged[0] != "keep-awake request accepted for the run" {
		t.Fatalf("logged %q", logged)
	}
}

func TestHoldAwakeFailureAllowsRelease(t *testing.T) {
	old := keepJourneyAwake
	t.Cleanup(func() { keepJourneyAwake = old })
	keepJourneyAwake = func(string, string, bool) (func(), error) {
		return nil, errors.New("wake request failed")
	}
	var logged []string
	release := holdAwake(func(line string, _ ...any) { logged = append(logged, line) })
	release()
	release()
	if len(logged) != 1 || logged[0] != "display hold failed; the screen may time out during the run" {
		t.Fatalf("logged %q", logged)
	}
}
