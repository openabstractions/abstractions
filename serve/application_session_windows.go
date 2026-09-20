package main

import (
	"os"
	"strconv"

	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
)

func applicationSession(peer *identity.Peer) (string, bool) {
	process, err := peer.Process.AtLeast(identity.ProofPID)
	if err != nil || process.PID <= 0 || process.Recycled {
		return "", false
	}
	var caller, runtime uint32
	ok := windows.ProcessIdToSessionId(uint32(process.PID), &caller) == nil &&
		windows.ProcessIdToSessionId(uint32(os.Getpid()), &runtime) == nil && caller != 0 && caller == runtime
	return "windows:" + strconv.FormatUint(uint64(caller), 10), ok
}
