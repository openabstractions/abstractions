//go:build !windows && (!darwin || !cgo)

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestApplicationManifestNativeReaderIsExplicitlyUnsupported(t *testing.T) {
	h, err := openSignedApplicationManifest(context.Background(), fileid.Self, "/unopened")
	if h != nil || !errors.Is(err, errApplicationManifestUnsupported) {
		t.Fatalf("%v %v", h, err)
	}
}
