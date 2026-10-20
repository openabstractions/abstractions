//go:build !windows && !linux && !darwin

package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

func outputInputEcho(*os.File) bool { return false }

func outputInputByte(context.Context, *os.File, time.Duration) (byte, bool, error) {
	return 0, false, fmt.Errorf("terminal choice input is unsupported on this platform; use --wait-output-choice for an external answer")
}
