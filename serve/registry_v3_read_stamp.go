package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// registrationReadSettle is how long every file in the registrations
// directory must have kept its write time before a read-only transaction
// trusts the directory listing. A write inside the window can leave size and
// write time unchanged on a file system with coarse timestamps.
const registrationReadSettle = 5 * time.Second

// refreshLocked is readLocked for a read-only registration transaction. Every
// registration writer replaces files in this directory under the descriptors
// CAS the caller holds, and a replaced file carries a new write time. When the
// directory lists the same names, sizes and settled write times as at the last
// read-only read, and no write transaction or failure came between (both clear
// readStamp), the decoded state in memory is that directory's state. The read
// is then skipped, and the caller reuses the role projection made from that
// state (readRoles). Message progress and every client call that checks an
// agent's enrollment take this path.
func (s *applicationV3Registrations) refreshLocked(ctx context.Context) (reused bool, err error) {
	stamp, settled := s.listingStamp(time.Now())
	if settled && s.readStamp != "" && stamp == s.readStamp {
		return true, nil
	}
	if err := s.readLocked(ctx); err != nil {
		return false, err
	}
	if settled {
		// Taken before the read: a file readLocked itself rewrote or removed
		// changes the next listing, which then reads again.
		s.readStamp = stamp
	}
	return false, nil
}

// listingStamp names every entry of the registrations directory with its size
// and write time. settled is false when the listing cannot be read in full or
// a write time is inside registrationReadSettle of now.
func (s *applicationV3Registrations) listingStamp(now time.Time) (string, bool) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < registrationReadSettle {
			return "", false
		}
		b.WriteString(e.Name())                                         //unchecked: strings.Builder writes never return an error
		b.WriteByte(0)                                                  //unchecked: strings.Builder writes never return an error
		b.WriteString(strconv.FormatInt(info.Size(), 10))               //unchecked: strings.Builder writes never return an error
		b.WriteByte(0)                                                  //unchecked: strings.Builder writes never return an error
		b.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10)) //unchecked: strings.Builder writes never return an error
		b.WriteByte(0)                                                  //unchecked: strings.Builder writes never return an error
		b.WriteString(strconv.FormatUint(uint64(info.Mode()), 10))      //unchecked: strings.Builder writes never return an error
		b.WriteByte('\n')                                               //unchecked: strings.Builder writes never return an error
	}
	return b.String(), true
}
