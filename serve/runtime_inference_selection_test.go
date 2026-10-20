package main

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

func TestSelectionOperatorV2UnpinnedCASAndBoundScope(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint, err := inferenceEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator := inferenceclient.NewSelectionOperatorV2(endpoint)
	policy := iwire.SelectionPolicyV2{Name: "offline", Candidates: []iwire.SelectionCandidateV2{
		{Model: "cloud-model", Placement: iwire.CandidatePlacementHosted, Credential: "cloudcred"},
		{Model: "local-model", Placement: iwire.CandidatePlacementLocal}}}
	if change, err := operator.Put(call, "", "", iwire.SelectionPolicyV2{Name: "invalid", Candidates: []iwire.SelectionCandidateV2{
		{Model: "local-model", Placement: iwire.CandidatePlacementLocal, Credential: "cloudcred"}}}); err != nil || change.Outcome != iwire.EditOutcomeInvalid {
		t.Fatalf("invalid local credential: %+v %v", change, err)
	}
	created, err := operator.Put(call, "", "", policy)
	if err != nil || created.Outcome != iwire.EditOutcomeApplied || created.Revision == "" {
		t.Fatalf("create: %+v %v", created, err)
	}
	read, err := operator.Read(call, "", policy.Name)
	if err != nil || read.Outcome != iwire.SelectionReadOutcomeFound || read.Revision != created.Revision || read.Policy == nil || len(read.Policy.Candidates) != 2 {
		t.Fatalf("read: %+v %v", read, err)
	}
	if conflict, err := operator.Put(call, "", "", policy); err != nil || conflict.Outcome != iwire.EditOutcomeConflict || conflict.Revision != created.Revision {
		t.Fatalf("stale create: %+v %v", conflict, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	store := &inferenceOperator{dir: filepath.Join(options.stateDir, "inference"), credentials: &runtimeCredentials{runtimeRights: &runtimeRights{owner: account.Uid}}}
	if got, found, err := store.ResolveSelectionV2(call, inference.Subject{Account: account.Uid, Program: filepath.Clean(self)}, policy.Name); err != nil || !found || !reflect.DeepEqual(got, policy) {
		t.Fatalf("reopened store lost unpinned policy: %+v %v %v", got, found, err)
	}
	if _, found, err := store.ResolveSelectionV2(call, inference.Subject{Account: account.Uid, Program: filepath.Join(t.TempDir(), "another-program")}, policy.Name); err != nil || found {
		t.Fatalf("different program read policy: found %v, err %v", found, err)
	}
	if _, found, err := store.ResolveSelectionV2(call, inference.Subject{Account: "another-account", Program: filepath.Clean(self)}, policy.Name); err == nil || found {
		t.Fatalf("different account read policy: found %v, err %v", found, err)
	}
	if removed, err := operator.DeletePolicy(call, "", created.Revision, policy.Name); err != nil || removed.Outcome != iwire.EditOutcomeApplied || removed.Revision == "" {
		t.Fatalf("delete: %+v %v", removed, err)
	}
	if unknown, err := operator.Read(call, "", policy.Name); err != nil || unknown.Outcome != iwire.SelectionReadOutcomeUnknown {
		t.Fatalf("read deleted: %+v %v", unknown, err)
	}
	if restored, err := operator.Put(call, "", "", policy); err != nil || restored.Outcome != iwire.EditOutcomeApplied || restored.Revision == created.Revision {
		t.Fatalf("recreate after delete: %+v %v", restored, err)
	}

	// An edit denial leaves the existing own-read scope available.
	rightsOperator, err := client.New(options.endpoint).ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := rightsOperator.ListPolicyContext(call, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("policy list: %+v %v", page, err)
	}
	deny := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}, Action: inference.ActionSelectionManage, Resource: "account", Permit: false}
	if edit, err := rightsOperator.SetRuleContext(call, page.Revision, deny); err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("deny selection.manage: %+v %v", edit, err)
	}
	if got, err := operator.Read(call, "", policy.Name); err != nil || got.Outcome != iwire.SelectionReadOutcomeFound {
		t.Fatalf("own read after edit denial: %+v %v", got, err)
	}
	if got, err := operator.Put(call, "", "", policy); err != nil || got.Outcome != iwire.EditOutcomeForbidden {
		t.Fatalf("denied put: %+v %v", got, err)
	}
	if got, err := operator.DeletePolicy(call, "", "", policy.Name); err != nil || got.Outcome != iwire.EditOutcomeForbidden {
		t.Fatalf("denied delete: %+v %v", got, err)
	}
}

