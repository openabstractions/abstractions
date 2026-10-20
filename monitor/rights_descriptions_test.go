package main

import (
	"context"
	"encoding/json"
	"errors"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"strings"
	"testing"
	"time"
)

type descriptionPages struct {
	pages []rightsclient.DescriptionPage
	err   error
	calls int
}

type selectorJoinPages struct {
	definitions wire.PermissionDefinitionPage
	bundles     wire.PermissionBundlePage
	rules       []wire.SelectorRulePage
	err         error
	calls       int
}

func (f *selectorJoinPages) ListPermissionDefinitionsContext(context.Context, string, int64) (wire.PermissionDefinitionPage, error) {
	return f.definitions, f.err
}
func (f *selectorJoinPages) ListPermissionBundlesContext(context.Context, string, int64) (wire.PermissionBundlePage, error) {
	return f.bundles, nil
}
func (f *selectorJoinPages) ListSelectorRulesContext(context.Context, string, int64) (wire.SelectorRulePage, error) {
	page := f.rules[f.calls]
	f.calls++
	return page, nil
}

func TestPolicySelectorJoinRevisionSafeTargetsAndOrigins(t *testing.T) {
	subject := wire.Subject{Account: "account", Program: "/apps/requester"}
	target, err := credentials.PrepareCredentialTarget("demo-secret")
	if err != nil {
		t.Fatal(err)
	}
	keys := []wire.PermissionKey{{Action: credentials.ActionApply, ResourceProfile: credentials.ScopedUseResourceProfile}}
	for _, action := range []string{credentials.ActionSecretRead, credentials.ActionSecretCreate, credentials.ActionSecretRotate, credentials.ActionSecretRevoke, credentials.ActionSecretMetadataRead} {
		keys = append(keys, wire.PermissionKey{Action: action, ResourceProfile: credentials.NamedSecretResourceProfile})
	}
	origin := &wire.BundleOrigin{ID: "original-application", Expansion: wire.BundleExpansion{BundleRef: credentials.FullControlBundle, DefinitionDigest: "frozen-v1", Selector: wire.ResourceSelector{Kind: wire.ResourceSelectorKindOne, Target: target}}}
	fixture := &selectorJoinPages{definitions: wire.PermissionDefinitionPage{Outcome: wire.PolicyPageOutcomePage, Revision: "one", Complete: true}, bundles: wire.PermissionBundlePage{Outcome: wire.PolicyPageOutcomePage, Revision: "one", Complete: true}, rules: []wire.SelectorRulePage{{Outcome: wire.PolicyPageOutcomePage, Revision: "one", Complete: true}}}
	for _, key := range keys {
		selection := wire.PermissionSelection{Permission: key, Selector: origin.Expansion.Selector}
		origin.Expansion.Selections = append(origin.Expansion.Selections, selection)
		fixture.definitions.Definitions = append(fixture.definitions.Definitions, wire.SelectorPermissionDefinition{Definition: wire.PermissionDefinition{Action: key.Action, ResourceProfile: key.ResourceProfile, Served: true, FirstUse: wire.PermissionFirstUseNone}, Label: key.Action, SupportedSelectors: []wire.ResourceSelectorKind{wire.ResourceSelectorKindOne, wire.ResourceSelectorKindAll}, Collection: credentials.SavedSecretCollection})
		fixture.rules[0].Records = append(fixture.rules[0].Records, wire.SelectorRuleRecord{Rule: wire.SelectorRule{Subject: subject, Selection: selection, Permit: true}, SetBy: wire.Subject{Account: "account", Program: "/apps/operator"}, SetAt: "2026-10-05T12:00:00Z", BundleOrigin: origin})
	}
	fixture.bundles.Bundles = []wire.PermissionBundleDefinition{{BundleRef: credentials.FullControlBundle, DefinitionDigest: "frozen-v1", Members: keys, SupportedSelectors: []wire.ResourceSelectorKind{wire.ResourceSelectorKindOne, wire.ResourceSelectorKindAll}}}
	fixture.definitions.Definitions = append(fixture.definitions.Definitions, wire.SelectorPermissionDefinition{Definition: wire.PermissionDefinition{Action: "old/action", ResourceProfile: rights.LegacyExactResourceProfile, Served: true, FirstUse: wire.PermissionFirstUseNone}})
	view := policyPageView{Outcome: "page", Revision: "one", Catalog: []string{"old/action", credentials.ActionSecretRead}, Rules: []policyRuleView{{Subject: subject, Action: "old/action", Resource: "*"}}}
	got := joinPolicySelectors(context.Background(), view, fixture, fixture)
	if got.Outcome != "page" || !got.SelectorAvailable || len(got.Rules) != 7 || got.Rules[0].Resource != "*" || !got.Bundles[0].Editable {
		t.Fatalf("joined view %+v", got)
	}
	if len(got.Catalog) != 1 || got.Catalog[0] != "old/action" {
		t.Fatalf("typed-only action offered through the legacy editor: %v", got.Catalog)
	}
	row := got.Rules[1]
	if row.Subject != subject || row.Selector.Name != "demo-secret" || !row.Selector.Editable || row.SetBy.Program != "/apps/operator" || row.Selector.Origin.Remaining != 6 || row.Selector.Origin.State != "complete original grant" {
		t.Fatalf("target/provenance %+v", row)
	}
	fixture.calls = 0
	fixture.rules[0].Records[1].BundleOrigin = nil
	got = joinPolicySelectors(context.Background(), view, fixture, fixture)
	if got.Rules[1].Selector.Origin.Remaining != 5 || got.Rules[1].Selector.Origin.State != "partial/changed" || len(got.Rules[1].Selector.Origin.Members) != 6 || len(got.Rules[1].Selector.Origin.LinkedMembers) != 5 {
		t.Fatal("individual edit lost original frozen expansion or still claimed complete grant")
	}
	fixture.calls = 0
	fixture.rules[0].Records[2].Expires = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
	got = joinPolicySelectors(context.Background(), view, fixture, fixture)
	if got.Rules[1].Selector.Origin.Remaining != 4 || len(got.Rules[1].Selector.Origin.LinkedMembers) != 5 || !got.Rules[3].Expired {
		t.Fatal("expiry still portrayed as a complete original grant")
	}
	fixture.calls = 0
	fixture.definitions.Definitions[0].Definition.Served = false
	fixture.definitions.Definitions[0].SupportedSelectors = nil
	got = joinPolicySelectors(context.Background(), view, fixture, fixture)
	if !got.Rules[1].Selector.Editable || got.Rules[1].Selector.CanGrant || got.Definitions[0].Editable || !got.Definitions[0].CodecAvailable {
		t.Fatal("known unserved rule lost safe removal or enabled a fresh grant")
	}
	fixture.calls = 0
	fixture.rules[0].Revision = "different"
	got = joinPolicySelectors(context.Background(), view, fixture, fixture)
	if got.Outcome != "gap" || got.Revision != "" || len(got.Rules) != 0 || got.SelectorAvailable {
		t.Fatal("mixed rule/definition/legacy revisions exposed editable state")
	}
}

