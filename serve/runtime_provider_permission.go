package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

// One native admission builder owns the catalogue-dependent provider pair.
// Persisted validation remains structural and never consults this catalogue.
func providerPermission(subject rwire.Subject, name string) (asks.PermissionRequest, bool) {
	binding, err := hostedcatalogue.Select(name)
	if err != nil || binding.Name != name || binding.Credential != name {
		return asks.PermissionRequest{}, false
	}
	p, err := asks.NewPermissionRequest(asks.PermissionTarget{Account: subject.Account, Program: subject.Program, Action: credentials.ActionApply, Resource: credentials.ResourceFor(name)})
	if err != nil {
		return asks.PermissionRequest{}, false
	}
	companion := p.Targets[0]
	companion.Action = inference.ActionComplete
	companion.Resource = inference.ResourceHost(name)
	p.Targets = append(p.Targets, companion)
	return p, asks.ValidPermissionRequest(p)
}

func (a *runtimeAsks) providerCompletionPermission(subject rwire.Subject, resource string) (asks.PermissionRequest, bool) {
	if a.servers == nil {
		return asks.PermissionRequest{}, false
	}
	for _, h := range a.servers() {
		if h == nil || inference.ResourceHost(h.Name) != resource {
			continue
		}
		binding, err := hostedcatalogue.Select(h.Name)
		if err == nil && h.Hosted && h.Base == binding.Base && h.Wire == binding.Kind && h.Credential == binding.Credential {
			return providerPermission(subject, h.Name)
		}
	}
	return asks.PermissionRequest{}, false
}

func firstUsePermissionKey(p asks.PermissionRequest) string {
	if _, paired := asks.ProviderPermissionName(p); paired {
		raw, _ := json.Marshal(p.Targets)
		sum := sha256.Sum256(raw)
		return "first-use-pair:" + hex.EncodeToString(sum[:20])
	}
	// Existing single and uniquely determined content pairs keep their keys.
	t := p.Targets[0]
	return firstUseKey(t.Program, t.Action, t.Resource)
}
