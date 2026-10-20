package main

import (
	"errors"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestOwnedApplicationTokenRefusesUnsafeOrUnknownPlacement(t *testing.T) {
	for _, name := range []string{"ordinary", "elevated", "virtualized", "container", "impersonation", "unknown", "short"} {
		t.Run(name, func(t *testing.T) {
			inspect := func(_ windows.Token, class uint32, value *byte, _ uint32, size *uint32) error {
				if name == "unknown" {
					return errors.New("inspection unavailable")
				}
				*size = 4
				if class == windows.TokenType && name != "impersonation" {
					*(*uint32)(unsafe.Pointer(value)) = 1
				}
				if name == "short" {
					*size = 2
				}
				if (name == "elevated" && class == windows.TokenElevation) || (name == "virtualized" && class == windows.TokenVirtualizationEnabled) || (name == "container" && class == ownedApplicationTokenIsAppContainer) {
					*(*uint32)(unsafe.Pointer(value)) = 1
				}
				return nil
			}
			err := ownedApplicationTokenFlags(0, inspect)
			if (err == nil) != (name == "ordinary") {
				t.Fatalf("placement %s: %v", name, err)
			}
		})
	}
}
