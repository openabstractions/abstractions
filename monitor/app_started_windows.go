//go:build windows

package main

import (
	"errors"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActive is GetExitCodeProcess's code of a running process.
const stillActive = 259

// appProcessHandle opens p while it runs with its recorded creation time.
func appProcessHandle(p appStartedProcess, access uint32) (windows.Handle, bool) {
	if p.PID <= 0 {
		return 0, false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|access, false, uint32(p.PID))
	if err != nil {
		return 0, false
	}
	var code uint32
	var creation, exit, kernel, user windows.Filetime
	if windows.GetExitCodeProcess(h, &code) != nil || code != stillActive || windows.GetProcessTimes(h, &creation, &exit, &kernel, &user) != nil ||
		strconv.FormatUint(uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime), 10) != p.Created {
		windows.CloseHandle(h)
		return 0, false
	}
	return h, true
}

// appProcessParent is pid's parent process ID, which Windows keeps after the
// parent ended.
func appProcessParent(pid int) (int, bool) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if int(entry.ProcessID) == pid {
			return int(entry.ParentProcessID), entry.ParentProcessID != 0
		}
	}
	return 0, false
}

func appProcessRuns(p appStartedProcess) bool {
	h, ok := appProcessHandle(p, 0)
	if ok {
		windows.CloseHandle(h)
	}
	return ok
}

// appStopProcess stops p by PID once its creation time is proven.
func appStopProcess(p appStartedProcess) error {
	h, ok := appProcessHandle(p, windows.PROCESS_TERMINATE)
	if !ok {
		return errors.New("it is no longer the recorded process")
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
