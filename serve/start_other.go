//go:build !windows && !linux && !darwin

package main

import (
	"context"
	"fmt"
)

func startGuard() error { return nil }
func activateInstalled(context.Context) error {
	return fmt.Errorf("start unsupported: this platform has no installed shared-runtime activation")
}
