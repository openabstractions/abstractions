package main

import (
	"fmt"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
)

// exitVirtualizedProfile is the status of `start`, `serve host` and `serve
// runtime` refused because this process sees a packaged app's private copy of
// AppData. The SDK's activation reads it from `start` as ErrActivationRefused.
const exitVirtualizedProfile = 4

// refuseVirtualizedProfile refuses command when probe reports a virtualized or
// unknown profile view. A runtime there would serve one package's private copy
// of the account's rights, credentials and config, and every other application
// of the account would see different state (research/packaged-activation).
func refuseVirtualizedProfile(command string, probe func() (bootstrap.ProfileView, error)) error {
	view, err := probe()
	if err != nil {
		return &exitError{code: exitVirtualizedProfile, err: fmt.Errorf("%s: virtualized_profile: the profile view is unknown: %w", command, err)}
	}
	if !view.Virtualized {
		return nil
	}
	return &exitError{code: exitVirtualizedProfile, err: fmt.Errorf("%s: virtualized_profile: this process sees package %s's private copy of AppData, and a runtime started here would keep state only that package sees; start it from the Start menu, the Startup shortcut or a shell outside a packaged app", command, view.Family)}
}
