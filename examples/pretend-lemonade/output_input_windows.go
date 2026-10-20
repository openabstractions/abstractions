//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	consoleInput = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")
	peekPipe     = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")
)

func outputInputEcho(file *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(file.Fd()), &mode) == nil
}

// Read only ready input. Go's blocking console Read cannot be stopped by Close;
// consuming one ready input event avoids a reader goroutine and console-mode edits.
func outputInputByte(ctx context.Context, file *os.File, maximum time.Duration) (byte, bool, error) {
	handle := windows.Handle(file.Fd())
	var mode uint32
	console := windows.GetConsoleMode(handle, &mode) == nil
	kind, err := windows.GetFileType(handle)
	if err != nil {
		return 0, false, err
	}
	if !console && kind != windows.FILE_TYPE_PIPE && kind != windows.FILE_TYPE_DISK {
		return 0, false, fmt.Errorf("unsupported standard input; use --wait-output-choice for an external answer")
	}
	until := time.Now().Add(maximum)
	for {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		if console {
			var available uint32
			if err := windows.GetNumberOfConsoleInputEvents(handle, &available); err != nil {
				return 0, false, err
			}
			if available != 0 {
				// The KEY_EVENT_RECORD arm of the native 20-byte INPUT_RECORD.
				var event struct {
					kind, padding                  uint16
					keyDown                        int32
					repeat, virtualKey, scan, char uint16
					control                        uint32
				}
				var read uint32
				ok, _, callErr := consoleInput.Call(uintptr(handle), uintptr(unsafe.Pointer(&event)), 1, uintptr(unsafe.Pointer(&read)))
				if ok == 0 {
					return 0, false, callErr
				}
				if read != 0 && event.kind == 1 && event.keyDown != 0 {
					char := event.char
					if char == 26 { // Ctrl+Z closes app input without selecting.
						return 0, false, io.EOF
					}
					if char != 0 {
						if char > 127 {
							return 0, false, fmt.Errorf("enter Replace, Keep or Cancel using ASCII letters")
						}
						return byte(char), true, nil
					}
				}
			}
		} else {
			ready := kind == windows.FILE_TYPE_DISK
			if kind == windows.FILE_TYPE_PIPE {
				var available uint32
				ok, _, callErr := peekPipe.Call(uintptr(handle), 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0)
				if ok == 0 {
					if callErr == windows.ERROR_BROKEN_PIPE || callErr == windows.ERROR_PIPE_NOT_CONNECTED {
						return 0, false, io.EOF
					}
					return 0, false, callErr
				}
				ready = available != 0
			}
			if ready {
				var buffer [1]byte
				n, err := file.Read(buffer[:])
				return buffer[0], n != 0, err
			}
		}
		if !time.Now().Before(until) {
			return 0, false, nil
		}
		if err := outputInputPause(ctx, min(10*time.Millisecond, time.Until(until))); err != nil {
			return 0, false, err
		}
	}
}
