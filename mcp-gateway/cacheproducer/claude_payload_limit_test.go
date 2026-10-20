package cacheproducer

import (
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/claude-plugin/hooks"
)

// The projected Claude Mod keeps 4 KiB of headroom under the payload bound,
// so a hook change that nears it fails here before Setup refuses to install.
func TestProjectedClaudeModuleFitsPayloadLimit(t *testing.T) {
	module, err := hooks.ProjectRegister(claudePluginModule)
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
