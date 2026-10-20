//go:build !windows

package main

import "errors"

func init() {
	startMenuPrograms = func() (string, error) { return "", errors.ErrUnsupported }
	readStartShortcut = func(string) (startShortcut, error) { return startShortcut{}, errors.ErrUnsupported }
	writeStartShortcut = func(string, startShortcut) error { return errors.ErrUnsupported }
}
