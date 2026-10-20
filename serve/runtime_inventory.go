package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	router "github.com/openabstractions/abstraction-router/go"
	storage "github.com/openabstractions/abstraction-storage/go"
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
// store changes when a person downloads a model in another app; the model
// host's allowlist follows on this clock (models_discovered.go), so a model
// added there reaches the apps within it. A walk reads listings and cached
// headers, and hashes nothing.
const heldRefresh = 30 * time.Second

// inventoryProvenance keeps the router's model catalogue told what the
// storage inventory says this machine holds. It reads the same composition
// the runtime serves, and writes only router provenance.
type inventoryProvenance struct {
	providers *runtimeProviders
	router    *router.Router
	report    func(error)
	wake      chan struct{}
	// busy reports whether program holds resource with a verified lease (a
	// loaded engine); nil reports nothing busy.
	busy func(program, resource string) bool
	// held is what the router was last told; a store whose source did not
	// answer keeps its part of it.
	held []router.HeldObject
	// missed counts, per store, the refreshes in a row it went unread
	// (boundUnread).
	missed map[string]int
	// installed reads the file-backed stores (provider_installed.go).
	installed installedStores
}

// installedHeld is the last finished reading of the file-backed stores; a
// reading whose result changed wakes the next refresh.
func (p *inventoryProvenance) installedHeld(ctx context.Context) []router.HeldObject {
	report := func(err error) {
		if ctx.Err() == nil && p.report != nil {
			p.report(err)
		}
	}
	return p.installed.current(ctx, p.providers.fileBackedStores(ctx), report, p.changed)
}

func newInventoryProvenance(providers *runtimeProviders, r *router.Router, report func(error)) *inventoryProvenance {
	if providers == nil || r == nil {
		return nil
	}
	p := &inventoryProvenance{providers: providers, router: r, report: report, wake: make(chan struct{}, 1)}
	if providers.disabledPath != "" {
		p.installed.cache = filepath.Join(filepath.Dir(providers.disabledPath), installedHashesFile)
	}
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
			// A file-backed store reading may be saving its digest cache.
			p.installed.wait()
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
		p.held = nil
		p.router.SetHeld(p.installedHeld(ctx))
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
	catalogue := codeCatalogue(ctx)
	hosted, hostedErr := p.providers.catalogueHosted()
	var verification []catalogueVerifyTarget
	if hostedErr == nil {
		verification = catalogueVerifyTargets(composed, hosted, catalogue)
		composed = catalogueInventory(composed, hosted, catalogue)
	} else if p.report != nil {
		p.report(fmt.Errorf("runtime catalogue hosting: %w", hostedErr))
	}
	if p.missed == nil {
		p.missed = map[string]int{}
	}
	held, current, gone := boundUnread(p.missed, p.held, composed)
	if ctx.Err() == nil && p.report != nil {
		if len(composed.Incomplete) > 0 {
			p.report(fmt.Errorf("runtime inventory: %s did not answer in time; their stores keep what was last read", strings.Join(composed.Incomplete, ", ")))
		}
		if len(gone) > 0 {
			p.report(fmt.Errorf("runtime inventory: %s went unread for %d refreshes and count as empty until read again", strings.Join(gone, ", "), missedLimit))
		}
	}
	p.held = held
	p.router.SetHeld(append(slices.Clone(held), p.installedHeld(ctx)...))
	if pictureStoreReadIn(current) && p.providers.pictureStoreRead.CompareAndSwap(false, true) {
		// The router's hosts are built again with the PyTorch set table,
		// now that the store's holdings are what the router was told.
		p.providers.notify()
	}
	wrote, err := p.providers.reconcileModelHost(ctx, current, p.busy)
	if err != nil && ctx.Err() == nil && p.report != nil {
		p.report(fmt.Errorf("runtime model host: %w", err))
	} else if wrote != "" && p.report != nil {
		p.report(fmt.Errorf("runtime model host: the inventory changed the hosted models: %s", wrote))
	}
	if err := verifyCatalogueModels(call, sources, verification); err != nil && ctx.Err() == nil && p.report != nil {
		p.report(fmt.Errorf("runtime catalogue verification: %w", err))
	}
}

