//go:build !windows

package main

import (
	"errors"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func claimSetupStep() (release func(), acquired bool, err error) {
	return claimNamed("")
}

func claimNamed(string) (release func(), acquired bool, err error) {
	return func() {}, true, nil
}

// runSetupStepNative: slice 1 of the setup step is windows-x64; macOS asks
// through its native launcher in a later slice.
func runSetupStepNative(requester *setup.StepRequester, finish bool, update ...bool) (open bool, err error) {
	return finish, errors.New("the setup step requires Windows in this release")
}
