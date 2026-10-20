//go:build !windows

package main

import (
	"context"
	"fmt"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

func holdInstalledConnectionHost(_ context.Context, connection installedAgentConnection) (fwire.ApplicationDescriptor, claudePanelImage, error) {
	return fwire.ApplicationDescriptor{}, nil, fmt.Errorf("installed %s connection is available on Windows", connection.title)
}
