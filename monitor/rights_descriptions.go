package main

import (
	"bytes"
	"context"
	"errors"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

type policyDescriptionReader interface {
	ListActionDescriptionsContext(context.Context, string, int64) (rightsclient.DescriptionPage, error)
}

type selectorCatalogueReader interface {
	ListPermissionDefinitionsContext(context.Context, string, int64) (wire.PermissionDefinitionPage, error)
	ListPermissionBundlesContext(context.Context, string, int64) (wire.PermissionBundlePage, error)
}

type selectorPolicyReader interface {
	ListSelectorRulesContext(context.Context, string, int64) (wire.SelectorRulePage, error)
}

func editableCredentialPermission(key wire.PermissionKey) bool {
	if key.Action == credentials.ActionApply {
		return key.ResourceProfile == credentials.NamedUseResourceProfile || key.ResourceProfile == credentials.ScopedUseResourceProfile
	}
	if key.ResourceProfile != credentials.NamedSecretResourceProfile {
		return false
	}
	switch key.Action {
	case credentials.ActionSecretRead, credentials.ActionSecretCreate, credentials.ActionSecretRotate, credentials.ActionSecretRevoke, credentials.ActionSecretMetadataRead:
		return true
	}
	return false
}

func presentSelector(selection wire.PermissionSelection) selectorRuleView {
	view := selectorRuleView{Permission: selection.Permission, Scope: selection.Selector.Kind.String(), Target: "Structured target: profile decoder unavailable"}
	if !editableCredentialPermission(selection.Permission) {
		return view
	}
	switch selection.Selector.Kind {
	case wire.ResourceSelectorKindAll:
		if len(selection.Selector.Target) == 0 {
			view.Target, view.Editable = "All saved application secrets in this account", true
		}
	case wire.ResourceSelectorKindOne:
		if target, err := credentials.DecodeCredentialTarget(selection.Selector.Target); err == nil {
			view.Name, view.Target, view.Editable = target.Name, target.Name, true
		}
	}
	return view
}

// Equality here explains retained bundle provenance. Access decisions remain
// public service calls; the Panel never evaluates a selector against a request.
func samePermissionSelection(a, b wire.PermissionSelection) bool {
	return a.Permission == b.Permission && a.Selector.Kind == b.Selector.Kind && bytes.Equal(a.Selector.Target, b.Selector.Target)
}

func presentBundleOrigin(subject wire.Subject, origin *wire.BundleOrigin, records []wire.SelectorRuleRecord, now time.Time) *bundleOriginView {
	if origin == nil {
		return nil
	}
	view := &bundleOriginView{ID: origin.ID, BundleRef: origin.Expansion.BundleRef, Digest: origin.Expansion.DefinitionDigest, Original: len(origin.Expansion.Selections), State: "partial/changed"}
	for _, selection := range origin.Expansion.Selections {
		view.Members = append(view.Members, presentSelector(selection))
		for _, record := range records {
			if record.Rule.Subject != subject || record.BundleOrigin == nil || record.BundleOrigin.ID != origin.ID || !samePermissionSelection(record.Rule.Selection, selection) {
				continue
			}
			// RemoveBundle removes every currently linked row, including expired
			// rows. Its complete preview is separate from current live grants.
			view.LinkedMembers = append(view.LinkedMembers, presentSelector(selection))
			if record.Expires != "" {
				at, err := time.Parse(time.RFC3339Nano, record.Expires)
				if err != nil || !at.After(now) {
					continue
				}
			}
			if record.Rule.Permit {
				view.Remaining++
			}
			break
		}
	}
	if view.Remaining == view.Original && view.Original > 0 {
		view.State = "complete original grant"
	}
	return view
}

// Discovery, records and legacy rules form one editable snapshot only at the
// same effective revision. The first legacy page joins the complete bounded
// selector snapshot; subsequent legacy pages retain their existing cursor.
func joinPolicySelectors(ctx context.Context, view policyPageView, reader selectorCatalogueReader, operator selectorPolicyReader) policyPageView {
	if view.Outcome != "page" {
		return view
	}
	failed := func(outcome string) policyPageView {
		return policyPageView{Outcome: outcome, Rules: []policyRuleView{}}
	}
	cursor := ""
	definitions := []permissionDefinitionView{}
	seenDefinitions := map[wire.PermissionKey]bool{}
	exactActions := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages >= 4 {
			return failed("unavailable")
		}
		page, err := reader.ListPermissionDefinitionsContext(ctx, cursor, 64)
		if err != nil {
			var service *rightsclient.ServiceError
			if pages == 0 && errors.As(err, &service) && service.Code == wire.ServiceErrorCodeUnknownMethod {
				view.SelectorStatus = "unsupported"
				return view
			}
			return failed("unavailable")
		}
		if page.Outcome != wire.PolicyPageOutcomePage {
			return failed(page.Outcome.String())
		}
		if page.Revision != view.Revision {
			return failed("gap")
		}
		for _, definition := range page.Definitions {
			key := wire.PermissionKey{Action: definition.Definition.Action, ResourceProfile: definition.Definition.ResourceProfile}
			if seenDefinitions[key] {
				return failed("unavailable")
			}
			seenDefinitions[key] = true
			// Only profiles whose existing @1 string editor we understand appear
			// as exact choices. Typed-only actions keep their full-key controls.
			if key.ResourceProfile == rights.LegacyExactResourceProfile || key.ResourceProfile == credentials.NamedUseResourceProfile {
				exactActions[key.Action] = true
			}
			codec := editableCredentialPermission(key)
			definitions = append(definitions, permissionDefinitionView{SelectorPermissionDefinition: definition, CodecAvailable: codec, Editable: codec && definition.Definition.Served && len(definition.SupportedSelectors) > 0})
		}
		if page.Complete {
			break
		}
		if page.Next == "" || page.Next == cursor {
			return failed("unavailable")
		}
		cursor = page.Next
	}
	if operator == nil {
		return failed("unavailable")
	}
	bundles := []permissionBundleView{}
	seenBundles := map[string]bool{}
	page, err := reader.ListPermissionBundlesContext(ctx, "", 64)
	if err != nil {
		return failed("unavailable")
	}
	if page.Outcome != wire.PolicyPageOutcomePage {
		return failed(page.Outcome.String())
	}
	if page.Revision != view.Revision {
		return failed("gap")
	}
	if !page.Complete || len(page.Bundles) > 64 {
		return failed("unavailable")
	}
	for _, bundle := range page.Bundles {
		if seenBundles[bundle.BundleRef] {
			return failed("unavailable")
		}
		seenBundles[bundle.BundleRef] = true
		editable := bundle.BundleRef == credentials.FullControlBundle && len(bundle.Members) == 6 && len(bundle.SupportedSelectors) > 0
		for _, member := range bundle.Members {
			editable = editable && editableCredentialPermission(member)
		}
		bundles = append(bundles, permissionBundleView{PermissionBundleDefinition: bundle, Editable: editable})
	}
	cursor = ""
	records := []wire.SelectorRuleRecord{}
	type ruleKey struct {
		subject    wire.Subject
		permission wire.PermissionKey
		kind       wire.ResourceSelectorKind
		target     string
	}
	seenRules := map[ruleKey]bool{}
	for pages := 0; ; pages++ {
		if pages >= 64 {
			return failed("unavailable")
		}
		page, err := operator.ListSelectorRulesContext(ctx, cursor, 64)
		if err != nil {
			return failed("unavailable")
		}
		if page.Outcome != wire.PolicyPageOutcomePage {
			return failed(page.Outcome.String())
		}
		if page.Revision != view.Revision {
			return failed("gap")
		}
		for _, record := range page.Records {
			key := ruleKey{record.Rule.Subject, record.Rule.Selection.Permission, record.Rule.Selection.Selector.Kind, string(record.Rule.Selection.Selector.Target)}
			if seenRules[key] {
				return failed("unavailable")
			}
			seenRules[key] = true
			records = append(records, record)
		}
		if len(records) > 4096 {
			return failed("unavailable")
		}
		if page.Complete {
			break
		}
		if page.Next == "" || page.Next == cursor {
			return failed("unavailable")
		}
		cursor = page.Next
	}
	now := time.Now()
	for _, record := range records {
		selector := presentSelector(record.Rule.Selection)
		served := false
		for _, definition := range definitions {
			key := wire.PermissionKey{Action: definition.Definition.Action, ResourceProfile: definition.Definition.ResourceProfile}
			if key == selector.Permission && definition.Editable {
				for _, kind := range definition.SupportedSelectors {
					served = served || kind.String() == selector.Scope
				}
			}
		}
		selector.CanGrant = selector.Editable && served
		selector.Origin = presentBundleOrigin(record.Rule.Subject, record.BundleOrigin, records, now)
		setter := record.SetBy
		row := policyRuleView{Subject: record.Rule.Subject, Action: record.Rule.Selection.Permission.Action, Resource: selector.Target, Permit: record.Rule.Permit, SetBy: &setter, SetAt: record.SetAt, Why: record.Why, Expires: record.Expires, Selector: &selector}
		if record.Expires != "" {
			at, err := time.Parse(time.RFC3339Nano, record.Expires)
			row.Expired = err != nil || !at.After(now)
		}
		view.Rules = append(view.Rules, row)
	}
	catalog := make([]string, 0, len(view.Catalog))
	for _, action := range view.Catalog {
		if exactActions[action] {
			catalog = append(catalog, action)
		}
	}
	view.Catalog = catalog
	view.SelectorAvailable, view.Definitions, view.Bundles = true, definitions, bundles
	return view
}

