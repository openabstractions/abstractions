//go:build !darwin

package main

import "errors"

func runNativeAsks() error {
	return errors.New("native Asks bridge requires the macOS launcher")
}
