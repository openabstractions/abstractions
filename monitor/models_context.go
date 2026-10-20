package main

import (
	modelservice "github.com/openabstractions/abstraction-model/go/service"
)

// The Models page's Context chooser (architect decision D, 2026-11-08): a
// local model OA's model host runs offers Automatic plus each
// per-conversation size that fits this machine. The model owner's
// PlanContext decides the sizes and the conversations each keeps; the page
// stores the person's choice in the model_context setting. The model host
// watches it, unloads the model once no request uses it, and loads the next
// conversation at the new size.

// libraryContextOption is one size: the tokens each conversation gets and
// the conversations kept resident at that size.
type libraryContextOption struct {
	Context int64 `json:"context"`
	Slots   int   `json:"slots"`
}

// libraryContext is a model's Context chooser. Automatic is what Automatic
// gives on this machine; Sizes are the other choices, smallest first, and
// empty when the machine's memory is unknown.
type libraryContext struct {
	Automatic libraryContextOption   `json:"automatic"`
	Sizes     []libraryContextOption `json:"sizes"`
}

// libraryPool is the bytes the model host's card pool holds by the resource
// table's rule: card:0's capacity when the instrument reports one, else the
// machine's memory. Zero means unknown.
func libraryPool(resources []cardResourceState) int64 {
	capacity := map[string]int64{}
	for _, r := range resources {
		capacity[r.Resource] = r.Capacity
	}
	if capacity["card:0"] > 0 {
		return capacity["card:0"]
	}
	return max(capacity["memory"], 0)
}

// planLibraryContext is family's chooser on a machine whose pool holds pool
// bytes, or nil for a family the model catalogue does not curate.
func planLibraryContext(family string, pool int64) *libraryContext {
	automatic, ok := modelservice.PlanContext(family, pool, 0)
	if !ok {
		return nil
	}
	chooser := &libraryContext{Automatic: libraryContextOption{Context: automatic.Context, Slots: automatic.Slots}, Sizes: []libraryContextOption{}}
	for _, size := range automatic.Sizes {
		plan, _ := modelservice.PlanContext(family, pool, size)
		chooser.Sizes = append(chooser.Sizes, libraryContextOption{Context: plan.Context, Slots: plan.Slots})
	}
	return chooser
}

// attachLibraryContexts gives each model OA's model host runs on this
// computer its Context chooser. A model with no hosted copy gets none.
func attachLibraryContexts(models []libraryModel, resources []cardResourceState) {
	pool := libraryPool(resources)
	for i := range models {
		if !hasHostedObject(models[i].Objects) {
			continue
		}
		models[i].Context = planLibraryContext(models[i].Key, pool)
	}
}

func hasHostedObject(objects []libraryObject) bool {
	for _, o := range objects {
		if o.Hosted {
			return true
		}
	}
	return false
}
