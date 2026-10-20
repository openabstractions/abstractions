package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	"github.com/openabstractions/abstraction-facade/go/grants"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestPermissionSecretNameSuggestionsUseOwnerMetadata(t *testing.T) {
	page := cwire.MetadataPage{Outcome: cwire.PageOutcomePage, Next: "next-page", Records: []cwire.Metadata{
		{Name: "internal-local", Kind: credentials.KindLocalKey},
		{Name: "unrecognized", Kind: "unknown-kind"},
	}}
	for _, kind := range credentials.SupportedKinds {
		page.Records = append(page.Records, cwire.Metadata{Name: "saved-" + kind, Kind: kind})
	}
	view := presentCredentialNames(page)
	if view.Outcome != "page" || view.Next != page.Next || view.Complete || len(view.Names) != len(credentials.SupportedKinds) {
		t.Fatalf("owner metadata projection %+v", view)
	}
	for i, name := range view.Names {
		if name != "saved-"+credentials.SupportedKinds[i] {
			t.Fatalf("internal or unsupported kind suggested %q", name)
		}
	}
	own(t)
	panelCredentialsRuntime(t)
	h := (&servicePanel{}).handler("test-key")
	stored := panelRequest(t, h, "/credentials", credentialEdit{Action: "add", Name: "saved-alpha", Kind: "bearer", Targets: []string{"example.com"}, Consumers: []string{"abstraction.download/http-execution@1"}, Secret: "NEVER-IN-PERMISSION-STATE"})
	if stored.Code != 200 || !strings.Contains(stored.Body.String(), `"Outcome":"stored"`) {
		t.Fatalf("fixture stored %d %s", stored.Code, stored.Body.String())
	}
	r := panelRequest(t, h, "/rights?credential_names=1&cursor=", nil)
	got := decodePanel[credentialNamesView](t, r.Code, r.Body.Bytes())
	if got.Outcome != "page" || !got.Complete || len(got.Names) != 1 || got.Names[0] != "saved-alpha" || strings.Contains(r.Body.String(), "NEVER-IN-PERMISSION-STATE") || strings.Contains(r.Body.String(), "Targets") || strings.Contains(r.Body.String(), "Kind") {
		t.Fatalf("native names-only projection %s", r.Body.String())
	}
	if r := panelRequest(t, h, "/rights?credential_names=1&cursor="+strings.Repeat("x", 257), nil); r.Code != 400 {
		t.Fatalf("unbounded metadata cursor %d", r.Code)
	}
}

// This fixture installs the production catalogue with an explicit test receiver
// presence assumption. The real native Rights API owns decisions and mutations.
// It exercises public permission management, not Credentials receiving effects.
func panelSelectorRuntime(t *testing.T, allow, outage *atomic.Bool) {
	t.Helper()
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{Credentials: true})
	if err != nil {
		t.Fatal(err)
	}
	panelRightsRuntimeWithDefinitions(t, allow, outage, catalog)
}

