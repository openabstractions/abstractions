package main

import (
	"context"
	"slices"
	"strings"
)

// Inventory sources on the provider rail: abstraction.storage/inventory-source@1
// accepts a source only under an abstraction.storage/inventory.provide rule
// for the bound program and the named stores (content.thrift InventorySource).
// A declaration names the stores the person accepts as resources store:<name>;
// Declare writes the rule for each, and the runtime accepts a described store
// only when both hold.
const (
	ActionInventoryProvide = "abstraction.storage/inventory.provide"
	providerAddWhy         = "provider add"
)

// ResourceStore is the rights resource of inventory.provide for one store.
func ResourceStore(name string) string { return "store:" + name }

// validProviderStores returns "resources" when a declaration's store
// resources belong to no inventory source, or a source names none.
func validProviderStores(d providerDeclaration) string {
	source := slices.Contains(d.Contracts, storageInventorySource)
	stores := d.resources("store")
	if !source && len(stores) > 0 || source && len(stores) == 0 {
		return "resources"
	}
	return ""
}

// acceptStores is the described stores the runtime accepts, as resources
// store:<name> in order: declared and permitted by inventory.provide.
func (s *providerSupervisor) acceptStores(ctx context.Context, described []string) []string {
	declared := s.file.Declaration.resources("store")
	var accepted []string
	for _, store := range described {
		resource := ResourceStore(store)
		if !slices.Contains(declared, store) || slices.Contains(accepted, resource) {
			continue
		}
		if s.p.decide != nil && s.p.decide(ctx, s.file.Declaration.Program, ActionInventoryProvide, resource) {
			accepted = append(accepted, resource)
		}
	}
	slices.SortFunc(accepted, strings.Compare)
	return accepted
}
