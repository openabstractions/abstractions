package main

import (
	"fmt"

	download "github.com/openabstractions/abstraction-download/go"
)

// The local download reserve is independent of the NAS archive's reserve.
func checkLocalDownloadSpace(folder string, additional int64) error {
	return checkDownloadReserve(folder, additional, localDownloadDiskSpace)
}

func checkDownloadReserve(folder string, additional int64, read func(string) (free, total uint64, err error)) error {
	free, total, err := read(folder)
	if err != nil || additional <= 0 {
		return nil
	}
	reserve := min(total/10, 20<<30)
	if free < reserve || uint64(additional) > free-reserve {
		return download.Terminal(fmt.Errorf("%w: download needs %d more bytes and would leave less than %d bytes free", download.ErrRefused, additional, reserve))
	}
	return nil
}