func TestServicePanelSelectorRightsUsesNativePublicPolicy(t *testing.T) {
	own(t)
	var allow, outage atomic.Bool
	panelSelectorRuntime(t, &allow, &outage)
	h := (&servicePanel{}).handler("test-key")
	target := rightsSubject(exploreSelf())
	list := func() policyPageView {
		r := panelRequest(t, h, "/rights?cursor=", nil)
		return decodePanel[policyPageView](t, r.Code, r.Body.Bytes())
	}
	edit := func(e rightsEdit) selectorEditView {
		r := panelRequest(t, h, "/rights", e)
		return decodePanel[selectorEditView](t, r.Code, r.Body.Bytes())
	}
	use := func(revision, scope, name string, permit bool) rightsEdit {
		return rightsEdit{Edit: "set", Revision: revision, Account: target.Account, Program: target.Program,
			Action: credentials.ActionApply, ResourceProfile: credentials.ScopedUseResourceProfile, Scope: scope, CredentialName: name, Permit: permit}
	}
	if page := list(); page.Outcome != "forbidden" || page.SelectorAvailable || len(page.Rules) != 0 {
		t.Fatalf("unauthorized catalogue %+v", page)
	}
	if result := edit(use("any-revision", "all", "", true)); result.Outcome != "forbidden" || result.Revision != "" {
		t.Fatalf("unauthorized typed edit %+v", result)
	}
	allow.Store(true)
	empty := list()
	if empty.Outcome != "page" || !empty.SelectorAvailable || len(empty.Bundles) != 1 || !empty.Bundles[0].Editable || len(empty.Bundles[0].Members) != 6 {
		t.Fatalf("public catalogue %+v", empty)
	}
	for _, action := range empty.Catalog {
		if action == credentials.ActionSecretRead {
			t.Fatal("typed-only Read offered as a legacy exact permission")
		}
	}
	checker, err := panelMachine().ResolveRightsSelector(context.Background(), exploreLocal)
	if err != nil {
		t.Fatal(err)
	}
	check := func(revision, action, profile string, wantAlpha, wantBeta wire.DecisionOutcome) {
		t.Helper()
		key := wire.PermissionKey{Action: action, ResourceProfile: profile}
		request := wire.CheckBatchRequest{ExpectedRevision: revision, CollectionAdmission: &key}
		for _, name := range []string{"alpha", "beta"} {
			raw, err := credentials.PrepareCredentialTarget(name)
			if err != nil {
				t.Fatal(err)
			}
			request.Resources = append(request.Resources, wire.ConcreteResource{Permission: key, Target: raw})
		}
		result, err := checker.CheckBatchContext(context.Background(), request)
		if err != nil || result.Outcome != wire.BatchCheckOutcomeChecked || result.Revision != revision || len(result.Outcomes) != 2 || result.Outcomes[0] != wantAlpha || result.Outcomes[1] != wantBeta {
			t.Fatalf("native public decisions %+v %v, want %s/%s", result, err, wantAlpha, wantBeta)
		}
	}
	allDraft := use(empty.Revision, "all", "", true)
	allDraft.TTLMS, allDraft.Why = 300000, "installation"
	all := edit(allDraft)
	if all.Outcome != "applied" || len(all.Current) != 1 || all.Current[0].Selector.Scope != "all" || all.Current[0].SetBy == nil || all.Current[0].SetBy.Account != target.Account || all.Current[0].SetBy.Program != target.Program || all.Current[0].Expires == "" || all.Current[0].Why != "installation" {
		t.Fatalf("All provenance/lifetime %+v", all)
	}
	check(all.Revision, credentials.ActionApply, credentials.ScopedUseResourceProfile, wire.DecisionOutcomePermitted, wire.DecisionOutcomePermitted)
	stale := edit(use(empty.Revision, "one", "alpha", false))
	if stale.Outcome != "conflict" || stale.Revision != all.Revision {
		t.Fatalf("stale typed draft %+v", stale)
	}
	check(all.Revision, credentials.ActionApply, credentials.ScopedUseResourceProfile, wire.DecisionOutcomePermitted, wire.DecisionOutcomePermitted)
	blocked := edit(use(all.Revision, "one", "alpha", false))
	if blocked.Outcome != "applied" || blocked.Current[0].Selector.Name != "alpha" || blocked.Current[0].Permit {
		t.Fatalf("exact typed Block %+v", blocked)
	}
	check(blocked.Revision, credentials.ActionApply, credentials.ScopedUseResourceProfile, wire.DecisionOutcomeDenied, wire.DecisionOutcomePermitted)
	// A legacy exact Block shares the revision and still constrains typed All.
	r := panelRequest(t, h, "/rights", rightsEdit{Edit: "set", Revision: blocked.Revision, Account: target.Account, Program: target.Program, Action: credentials.ActionApply, Resource: "credential:beta", Permit: false})
	legacy := decodePanel[wire.PolicyEdit](t, r.Code, r.Body.Bytes())
	if legacy.Outcome != wire.PolicyEditOutcomeApplied {
		t.Fatalf("legacy Block %+v", legacy)
	}
	check(legacy.Revision, credentials.ActionApply, credentials.ScopedUseResourceProfile, wire.DecisionOutcomeDenied, wire.DecisionOutcomeDenied)
	r = panelRequest(t, h, "/rights", rightsEdit{Edit: "revoke", Revision: legacy.Revision, Account: target.Account, Program: target.Program, Action: credentials.ActionApply, Resource: "credential:beta"})
	legacy = decodePanel[wire.PolicyEdit](t, r.Code, r.Body.Bytes())
	remove := use(legacy.Revision, "one", "alpha", false)
	remove.Edit = "revoke"
	removed := edit(remove)
	if removed.Outcome != "applied" {
		t.Fatalf("remove One %+v", removed)
	}
	check(removed.Revision, credentials.ActionApply, credentials.ScopedUseResourceProfile, wire.DecisionOutcomePermitted, wire.DecisionOutcomePermitted)
	bundle := edit(rightsEdit{Edit: "set", Revision: removed.Revision, Account: target.Account, Program: target.Program, BundleRef: credentials.FullControlBundle, Scope: "one", CredentialName: "alpha", Permit: true, TTLMS: 300000, Why: "finite operation test"})
	if bundle.Outcome != "applied" || len(bundle.Current) != 6 || bundle.BundleOrigin == nil || bundle.BundleOrigin.Original != 6 || bundle.BundleOrigin.Digest != empty.Bundles[0].DefinitionDigest {
		t.Fatalf("frozen bundle %+v", bundle)
	}
	page := list()
	if len(page.Rules) != 7 || page.Revision != bundle.Revision {
		t.Fatalf("joined bundle %+v", page)
	}
	for _, row := range page.Rules {
		if row.Selector.Origin != nil && (row.Selector.Origin.State != "complete original grant" || row.Selector.Origin.Remaining != 6 || len(row.Selector.Origin.Members) != 6) {
			t.Fatalf("complete original expansion %+v", row)
		}
	}
	readBlock := use(bundle.Revision, "one", "alpha", false)
	readBlock.Action, readBlock.ResourceProfile = credentials.ActionSecretRead, credentials.NamedSecretResourceProfile
	read := edit(readBlock)
	if read.Outcome != "applied" {
		t.Fatalf("separate Read Block %+v", read)
	}
	check(read.Revision, credentials.ActionSecretRead, credentials.NamedSecretResourceProfile, wire.DecisionOutcomeDenied, wire.DecisionOutcomeNotGranted)
	page = list()
	for _, row := range page.Rules {
		if row.Selector.Origin != nil && (row.Selector.Origin.State != "partial/changed" || row.Selector.Origin.Remaining != 5 || row.Selector.Origin.Original != 6 || len(row.Selector.Origin.Members) != 6) {
			t.Fatalf("partial original expansion %+v", row)
		}
	}
	revoked := edit(rightsEdit{Edit: "remove_bundle", Revision: read.Revision, Account: target.Account, Program: target.Program, BundleOriginID: bundle.BundleOrigin.ID})
	if revoked.Outcome != "applied" || len(list().Rules) != 2 {
		t.Fatalf("remove remaining origin rules %+v", revoked)
	}
	check(revoked.Revision, credentials.ActionSecretRead, credentials.NamedSecretResourceProfile, wire.DecisionOutcomeDenied, wire.DecisionOutcomeNotGranted)
	check(revoked.Revision, credentials.ActionApply, credentials.ScopedUseResourceProfile, wire.DecisionOutcomePermitted, wire.DecisionOutcomePermitted)
	removeAll := use(revoked.Revision, "all", "", false)
	removeAll.Edit = "revoke"
	outage.Store(true)
	if result := edit(removeAll); result.Outcome != "unavailable" || result.Revision != "" || len(result.Current) != 0 {
		t.Fatalf("operator outage %+v", result)
	}
	outage.Store(false)
	allow.Store(false)
	if result := edit(removeAll); result.Outcome != "forbidden" || result.Revision != "" {
		t.Fatalf("operator refusal %+v", result)
	}
	allow.Store(true)
	check(revoked.Revision, credentials.ActionApply, credentials.ScopedUseResourceProfile, wire.DecisionOutcomePermitted, wire.DecisionOutcomePermitted)
	for _, bad := range []rightsEdit{
		use(revoked.Revision, "one", "bad/name", true),
		use(revoked.Revision, "all", "alpha", true),
		{Edit: "set", Revision: revoked.Revision, Account: target.Account, Program: target.Program, Action: credentials.ActionSecretRead, ResourceProfile: "unknown/profile@1", Scope: "all", Permit: true},
		{Edit: "set", Revision: revoked.Revision, Account: target.Account, Program: target.Program, BundleRef: credentials.FullControlBundle, Scope: "one", CredentialName: "alpha", Permit: false},
	} {
		if r := panelRequest(t, h, "/rights", bad); r.Code != 400 || !strings.Contains(r.Body.String(), "invalid rights edit") {
			t.Fatalf("invalid typed request reached service: %d %s", r.Code, r.Body.String())
		}
	}
}
