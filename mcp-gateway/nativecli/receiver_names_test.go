package nativecli

import "testing"

func TestAgentReceiverName(t *testing.T) {
	for label, want := range map[string]string{
		"Claude": "oa-claude-integration", "claude": "oa-claude-integration", "Claude Code": "oa-claude-integration", " claude code ": "oa-claude-integration",
		"Codex": "oa-codex-integration", "codex": "oa-codex-integration",
		"OpenCode": "oa-opencode-integration", "opencode": "oa-opencode-integration",
		"oa-opencode-integration": "oa-opencode-integration", "native-agent": "native-agent", "CLAUDE": "CLAUDE", "": "",
	} {
		if got := AgentReceiverName(label); got != want {
			t.Errorf("AgentReceiverName(%q) = %q, want %q", label, got, want)
		}
	}
}
