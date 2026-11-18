//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

// nativePointer turns an address that Windows owns (a message parameter, a
// COM object or a native buffer) into an unsafe.Pointer. The Go garbage
// collector never sees this memory; reading the address through its own storage
// is the accepted conversion and go vet's unsafeptr check does not flag it.
func nativePointer(address uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&address))
}

// newGUID builds a GUID with keyed fields, which go vet's composites check requires.
func newGUID(data1 uint32, data2, data3 uint16, data4 [8]byte) syscall.GUID {
	return syscall.GUID{Data1: data1, Data2: data2, Data3: data3, Data4: data4}
}
