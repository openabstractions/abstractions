//go:build !windows

package main

import "errors"

func appWindow(connection) error {
	return errors.New("the native app window is available on Windows; use menu for this platform")
}

func reportWindowError() {}
