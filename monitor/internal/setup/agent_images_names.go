//go:build windows || darwin

package setup

import (
	"errors"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

func agentApplicationName(party AgentParty, name string) (string, error) {
	base := nativecli.AgentApplicationForParty(string(party))
	application := nativecli.AgentApplicationName(name)
	if base != "" && nativecli.AgentApplicationBase(application) == base {
		return application, nil
	}
	return "", errors.New("selected helper name is not an owned integration version")
}
