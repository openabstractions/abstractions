package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmQueryEndSession      = 0x0011
	wmEndSession           = 0x0016
	wmClose                = 0x0010
	wmDestroy              = 0x0002
	wmDisposeSessionWindow = 0x8001
)

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	getModuleHandle           = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	registerWindowClass       = user32.NewProc("RegisterClassExW")
	unregisterWindowClass     = user32.NewProc("UnregisterClassW")
	createWindow              = user32.NewProc("CreateWindowExW")
	defaultWindowProc         = user32.NewProc("DefWindowProcW")
	destroyWindow             = user32.NewProc("DestroyWindow")
	getMessage                = user32.NewProc("GetMessageW")
	dispatchMessage           = user32.NewProc("DispatchMessageW")
	postMessage               = user32.NewProc("PostMessageW")
	postQuitMessage           = user32.NewProc("PostQuitMessage")
	windowSequence            atomic.Uint64
	errSessionShutdownTimeout = errors.New("session shutdown exceeded its waiting budget")
)

type windowClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}

type windowMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

type sessionWindow struct {
	handle   uintptr
	done     chan struct{}
	loopErr  error // Published by closing done.
	timedOut atomic.Bool
}

// A hidden top-level window participates in native session/Restart Manager
// shutdown. A message-only window would not receive these notifications.
func startSessionWindow(cancel context.CancelFunc, completed <-chan struct{}, budget time.Duration) (*sessionWindow, error) {
	return startSessionWindowWithMessages(cancel, completed, budget, func(message *windowMessage) (int32, error) {
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(message)), 0, 0, 0)
		return int32(result), err
	})
}

func startSessionWindowWithMessages(cancel context.CancelFunc, completed <-chan struct{}, budget time.Duration,
	nextMessage func(*windowMessage) (int32, error)) (*sessionWindow, error) {
	w := &sessionWindow{done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer func() { cancel(); close(w.done) }()
		name, _ := windows.UTF16PtrFromString(fmt.Sprintf("OpenAbstractionsHost-%d-%d", os.Getpid(), windowSequence.Add(1)))
		instance, _, err := getModuleHandle.Call(0)
		if instance == 0 {
			ready <- err
			return
		}
		stop := func() {
			cancel()
			timer := time.NewTimer(budget)
			defer timer.Stop()
			select {
			case <-completed:
			case <-timer.C:
				w.timedOut.Store(true)
			}
		}
		callback := syscall.NewCallback(func(hwnd uintptr, message uint32, wp, lp uintptr) uintptr {
			switch message {
			case wmQueryEndSession:
				return 1
			case wmEndSession:
				if wp != 0 {
					stop()
				}
				return 0
			case wmClose:
				stop()
				return 0
			case wmDisposeSessionWindow:
				destroyWindow.Call(hwnd)
				return 0
			case wmDestroy:
				postQuitMessage.Call(0)
				return 0
			}
			result, _, _ := defaultWindowProc.Call(hwnd, uintptr(message), wp, lp)
			return result
		})
		class := windowClass{Proc: callback, Instance: uintptr(instance), Name: name}
		class.Size = uint32(unsafe.Sizeof(class))
		if result, _, err := registerWindowClass.Call(uintptr(unsafe.Pointer(&class))); result == 0 {
			ready <- fmt.Errorf("register session window: %w", err)
			return
		}
		defer unregisterWindowClass.Call(uintptr(unsafe.Pointer(name)), uintptr(instance))
		hwnd, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, 0, uintptr(instance), 0)
		if hwnd == 0 {
			ready <- fmt.Errorf("create session window: %w", err)
			return
		}
		w.handle = hwnd
		defer destroyWindow.Call(hwnd)
		ready <- nil
		var message windowMessage
		for {
			result, err := nextMessage(&message)
			if result == -1 {
				w.loopErr = fmt.Errorf("session message loop: %w", err)
				return
			}
			if result == 0 {
				return
			}
			dispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
		}
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return w, nil
}

func (w *sessionWindow) Close() error {
	resultError := func() error {
		if w.timedOut.Load() {
			return errors.Join(w.loopErr, errSessionShutdownTimeout)
		}
		return w.loopErr
	}
	select {
	case <-w.done:
		return resultError()
	default:
	}
	var postErr error
	if result, _, err := postMessage.Call(w.handle, wmDisposeSessionWindow, 0, 0); result == 0 {
		postErr = fmt.Errorf("close session window: %w", err)
	}
	select {
	case <-w.done:
		return resultError()
	case <-time.After(time.Second):
		return errors.Join(postErr, errors.New("session window close was not observed"))
	}
}

func withSessionShutdown(ctx context.Context, run func(context.Context) error) error {
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := make(chan struct{})
	window, err := startSessionWindow(cancel, completed, 20*time.Second)
	if err != nil {
		return err
	}
	err = run(child)
	close(completed)
	return errors.Join(err, window.Close())
}
