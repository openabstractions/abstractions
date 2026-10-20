//go:build darwin

package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/openabstractions/abstraction-facade/go/probe"
)

// The existing launcher owns this child and its pipes. Native permission
// questions need no browser server. Applications present ordinary choices.
// Only an explicit window request opens the Panel, and the window's requests
// come back over the same pipes (native_panel_bridge.go): nothing listens.
func runNativeAsks() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bridge := newNativeAsksBridge(ctx, os.Stdout)
	if startPermissionReview != (permissionReviewContext{}) {
		run, err := newPanelRun("permission-review")
		if err != nil {
			return err
		}
		// The review is the Panel's page; its Allow button is the one
		// confirmation (ARCHITECTURE.md: the Panel draws no native question).
		resources, start := run.resources(panelResourceOriginMacOS)
		bridge.servePanel(newNativePanelServer(ctx, resources, bridge.write))
		openReview := func(string) error {
			bridge.outMu.Lock()
			defer bridge.outMu.Unlock()
			return writePanelURL(os.Stdout, start, false)
		}
		go func() {
			bridge.close(bridge.read(os.Stdin, openReview))
			stop()
		}()
		if err := openReview(""); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
	var panelURL string
	var panelMu sync.Mutex
	openPanel := func(page string) error {
		panelMu.Lock()
		defer panelMu.Unlock()
		if panelURL == "" {
			// Retain an overview base URL. The existing Swift fragment navigation
			// selects Questions without making a first review request permanent.
			run, err := newPanelRun("overview")
			if err != nil {
				return err
			}
			resources, start := run.resources(panelResourceOriginMacOS)
			bridge.servePanel(newNativePanelServer(ctx, resources, bridge.write))
			panelURL = start
		}
		bridge.outMu.Lock()
		defer bridge.outMu.Unlock()
		return writePanelURL(os.Stdout, panelURL, false)
	}
	// Application notifications (ASK-N7) travel the same private channel. A
	// click that launched this app reaches the service once the child reads.
	surface := bridgeNotificationSurface{bridge: bridge}
	clicks := panelNotificationClicks(surface)
	clicks.activity = func(operationID string) error {
		return bridge.write(nativeAsksFrame{Kind: "open-panel", Content: "/work?operation=" + url.QueryEscape(operationID)})
	}
	bridge.onNotification = func(args string) {
		if err := clicks.handle(ctx, args); err != nil {
			log.Printf("notification click: %v", err)
		}
	}
	bridge.onNotificationClosed = func(id string) {
		closeCtx, cancel := context.WithTimeout(ctx, trayAnswerBudget)
		defer cancel()
		operator, err := clicks.operator(closeCtx)
		if err == nil {
			_, err = operator.DismissContext(closeCtx, id)
		}
		if err != nil {
			log.Printf("notification %s close: %v", id, err)
		}
	}
	go pollNotifications(ctx, newNotificationPresenter(surface), bridge.Unlocked)
	readDone := make(chan error, 1)
	go func() {
		err := bridge.read(os.Stdin, openPanel)
		bridge.close(err)
		readDone <- err
		stop()
	}()
	presenter := &questionPresenter{session: bridge, surface: bridge, operator: defaultPresenterOperator, spend: presenterSpend, providerForms: nativeProviderForm, providerComplete: completeProviderConnection,
		account: probe.Self().Account, watchEvery: trayPollInterval}
	announcer := newTrayAnnouncer()
	ticker := time.NewTicker(trayPollInterval)
	defer ticker.Stop()
	degraded := false
	for ctx.Err() == nil {
		if bridge.Unlocked() {
			fresh, _, unavailable := pollTray(ctx, panelMachine(), announcer)
			if unavailable != degraded {
				degraded = unavailable
				log.Printf("native question prompts: runtime unavailable=%t", unavailable)
			}
			if !presentFresh(ctx, fresh, presenter.present, announcer,
				func() {
					if err := bridge.write(nativeAsksFrame{Kind: "open-panel", Content: "questions"}); err != nil {
						log.Printf("open Questions: %v", err)
					}
				}) {
				break
			}
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	select {
	case err := <-readDone:
		if !errors.Is(err, io.EOF) {
			return err
		}
	default:
	}
	return nil
}
