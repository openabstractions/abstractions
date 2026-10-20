//go:build !windows

package main

import (
	"context"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

func activateAgentUpdateNotification(context.Context, askclient.NotificationRecord, string) error {
	return nil
}
