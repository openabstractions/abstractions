//go:build !darwin

package setup

import "testing"

// assertTimeMachineExcluded has nothing to check off macOS.
func assertTimeMachineExcluded(*testing.T, string) {}
