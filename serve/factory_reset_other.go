//go:build !windows

package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
)

func factoryResetRoots() (resetRoots, error) {
	return resetRoots{}, errors.New("factory-reset: Windows settings reset is supported here")
}

func factoryResetAccountRoots() (resetRoots, error) { return factoryResetRoots() }
func factoryResetOffline() error                    { return errors.New("factory-reset: Windows required") }
func runtimeSettingsGuard(ctx context.Context, options runtimeFlags) (func(), error) {
	return runtimeStateGuardWithDefaults(ctx, options, func() (resetRoots, error) { return resetRoots{}, nil }, func(context.Context, resetRoots, bool) (func(), error) { return func() {}, nil }, func() ([]string, error) {
		state, err := defaultRuntimeStateDir()
		if err != nil {
			return nil, err
		}
		log, err := runtimeLogPath("")
		if err != nil {
			return nil, err
		}
		return []string{state, filepath.Dir(log)}, nil
	})
}

func settingsAccountRoots() (resetRoots, error) { return resetRoots{}, nil }

func factoryResetProfileGuard(context.Context, resetRoots) (func(), error) { return func() {}, nil }
func factoryResetSecretPlan() ([]string, error)                            { return nil, errors.New("Windows required") }
func factoryResetSecrets([]string, io.Writer) error                        { return errors.New("Windows required") }
