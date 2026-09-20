package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
)

// providerMediation owns one application-facing endpoint for every supported
// declaration generation. Resolution therefore chooses the exact router host
// and a replacement cannot inherit calls made through a stale reference.
type providerMediation struct {
	mu        sync.Mutex
	base      string
	provider  *inference.Provider
	providers *runtimeProviders
	report    func(error)
	routes    map[string]*providerMediationRoute
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	workers   sync.WaitGroup
}

type providerMediationRoute struct {
	endpoint string
	host     *inferenceservice.Host
	serving  bool
	retired  bool
	sweeping bool
}

func newProviderMediation(base string, provider *inference.Provider, providers *runtimeProviders, report func(error)) (*providerMediation, error) {
	m := &providerMediation{base: base, provider: provider, providers: providers, report: report, routes: map[string]*providerMediationRoute{}}
	if err := m.sync(); err != nil {
		m.Close()
		return nil, err
	}
	providers.Watch(func() {
		if err := m.sync(); err != nil && report != nil {
			report(err)
		}
	})
	return m, nil
}

func mediatedProviderEndpoint(base, bindingID string) string {
	sum := sha256.Sum256([]byte(base + "\x00" + bindingID))
	name := "oa-provider-" + hex.EncodeToString(sum[:8])
	if runtime.GOOS == "windows" {
		return base + "-" + name
	}
	return filepath.Join(filepath.Dir(base), name)
}

func (m *providerMediation) desired() map[string]inference.ExecutionBinding {
	desired := map[string]inference.ExecutionBinding{}
	for _, supervisor := range m.providers.sorted() {
		declaration := supervisor.file.Declaration
		if declaration.remote() || !slices.Contains(declaration.Contracts, inference.Contract) || len(declaration.Models) == 0 {
			continue
		}
		bindingID := supervisor.file.bindingID()
		desired[bindingID] = inference.ExecutionBinding{Host: declaration.Name, BindingID: bindingID}
	}
	return desired
}

// sync retires withdrawn generations before opening replacements. A route with
// retained operations keeps Observe and Cancel available while refusing Start.
// Active and retired listeners together stay within the declaration bound.
func (m *providerMediation) sync() error {
	desired := m.desired()
	if len(desired) > maxProviders {
		return fmt.Errorf("provider mediation: at most %d routes", maxProviders)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	for bindingID, route := range m.routes {
		if _, exists := desired[bindingID]; exists {
			continue
		}
		// Lock order is m.mu -> Host admission. Start holds the Host admission
		// read lock through Provider.Start, whose path never enters the provider
		// registry or this manager.
		route.host.RetireAdmissions()
		if m.provider.HasBindingOperations(bindingID) {
			route.retired = true
			m.sweepLocked(bindingID, route)
			continue
		}
		_ = route.host.Close()
		delete(m.routes, bindingID)
	}
	endpoints := make(map[string]mediatedProvider, len(desired))
	for bindingID, binding := range desired {
		if route, exists := m.routes[bindingID]; exists {
			endpoints[bindingID] = mediatedProvider{Endpoint: route.endpoint, Ready: true}
			continue
		}
		endpoint := mediatedProviderEndpoint(m.base, bindingID)
		if len(m.routes) >= maxProviders {
			endpoints[bindingID] = mediatedProvider{Endpoint: endpoint}
			continue
		}
		host, err := inferenceservice.ListenMediated(endpoint, m.provider, inference.ExecutionLocal, binding)
		if err != nil {
			endpoints[bindingID] = mediatedProvider{Endpoint: endpoint}
			if m.report != nil {
				m.report(fmt.Errorf("provider mediation: %s: %w", binding.Host, err))
			}
			continue
		}
		host.OnError = m.report
		route := &providerMediationRoute{endpoint: endpoint, host: host}
		m.routes[bindingID] = route
		m.startLocked(route)
		endpoints[bindingID] = mediatedProvider{Endpoint: endpoint, Ready: true}
	}
	m.providers.setMediated(endpoints)
	return nil
}

func (m *providerMediation) sweepLocked(bindingID string, route *providerMediationRoute) {
	if route.sweeping || m.ctx == nil {
		return
	}
	route.sweeping = true
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				if m.provider.HasBindingOperations(bindingID) {
					continue
				}
				m.mu.Lock()
				removed := false
				current := m.routes[bindingID]
				if !m.closed && current == route && current.retired {
					_ = current.host.Close()
					delete(m.routes, bindingID)
					removed = true
				}
				m.mu.Unlock()
				if removed {
					m.providers.notify()
				}
				return
			}
		}
	}()
}

func (m *providerMediation) startLocked(route *providerMediationRoute) {
	if m.ctx == nil || route.serving {
		return
	}
	route.serving = true
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		if err := route.host.Serve(m.ctx); err != nil && m.ctx.Err() == nil && m.report != nil {
			m.report(err)
		}
	}()
}

func (m *providerMediation) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx != nil {
		return errors.New("provider mediation: already served")
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	for bindingID, route := range m.routes {
		m.startLocked(route)
		if route.retired {
			m.sweepLocked(bindingID, route)
		}
	}
	return nil
}

func (m *providerMediation) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
	var closeErr error
	for _, route := range m.routes {
		closeErr = errors.Join(closeErr, route.host.Close())
	}
	m.routes = map[string]*providerMediationRoute{}
	m.providers.setMediated(map[string]mediatedProvider{})
	m.mu.Unlock()
	m.workers.Wait()
	return closeErr
}