// A description joins the original editable rule snapshot only at its exact
// content revision. FeatureApp is metadata; every row retains its own target.
func joinPolicyDescriptions(ctx context.Context, view policyPageView, reader policyDescriptionReader) policyPageView {
	if view.Outcome != "page" {
		return view
	}
	failed := func(outcome string) policyPageView {
		return policyPageView{Outcome: outcome, Rules: []policyRuleView{}}
	}
	items := map[string]rightsclient.DescribedAction{}
	cursor := ""
	for pages := 0; pages < 64; pages++ {
		page, err := reader.ListActionDescriptionsContext(ctx, cursor, 64)
		if err != nil {
			var service *rightsclient.ServiceError
			if pages == 0 && errors.As(err, &service) && service.Code == wire.ServiceErrorCodeUnknownMethod {
				return view
			}
			return failed("unavailable")
		}
		if page.Outcome != wire.PolicyPageOutcomePage {
			return failed(page.Outcome.String())
		}
		if page.Revision != view.Revision {
			return failed("gap")
		}
		for _, item := range page.Actions {
			if _, duplicate := items[item.Action]; duplicate {
				return failed("unavailable")
			}
			items[item.Action] = item
		}
		if len(items) > 64 {
			return failed("unavailable")
		}
		if page.Complete {
			if len(items) != len(view.Catalog) {
				return failed("gap")
			}
			plain := make(map[string]string, len(view.ActionPlain))
			for action, phrase := range view.ActionPlain {
				plain[action] = phrase
			}
			for _, action := range view.Catalog {
				item, found := items[action]
				if !found {
					return failed("gap")
				}
				if d := item.Description; d != nil {
					plain[action] = d.AppLabel + ": " + d.Description
				}
			}
			view.ActionPlain, view.Descriptions = plain, items
			return view
		}
		if page.Next == "" || page.Next == cursor {
			return failed("unavailable")
		}
		cursor = page.Next
	}
	return failed("unavailable")
}
