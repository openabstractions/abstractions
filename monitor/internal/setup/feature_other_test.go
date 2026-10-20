//go:build !windows

package setup

import (
	"runtime"
	"strings"
	"testing"
)

// Off Windows the ModelHost feature is not offered, and the reason the Panel
// shows names the platform's own cause: on a Mac, that native providers are
// not available there in 0.4.0.
func TestModelHostFeatureSaysWhyItIsUnavailable(t *testing.T) {
	status, err := ModelHostFeatureStatus()
	if err != nil || status.Available || status.Installed || status.Reason != modelHostFeatureReason(runtime.GOOS) {
		t.Fatalf("status %+v, %v", status, err)
	}
	if mac := modelHostFeatureReason("darwin"); !strings.Contains(mac, "not available on this Mac") || !strings.Contains(mac, "XPC endpoint") {
		t.Fatalf("macOS reason %q", mac)
	}
	if linux := modelHostFeatureReason("linux"); linux != "The OpenAbstractions ModelHost feature requires Windows x64" {
		t.Fatalf("Linux reason %q", linux)
	}
}
