//go:build !windows

package main

import "errors"

func localDownloadDiskSpace(string) (uint64, uint64, error) {
	return 0, 0, errors.ErrUnsupported
}
