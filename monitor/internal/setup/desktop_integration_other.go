//go:build !windows

package setup

import (
	"context"
	"errors"
)

func (DesktopIntegration) packageState() (string, bool, error) {
	return "", false, errors.New("This integration requires Windows x64.")
}

func (DesktopIntegration) runPackage(context.Context, string, string) error {
	return errors.New("This integration requires Windows x64.")
}

func (DesktopIntegration) runPackageWithAdmission(context.Context, string, string, func(context.Context) error) error {
	return errors.New("This integration requires Windows x64.")
}

func (DesktopIntegration) Running() (bool, error) {
	return false, errors.New("This integration requires Windows x64.")
}

func (DesktopIntegration) publishStaged(context.Context, string, string) error {
	return errors.New("This integration requires Windows x64.")
}
