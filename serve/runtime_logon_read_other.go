//go:build !windows

package main

import "context"

// readRuntimeLogon has no platform read here yet; ReadSignInStart answers
// unavailable. Slice 1 of the setup step targets windows-x64.
func readRuntimeLogon(context.Context) (runtime, presenter bool, err error) {
	return false, false, runtimeLogonUnavailable("this platform's sign-in read")
}
