package main

import (
	"fmt"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
)

// exitVirtualizedProfile is the status of `start`, `serve host` and `serve
// runtime` refused because this process sees a packaged app's private copy of
// AppData. The SDK's activation reads it from `start` as ErrActivationRefused.
const exitVirtualizedProfile = 4

// wrongRuntimeFirstLine is the one sentence every command uses to report that
// the runtime it reached, or would reach, is not the one it expects. The
// specific cause follows on its own second line.
const wrongRuntimeFirstLine = "the runtime this command reached is not the one it expects"

// noRuntimeFirstLine is the one sentence every command uses to report that no
// runtime answered at all. The specific cause follows on its own second line.
const noRuntimeFirstLine = "no runtime answered"

// wrongRuntime formats the two-line message: the standard first sentence,
// then cause naming the specific reason.
func wrongRuntime(command, cause string) string {
	return fmt.Sprintf("%s: %s\n%s", command, wrongRuntimeFirstLine, cause)
}

// runtimeMismatchCause reports the second line for a runtime this command
// reached that answered as a different program, account or process than the
// one this command expects: the identity/listen package's server-trust check
// failed. detail is an error's full chain text, or its already-stringified
// form; both carry the same marker.
func runtimeMismatchCause(detail string) (string, bool) {
	const marker = "connected server is not trusted"
	i := strings.Index(detail, marker)
	if i < 0 {
		return "", false
	}
	cause := strings.TrimSpace(strings.TrimPrefix(detail[i+len(marker):], ":"))
	if cause == "" {
		cause = "the connected server did not match the program, account or process this command expects"
	}
	return cause, true
}

// packagedAppPhrase names a packaged host in the one sentence every command
// uses to say a runtime started here would keep private AppData: the family
// name in parentheses when there is one to show, since it identifies which
// packaged app this is; the plain phrase otherwise.
func packagedAppPhrase(family string) string {
	if family == "" {
		return "this shell runs inside a packaged app, whose private AppData a runtime started here would keep"
	}
	return fmt.Sprintf("this shell runs inside a packaged app (%s), whose private AppData a runtime started here would keep", family)
}

// refuseVirtualizedProfile refuses command when probe reports a virtualized or
// unknown profile view. A runtime there would serve one package's private copy
// of the account's rights, credentials and config, and every other application
// of the account would see different state (research/packaged-activation).
func refuseVirtualizedProfile(command string, probe func() (bootstrap.ProfileView, error)) error {
	view, err := probe()
	if err != nil {
		return &exitError{code: exitVirtualizedProfile, err: fmt.Errorf("%s", wrongRuntime(command, fmt.Sprintf("this process's profile view could not be read: %v", err)))}
	}
	if !view.Virtualized {
		return nil
	}
	return &exitError{code: exitVirtualizedProfile, err: fmt.Errorf("%s", wrongRuntime(command, packagedAppPhrase(view.Family)))}
}
