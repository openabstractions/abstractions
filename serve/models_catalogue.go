package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	registrywire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"

	modelwire "github.com/openabstractions/abstraction-model/go/abstraction/model/api"
	modelservice "github.com/openabstractions/abstraction-model/go/service"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// codeCatalogue reuses model's compiled catalogue reader. Fit's entries carry
// their curated metadata even when this zero pool fits none; no store, model
// file, registry or resource service is read here.
func codeCatalogue(ctx context.Context) []modelwire.FitEntry {
	result := modelservice.FitCatalog(ctx, modelwire.CatalogUseCode, modelservice.CatalogSources{
		Pool:   func(context.Context) (int64, error) { return 0, nil },
		Served: func(context.Context, string) ([]modelservice.ServedFamily, error) { return nil, nil },
	})
	return result.Entries
}

func catalogueContent(ctx context.Context, digest string, size int64) (string, error) {
	matches := 0
	for _, entry := range codeCatalogue(ctx) {
		if entry.Digest == digest && entry.DownloadBytes == size {
			matches++
		}
	}
	if matches != 1 {
		return "", errors.New("--content digest and object size must name one reviewed coding catalogue entry")
	}
	return digest + ":" + strconv.FormatInt(size, 10), nil
}

// catalogueModel identifies a singleton weights object only from its live
// hashed inventory evidence. A named digest or digest-looking filename is an
// unverified claim and supplies no catalogue identity.
func catalogueModel(m content.Manifest, entries []modelwire.FitEntry) (modelwire.FitEntry, bool) {
	if m.Kind != modelDescriptor || m.SupersededBy != "" || len(m.Entries) != 1 {
		return modelwire.FitEntry{}, false
	}
	e := m.Entries[0]
	if e.Role != "weights" && e.Role != "" || e.Evidence != content.EvidenceHashed {
		return modelwire.FitEntry{}, false
	}
	var result modelwire.FitEntry
	found := false
	for _, entry := range entries {
		if entry.Digest != e.Digest || entry.DownloadBytes != e.Size {
			continue
		}
		if found && result.Family != entry.Family {
			return modelwire.FitEntry{}, false
		}
		result, found = entry, true
	}
	return result, found
}

// catalogueInventory changes the runtime's projection alone. Source object
// IDs, bytes, registrations, policy rules and the existing hosted aliases stay
// intact. The configured alias is joined by exact store/object association.
func catalogueInventory(composed storageservice.ComposedInventory, hosted []hostedModel, entries []modelwire.FitEntry) storageservice.ComposedInventory {
	current := map[string]bool{}
	for _, store := range composed.Stores {
		current[store.Name] = store.Placement == content.PlacementLocal && store.Present && store.Domain == "" && len(store.Errors) == 0
	}
	composed.Manifests = slices.Clone(composed.Manifests)
	for i := range composed.Manifests {
		m := &composed.Manifests[i].Manifest
		if !current[m.Store] {
			continue
		}
		entry, ok := catalogueModel(*m, entries)
		if !ok {
			continue
		}
		m.Descriptor = maps.Clone(m.Descriptor)
		if m.Descriptor == nil {
			m.Descriptor = map[string]string{}
		}
		m.Descriptor["family"] = entry.Family
		m.Names = slices.Clone(m.Names)
		for _, alias := range hosted {
			if alias.Store == m.Store && alias.Object == m.ID && alias.Name != "" && !slices.ContainsFunc(m.Names, func(n content.Name) bool { return n.Name == alias.Name }) {
				m.Names = append(m.Names, content.Name{Scheme: m.Store, Name: alias.Name})
			}
		}
	}
	return composed
}

type catalogueVerifyTarget struct{ store, object string }

func (p *runtimeProviders) catalogueHosted() ([]hostedModel, error) {
	p.mu.Lock()
	files, _, err := p.read()
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.Declaration.Name == modelHostName && !file.Disabled && file.Source == declarationSourceOperator {
			_, hosted := readHostDeclaration(registrywire.DeclarationList{Declarations: []registrywire.DeclarationState{{Declaration: file.Declaration.wire()}}})
			return hosted, nil
		}
	}
	return nil, nil
}

func verifyCatalogueModels(ctx context.Context, sources []storageservice.DesignatedSource, targets []catalogueVerifyTarget) error {
	var failures []error
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, source := range sources {
			if source.Reader == nil || !slices.Contains(source.Stores, target.store) {
				continue
			}
			// Verify only queues work. Its immediate result never establishes
			// identity; a later composition must carry live hashed evidence.
			if page, err := source.Reader.Verify(target.object); err != nil {
				failures = append(failures, err)
			} else if page.Outcome != content.SourcePageOutcomePage {
				failures = append(failures, fmt.Errorf("catalogue verification: %s", page.Outcome))
			}
			break
		}
	}
	return errors.Join(failures...)
}

// catalogueVerifyTargets selects work, never identity: a currently hosted,
// digestless singleton with a curated size. The source's existing on-demand
// hash scheduler owns deduplication, cancellation and stamp-bound evidence.
func catalogueVerifyTargets(composed storageservice.ComposedInventory, hosted []hostedModel, entries []modelwire.FitEntry) []catalogueVerifyTarget {
	current := map[string]bool{}
	for _, store := range composed.Stores {
		current[store.Name] = store.Placement == content.PlacementLocal && store.Present && store.Domain == "" && len(store.Errors) == 0
	}
	var targets []catalogueVerifyTarget
	for _, mh := range composed.Manifests {
		m := mh.Manifest
		if !current[m.Store] || m.Kind != modelDescriptor || m.SupersededBy != "" || len(m.Entries) != 1 {
			continue
		}
		e := m.Entries[0]
		if e.Evidence != content.EvidenceNone || e.Digest != "" || e.Role != "" && e.Role != "weights" {
			continue
		}
		if !slices.ContainsFunc(hosted, func(h hostedModel) bool { return h.Store == m.Store && h.Object == m.ID }) || !slices.ContainsFunc(entries, func(c modelwire.FitEntry) bool { return c.DownloadBytes == e.Size }) {
			continue
		}
		target := catalogueVerifyTarget{store: m.Store, object: m.ID}
		if !slices.Contains(targets, target) {
			targets = append(targets, target)
		}
	}
	return targets
}
