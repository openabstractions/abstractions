package nativecli

import "strings"

// AgentReceiverName translates a person's label for an agent app (Claude,
// Codex or OpenCode, as the person or an assistant writes it) into that
// agent's connector base name, and returns any other label unchanged. The
// MCP gateway and the runtime's Messages directory both read it. It
// translates display labels only. Native enrollment, authorization and live
// inbox lookup remain the receiving service's authority.
func AgentReceiverName(label string) string {
	switch strings.TrimSpace(label) {
	case "Claude", "claude", "Claude Code", "claude code":
		return "oa-claude-integration"
	case "Codex", "codex":
		return "oa-codex-integration"
	case "OpenCode", "opencode":
		return "oa-opencode-integration"
	}
	return label
}
