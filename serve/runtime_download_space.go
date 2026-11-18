package main

import (
	"fmt"
	"log"

	download "github.com/openabstractions/abstraction-download/go"
)

const (
	// downloadFloorCap is the most free space a managed download leaves on
	// its volume; a smaller volume keeps 2% of its size [DL-R45].
	downloadFloorCap = 1 << 30
	// downloadComfortCap is the most free space below which a download writes
	// a nearly-full warning; a smaller volume warns below a tenth of its size.
	downloadComfortCap = 20 << 30
	// smallDownload is the size below which a download never warns.
	smallDownload = 64 << 20
)

// localDownloadDiskSpace reads the free and total bytes of a folder's
// volume; tests replace it.
var localDownloadDiskSpace = readLocalDownloadDiskSpace

// The local download floor is independent of the NAS archive's reserve.
func checkLocalDownloadSpace(folder string, additional int64) error {
	return checkDownloadReserve(folder, additional, localDownloadDiskSpace)
}

// checkDownloadReserve refuses additional bytes only when writing them would
// leave the volume with less than its floor, the smaller of 1 GiB and 2% of
// the volume. Below the larger comfort reserve a download of 64 MiB or more
// writes one log line and goes ahead. A volume whose space cannot be read
// takes the download.
func checkDownloadReserve(folder string, additional int64, read func(string) (free, total uint64, err error)) error {
	free, total, err := read(folder)
	if err != nil || additional <= 0 {
		return nil
	}
	floor := min(total/50, downloadFloorCap)
	if free < floor || uint64(additional) > free-floor {
		return download.Terminal(fmt.Errorf("%w: this download needs %s and the disk has %s free; OpenAbstractions keeps %s free",
			download.ErrDiskFloor, humanBytes(additional), humanBytes(int64(free)), humanBytes(int64(floor))))
	}
	if comfort := min(total/10, downloadComfortCap); additional >= smallDownload && free-uint64(additional) < comfort {
		log.Printf("download: your disk is nearly full: a %s download leaves %s free", humanBytes(additional), humanBytes(int64(free-uint64(additional))))
	}
	return nil
}
