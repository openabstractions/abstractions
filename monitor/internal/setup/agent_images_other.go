//go:build !windows && !darwin

package setup

import (
	"context"
	"errors"
)

type AgentParty string

const (
	AgentCodex    AgentParty = "codex"
	AgentClaude   AgentParty = "claude"
	AgentOpenCode AgentParty = "opencode"
)

type AgentImage struct{}

func (*AgentImage) Program() string { return "" }
func (*AgentImage) FileKey() string { return "" }
func (*AgentImage) Check(context.Context) error {
	return errors.New("native agent integration images are unavailable on this platform")
}
func (*AgentImage) Close() error { return nil }
func (*AgentImage) InstallerReceipt(context.Context) (string, error) {
	return "", errors.New("native agent integration images are unavailable on this platform")
}
func AgentGatewayProgram(context.Context) (*AgentImage, error) {
	return nil, errors.New("native agent integration images are unavailable on this platform")
}
func PrepareAgentImage(context.Context, AgentParty) (*AgentImage, error) {
	return nil, errors.New("native agent integration images are unavailable on this platform")
}

func PrepareInstallerAgentImage(context.Context, AgentParty) (*AgentImage, error) {
	return nil, errors.New("native agent integration images are unavailable on this platform")
}
