//go:build !windows && (!darwin || !cgo)

package main

import (
	"context"
	"errors"
)

func startOwnedApplication(_ context.Context, image ownedApplicationImage, _ ownedApplicationBinding, _ []string, _ func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, error) {
	if image.close != nil {
		_ = image.close()
	}
	return nil, errors.New("application host: native owned launch is unsupported on this platform")
}
