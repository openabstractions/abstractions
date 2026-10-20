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
//
// An installer's -register-application or -remove-application run asks only
// that confirmation (application_native.go) and exits: 0 when the entry is
// as asked, an error otherwise. It presents no other question.
func runNativeAsks(request applicationStart) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bridge := newNativeAsksBridge(ctx, os.Stdout)
	if startPermissionReview != (permissionReviewContext{}) {
		run, err := newPanelRun("permission-review")
		if err != nil {
			return err
		}
		go func() {
			bridge.close(bridge.read(os.Stdin, func(string) error { return errors.New("standalone review opens no Panel page") }))
			stop()
		}()
		// The launcher first sends its current interactive-session state.
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for !bridge.Unlocked() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
		err = run.panel.askPersonalPermissionReview(ctx, bridge, bridge)
		return finishPersonalPermissionReview(err, bridge.showReport)
	}
	if request != (applicationStart{}) {
		run, err := newPanelRun("overview")
		if err != nil {
			return err
		}
		run.panel.appRequest.register, run.panel.appRequest.remove = request.register, request.remove
		go func() {
			// This run opens no Panel window.
			bridge.close(bridge.read(os.Stdin, func(string) error { return nil }))
			stop()
		}()
		return run.panel.askApplicationRequest(ctx, bridge)
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
	presenter := &questionPresenter{session: bridge, surface: bridge, operator: defaultPresenterOperator,
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
				}, bridge.showReport) {
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
