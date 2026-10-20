//go:build !windows

package setup

import (
	"context"
	"errors"
)

func readMaintenanceRegistrations(context.Context) ([]maintenanceRegistration, error) {
	return nil, errors.New("installed Setup maintenance requires Windows")
}

func openMaintenanceImage(context.Context, maintenanceRegistration) (maintenanceImage, error) {
	return maintenanceImage{}, errors.New("installed Setup maintenance requires Windows")
}
