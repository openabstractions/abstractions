//go:build !windows

package main

import "errors"

func runTray(_ string) error {
	return errors.New("notification tray unavailable on this platform")
}
