//go:build windows

package main

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"time"
)

func runOwnedPanel() error {
	return runOwnedPanelWith(os.Stdin, os.Stdout, resolvePanelOwnedPresence, runResourceWindow)
}

func runOwnedPanelWith(in io.ReadCloser, out io.Writer, resolve func(context.Context) (panelOwnedPresenceCalls, error), open func(*panelRun, bool) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	calls, err := resolve(ctx)
	if err != nil {
		cancel()
		return err
	}
	presence, err := announcePanelOwned(ctx, calls)
	cancel()
	if err != nil {
		return err
	}
	defer presence.withdraw()
	manifest, err := panelOwnedResources.ReadFile("panel-application.json")
	if err != nil {
		return err
	}
	sdk, err := fs.Sub(panelOwnedResources, "static/native-sdk")
	if err != nil {
		return err
	}
	run, err := newPanelRun("credentials")
	if err != nil {
		return err
	}
	frames := newPanelOwnedFrames(in, out, presence.withdraw)
	defer frames.close()
	model, err := panelCreateNativeModel(presence.instance, manifest)
	if err != nil {
		return err
	}
	if err := frames.attachModel(model); err != nil {
		_ = model.Close()
		return err
	}
	run.owned = &panelOwnedWindow{frames: frames, instance: presence.instance, manifest: string(manifest),
		sdk: http.StripPrefix("/native-host/sdk/", http.FileServerFS(sdk))}
	go frames.read()
	presenceDone := make(chan struct{})
	go func() { defer close(presenceDone); presence.heartbeat(frames.ctx, frames.retire) }()
	err = open(run, true)
	_ = frames.close()
	<-frames.done
	<-presenceDone
	return err
}
