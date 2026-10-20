//go:build !windows && !darwin && !linux

package main

import "context"

func reconcileRuntimeLogon(context.Context, bool) error {
	return runtimeLogonUnavailable("this platform")
}