// missedLimit is how many refreshes in a row a store may go unread before
// the runtime counts it as empty: 10 refreshes of heldRefresh (30 s) is five
// minutes. That is long enough for a slow walk of a large store (a mapped
// or network Hugging Face cache of tens of GB) to finish inside inventoryd,
// which keeps walking and answers with the result once it has it, and short
// enough that a hung drive or a removed app's store stops being offered to
// apps within minutes.
const missedLimit = 10

// unreadReason is why this composition did not read a store now: its source
// did not answer, or answered from an earlier observation
// (storage.UnansweredSourceError, storage.StaleObservationError); "" for a
// store it read.
func unreadReason(s content.Store) string {
	for _, e := range s.Errors {
		switch {
		case e.Detail == storage.UnansweredSourceError:
			return "unanswered"
		case strings.HasPrefix(e.Detail, storage.StaleObservationError):
			return "stale"
		}
	}
	return ""
}

// boundUnread applies one composition to the counts of refreshes each store
// went unread (missed, updated in place) and answers what the router is told,
// what the model host's reconciler reads, and the stores past missedLimit.
//
//   - A store its source read now resets its count.
//   - A store not read now keeps what was last known of it: an unanswered
//     one what the router was last told (last), a stale one the earlier
//     observation its source answered with. Its errors make the reconciler
//     keep its entries and add none.
//   - A store not read for missedLimit refreshes counts as read and empty:
//     the router is told nothing of it and the reconciler drops its entries,
//     until its source reads it again.
func boundUnread(missed map[string]int, last []router.HeldObject, composed storageservice.ComposedInventory) ([]router.HeldObject, storageservice.ComposedInventory, []string) {
	reasons := map[string]string{}
	for _, s := range composed.Stores {
		if r := unreadReason(s); r != "" {
			reasons[s.Name] = r
		}
	}
	for name := range missed {
		if reasons[name] == "" {
			delete(missed, name)
		}
	}
	gone := map[string]bool{}
	var goneNames []string
	for name := range reasons {
		missed[name]++
		if missed[name] >= missedLimit {
			gone[name] = true
			goneNames = append(goneNames, name)
		}
	}
	slices.Sort(goneNames)
	current := composed
	if len(gone) > 0 {
		current = storageservice.ComposedInventory{Incomplete: composed.Incomplete}
		for _, s := range composed.Stores {
			if gone[s.Name] {
				s.Errors = nil
			}
			current.Stores = append(current.Stores, s)
		}
		for _, m := range composed.Manifests {
			if !gone[m.Manifest.Store] {
				current.Manifests = append(current.Manifests, m)
			}
		}
		for _, o := range composed.Objects {
			if !gone[o.Store] {
				current.Objects = append(current.Objects, o)
			}
		}
		current.Dangling = composed.Dangling
	}
	held := heldObjects(current)
	for _, o := range last {
		if reasons[o.Store] == "unanswered" && !gone[o.Store] {
			held = append(held, o)
		}
	}
	return held, current, goneNames
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
		held := router.HeldObject{Store: m.Manifest.Store, Manifest: m.Manifest.ID, EntryCount: len(m.Manifest.Entries)}
		if m.Manifest.Kind == modelDescriptor {
			held.Descriptor = m.Manifest.Descriptor
		}
		for _, name := range m.Manifest.Names {
			held.Names = append(held.Names, name.Name)
		}
		for index, e := range m.Manifest.Entries {
			entry := held
			entry.Entry, entry.EntryRole = index, e.Role
			entry.Evidence = e.Evidence
			if len(m.Manifest.Entries) == 1 {
				entry.Object = m.Manifest.ID
			}
			if e.Evidence != content.EvidenceNone && e.Digest != "" {
				entry.Digest = e.Digest
			}
			out = append(out, entry)
		}
		if len(m.Manifest.Entries) == 0 && len(held.Names) > 0 {
			out = append(out, held)
		}
	}
	for _, o := range composed.Objects {
		if o.Object.Evidence == content.EvidenceNone || o.Object.Digest == "" {
			continue
		}
		out = append(out, router.HeldObject{Store: o.Store, Digest: o.Object.Digest, Evidence: o.Object.Evidence})
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
