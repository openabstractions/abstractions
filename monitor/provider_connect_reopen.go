package main

import (
	"context"
	"errors"
)

var providerSetupReopenSupported = providerSetupNativeSupported
var providerSetupReopen = runProviderSetupNative

// startProviderSetup hands the provider occurrence id to the presenter, which
// draws its credential form, and returns the occurrence's outcome.
func (p *servicePanel) startProviderSetup(ctx context.Context, id string) (string, error) {
	if !providerSetupReopenSupported() {
		return "", errors.New("Catalog is not available on this native surface.")
	}
	if !p.setupActionMu.TryLock() {
		return "", errors.New("Another setup window is open. Finish it before opening setup again.")
	}
	defer p.setupActionMu.Unlock()
	return providerSetupReopen(ctx, id)
}
