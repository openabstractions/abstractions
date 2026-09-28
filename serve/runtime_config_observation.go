package main

import (
	"context"
	"os"
	"time"

	configservice "github.com/openabstractions/abstraction-config/go/service"
)

// The installed runtime's default configuration store is the native per-user
// file config.UserPath() names, and its observation is already wired by
// abstraction-config/go/service.Listen through the platform's own directory
// notification. A runtime whose configuration store is instead scoped to its
// own state directory, which isolatedConfigStore selects for every --isolated
// runtime and any composition given an explicit --state-dir, has no such
// wiring: that file lives nowhere the native watcher looks. This source
// closes that gap by polling the file's own state.

// configFilePollInterval bounds how stale a state-dir-scoped runtime's
// configuration observation can be. It is not the native directory
// notification the installed runtime's default store gets; it is close
// enough for a person watching the Panel to see a save land, and short
// enough that nothing here needs the platform's own file-change APIs.
const configFilePollInterval = 300 * time.Millisecond

// fileConfigObservationSource polls one configuration file's size and
// modification time for the changes ObservationSource reports, honoring its
// contract (abstraction-config/go/service.ObservationSource): it returns
// promptly, honors ctx, and its stop joins the polling goroutine. The file
// need not exist yet; a state-dir-scoped runtime creates it on first edit,
// and that creation is itself a reported change.
func fileConfigObservationSource(path string) configservice.ObservationSource {
	return func(ctx context.Context) (<-chan struct{}, func(), error) {
		stopCtx, cancel := context.WithCancel(ctx)
		events := make(chan struct{}, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer close(events)
			ticker := time.NewTicker(configFilePollInterval)
			defer ticker.Stop()
			last, lastKnown := statSignature(path)
			for {
				select {
				case <-stopCtx.Done():
					return
				case <-ticker.C:
					sig, known := statSignature(path)
					if known && (!lastKnown || sig != last) {
						select {
						case events <- struct{}{}:
						default:
						}
					}
					last, lastKnown = sig, known
				}
			}
		}()
		return events, func() { cancel(); <-done }, nil
	}
}

// fileSignature changes whenever a rewrite of the file is visible through
// stat, without reading its contents on every poll.
type fileSignature struct {
	size    int64
	modTime int64
}

// statSignature reports false for a file that does not exist yet, which is
// the normal state before a state-dir-scoped runtime's first edit and not a
// failure this source needs to report.
func statSignature(path string) (fileSignature, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return fileSignature{}, false
	}
	return fileSignature{size: info.Size(), modTime: info.ModTime().UnixNano()}, true
}
