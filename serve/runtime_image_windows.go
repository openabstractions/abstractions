package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// ownRuntimeImageWatch watches this process's image. os.Executable keeps the
// path the loader started it from; QueryFullProcessImageName names the image
// file where it is now, which is the name a client reads for this server.
func ownRuntimeImageWatch() (runtimeImageWatch, bool) {
	launched, err := os.Executable()
	if err != nil || launched == "" {
		return runtimeImageWatch{}, false
	}
	return runtimeImageWatch{launched: launched, current: currentImageName}, true
}

func currentImageName() (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(windows.CurrentProcess(), 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}