func TestPolicySelectorOpaqueProfilesAndOldProviders(t *testing.T) {
	unknown := wire.PermissionKey{Action: "unknown/operation", ResourceProfile: "unknown/target@1"}
	fixture := &selectorJoinPages{definitions: wire.PermissionDefinitionPage{Outcome: wire.PolicyPageOutcomePage, Revision: "one", Definitions: []wire.SelectorPermissionDefinition{{Definition: wire.PermissionDefinition{Action: unknown.Action, ResourceProfile: unknown.ResourceProfile, FirstUse: wire.PermissionFirstUseNone}}}, Complete: true}, bundles: wire.PermissionBundlePage{Outcome: wire.PolicyPageOutcomePage, Revision: "one", Complete: true}, rules: []wire.SelectorRulePage{{Outcome: wire.PolicyPageOutcomePage, Revision: "one", Complete: true, Records: []wire.SelectorRuleRecord{{Rule: wire.SelectorRule{Selection: wire.PermissionSelection{Permission: unknown, Selector: wire.ResourceSelector{Kind: wire.ResourceSelectorKindOne, Target: []byte("RAW_SECRET_SENTINEL")}}}}}}}}
	view := policyPageView{Outcome: "page", Revision: "one", Catalog: []string{"legacy/exact"}, Rules: []policyRuleView{{Resource: "legacy exact"}}}
	got := joinPolicySelectors(context.Background(), view, fixture, fixture)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Definitions[0].Editable || got.Rules[1].Selector.Editable || strings.Contains(string(encoded), "RAW_SECRET_SENTINEL") || strings.Contains(string(encoded), "UkFXX1NFQ1JFVF9TRU5USU5FTA") {
		t.Fatal("opaque target exposed bytes or an editor")
	}
	edit := presentSelectorEdit(wire.SelectorEdit{Outcome: wire.PolicyEditOutcomeApplied, Revision: "one", Current: fixture.rules[0].Records})
	encoded, err = json.Marshal(edit)
	if err != nil || strings.Contains(string(encoded), "RAW_SECRET_SENTINEL") || strings.Contains(string(encoded), "UkFXX1NFQ1JFVF9TRU5USU5FTA") {
		t.Fatal("mutation projection exposed opaque target bytes")
	}
	fixture.err = &rightsclient.ServiceError{Code: wire.ServiceErrorCodeUnknownMethod}
	got = joinPolicySelectors(context.Background(), view, fixture, nil)
	if got.Outcome != "page" || got.SelectorAvailable || len(got.Rules) != 1 || len(got.Catalog) != 1 || got.Catalog[0] != "legacy/exact" {
		t.Fatal("explicit old method refusal lost exact fallback")
	}
	fixture.err = errors.New("outage")
	got = joinPolicySelectors(context.Background(), view, fixture, nil)
	if got.Outcome != "unavailable" || len(got.Rules) != 0 || got.Revision != "" {
		t.Fatal("outage exposed an editable combined snapshot")
	}
}

