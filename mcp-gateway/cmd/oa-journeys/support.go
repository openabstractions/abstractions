//go:build windows

package main

import (
	"context"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ntdll          = windows.NewLazySystemDLL("ntdll.dll")
	suspendProcess = ntdll.NewProc("NtSuspendProcess")
	resumeProcess  = ntdll.NewProc("NtResumeProcess")
)

// freezeRuntime stops the isolated runtime this run started from answering
// for d, then lets it continue: every call in flight waits, as on a machine
// too busy to answer within a call's budget.
func (e *env) freezeRuntime(d time.Duration) error {
	if e.runtimeCmd == nil || e.runtimeCmd.Process == nil {
		return fmt.Errorf("no runtime process")
	}
	h, err := windows.OpenProcess(0x0800 /* PROCESS_SUSPEND_RESUME */, false, uint32(e.runtimeCmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if status, _, _ := suspendProcess.Call(uintptr(h)); status != 0 {
		return fmt.Errorf("NtSuspendProcess 0x%x", status)
	}
	e.logf("runtime frozen for %s", d)
	time.Sleep(d)
	if status, _, _ := resumeProcess.Call(uintptr(h)); status != 0 {
		return fmt.Errorf("NtResumeProcess 0x%x", status)
	}
	e.logf("runtime resumed")
	return nil
}

// answeredSince counts the questions answered after mark.
func (a *answerer) answeredSince(mark int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, q := range a.log[mark:] {
		if q.Answer != "" {
			n++
		}
	}
	return n
}

// waitAnswered waits until the person has answered a question asked after mark.
func (a *answerer) waitAnswered(ctx context.Context, mark int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if a.answeredSince(mark) > 0 {
			// The runtime records the rule before the agent's next call.
			time.Sleep(300 * time.Millisecond)
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// waitQuiet waits until every question asked so far has been answered.
func (a *answerer) waitQuiet(ctx context.Context, limit time.Duration) {
	deadline := time.Now().Add(limit)
	quiet := 0
	for time.Now().Before(deadline) && ctx.Err() == nil && quiet < 3 {
		a.mu.Lock()
		open := 0
		for _, q := range a.log {
			if q.Answer == "" {
				open++
			}
		}
		a.mu.Unlock()
		if open == 0 {
			quiet++
		} else {
			quiet = 0
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// waitIDs waits until the person has answered each named question; with no
// names it waits until every listed question is answered.
func (a *answerer) waitIDs(ctx context.Context, ids []string, limit time.Duration) {
	if len(ids) == 0 {
		a.waitQuiet(ctx, limit)
		return
	}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		a.mu.Lock()
		done := 0
		for _, id := range ids {
			for _, q := range a.log {
				if q.ID == id && q.Answer != "" {
					done++
					break
				}
			}
		}
		a.mu.Unlock()
		if done == len(ids) {
			time.Sleep(300 * time.Millisecond)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var (
	jobHandle windows.Handle
	jobOnce   sync.Once
)

// own puts a started child in a job that ends with this driver, so a stopped
// run leaves no runtime or helper behind. Breakaway stays allowed, as for an
// unsupervised development runtime.
func own(pid int) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			return
		}
		jobHandle = h
	})
	if jobHandle == 0 {
		return
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(p)
	_ = windows.AssignProcessToJobObject(jobHandle, p)
}

// killDescendants stops every process descended from pid, deepest first, by
// process ID.
func killDescendants(pid int, logf func(string, ...any)) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	children := map[int][]int{}
	names := map[int]string{}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		children[int(entry.ParentProcessID)] = append(children[int(entry.ParentProcessID)], int(entry.ProcessID))
		names[int(entry.ProcessID)] = windows.UTF16ToString(entry.ExeFile[:])
	}
	windows.CloseHandle(snap)
	var order []int
	var walk func(int, int)
	walk = func(p, depth int) {
		if depth > 16 {
			return
		}
		for _, c := range children[p] {
			if c != p && c != 0 {
				walk(c, depth+1)
				order = append(order, c)
			}
		}
	}
	walk(pid, 0)
	for _, c := range order {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(c))
		if err != nil {
			continue
		}
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
		if logf != nil {
			logf("stopped %s pid %d (started by the runtime)", names[c], c)
		}
	}
}
