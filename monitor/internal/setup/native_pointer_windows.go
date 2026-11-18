//go:build windows

package setup

import "unsafe"

// nativePointer turns an address that Windows owns (a message parameter, a
// COM object or a native buffer) into an unsafe.Pointer. The Go garbage
// collector never sees this memory; reading the address through its own storage
// is the accepted conversion and go vet's unsafeptr check does not flag it.
func nativePointer(address uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&address))
}
