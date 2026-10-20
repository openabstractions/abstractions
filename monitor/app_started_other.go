//go:build !windows

package main

import "errors"

// Only Windows integrations record started servers today.
func appProcessRuns(appStartedProcess) bool { return false }

func appProcessParent(int) (int, bool) { return 0, false }

func appStopProcess(appStartedProcess) error {
	return errors.New("stopping a started server is Windows-only")
}
