//go:build windows

package main

import (
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The runtime records who opened an app it started in its own log, with the
// host's PID and creation time in the FILETIME form an integration records,
// so the Panel can join the two (ruling 2026-11-12 point 3). The host's
// binding is built the way startOwnedApplication builds it.
func TestTheRuntimeRecordsWhoOpenedAnAppItStarted(t *testing.T) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	var created, exited, kernel, user windows.Filetime
	err = windows.GetProcessTimes(h, &created, &exited, &kernel, &user)
	windows.CloseHandle(h)
	if err != nil {
		t.Fatal(err)
	}
	binding := ownedApplicationBinding{application: "comfyui", pid: os.Getpid(), started: time.Unix(0, created.Nanoseconds())}
	logs := &elevatedLogs{}
	g := &applicationV3Gateway{policy: &runtimeRights{}}
	g.policy.useLogs(logs)
	if !g.recordOwnedStart("comfyui", `C:\Users\person\AppData\Local\opencode\opencode.exe`, binding) ||
		logs.events(ownedStartEvent) != 1 {
		t.Fatalf("owned start lines: %+v", logs.records)
	}
	attrs := logs.records[0].Attrs
	want := strconv.FormatUint(uint64(created.HighDateTime)<<32|uint64(created.LowDateTime), 10)
	if attrs["application"] != "comfyui" || attrs["opener"] != `C:\Users\person\AppData\Local\opencode\opencode.exe` ||
		attrs["host_pid"] != strconv.Itoa(os.Getpid()) || attrs["host_created"] != want {
		t.Fatalf("owned start line %+v, want host_created %s", attrs, want)
	}
	// A host whose process facts are unknown leaves no line.
	if g.recordOwnedStart("comfyui", "", ownedApplicationBinding{}) || logs.events(ownedStartEvent) != 1 {
		t.Fatal("a start without process facts was recorded")
	}
}
