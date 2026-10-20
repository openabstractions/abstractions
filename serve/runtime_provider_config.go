package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	cas "github.com/openabstractions/abstraction-cas/go"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Existing installation-marker metadata records only a completed conditional
// system removal. Its policy revision permits restoration only while no later
// person or concurrent policy edit has occurred. Uncertain gaps fail closed.
type configRemovedRule struct {
	Rule     appliedRule `json:"rule"`
	Revision string      `json:"revision"`
}

func (r *runtimeRights) markConfigRemoved(key appliedRule, revision string, clear string, previous string) error {
	return cas.ChangeLimit(r.marker, maxDefaultsMarker, func(data []byte) ([]byte, error) {
		var marker defaultsMarker
		if json.Unmarshal(data, &marker) != nil || marker.Profile != defaultsMarkerProfile {
			return nil, errors.New("rights: invalid installation marker")
		}
		index := slices.IndexFunc(marker.ConfigRemoved, func(v configRemovedRule) bool { return v.Rule == key })
		if clear != "" {
			if index >= 0 && marker.ConfigRemoved[index].Revision == clear {
				marker.ConfigRemoved = slices.Delete(marker.ConfigRemoved, index, index+1)
			}
		} else if index >= 0 {
			marker.ConfigRemoved[index].Revision = revision
		} else {
			marker.ConfigRemoved = append(marker.ConfigRemoved, configRemovedRule{key, revision})
		}
		// Advance only metadata proved unchanged by this owner's exact CAS.
		// This lets one multi-key system removal/Keep complete as a batch.
		for i := range marker.ConfigRemoved {
			if marker.ConfigRemoved[i].Rule != key && marker.ConfigRemoved[i].Revision == previous {
				marker.ConfigRemoved[i].Revision = revision
			}
		}
		return json.Marshal(marker)
	})
}

func (r *runtimeRights) keepProviderConfig(ctx context.Context, program string, keys []string) error {
	if r == nil || r.policy == nil {
		return errors.New("config: rights unavailable")
	}
	marker, err := r.startMarker()
	if err != nil {
		return err
	}
	// Record factory custody before writing a rule. A crash can leave a
	// receipt without a row; receipt presence alone never authorizes a read.
	for _, key := range keys {
		target := appliedRule{program, host.ConfigKeyReadAction, key}
		if !slices.Contains(marker.ProviderConfig, target) {
			marker.ProviderConfig = append(marker.ProviderConfig, target)
		}
	}
	if err := r.writeMarker(marker); err != nil {
		return err
	}
	for _, key := range keys {
		marker, err := r.readMarker()
		if err != nil {
			return err
		}
		target := appliedRule{program, host.ConfigKeyReadAction, key}
		for _, removed := range marker.ConfigRemoved {
			if removed.Rule != target {
				continue
			}
			subject := rwire.Subject{Account: r.owner, Program: program}
			current := r.policy.ReadRule(subject, target.Action, target.Resource)
			if current.Outcome != rwire.RuleReadOutcomeUnknown || current.Revision != removed.Revision {
				continue
			}
			permit := true
			edit, err := r.policy.ChangeRuleContext(ctx, current.Revision, subject, target.Action, target.Resource, rights.RuleEdit{Permit: &permit, By: r.self(), Why: installationWhy}, ctx.Err)
			if err != nil {
				return err
			}
			if edit.Outcome == rwire.PolicyEditOutcomeApplied {
				if err := r.markConfigRemoved(target, edit.Revision, removed.Revision, removed.Revision); err != nil {
					return err
				}
			}
		}
	}
	rules := make([]installationRule, 0, len(keys))
	for _, key := range keys {
		rules = append(rules, installationRule{host.ConfigKeyReadAction, key})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.installFor([]string{program}, rules)
}

func (r *runtimeRights) retireProviderConfig(ctx context.Context, keep map[appliedRule]bool) error {
	if r == nil || r.policy == nil {
		return errors.New("config: rights unavailable")
	}
	marker, err := r.readMarker()
	if err != nil {
		return err
	}
	for _, target := range marker.ProviderConfig {
		if target.Action != host.ConfigKeyReadAction || keep[target] || (r.configReadDefault != nil && r.configReadDefault(target.Program, target.Resource)) {
			continue
		}
		subject := rwire.Subject{Account: r.owner, Program: target.Program}
		normalized, err := rights.NormalizeDecisionSubject(subject)
		if err != nil {
			return err
		}
		for attempt := 0; attempt < installationAttempts; attempt++ {
			current := r.policy.ReadRule(subject, target.Action, target.Resource)
			if current.Outcome != rwire.RuleReadOutcomeFound || current.Record == nil || !current.Record.Rule.Permit || current.Record.SetBy != r.self() || current.Record.Why != installationWhy {
				break
			}
			if current.Record.Rule.Subject != normalized || current.Record.Rule.Action != target.Action || current.Record.Rule.Resource != target.Resource {
				break
			}
			edit, err := r.policy.ChangeRuleContext(ctx, current.Revision, subject, target.Action, target.Resource, rights.RuleEdit{By: r.self(), Exact: true}, ctx.Err)
			if err != nil {
				return err
			}
			if edit.Outcome == rwire.PolicyEditOutcomeConflict {
				if attempt == installationAttempts-1 {
					return errors.New("config: policy kept changing during grant retirement")
				}
				continue
			}
			if edit.Outcome != rwire.PolicyEditOutcomeApplied {
				return fmt.Errorf("config: retire grant: %s", edit.Outcome)
			}
			if err := r.markConfigRemoved(target, edit.Revision, "", current.Revision); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func (r *runtimeRights) removeProviderConfig(ctx context.Context, program string) error {
	marker, err := r.readMarker()
	if err != nil {
		return err
	}
	keep := map[appliedRule]bool{}
	for _, target := range marker.ProviderConfig {
		if target.Program != program {
			keep[target] = true
		}
	}
	return r.retireProviderConfig(ctx, keep)
}

// Registry custody spans the Rights reconciliation. Pending current programs
// preserve factory rows from an unfinished Keep; the read proof still refuses
// them. Removed/replaced facets lose proof before this cleanup runs.
func (d *applicationDirectory) reconcileProviderConfigGrants(ctx context.Context) error {
	if d == nil || d.programConfigReconcile == nil {
		return nil
	}
	if !d.lock(ctx) {
		return ctx.Err()
	}
	defer d.unlock()
	return d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		keep := map[appliedRule]bool{}
		if d.v3 != nil {
			for _, name := range d.v3.programKeys() {
				record, ok := d.v3.programRecord(name)
				source, has := d.v3.programSources[name]
				approval := d.v3.programApprovals[name]
				if !ok || !has || approval.Removing {
					continue
				}
				for _, facet := range record.Spec.Providers {
					condition := registrationConditionOf(d.v3, name, registration.FacetReference{Kind: registration.FacetKindProvider, Name: facet.Name})
					if condition.Disabled && !approval.Pending {
						continue
					}
					for _, key := range facet.ConfigKeys {
						keep[appliedRule{source.Program, host.ConfigKeyReadAction, key}] = true
					}
				}
			}
		}
		return d.programConfigReconcile(ctx, keep)
	})
}
