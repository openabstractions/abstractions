package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"

	cas "github.com/openabstractions/abstraction-cas/go"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

var errAccountStateOverlap = layout.ErrAccountStateOverlap

// The caller supplies account metadata and its existing shared/exclusive lock
// owner. Private state never opens that lock or an account store.
func runtimeStateGuard(ctx context.Context, options runtimeFlags, account func() (resetRoots, error), lock func(context.Context, resetRoots, bool) (func(), error)) (func(), error) {
	return runtimeStateGuardWithDefaults(ctx, options, account, lock, nil)
}

func runtimeStateGuardWithDefaults(ctx context.Context, options runtimeFlags, account func() (resetRoots, error), lock func(context.Context, resetRoots, bool) (func(), error), defaults func() ([]string, error)) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRuntimeStateSelection(options); err != nil {
		return nil, err
	}
	r, err := account()
	if err != nil {
		return nil, err
	}
	if options.stateDir == "" {
		return lock(ctx, r, true)
	}
	// These are the reset owner's directory entries, without its effectful
	// plan/walk. Pending resolves aliases and not-yet-created descendants using
	// the existing identity owner; PathNameWithin compares canonical names.
	roots := resetOwnedRootNames(r)
	if defaults != nil {
		paths, err := defaults()
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				return nil, fmt.Errorf("runtime: unresolved account default metadata")
			}
		}
		roots = append(roots, paths...)
	}
	for _, path := range []string{options.stateDir, options.out, options.jobRoot, filepath.Join(options.stateDir, "host.log")} {
		if path == "" {
			continue
		}
		if err := privateStatePath(ctx, path, roots); err != nil {
			return nil, err
		}
	}
	// Log/config/policy/credentials/registrations all belong to stateDir. A
	// separately selected jobs root remains admitted by its own existing owner.
	state, err := fileid.Pending(ctx, fileid.Self, options.stateDir)
	if err != nil {
		return nil, err
	}
	log, err := fileid.Pending(ctx, fileid.Self, options.out)
	if err != nil {
		return nil, err
	}
	if !identity.PathNameWithin(log.Path(), state.Path()) {
		return nil, fmt.Errorf("runtime: private log must be inside --state-dir")
	}
	if err := layout.Ensure(options.stateDir, options.stateDir); err != nil {
		return nil, err
	}
	return cas.LockContext(ctx, filepath.Join(options.stateDir, ".openabstractions-settings-lifecycle"), true)
}

func privateStatePath(ctx context.Context, path string, roots []string) error {
	return layout.CheckPrivateStatePath(ctx, path, roots)
}

// Explicit local storage operators use their selected state. Read-only
// operators do not call this writer guard and retain their no-creation rule.
func selectedStateGuard(ctx context.Context, state string) (func(), error) {
	if state == "" {
		return runtimeSettingsGuard(ctx, runtimeFlags{})
	}
	return selectedStateGuardWithRoots(ctx, state, settingsAccountRoots, resetGuard)
}

func selectedStateGuardWithRoots(ctx context.Context, state string, account func() (resetRoots, error), lock func(context.Context, resetRoots, bool) (func(), error)) (func(), error) {
	r, err := account()
	if err != nil {
		return nil, err
	}
	roots := resetOwnedRootNames(r)
	if err := privateStatePath(ctx, state, roots); errors.Is(err, errAccountStateOverlap) {
		return lock(ctx, r, true)
	} else if err != nil {
		return nil, err
	}
	if err := layout.Ensure(state, state); err != nil {
		return nil, err
	}
	return cas.LockContext(ctx, filepath.Join(state, ".openabstractions-settings-lifecycle"), true)
}

// Direct runRuntimeReady callers receive the same complete-selection and
// endpoint validation as CLI callers, before any guard creates state.
func validateRuntimeStateSelection(o runtimeFlags) error {
	if !(o.gatewayEphemeral && o.stateDir != "" && o.gateway == "127.0.0.1:0") {
		if err := gatewayAddress(o.gateway); err != nil {
			return err
		}
	}
	if o.withoutModels && o.modelEndpoint != "" {
		return fmt.Errorf("runtime: conflicting model selection")
	}
	if o.isolated != "" {
		copy := o
		if err := isolateRuntime(&copy, copy.defaultEndpoint); err != nil {
			return err
		}
	}
	if o.withoutJobs && (o.downloadBackend != "" || o.downloadNASRoot != "") {
		return fmt.Errorf("runtime: conflicting download selection")
	}
	if o.stateDir != "" && (!filepath.IsAbs(o.stateDir) || o.jobRoot != "") {
		return fmt.Errorf("runtime: --state-dir requires an absolute path and managed jobs")
	}
	if (o.jobRoot == "") != (o.jobOwner == "") || o.withoutJobs && (o.jobRoot != "" || o.jobEndpoint != "" || o.executeDownloads) {
		return fmt.Errorf("runtime: conflicting job selection")
	}
	for _, item := range endpointFlags(o) {
		if o.xpc {
			continue
		}
		if err := endpointForm(runtime.GOOS, item.flag, item.value); err != nil {
			return err
		}
	}
	if err := completeSelection(o); err != nil {
		return err
	}
	if o.stateDir != "" && (o.endpoint == "" || o.logEndpoint == "" || o.configEndpoint == "" || o.out == "") {
		return fmt.Errorf("runtime: private state requires explicit listeners and log output; %s", isolatedRuntimeUsage)
	}
	if o.stateDir != "" {
		for _, item := range []struct{ value, service string }{
			{o.endpoint, "runtime-v1"}, {o.logEndpoint, "logging-v1"}, {o.configEndpoint, "config-v1"}, {o.jobEndpoint, "job-acceptance-v1"}, {o.modelEndpoint, "model-v1"},
		} {
			if item.value == "" {
				continue
			}
			installed, err := o.installedEndpoint(item.service)
			if err != nil {
				return err
			}
			if identity.SamePathName(item.value, installed) {
				return fmt.Errorf("runtime: private state cannot use an installed listener")
			}
		}
	}
	if _, err := isolatedOperatorProgramWithState(o, false); err != nil {
		return err
	}
	return checkRuntimeTransport(o)
}
