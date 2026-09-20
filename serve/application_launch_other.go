//go:build !windows && !linux && !darwin

package main

import "errors"

func launchApplication(string, []string) error {
	return errors.New("application activation has no verified interactive session on this platform")
}
