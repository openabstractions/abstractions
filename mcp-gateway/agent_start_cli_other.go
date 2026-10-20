//go:build !windows

package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func selectAgentStartCLI(context.Context, startprofile.Profile, func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
	return nil, errors.New("native_product_selection_unsupported")
}
