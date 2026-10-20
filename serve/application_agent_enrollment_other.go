//go:build !windows && !darwin

package main

import (
	"context"
	"errors"
)

func readApplicationAgentImage(context.Context, string, string) (applicationAgentImage, error) {
	return applicationAgentImage{}, errors.New("agent enrollment: native image owner unsupported")
}
func readApplicationAgentStamp(string) (applicationAgentStamp, error) {
	return applicationAgentStamp{}, errors.New("agent enrollment: native image owner unsupported")
}
func applicationAgentGatewayExclusion(context.Context, string, string) (bool, error) {
	return false, errors.New("agent enrollment: native installation owner unsupported")
}

func installedSharedGatewayKey(context.Context) (string, bool, error) { return "", false, nil }
