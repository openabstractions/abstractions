//go:build !windows

package setup

import "errors"

func modelDiskFree(string) (uint64, error) {
	return 0, errors.New("model disk preflight is unavailable on this platform")
}
