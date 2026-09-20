//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
)

func linuxAuditSession(path string) (uint64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
	return value, err == nil && value != 1<<32-1
}

func applicationSession(peer *identity.Peer) (string, bool) {
	process, err := peer.Process.AtLeast(identity.ProofPID)
	if err != nil || process.PID <= 0 || process.Recycled {
		return "", false
	}
	runtime, ok := linuxAuditSession("/proc/self/sessionid")
	caller, callerOK := linuxAuditSession("/proc/" + strconv.Itoa(process.PID) + "/sessionid")
	matched := ok && callerOK && caller == runtime
	return "linux:" + strconv.FormatUint(caller, 10), matched
}
