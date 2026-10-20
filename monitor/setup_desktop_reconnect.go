package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The record suppresses a repeat notification for this release package. The
// Apps row checks current installed state and its click owns all installation.
func notifyDesktopReconnect(ctx context.Context, pin setup.DesktopIntegration, config string, notify func(context.Context, setup.StepNotice) error) error {
	if !desktopReconnectAvailable(pin) {
		return nil
	}
	if err := os.MkdirAll(config, 0o700); err != nil {
		return err
	}
	path := filepath.Join(config, "desktop-reconnect-notice")
	return cas.ChangeLimitContext(ctx, path, 256, func(current []byte) ([]byte, error) {
		if string(current) == pin.SHA256 {
			return current, nil
		}
		if err := notify(ctx, setup.StepNotice{Title: desktopReconnectTitle(pin.Title), Body: "Open Apps in the OpenAbstractions Panel and choose Reconnect.", Tag: "desktop-reconnect"}); err != nil {
			return nil, err
		}
		return []byte(pin.SHA256), nil
	})
}

func desktopReconnectNoticeAtStart(lifetime context.Context) error {
	home, err := setup.CurrentHome()
	if err != nil || home.Isolated {
		return err
	}
	journal, err := setup.DefaultJournal()
	if err != nil {
		return err
	}
	pin := journal.Catalog().DesktopIntegration
	if pin == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(lifetime, 20*time.Second)
	defer cancel()
	return notifyDesktopReconnect(ctx, *pin, home.Config, stepNotify)
}
