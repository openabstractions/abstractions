package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	router "github.com/openabstractions/abstraction-router/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// ActionInventoryRead gates abstraction.storage/inventory@1, the runtime's
// composition of every accepted declared source.
const ActionInventoryRead = "abstraction.storage/inventory.read"

// inventorySource is the reader of one ready declared source and the stores
// the runtime accepted for it, or false when this declaration is not one.
func (s *providerSupervisor) inventorySource(ctx context.Context) (storageservice.DesignatedSource, bool) {
	d := s.file.Declaration
	if !slices.Contains(d.Contracts, storageInventorySource) {
		return storageservice.DesignatedSource{}, false
	}
	s.mu.Lock()
	ready, accepted := s.readiness == wire.DeclarationReadinessReady, slices.Clone(s.accepted)
	s.mu.Unlock()
	if !ready || len(accepted) == 0 {
		return storageservice.DesignatedSource{}, false
	}
	transport, err := s.transport(ctx, s.p.timing.inventoryBudget)
	if err != nil {
		s.p.report(err)
		return storageservice.DesignatedSource{}, false
	}
	stores := make([]string, 0, len(accepted))
	for _, resource := range accepted {
		stores = append(stores, strings.TrimPrefix(resource, "store:"))
	}
	return storageservice.DesignatedSource{Program: d.Program, Stores: stores,
		Reader: content.NewInventorySourceClient(transport)}, true
}

// heldRefresh is how often the runtime re-reads what the machine holds. A
// store changes when a person downloads a model, which is slower than the
// host survey and costs a walk of every accepted store.
const heldRefresh = 2 * time.Minute

// inventoryProvenance keeps the router's model catalogue told what the
// storage inventory says this machine holds. It reads the same composition
// the runtime serves, and writes only router provenance.
type inventoryProvenance struct {
	providers *runtimeProviders
	router    *router.Router
	report    func(error)
	wake      chan struct{}
}

func newInventoryProvenance(providers *runtimeProviders, r *router.Router, report func(error)) *inventoryProvenance {
	if providers == nil || r == nil {
		return nil
	}
	p := &inventoryProvenance{providers: providers, router: r, report: report, wake: make(chan struct{}, 1)}
	providers.Watch(p.changed)
	return p
}

// changed asks for one refresh; it never blocks a caller of notify.
func (p *inventoryProvenance) changed() {
	if p == nil {
		return
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// run refreshes provenance until ctx ends: once at the start, whenever a
// declaration or its reading changes, and on the clock.
func (p *inventoryProvenance) run(ctx context.Context) {
	if p == nil {
		return
	}
	t := time.NewTicker(heldRefresh)
	defer t.Stop()
	for {
		p.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-t.C:
		}
	}
}

// refresh reads every accepted source once and tells the router what the
// stores hold. A composition that refuses leaves the last answer in place:
// the router's provenance says what was last read, never that a store went
// empty because a source was busy.
func (p *inventoryProvenance) refresh(ctx context.Context) {
	sources := p.providers.inventorySources(ctx)
	if len(sources) == 0 {
		p.router.SetHeld(nil)
		return
	}
	call, cancel := context.WithTimeout(ctx, p.providers.timing.inventoryBudget)
	defer cancel()
	composed, err := storageservice.Compose(call, sources)
	if err != nil {
		if ctx.Err() == nil && p.report != nil {
			p.report(fmt.Errorf("runtime inventory: %w", err))
		}
		return
	}
	p.router.SetHeld(heldObjects(composed))
}

// modelDescriptor is the manifest kind whose descriptor says what weights are.
const modelDescriptor = "abstraction.model/descriptor@1"

// heldObjects is what the router matches host models against: each manifest
// under the names its store gives it, the model descriptor its store published
// for it, and each stray object under the digest its store published. Nothing
// is hashed and no path is exposed.
func heldObjects(composed storageservice.ComposedInventory) []router.HeldObject {
	out := make([]router.HeldObject, 0, len(composed.Manifests)+len(composed.Objects))
	for _, m := range composed.Manifests {
		held := router.HeldObject{Store: m.Manifest.Store}
		if m.Manifest.Kind == modelDescriptor {
			held.Descriptor = m.Manifest.Descriptor
		}
		for _, name := range m.Manifest.Names {
			held.Names = append(held.Names, name.Name)
		}
		for _, e := range m.Manifest.Entries {
			if e.Evidence != content.EvidenceNone && e.Digest != "" {
				held.Digest = e.Digest
				break
			}
		}
		if len(held.Names) > 0 || held.Digest != "" {
			out = append(out, held)
		}
	}
	for _, o := range composed.Objects {
		if o.Object.Evidence == content.EvidenceNone || o.Object.Digest == "" {
			continue
		}
		out = append(out, router.HeldObject{Store: o.Store, Digest: o.Object.Digest})
	}
	return out
}

// inventorySources is every accepted declared source, in declaration order.
// A declaration the runtime has not accepted a store for is not a source.
func (p *runtimeProviders) inventorySources(ctx context.Context) []storageservice.DesignatedSource {
	if p == nil {
		return nil
	}
	var out []storageservice.DesignatedSource
	for _, s := range p.sorted() {
		if source, ok := s.inventorySource(ctx); ok {
			out = append(out, source)
		}
	}
	return out
}