func TestSelectionPolicyBoundsAndPlacement(t *testing.T) {
	local := iwire.SelectionCandidateV2{Model: "local", Placement: iwire.CandidatePlacementLocal}
	for _, tc := range []struct {
		policy iwire.SelectionPolicyV2
		field  string
	}{
		{iwire.SelectionPolicyV2{Name: "valid", Candidates: []iwire.SelectionCandidateV2{local}}, ""},
		{iwire.SelectionPolicyV2{Name: "valid"}, "candidates"},
		{iwire.SelectionPolicyV2{Name: "valid", Candidates: []iwire.SelectionCandidateV2{local, local}}, "candidate.duplicate"},
		{iwire.SelectionPolicyV2{Name: "valid", Candidates: []iwire.SelectionCandidateV2{{Model: "cloud", Placement: iwire.CandidatePlacementHosted}}}, "candidate.credential"},
		{iwire.SelectionPolicyV2{Name: "valid", Candidates: []iwire.SelectionCandidateV2{{Model: "m", Placement: iwire.CandidatePlacement(99)}}}, "candidate.placement"},
	} {
		if got := validSelectionPolicyV2(tc.policy); got != tc.field {
			t.Fatalf("policy %+v: field %q, want %q", tc.policy, got, tc.field)
		}
	}
}

