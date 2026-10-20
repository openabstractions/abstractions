//go:build !windows && !darwin

package nativecli

import (
	"context"
	"errors"
)

type Image struct{}

func (*Image) Program() string { return "" }
func (*Image) FileKey() string { return "" }
func (*Image) Check(context.Context) error {
	return errors.New("native agent integration images are unavailable on this platform")
}
func (*Image) Close() error { return nil }
