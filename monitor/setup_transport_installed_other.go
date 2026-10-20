//go:build !windows

package main

import (
	"errors"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func installedSetupTransport(setup.QualificationInput) (*setup.PublicTransfer, error) {
	return nil, errors.New("installed Setup qualification requires Windows x64")
}
