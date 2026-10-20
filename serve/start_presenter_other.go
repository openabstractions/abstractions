//go:build !windows

package main

import (
	"context"
	"io"
)

// Other installed platforms own presenter activation through their user units.
func activateInstalledPresenter(context.Context, io.Writer) error { return nil }
