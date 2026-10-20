package setup

import (
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// A model the person already has is adopted where it lies (VISION: adopt
// models in place first). The model inventory owns finding it: inventoryd
// lists the model files of every known store with the SHA-256 the store
// names, and, when asked about one manifest, hashes its weights file once
// (LS-13). Setup asks the storage inventory for the plan's pinned digest,
// asks it to hash only same-size candidates, and hosts the object it finds
// through ModelHost.

// StepLocal is an inventory object that holds a plan's pinned file, byte for
// byte, on this computer.
type StepLocal struct {
	Store  string `json:"store"`
	Object string `json:"object"`
}

// SameSizeCandidates are the model manifests whose one weights entry has
// the pinned size and no digest yet. Setup asks the inventory to hash
// exactly these (Holders on each id reaches the source's Verify, LS-13), so
// nothing else in any store is read.
func SameSizeCandidates(plan ModelPlan, manifests []content.ManifestHolders) []string {
	if len(plan.Assets) != 1 {
		return nil
	}
	var ids []string
	for _, item := range manifests {
		m := item.Manifest
		if m.Kind != "abstraction.model/descriptor@1" || m.ID == "" {
			continue
		}
		var weights []content.Entry
		for _, entry := range m.Entries {
			if entry.Role == "weights" {
				weights = append(weights, entry)
			}
		}
		if len(weights) == 1 && weights[0].Digest == "" && weights[0].Size == plan.Assets[0].Download.Bytes {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// PinnedObject is PinnedEntry's object.
func PinnedObject(plan ModelPlan, found []content.ManifestHolders) *StepLocal {
	local, _ := PinnedEntry(plan, found)
	return local
}

// PinnedEntry picks, from the manifests the storage inventory's Find
// answered for plan's pinned digest, a model whose one weights entry has
// that digest and the pinned size, with that entry's locator in its store.
// It answers nil when none does.
func PinnedEntry(plan ModelPlan, found []content.ManifestHolders) (*StepLocal, string) {
	if len(plan.Assets) != 1 {
		return nil, ""
	}
	pin := plan.Assets[0].Download
	for _, item := range found {
		m := item.Manifest
		if m.Kind != "abstraction.model/descriptor@1" || m.Store == "" || m.ID == "" {
			continue
		}
		var weights []content.Entry
		for _, entry := range m.Entries {
			if entry.Role == "weights" {
				weights = append(weights, entry)
			}
		}
		if len(weights) == 1 && weights[0].Digest == "sha256:"+pin.SHA256 && weights[0].Size == pin.Bytes {
			return &StepLocal{Store: m.Store, Object: m.ID}, weights[0].Locator
		}
	}
	return nil, ""
}
