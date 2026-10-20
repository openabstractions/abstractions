package main

import (
	"time"

	"golang.org/x/sys/unix"
)

// darwinZombie is p_stat's SZOMB (sys/proc.h): exited, not yet reaped.
const darwinZombie = 5

// agentDiagnosticBirth is this process's kernel p_starttime, the same
// wall-clock creation time the Windows build reads from GetProcessTimes.
func agentDiagnosticBirth() string {
	birth, _ := darwinProcessBirth(unix.Getpid())
	return birth
}

// darwinProcessBirth answers kern.proc.pid for pid: its birth and whether it
// still runs. An empty answer names no process. A failed read reports "", true
// so the caller proves nothing.
func darwinProcessBirth(pid int) (birth string, running bool) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", true
	}
	if len(procs) == 0 {
		return "", false
	}
	p := procs[0].Proc
	if p.P_pid != int32(pid) {
		return "", true
	}
	tv := p.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000).UTC().Format(time.RFC3339Nano), p.P_stat != darwinZombie
}

// agentProcessGone reports a process that no longer runs: its PID names no
// process, a process born at another time, or one that has exited. A failed
// read proves nothing and reports false.
func agentProcessGone(pid int, birth string) bool {
	if pid <= 0 || pid > 1<<31-1 {
		return false
	}
	current, running := darwinProcessBirth(pid)
	if !running {
		return true
	}
	return current != "" && current != birth
}
