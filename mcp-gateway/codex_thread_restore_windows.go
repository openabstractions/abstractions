package main

import (
	"errors"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Restart Manager reports which processes hold a file open without this
// process opening or locking it, so Codex's own lock is never disturbed.
var (
	restartManager        = windows.NewLazySystemDLL("rstrtmgr.dll")
	rmStartSession        = restartManager.NewProc("RmStartSession")
	rmEndSession          = restartManager.NewProc("RmEndSession")
	rmRegisterResources   = restartManager.NewProc("RmRegisterResources")
	rmGetList             = restartManager.NewProc("RmGetList")
	errRestartManagerFail = errors.New("Restart Manager unavailable")
)

// restartManagerReady resolves every procedure once, so a missing one is an
// error here instead of a panic at its first call.
var restartManagerReady = sync.OnceValue(func() error {
	for _, proc := range []*windows.LazyProc{rmStartSession, rmEndSession, rmRegisterResources, rmGetList} {
		if err := proc.Find(); err != nil {
			return errors.Join(errRestartManagerFail, err)
		}
	}
	return nil
})

type rmUniqueProcess struct {
	PID   uint32
	Start windows.Filetime
}

type rmProcessInfo struct {
	Process     rmUniqueProcess
	AppName     [256]uint16
	ServiceName [64]uint16
	AppType     uint32
	AppStatus   uint32
	Session     uint32
	Restartable int32
}

const rmMaxHolders = 64

// codexLockHolders lists the processes holding one file open.
func codexLockHolders(path string) ([]rmUniqueProcess, error) {
	if err := restartManagerReady(); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var session uint32
	var key [33]uint16
	if r, _, _ := rmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return nil, errRestartManagerFail
	}
	defer rmEndSession.Call(uintptr(session))
	files := []*uint16{name}
	if r, _, _ := rmRegisterResources.Call(uintptr(session), 1, uintptr(unsafe.Pointer(&files[0])), 0, 0, 0, 0); r != 0 {
		return nil, errRestartManagerFail
	}
	var needed, reasons uint32
	count := uint32(rmMaxHolders)
	info := make([]rmProcessInfo, rmMaxHolders)
	if r, _, _ := rmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&reasons))); r != 0 {
		return nil, errRestartManagerFail
	}
	if count > rmMaxHolders {
		return nil, errRestartManagerFail
	}
	holders := make([]rmUniqueProcess, count)
	for i := range holders {
		holders[i] = info[i].Process
	}
	return holders, nil
}

func processCreated(handle windows.Handle) (windows.Filetime, error) {
	var created, exited, kernel, user windows.Filetime
	err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user)
	return created, err
}

func filetimeTime(t windows.Filetime) time.Time { return time.Unix(0, t.Nanoseconds()) }

// codexParent identifies this process's parent by PID and creation time. The
// PID names the parent only while that process started before this one and
// within the restore window; otherwise the parent is not the app-server that
// resumed this helper's thread just now, or the PID was reused.
func codexParent() (rmUniqueProcess, error) {
	self, err := processCreated(windows.CurrentProcess())
	if err != nil {
		return rmUniqueProcess{}, err
	}
	pid := os.Getppid()
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return rmUniqueProcess{}, errors.New("parent process unavailable")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return rmUniqueProcess{}, err
	}
	defer windows.CloseHandle(handle)
	created, err := processCreated(handle)
	if err != nil {
		return rmUniqueProcess{}, err
	}
	if !codexParentEligible(filetimeTime(created), filetimeTime(self)) {
		return rmUniqueProcess{}, errors.New("parent process outside the restore window")
	}
	return rmUniqueProcess{PID: uint32(pid), Start: created}, nil
}

func codexParentLoadedThreads(home string) ([]string, error) {
	parent, err := codexParent()
	if err != nil {
		return nil, err
	}
	paths, err := codexThreadLockPaths(home)
	if err != nil {
		return nil, err
	}
	var threads []string
	for _, path := range paths {
		holders, err := codexLockHolders(path)
		if err != nil {
			return nil, err
		}
		for _, holder := range holders {
			if holder == parent {
				threads = append(threads, codexLockThread(path))
				break
			}
		}
	}
	return threads, nil
}
