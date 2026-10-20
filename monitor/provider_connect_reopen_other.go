//go:build !windows

package main

import "context"

func providerSetupNativeSupported() bool { return false }
func runProviderSetupNative(context.Context, string) (string, error) {
	return occurrenceUnavailable, errProviderConnectionChanged
}
