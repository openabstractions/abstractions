package main

import (
	"context"
	"errors"
	"testing"
)

// backlog/JOURNEY-SCOPE.md 1: an isolated runtime enrolls a test helper when
// no runtime is installed, and still refuses the installed shared gateway
// image when one is.
func TestIsolatedAgentExclusionFollowsTheInstalledGateway(t *testing.T) {
	helper := applicationAgentImage{Program: `C:\journeys\agents\oa-opencode-integration\oa-opencode-integration.exe`, Key: "helper-key"}
	shared := applicationAgentImage{Program: `C:\Program Files\OpenAbstractions\tools\openabstractions-mcp.exe`, Key: "shared-key"}
	lookupFailed := errors.New("installation registration unreadable")
	for _, tc := range []struct {
		name      string
		installed string
		found     bool
		lookup    error
		image     applicationAgentImage
		want      error
	}{
		{name: "no installation enrolls the helper", image: helper},
		{name: "an installed gateway leaves another helper enrollable", installed: "shared-key", found: true, image: helper},
		{name: "the installed gateway image is refused", installed: "shared-key", found: true, image: shared, want: errApplicationAgentSharedGateway},
		{name: "an unreadable installation refuses", lookup: lookupFailed, image: helper, want: lookupFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := installedAgentGatewayKey
			defer func() { installedAgentGatewayKey = saved }()
			installedAgentGatewayKey = func(context.Context) (string, bool, error) { return tc.installed, tc.found, tc.lookup }
			d := &applicationDirectory{
				agentImage: func(context.Context, string, string) (applicationAgentImage, error) {
					return tc.image, nil
				},
				agentExcluded: isolatedAgentGatewayExclusion,
			}
			_, err := d.verifyAgentImage(context.Background(), tc.image.Program, "")
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("verifyAgentImage = %v, want %v", err, tc.want)
			}
		})
	}
}
