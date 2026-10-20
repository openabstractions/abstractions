package main

import (
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/claude-plugin/hooks"
)

// Setup reads the same projected Claude Mod as the cache producer; both keep
// 4 KiB of headroom under the payload bound and the marker at 32 KiB.
func TestSetupPayloadLimitHoldsProjectedClaudeModule(t *testing.T) {
	module, err := hooks.ProjectRegister(hooks.Register())
	if err != nil {
		t.Fatal(err)
	}
	if len(module)+4<<10 > claudePayloadLimit {
		t.Fatalf("projected register.js is %d bytes, within 4 KiB of the %d-byte payload bound", len(module), claudePayloadLimit)
	}
	if claudeMarkerLimit != 32<<10 || claudePayloadLimit != 128<<10 {
		t.Fatal("marker and payload bounds changed", claudeMarkerLimit, claudePayloadLimit)
	}
}
