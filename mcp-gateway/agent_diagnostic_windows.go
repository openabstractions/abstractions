package main

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

func agentDiagnosticBirth() string {
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user) != nil {
		return ""
	}
	return time.Unix(0, created.Nanoseconds()).UTC().Format(time.RFC3339Nano)
}

// agentProcessGone reports a process that no longer runs: its PID names no
// process, a process born at another time, or one that has exited. Any other
// failure, access denied included, proves nothing and reports false.
func agentProcessGone(pid int, birth string) bool {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(handle, &created, &exited, &kernel, &user) != nil {
		return false
	}
	if time.Unix(0, created.Nanoseconds()).UTC().Format(time.RFC3339Nano) != birth {
		return true
	}
	var code uint32
	if windows.GetExitCodeProcess(handle, &code) != nil {
		return false
	}
	const stillActive = 259
	return code != stillActive
}