func TestSelectionOperatorV2PinAndScope(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint, err := inferenceEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v2 := inferenceclient.NewSelectionOperatorV2(endpoint)
	policy := iwire.SelectionPolicyV2{Name: "pinned", Candidates: []iwire.SelectionCandidateV2{{Model: "image-model", Placement: iwire.CandidatePlacementLocal, Provider: "comfyui"}}}
	created, err := v2.Put(ctx, "", "", policy)
	if err != nil || created.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("v2 put: %+v %v", created, err)
	}
	read, err := v2.Read(ctx, "", policy.Name)
	if err != nil || read.Outcome != iwire.SelectionReadOutcomeFound || read.Policy == nil || read.Policy.Candidates[0].Provider != "comfyui" {
		t.Fatalf("v2 read: %+v %v", read, err)
	}
	changedPolicy := iwire.SelectionPolicyV2{Name: policy.Name, Candidates: []iwire.SelectionCandidateV2{{Model: "different-model", Placement: iwire.CandidatePlacementLocal}}}
	if change, err := v2.Put(ctx, "", "", changedPolicy); err != nil || change.Outcome != iwire.EditOutcomeConflict || change.Revision != created.Revision {
		t.Fatalf("stale pinned Put: %+v %v", change, err)
	}
	if change, err := v2.DeletePolicy(ctx, "", "", policy.Name); err != nil || change.Outcome != iwire.EditOutcomeConflict || change.Revision != created.Revision {
		t.Fatalf("stale pinned DeletePolicy: %+v %v", change, err)
	}
	stillPinned, err := v2.Read(ctx, "", policy.Name)
	if err != nil || stillPinned.Outcome != iwire.SelectionReadOutcomeFound || stillPinned.Revision != created.Revision || stillPinned.Policy == nil || len(stillPinned.Policy.Candidates) != 1 || stillPinned.Policy.Candidates[0].Model != "image-model" || stillPinned.Policy.Candidates[0].Provider != "comfyui" {
		t.Fatalf("stale edits changed pinned policy: %+v %v", stillPinned, err)
	}
	otherProgram := filepath.Join(t.TempDir(), "comfyui.exe")
	otherPolicy := iwire.SelectionPolicyV2{Name: "target-app", Candidates: []iwire.SelectionCandidateV2{{Model: "local-model", Placement: iwire.CandidatePlacementLocal}}}
	managed, err := v2.Put(ctx, otherProgram, "", otherPolicy)
	if err != nil || managed.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("manage other program: %+v %v", managed, err)
	}
	if read, err := v2.Read(ctx, otherProgram, otherPolicy.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeFound {
		t.Fatalf("read other program: %+v %v", read, err)
	}
	if runtime.GOOS == "windows" {
		if read, err := v2.Read(ctx, strings.ToUpper(otherProgram), otherPolicy.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeFound {
			t.Fatalf("canonical Windows program: %+v %v", read, err)
		}
	}
	if read, err := v2.Read(ctx, "", otherPolicy.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeUnknown {
		t.Fatalf("self scope leaked other policy: %+v %v", read, err)
	}
	if read, err := v2.Read(ctx, "relative.exe", otherPolicy.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeInvalid {
		t.Fatalf("relative program: %+v %v", read, err)
	}
	if changed, err := v2.Put(ctx, "relative.exe", "", otherPolicy); err != nil || changed.Outcome != iwire.EditOutcomeInvalid || changed.Reason != "program" {
		t.Fatalf("invalid target program: %+v %v", changed, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	store := &inferenceOperator{dir: filepath.Join(options.stateDir, "inference"), credentials: &runtimeCredentials{runtimeRights: &runtimeRights{owner: account.Uid}}}
	if got, found, err := store.ResolveSelectionV2(ctx, inference.Subject{Account: account.Uid, Program: filepath.Clean(self)}, policy.Name); err != nil || !found || !reflect.DeepEqual(got, policy) {
		t.Fatalf("reopened store lost pinned policy: %+v %v %v", got, found, err)
	}
	if got, found, err := store.ResolveSelectionV2(ctx, inference.Subject{Account: account.Uid, Program: otherProgram}, otherPolicy.Name); err != nil || !found || got.Name != otherPolicy.Name {
		t.Fatalf("target app resolve: %+v %v %v", got, found, err)
	}
	if _, found, err := store.ResolveSelectionV2(ctx, inference.Subject{Account: "different-account", Program: otherProgram}, otherPolicy.Name); err == nil || found {
		t.Fatalf("cross-account resolve: %v %v", found, err)
	}
	rightsOperator, err := client.New(options.endpoint).ResolveRightsOperator(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := rightsOperator.ListPolicyContext(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	deny := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}, Action: inference.ActionSelectionManageFor, Resource: "account", Permit: false}
	if changed, err := rightsOperator.SetRuleContext(ctx, page.Revision, deny); err != nil || changed.Outcome.String() != "applied" {
		t.Fatalf("deny manage_for: %+v %v", changed, err)
	}
	if changed, err := v2.Put(ctx, otherProgram, managed.Revision, otherPolicy); err != nil || changed.Outcome != iwire.EditOutcomeForbidden {
		t.Fatalf("self grant managed other: %+v %v", changed, err)
	}
	if read, err := v2.Read(ctx, "", policy.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeFound {
		t.Fatalf("self grant denied: %+v %v", read, err)
	}
	if change, err := v2.DeletePolicy(ctx, "", created.Revision, policy.Name); err != nil || change.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("v2 pinned delete: %+v %v", change, err)
	}
}

// A program reads its own selection policies with no grant (INF-R1; VISION
// 2026-09-17); writing and deleting them keep selection.manage, and reading
// another program's keeps selection.manage_for. Before this rule an
// application read its own policy as forbidden, so the OpenCode plugin never
// saw the chat model the Panel chose for it.
func TestSelectionOperatorV2OwnReadNeedsNoGrant(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint, err := inferenceEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v2 := inferenceclient.NewSelectionOperatorV2(endpoint)
	own := iwire.SelectionPolicyV2{Name: "oa-chat-default", Candidates: []iwire.SelectionCandidateV2{{Model: "local-model", Placement: iwire.CandidatePlacementLocal}}}
	created, err := v2.Put(ctx, "", "", own)
	if err != nil || created.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("own put: %+v %v", created, err)
	}
	otherProgram := filepath.Join(t.TempDir(), "other-app.exe")
	if change, err := v2.Put(ctx, otherProgram, "", own); err != nil || change.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("other put: %+v %v", change, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	rightsOperator, err := client.New(options.endpoint).ResolveRightsOperator(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{inference.ActionSelectionManage, inference.ActionSelectionManageFor} {
		page, err := rightsOperator.ListPolicyContext(ctx, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		deny := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}, Action: action, Resource: "account", Permit: false}
		if changed, err := rightsOperator.SetRuleContext(ctx, page.Revision, deny); err != nil || changed.Outcome.String() != "applied" {
			t.Fatalf("deny %s: %+v %v", action, changed, err)
		}
	}
	if read, err := v2.Read(ctx, "", own.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeFound || read.Revision != created.Revision || read.Policy == nil {
		t.Fatalf("own read without selection.manage: %+v %v", read, err)
	}
	if read, err := v2.Read(ctx, filepath.Clean(self), own.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeFound {
		t.Fatalf("own read naming the bound program: %+v %v", read, err)
	}
	if runtime.GOOS == "windows" {
		if read, err := v2.Read(ctx, strings.ToUpper(filepath.Clean(self)), own.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeFound {
			t.Fatalf("own read in another spelling: %+v %v", read, err)
		}
	}
	if read, err := v2.Read(ctx, otherProgram, own.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeForbidden || read.Policy != nil {
		t.Fatalf("other program's read without selection.manage_for: %+v %v", read, err)
	}
	if change, err := v2.Put(ctx, "", created.Revision, own); err != nil || change.Outcome != iwire.EditOutcomeForbidden {
		t.Fatalf("own put without selection.manage: %+v %v", change, err)
	}
	if change, err := v2.DeletePolicy(ctx, "", created.Revision, own.Name); err != nil || change.Outcome != iwire.EditOutcomeForbidden {
		t.Fatalf("own delete without selection.manage: %+v %v", change, err)
	}
}

func TestOwnSelectionScopeIsTheBoundProgram(t *testing.T) {
	program := filepath.Join(t.TempDir(), "app.exe")
	if err := os.WriteFile(program, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: &runtimeRights{owner: "account"}}}
	caller := inference.Subject{Account: "account", Program: program}
	for _, target := range []string{"", program} {
		if scope, ok := o.ownSelectionScope(caller, target); !ok || scope != caller {
			t.Fatalf("target %q: %+v %v", target, scope, ok)
		}
	}
	if runtime.GOOS == "windows" {
		if scope, ok := o.ownSelectionScope(caller, strings.ToUpper(program)); !ok || scope.Program != program {
			t.Fatalf("another spelling: %+v %v", scope, ok)
		}
	}
	for _, target := range []string{filepath.Join(t.TempDir(), "other.exe"), "app.exe"} {
		if _, ok := o.ownSelectionScope(caller, target); ok {
			t.Fatalf("target %q read as own", target)
		}
	}
	if _, ok := o.ownSelectionScope(inference.Subject{Account: "other", Program: program}, ""); ok {
		t.Fatal("another account read as own")
	}
	if _, ok := o.ownSelectionScope(inference.Subject{Account: "account"}, ""); ok {
		t.Fatal("an unbound program read as own")
	}
}

func TestSelectionStoreRetainsUnpinnedAndPinnedJSON(t *testing.T) {
	raw := []byte(`{"version":1,"next":2,"records":[{"account":"account","program":"app","policy":{"name":"unpinned","candidates":[{"model":"local","placement":"local"}]},"revision":"selection-v1:0000000000000001"},{"account":"account","program":"app","policy":{"name":"pinned","candidates":[{"model":"local","placement":"local","provider":"native-runtime"}]},"revision":"selection-v1:0000000000000002"}]}`)
	index, err := decodeSelection(raw)
	if err != nil || index.Version != 1 || index.Next != 2 || len(index.Records) != 2 {
		t.Fatalf("existing stored JSON refused: %+v %v", index, err)
	}
	for i, provider := range []string{"", "native-runtime"} {
		candidate := index.Records[i].Policy.Candidates[0]
		if candidate.Model != "local" || candidate.Placement != iwire.CandidatePlacementLocal || candidate.Provider != provider {
			t.Fatalf("stored candidate changed: %+v", candidate)
		}
	}
}
