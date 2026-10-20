//go:build !windows

package main

func configureRuntimeAppSandbox(options runtimeFlags) (func() error, error) {
	return func() error { return nil }, nil
}
