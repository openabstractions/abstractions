//go:build linux || darwin

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// The terminal supplies its normal editing and echo. Redirected input echoes none.
func outputInputEcho(*os.File) bool { return false }

func outputInputByte(ctx context.Context, file *os.File, maximum time.Duration) (byte, bool, error) {
	fd := file.Fd()
	if fd > 1<<31-1 {
		return 0, false, fmt.Errorf("standard input descriptor unavailable")
	}
	until := time.Now().Add(maximum)
	for {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		// Poll accepts descriptors beyond select's fixed FD_SETSIZE.
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 0)
		if err != nil && err != unix.EINTR {
			return 0, false, err
		}
		if n > 0 {
			if fds[0].Revents&unix.POLLNVAL != 0 {
				return 0, false, fmt.Errorf("standard input closed")
			}
			if fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
				var buffer [1]byte
				n, err := file.Read(buffer[:])
				return buffer[0], n != 0, err
			}
		}
		if !time.Now().Before(until) {
			return 0, false, nil
		}
		if err := outputInputPause(ctx, min(10*time.Millisecond, time.Until(until))); err != nil {
			return 0, false, err
		}
	}
}
