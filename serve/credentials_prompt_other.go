//go:build !windows && !linux && !darwin

package main

import (
	"errors"
	"io"
)

func promptSecret(io.Writer) ([]byte, error) {
	return nil, errors.New("no echo-off prompt on this platform")
}
