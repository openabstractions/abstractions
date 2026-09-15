//go:build !windows

package main

import "context"

// Only the Windows installer holds an upgrade exclusion.
func jobdUpgradeGuard(context.Context) error { return nil }
