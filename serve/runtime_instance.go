package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// One runtime owns a state directory (serve CONTRACT RUN-R11). The claim is an
// exclusive native lock inside that directory: the account's runtime-v1, or
// --state-dir. A runtime takes it before it opens retained logging or binds
// any listener, and holds it until it returns; process exit releases it. A
// runtime that cannot take it within runtimeClaimWait binds nothing and fails
// with errRuntimeClaimed.

// errRuntimeClaimed is a runtime that found another runtime serving its state.
var errRuntimeClaimed = errors.New("runtime: another runtime already serves this account's state")

// runtimeClaimWait covers a predecessor that is still exiting, such as a child
// the supervisor is replacing.
var runtimeClaimWait = 2 * time.Second

const runtimeClaimName = ".openabstractions-runtime-instance"

// runtimeClaimPath is the claim of the state this runtime composes: --state-dir,
// or the account's runtime state. A runtime given explicit listeners and no
// state directory composes no account state and takes no claim.
func runtimeClaimPath(options runtimeFlags) (string, error) {
	state, err := credentialsState(options)
	if err != nil || state == "" {
		return "", err
	}
	return filepath.Join(state, runtimeClaimName), nil
}

// claimRuntime takes this runtime's instance claim. Credentials, applications
// and the other account stores live in the same state directory, so the claim
// follows credentialsState's selection.
func claimRuntime(ctx context.Context, options runtimeFlags) (func(), error) {
	path, err := runtimeClaimPath(options)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return func() {}, nil
	}
	release, err := claimExclusive(ctx, path, runtimeClaimWait)
	if errors.Is(err, errClaimHeld) {
		return nil, fmt.Errorf("%w (%s)", errRuntimeClaimed, filepath.Dir(path))
	}
	return release, err
}

var errClaimHeld = errors.New("claim held by another process")

// claimExclusive takes the exclusive native lock at path, waiting at most wait
// for a holder to let go. A holder that keeps it is errClaimHeld; the caller's
// own cancellation is its context error.
func claimExclusive(ctx context.Context, path string, wait time.Duration) (func(), error) {
	bounded, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	release, err := cas.LockContext(bounded, path, false)
	if err == nil {
		return release, nil
	}
	if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, errClaimHeld
	}
	return nil, err
}

// composeCollision reports a composition step whose listener or retained store
// another process holds. Such a runtime would serve without that service, so
// startup fails and the supervisor's bounded restart retries it (RUN-R7). Any
// other step failure leaves the remaining services serving.
func composeCollision(prefix string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, listen.ErrTaken) || errors.Is(err, logging.ErrRetentionOwner) {
		return fmt.Errorf("%s %w", prefix, err)
	}
	complain(prefix, err)
	return nil
}
