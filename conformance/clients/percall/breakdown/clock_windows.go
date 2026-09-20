package main

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	performanceCount = kernel32.NewProc("QueryPerformanceCounter")
	performanceFreq  = kernel32.NewProc("QueryPerformanceFrequency")
)

// stamp reads QueryPerformanceCounter. time.Now's monotonic reading on Windows
// follows the interrupt clock, which is coarser than one call.
func stamp() time.Duration {
	var counter, frequency int64
	performanceCount.Call(uintptr(unsafe.Pointer(&counter)))
	performanceFreq.Call(uintptr(unsafe.Pointer(&frequency)))
	return time.Duration(float64(counter) / float64(frequency) * float64(time.Second))
}
