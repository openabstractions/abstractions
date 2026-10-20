//go:build !windows && (!darwin || !cgo)

package main

import (
	"context"
	"errors"
)

func openMessageStartImage(context.Context, applicationMessageStartRequest) (ownedApplicationImage, error) {
	return ownedApplicationImage{}, errors.New("message start: native owned process unavailable")
}
