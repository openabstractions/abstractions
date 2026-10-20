//go:build !windows

package main

import (
	"context"
	"errors"
)

func installedApplicationAgentSource(context.Context) (agentUpdateImage, error) {
	return agentUpdateImage{}, errors.New("agent maintenance: native installed source owner unsupported")
}
