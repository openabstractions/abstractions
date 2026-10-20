//go:build !windows

package main

import "errors"

func runBackground() error {
	return errors.New("background question presentation requires Windows; on macOS use Abstraction Panel.app")
}