func (f *descriptionPages) ListActionDescriptionsContext(context.Context, string, int64) (rightsclient.DescriptionPage, error) {
	f.calls++
	if f.err != nil {
		return rightsclient.DescriptionPage{}, f.err
	}
	return f.pages[f.calls-1], nil
}

func TestPolicyDescriptionJoinKeepsRuleTargetsAndRevision(t *testing.T) {
	action := "opencode/log-source.share"
	target := rightsclient.Subject{Account: "rule account", Program: "rule program"}
	d := rightsclient.ActionDescription{Action: action, FeatureApp: rightsclient.Subject{Account: "feature account", Program: "feature program"}, AppLabel: "OpenCode", Description: "Share event contents", DeclaredRisk: "sensitive"}
	item := rightsclient.DescribedAction{Action: action, Description: &d}
	view := policyPageView{Outcome: "page", Revision: "original", Catalog: []string{action}, ActionPlain: rightsActionPlain, Rules: []policyRuleView{{Subject: target, Action: action, Resource: "exact-source", Permit: true}}, Complete: true}
	page := rightsclient.DescriptionPage{Outcome: wire.PolicyPageOutcomePage, Revision: "original", Actions: []rightsclient.DescribedAction{item}, Complete: true}
	got := joinPolicyDescriptions(context.Background(), view, &descriptionPages{pages: []rightsclient.DescriptionPage{page}})
	if got.Outcome != "page" || got.Revision != view.Revision || got.Rules[0].Subject != target || got.Rules[0].Resource != "exact-source" || got.ActionPlain[action] != "OpenCode: Share event contents" {
		t.Fatalf("joined wrong target/snapshot: %+v", got)
	}
	if rightsActionPlain[action] != "" {
		t.Fatal("publication changed global fallback phrases")
	}
	page.Revision = "new"
	if got := joinPolicyDescriptions(context.Background(), view, &descriptionPages{pages: []rightsclient.DescriptionPage{page}}); got.Outcome != "gap" || got.Revision != "" || len(got.Rules) != 0 {
		t.Fatalf("mixed revisions exposed editable rules: %+v", got)
	}
	page.Revision = "original"
	page.Complete = false
	page.Next = "cursor"
	next := page
	next.Revision = "changed"
	next.Complete = true
	next.Next = ""
	if got := joinPolicyDescriptions(context.Background(), view, &descriptionPages{pages: []rightsclient.DescriptionPage{page, next}}); got.Outcome != "gap" {
		t.Fatalf("continuation mixed revisions: %+v", got)
	}
}

func TestPolicyDescriptionsOldMethodAndFailureAreDistinct(t *testing.T) {
	view := policyPageView{Outcome: "page", Revision: "original", Rules: []policyRuleView{{Action: "old/action"}}}
	old := &descriptionPages{err: &rightsclient.ServiceError{Code: wire.ServiceErrorCodeUnknownMethod}}
	if got := joinPolicyDescriptions(context.Background(), view, old); got.Outcome != "page" || got.Revision != "original" || len(got.Rules) != 1 {
		t.Fatalf("old provider lost name-only compatibility: %+v", got)
	}
	for _, err := range []error{errors.New("outage"), context.Canceled} {
		if got := joinPolicyDescriptions(context.Background(), view, &descriptionPages{err: err}); got.Outcome != "unavailable" || got.Revision != "" || len(got.Rules) != 0 {
			t.Fatalf("failure exposed editable success: %+v", got)
		}
	}
}
