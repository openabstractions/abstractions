package main

import (
	"context"
	"testing"

	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

// Setup writes the account default of the chat profile, named chat
// (research/first-run/DECISION.md §4); an app asks for the generic policy
// oa-chat-default. A generic policy nobody set resolves to its profile's
// account default (INF-R1), relabelled with the name the app asked for; a
// set policy of that name wins, program or account; a name outside the
// generic convention never reaches a profile default.
func TestGenericPolicyFollowsItsProfileAccountDefault(t *testing.T) {
	ctx := context.Background()
	const account, program, other = "account-1", setupTestApp, setupTestOther
	o := &inferenceOperator{dir: t.TempDir(), credentials: &runtimeCredentials{runtimeRights: &runtimeRights{owner: account}}}
	put := func(scope inference.Subject, name, model string) string {
		t.Helper()
		read := o.readSelectionScopeV2(ctx, scope, name)
		change := o.putSelectionScopeV2(ctx, scope, read.Revision, iwire.SelectionPolicyV2{Name: name, Candidates: []iwire.SelectionCandidateV2{{Model: model, Placement: iwire.CandidatePlacementLocal}}})
		if change.Outcome != iwire.EditOutcomeApplied {
			t.Fatalf("put %s: %+v", name, change)
		}
		return change.Revision
	}
	resolve := func(who, name string) (string, bool) {
		t.Helper()
		policy, found, err := o.ResolveSelectionV2(ctx, inference.Subject{Account: account, Program: who}, name)
		if err != nil {
			t.Fatal(err)
		}
		if found && policy.Name != name {
			t.Fatalf("resolved %s under the name %s", name, policy.Name)
		}
		if !found {
			return "", false
		}
		return policy.Candidates[0].Model, true
	}
	if _, found := resolve(program, "oa-chat-default"); found {
		t.Fatal("virgin store resolved a policy")
	}
	setup := put(inference.Subject{Account: account}, "chat", "qwen3.6-35b-a3b")
	// Absent named policy: the chat profile's account default, for every app.
	for _, who := range []string{program, other} {
		if model, found := resolve(who, "oa-chat-default"); !found || model != "qwen3.6-35b-a3b" {
			t.Fatalf("%s: absent oa-chat-default resolved %q %v", who, model, found)
		}
	}
	read := o.ReadEffective(ctx, inference.Subject{Account: account, Program: program}, "oa-chat-default")
	if read.Outcome != iwire.SelectionReadOutcomeFound || read.Policy == nil || read.Policy.Name != "oa-chat-default" || read.Policy.Candidates[0].Model != "qwen3.6-35b-a3b" || read.Revision != setup {
		t.Fatalf("effective read of the absent generic policy: %+v", read)
	}
	// Raw reads keep their own scopes: nothing is written by the fallback.
	if raw := o.readSelectionScopeV2(ctx, inference.Subject{Account: account}, "oa-chat-default"); raw.Outcome != iwire.SelectionReadOutcomeUnknown {
		t.Fatalf("raw account default read: %+v", raw)
	}
	// Other profiles and other names do not borrow the chat default.
	if _, found := resolve(program, "oa-image-default"); found {
		t.Fatal("the picture policy borrowed the chat default")
	}
	for _, name := range []string{"first", "oa-chat", "oa-unknown-default"} {
		if _, found := resolve(program, name); found {
			t.Fatalf("%s reached a profile default", name)
		}
	}
	put(inference.Subject{Account: account}, "image", "flux2-klein-4b")
	if model, found := resolve(program, "oa-image-default"); !found || model != "flux2-klein-4b" {
		t.Fatalf("absent oa-image-default resolved %q %v", model, found)
	}
	// A set named policy wins: the same-named account default, then the
	// program's own policy.
	put(inference.Subject{Account: account}, "oa-chat-default", "chosen-for-apps")
	if model, _ := resolve(other, "oa-chat-default"); model != "chosen-for-apps" {
		t.Fatalf("the account's oa-chat-default lost to the profile default: %q", model)
	}
	put(inference.Subject{Account: account, Program: program}, "oa-chat-default", "chosen-for-opencode")
	if model, _ := resolve(program, "oa-chat-default"); model != "chosen-for-opencode" {
		t.Fatalf("the program's policy lost: %q", model)
	}
	if model, _ := resolve(other, "oa-chat-default"); model != "chosen-for-apps" {
		t.Fatalf("another program took the override: %q", model)
	}
}
