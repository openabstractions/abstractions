package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// setupExtensionAdapter binds a shipped extension to its presenter duty and
// its existing Panel routes. The adapter's handlers own operation admission.
type setupExtensionAdapter struct {
	maintenance func(setup.Journal) extensionMaintenance
	routes      []setupExtensionRoute
}

type setupExtensionRoute struct {
	path    string
	handler func(*servicePanel, http.ResponseWriter, *http.Request)
}

func configuredSetupExtensions() []setupExtensionAdapter {
	return []setupExtensionAdapter{officialPictureSetupExtension()}
}

func startSetupExtensionDuties(ctx context.Context) {
	go func() {
		journal, err := setup.DefaultJournal()
		if err != nil {
			log.Printf("setup: extension maintenance: %v", err)
			return
		}
		for _, extension := range configuredSetupExtensions() {
			go extension.maintenance(journal).duty(ctx)
		}
	}()
}

func (p *servicePanel) registerSetupExtensionRoutes(mux *http.ServeMux, key string) {
	for _, extension := range configuredSetupExtensions() {
		for _, route := range extension.routes {
			mux.HandleFunc(route.path, guard(key, func(w http.ResponseWriter, r *http.Request) {
				route.handler(p, w, r)
			}))
		}
	}
}

// extensionMaintenance binds a host's native claim and observation to one
// scheduler. A tick never runs a pass while another owner holds the claim.
type extensionMaintenance struct {
	title   string
	claim   func() (func(), bool, error)
	settled func() bool
	idle    func() bool
	pass    func(context.Context)
}

const (
	extensionWatchEvery = time.Minute
	// Only idle, unsettled passes increase the retry delay.
	extensionWatchBackoffLimit = 64
)

func (m extensionMaintenance) duty(ctx context.Context) {
	ticker := time.NewTicker(extensionWatchEvery)
	defer ticker.Stop()
	m.watch(ctx, ticker.C)
}

// tryPass takes the session claim before a pass. A one-shot caller leaves
// idle observation to the pass. Only the watcher needs it for backoff.
func (m extensionMaintenance) tryPass(ctx context.Context, watchIdle bool) (ran, idle bool) {
	release, acquired, err := m.claim()
	if err != nil {
		log.Printf("setup: %s integration update: %v", m.title, err)
		return false, false
	}
	if !acquired {
		return false, false
	}
	defer release()
	if watchIdle {
		idle = m.idle()
	}
	m.pass(ctx)
	return true, idle
}

func (m extensionMaintenance) watch(ctx context.Context, tick <-chan time.Time) {
	skip, wait := 0, 1
	for {
		if m.settled() {
			return
		}
		if skip > 0 {
			skip--
		} else if ran, idle := m.tryPass(ctx, true); ran {
			if m.settled() {
				return
			}
			if idle {
				skip, wait = wait, min(wait*2, extensionWatchBackoffLimit)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
	}
}
