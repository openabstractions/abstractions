package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

const typedTestProfile = "example.resources/named@1"
const typedTestAction = "example.resources/read"

func typedRuntime(t *testing.T) (*runtimeAsks, rwire.Subject) {
	t.Helper()
	a, _ := firstUseBook(t)
	base, err := rights.NewDefinitionCatalog(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition := rwire.SelectorPermissionDefinition{Definition: rwire.PermissionDefinition{Action: typedTestAction, OwnerContract: "example.resources/reader@1", Operations: []string{"example.resources/reader@1#Read"}, ResourceProfile: typedTestProfile, ResourceType: "example.Target", EnforcedBy: []string{"example.resources/reader@1"}, Disclosure: "Returns raw values to this application", Lifecycle: "Account-owned name survives rotation", Served: true}, Label: "Read saved values", SupportedSelectors: []rwire.ResourceSelectorKind{rwire.ResourceSelectorKindOne, rwire.ResourceSelectorKindAll}, FirstUseSelectors: []rwire.ResourceSelectorKind{rwire.ResourceSelectorKindOne, rwire.ResourceSelectorKindAll}, Collection: "saved values in this account", AllEffect: "All present and future saved values", RequestType: "example.Target"}
	valid := func(b []byte) bool {
		v, e := rwire.DecodeRequest(b)
		return e == nil && v.Op == "named-target" && v.Name != "" && bytes.Equal(b, rwire.EncodeRequest(v))
	}
	catalog, err := base.WithSelectors([]rwire.SelectorPermissionDefinition{definition}, map[string]rights.ResourceProfile{typedTestProfile: {Prepare: func(b []byte) ([]byte, error) {
		if !valid(b) {
			return nil, errors.New("invalid owning target")
		}
		return slices.Clone(b), nil
	}, Validate: valid, Equal: bytes.Equal, AllowAll: true, Describe: func(b []byte) (string, error) {
		v, e := rwire.DecodeRequest(b)
		if e != nil {
			return "", e
		}
		return v.Name, nil
	}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicyWithDefinitions(filepath.Join(t.TempDir(), "rights.json"), []string{typedTestAction}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: a.rights.owner, Program: identity.CanonicalProgramPath(program)}
	a.rights.policy, a.rights.typedFirstUse = policy, a.admitTypedService
	return a, subject
}
func typedTestSelection(all bool) rwire.PermissionSelection {
	s := rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: typedTestAction, ResourceProfile: typedTestProfile}, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindAll}}
	if !all {
		s.Selector.Kind = rwire.ResourceSelectorKindOne
		s.Selector.Target = rwire.EncodeRequest(&rwire.Request{Op: "named-target", Name: "test saved value"})
	}
	return s
}

func TestRuntimeTypedPermissionNativeRequestAllowAtomicRetryUndo(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("fixture uses native non-XPC Program transport; no Mac proof claimed")
	}
	a, subject := typedRuntime(t)
	endpoint := listen.Endpoint(fmt.Sprintf("runtime-typed-%d-%d", os.Getpid(), time.Now().UnixNano()))
	rightsHost, err := rightsservice.Listen(endpoint+"-rights", a.rights.policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = rightsHost.EnableOperator(func(context.Context, *identity.Peer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = rightsHost.EnablePermissionEffects(a.applyPermissionAllow, a.undoPermissionEffect); err != nil {
		t.Fatal(err)
	}
	rightsDone := make(chan error, 1)
	go func() { rightsDone <- rightsHost.Serve(context.Background()) }()
	t.Cleanup(func() {
		rightsHost.Close()
		select {
		case err := <-rightsDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Rights host did not drain")
		}
	})
	h, err := asksservice.Listen(endpoint, a.book)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePermission(a.preparePermission); err != nil {
		t.Fatal(err)
	}
	if err = h.EnableOperator(func(context.Context, *identity.Peer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = h.EnableAskPolicy(func(context.Context, *identity.Peer, string) error { return errors.New("no ordinary asking grant") }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(context.Background()) }()
	t.Cleanup(func() {
		h.Close()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(2 * time.Second):
			t.Error("typed host did not drain")
		}
	})
	ctx := context.Background()
	app, operator := askclient.NewPermission(endpoint), askclient.NewPermissionOperator(endpoint)
	selection := typedTestSelection(true)
	intent := askswire.PermissionIntent{RequestKey: "typed-all", Selection: &selection, Purpose: "Read saved values"}
	observation, err := app.RequestContext(ctx, intent)
	if err != nil || observation.Outcome != askswire.PermissionOutcomePending || observation.Answer == nil {
		t.Fatalf("request %+v %v", observation, err)
	}
	id := observation.Answer.ID
	details, err := askclient.ReadPermissionDetailsContext(ctx, operator, subject.Account, id)
	if err != nil || details.Effect.Subject != subject || details.Display.Selections[0].AllEffect != "All present and future saved values" {
		t.Fatalf("details %+v %v", details, err)
	}
	if _, err = operator.AnswerPermissionIfPendingContext(ctx, id, "allow"); err != nil {
		t.Fatal(err)
	}
	policyClient := rightsclient.NewSelectorOperator(endpoint + "-rights")
	result, err := grants.RetryPermissionEffect(ctx, operator, policyClient, subject.Account, id)
	if err != nil || result.Edit == nil || result.Edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("effect %+v %v", result, err)
	}
	current := a.rights.policy.CheckPermissionEffectContext(ctx, *details.Effect, "")
	if permissionDisposition(current, 1) != askswire.PermissionOutcomePermitted {
		t.Fatalf("retry not permitted: %+v", current)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := policyClient.ApplyPermissionAllowContext(ctx, revision, id)
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeConflict {
		t.Fatalf("receipt replay %+v %v", edit, err)
	}
	revision, _, _ = a.rights.policy.SelectorSnapshotContext(ctx)
	edit, err = policyClient.UndoPermissionEffectContext(ctx, revision, id)
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("Undo %+v %v", edit, err)
	}
	current = a.rights.policy.CheckPermissionEffectContext(ctx, *details.Effect, "")
	if permissionDisposition(current, 1) != askswire.PermissionOutcomePending {
		t.Fatalf("Undo left permission %+v", current)
	}
	revision, _, _ = a.rights.policy.SelectorSnapshotContext(ctx)
	edit, err = policyClient.ApplyPermissionAllowContext(ctx, revision, id)
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeConflict || permissionDisposition(a.rights.policy.CheckPermissionEffectContext(ctx, *details.Effect, ""), 1) != askswire.PermissionOutcomePending {
		t.Fatalf("completed occurrence revived Undo: %+v %v", edit, err)
	}
	observation, err = app.RequestContext(ctx, intent)
	if err != nil || observation.Outcome != askswire.PermissionOutcomeAnswered || observation.Answer.ID != id {
		t.Fatalf("immutable request replay %+v %v", observation, err)
	}
}

func TestRuntimeTypedServiceAdmissionPreservesCapturedTarget(t *testing.T) {
	a, subject := typedRuntime(t)
	selection := typedTestSelection(false)
	operation := rwire.ConcreteResource{Permission: selection.Permission, Target: selection.Selector.Target}
	decision := a.rights.decideTypedAsking(context.Background(), subject, operation)
	if decision.Outcome != rwire.BatchCheckOutcomeChecked || decision.Outcomes[0] != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("decision %+v", decision)
	}
	records, _, err := a.book.ApplicationHistory()
	if err != nil || len(records) != 1 || records[0].TypedPermission == nil || records[0].TypedPermission.Origin != "service" || records[0].Publisher != "" || !bytes.Equal(records[0].TypedPermission.Effect.Selections[0].Selector.Target, operation.Target) {
		t.Fatalf("service admission %+v %v", records, err)
	}
	if _, _, err = a.book.AnswerTypedPermissionIfPendingAuthorized(records[0].ID, "refuse", func(asks.Record, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	a.rights.decideTypedAsking(context.Background(), subject, operation)
	records, _, err = a.book.ApplicationHistory()
	if err != nil || len(records) != 1 {
		t.Fatalf("refusal pacing %+v %v", records, err)
	}
}
