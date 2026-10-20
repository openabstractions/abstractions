package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestNativeRawReadMissingPermissionAnswerRetryRevoke(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := credentialsRuntime(t)
	for _, name := range []string{"read-alpha", "read-beta"} {
		out, diag, err := runCredentials(t, testCredentialSecret+"\n", "add", name, "--target", "127.0.0.1", "--for", "abstraction.download/http-execution@1", "--from-stdin", "--endpoint", options.endpoint, "--timeout", "30s")
		requireUserScopeAdd(t, err, out+diag)
		if bytes.Contains([]byte(out+diag), []byte(testCredentialSecret)) {
			t.Fatal("setup disclosed value")
		}
	}
	ctx := context.Background()
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	reader, err := machine.ResolveCredentialReader(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	if r, e := reader.Read(ctx, "read-alpha"); e != nil || r.Outcome != cwire.SecretReadOutcomeForbidden || r.Value != nil {
		t.Fatal("legacy authority implied raw Read")
	}
	permission, err := machine.ResolveAsksPermissionOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := machine.ResolveRightsSelectorOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	page, err := permission.ListPermissionsContext(ctx, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	var effect rwire.PermissionEffect
	for _, record := range page.Records {
		if record.Key != asks.TypedPermissionKey || record.Option != "" {
			continue
		}
		details, e := askclient.ReadPermissionDetailsContext(ctx, permission, probeSelf().Account, record.ID)
		if e != nil || details.Effect == nil {
			t.Fatal("typed permission details unavailable")
		}
		if len(details.Effect.Selections) != 1 {
			continue
		}
		selection := details.Effect.Selections[0]
		if selection.Permission.Action != credentials.ActionSecretRead {
			continue
		}
		target, e := credentials.DecodeCredentialTarget(selection.Selector.Target)
		if e != nil || target.Name != "read-alpha" || selection.Permission.ResourceProfile != credentials.NamedSecretResourceProfile || selection.Selector.Kind != rwire.ResourceSelectorKindOne || details.Effect.Subject != (rwire.Subject{Account: probeSelf().Account, Program: probeSelf().Program}) || id != "" {
			t.Fatal("raw Read ask widened receiving effect")
		}
		id, effect = record.ID, *details.Effect
	}
	if id == "" {
		t.Fatal("missing raw Read produced no owning OA ask")
	}
	answered, err := grants.AnswerPermissionQuestion(ctx, permission, policy, effect.Subject.Account, id, "allow")
	if err != nil || answered.Edit == nil || answered.Edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal("recorded answer did not atomically permit raw Read")
	}
	read, err := reader.Read(ctx, "read-alpha")
	if err != nil || read.Outcome != cwire.SecretReadOutcomeRead || read.Value == nil || read.Value.Name != "read-alpha" || read.Value.Revision == "" {
		t.Fatal("named retry failed")
	}
	equal := bytes.Equal(read.Value.Secret, []byte(testCredentialSecret))
	clear(read.Value.Secret)
	if !equal {
		t.Fatal("named retry returned wrong value")
	}
	if r, e := reader.Read(ctx, "read-beta"); e != nil || r.Outcome != cwire.SecretReadOutcomeForbidden || r.Value != nil {
		t.Fatal("alpha permission disclosed beta")
	}
	selection := effect.Selections[0]
	rule, err := policy.ReadSelectorRuleContext(ctx, effect.Subject, selection)
	if err != nil || rule.Record == nil || !rule.Record.Rule.Permit {
		t.Fatal("exact permit missing")
	}
	removed, err := policy.RemoveSelectorRuleContext(ctx, rule.Revision, effect.Subject, selection)
	if err != nil || removed.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal("permit revoke failed")
	}
	if r, e := reader.Read(ctx, "read-alpha"); e != nil || r.Outcome != cwire.SecretReadOutcomeForbidden || r.Value != nil {
		t.Fatal("revoked Read still disclosed bytes")
	}
	if replay, e := policy.ApplyPermissionAllowContext(ctx, removed.Revision, id); e != nil || replay.Outcome != rwire.PolicyEditOutcomeConflict {
		t.Fatal("recorded answer revived revoked permission")
	}
	// Inspect retained state as well as public results. This fixture uses the
	// real platform store selected by credentialsRuntime and removes its items.
	if e := filepath.WalkDir(options.stateDir, func(path string, item os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() {
			if path != options.stateDir && item.Name() != "credentials" && item.Name() != "rights" && item.Name() != "asks" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".lock" {
			return nil
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if bytes.Contains(data, []byte(testCredentialSecret)) {
			t.Fatal("value entered runtime state, audit or Asks history")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestNativeFullControlAllUsesExplicitDownloadRouteAndScopedManagement(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := credentialsRuntime(t)
	for _, name := range []string{"use-alpha", "use-beta"} {
		out, diag, err := runCredentials(t, testCredentialSecret+"\n", "add", name, "--target", "127.0.0.1", "--for", "abstraction.download/http-execution@1", "--from-stdin", "--endpoint", options.endpoint, "--timeout", "30s")
		requireUserScopeAdd(t, err, out+diag)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	policy, err := machine.ResolveRightsSelectorOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: probeSelf().Account, Program: probeSelf().Program}
	// An original exact Use grant cannot authorize the explicit new route.
	if _, _, err := runCredentials(t, "", "allow", "use-alpha", "--program", subject.Program, "--endpoint", options.endpoint, "--timeout", "30s"); err != nil {
		t.Fatal("legacy control permit failed")
	}
	origin := newGatedOrigin(t, []byte("explicit scoped Use fixture"))
	required := append(slices.Clone(api.AdmissionGuarantees), request.ScopedCredentialGuarantees...)
	jobs, err := machine.ResolveJobs(ctx, client.Requirements{Scope: client.ScopeLocal, Guarantees: required})
	if err != nil {
		t.Fatal("actual scoped execution route not advertised")
	}
	operations, err := machine.ResolveJobOperations(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	window, err := jobs.GetHistoryWindow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	submit := func(name, key string) api.AcceptanceResult {
		t.Helper()
		result, e := jobs.Submit(ctx, api.Submission{Identity: api.RequestIdentity{HistoryEpoch: window.HistoryEpoch, Key: key}, Kind: download.Kind, RequiredGuarantees: required, Spec: request.Encode(&request.Request{Sources: []request.Source{{Scheme: "http", Locator: origin.URL + "/" + key + ".bin", Credential: name}}})})
		if e != nil {
			t.Fatal("scoped submission transport failed")
		}
		return result
	}
	if r := submit("use-alpha", "missing"); r.Outcome != api.AcceptanceOutcomeForbidden {
		t.Fatal("legacy exact permission implied typed Use")
	}
	if len(origin.authorizations()) != 0 {
		t.Fatal("missing typed permission reached origin")
	}
	assertCredentialAuditOutcome(t, options.endpoint, "not_granted")
	permission, err := machine.ResolveAsksPermissionOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	page, err := permission.ListPermissionsContext(ctx, "", 64)
	if err != nil || len(page.Records) != 1 {
		t.Fatal("missing typed Use did not produce one OA ask")
	}
	details, err := askclient.ReadPermissionDetailsContext(ctx, permission, subject.Account, page.Records[0].ID)
	if err != nil || details.Effect == nil || details.Effect.Subject != subject || len(details.Effect.Selections) != 1 || details.Effect.Selections[0].Permission != (rwire.PermissionKey{Action: credentials.ActionApply, ResourceProfile: credentials.ScopedUseResourceProfile}) {
		t.Fatal("typed Use ask lost receiving identity/version")
	}
	target, err := credentials.DecodeCredentialTarget(details.Effect.Selections[0].Selector.Target)
	if err != nil || target.Name != "use-alpha" {
		t.Fatal("typed Use ask widened name")
	}
	rule, err := policy.ReadSelectorRuleContext(ctx, subject, details.Effect.Selections[0])
	if err != nil {
		t.Fatal(err)
	}
	expansion, err := policy.ApplyBundleContext(ctx, rule.Revision, subject, credentials.FullControlBundle, rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindAll}, 0, "native fixture full control")
	if err != nil || expansion.Outcome != rwire.PolicyEditOutcomeApplied || expansion.BundleOrigin == nil || len(expansion.BundleOrigin.Expansion.Selections) != 6 {
		t.Fatal("frozen six-member bundle not applied atomically")
	}
	for _, name := range []string{"use-alpha", "use-beta"} {
		result := submit(name, name)
		if result.Outcome != api.AcceptanceOutcomeAccepted || result.Receipt == nil || !slices.Contains(result.Receipt.AcceptedGuarantees, downloadserve.ScopedCredentialGuarantee) {
			t.Fatal("All Use did not admit explicit route")
		}
		for {
			observed, e := operations.ObserveWork(ctx, result.Receipt.Identity)
			if e != nil || observed.Snapshot == nil {
				t.Fatal("scoped work observation failed")
			}
			if observed.Snapshot.State == api.WorkStateComplete {
				break
			}
			if observed.Snapshot.Failure != nil || ctx.Err() != nil {
				t.Fatal("scoped Use did not complete")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if headers := origin.authorizations(); len(headers) != 2 || headers[0] != "Bearer "+testCredentialSecret || headers[1] != "Bearer "+testCredentialSecret {
		t.Fatal("designated service did not apply both named secrets")
	}
	reader, err := machine.ResolveCredentialReader(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	read, err := reader.Read(ctx, "use-beta")
	if err != nil || read.Outcome != cwire.SecretReadOutcomeRead || read.Value == nil {
		t.Fatal("bundle raw Read member unavailable")
	}
	clear(read.Value.Secret)
	holder, err := machine.ResolveScopedCredentials(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	created, err := holder.Store(ctx, "", cwire.Registration{Name: "future-gamma", Kind: "bearer", Secret: []byte("future fixture value"), Scope: cwire.Scope{Targets: []string{"127.0.0.1"}, Consumers: []string{"abstraction.download/http-execution@1"}}})
	if err != nil || created.Outcome != cwire.StoreOutcomeStored {
		t.Fatal("All Create did not cover future slot")
	}
	rotated, err := holder.Rotate(ctx, created.Revision, cwire.Rotation{Name: "future-gamma", Secret: []byte("rotated fixture value")})
	if err != nil || rotated.Outcome != cwire.RotateOutcomeRotated {
		t.Fatal("bundle Rotate member unavailable")
	}
	metadata, err := holder.Metadata(ctx, "future-gamma")
	if err != nil || metadata.Outcome != cwire.MetadataReadOutcomeFound || metadata.Current == nil || metadata.Current.Revision != rotated.Revision {
		t.Fatal("bundle named Metadata member unavailable")
	}
	if removed, e := holder.Revoke(ctx, rotated.Revision, "future-gamma"); e != nil || removed.Outcome != cwire.RevokeOutcomeRevoked {
		t.Fatal("bundle Revoke member unavailable")
	}
	// Explicit blocks fence both versions while each retains its own permits.
	selection := details.Effect.Selections[0]
	rule, err = policy.ReadSelectorRuleContext(ctx, subject, selection)
	if err != nil {
		t.Fatal(err)
	}
	block, err := policy.SetSelectorRuleContext(ctx, rule.Revision, rwire.SelectorRule{Subject: subject, Selection: selection, Permit: false}, 0, "native fixture block")
	if err != nil || block.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal("typed block failed")
	}
	if r := submit("use-alpha", "typed-blocked"); r.Outcome != api.AcceptanceOutcomeForbidden {
		t.Fatal("typed block did not stop typed route")
	}
	assertCredentialAuditOutcome(t, options.endpoint, "denied")
	var out, diag bytes.Buffer
	if err := downloadCommand([]string{origin.URL + "/legacy-blocked.bin", "--credential", "use-alpha", "--endpoint", options.endpoint, "--out", t.TempDir(), "--timeout", "30s"}, &out, &diag); err == nil {
		t.Fatal("version switch bypassed explicit block")
	}
	if len(origin.authorizations()) != 2 {
		t.Fatal("blocked route reached origin")
	}
	assertCredentialAuditOutcome(t, options.endpoint, "denied")
}
