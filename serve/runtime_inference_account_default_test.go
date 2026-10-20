package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

func TestSelectionAccountDefaultOversizedRevisionRefusesBeforeEffects(t *testing.T) {
	// Nil credentials would panic if admission or store access were reached.
	o := &inferenceOperator{dir: filepath.Join(t.TempDir(), "never-created")}
	for _, change := range []iwire.SelectionChange{
		o.PutAccountDefault(context.Background(), inference.Subject{}, strings.Repeat("r", 257), iwire.SelectionPolicyV2{}),
		o.DeleteAccountDefault(context.Background(), inference.Subject{}, strings.Repeat("r", 257), "name"),
	} {
		if change.Outcome != iwire.EditOutcomeInvalid || change.Reason != "revision" {
			t.Fatalf("oversized revision: %+v", change)
		}
	}
	if _, err := os.Stat(o.dir); !os.IsNotExist(err) {
		t.Fatalf("invalid input touched store: %v", err)
	}
}

// The copied test executable is a second OS-observed program, with no operator
// installation grant. It reaches the same generated/native IPC service.
func TestAccountDefaultApplicationHelper(t *testing.T) {
	endpoint := os.Getenv("OA_ACCOUNT_DEFAULT_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("owned child fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	o := inferenceclient.NewSelectionOperatorV2(endpoint)
	name := "oa-chat-default"
	if r, err := o.Read(ctx, "", name); err != nil || r.Outcome != iwire.SelectionReadOutcomeUnknown || r.Policy != nil {
		t.Fatalf("raw own read: %+v %v", r, err)
	}
	if r, err := o.ReadEffective(ctx, name); err != nil || r.Outcome != iwire.SelectionReadOutcomeFound || r.Policy == nil || r.Policy.Candidates[0].Model != "account-model" || r.Revision != os.Getenv("OA_ACCOUNT_DEFAULT_TEST_REVISION") {
		t.Fatalf("effective own read without management grant: %+v %v", r, err)
	}
	if r, err := o.ReadAccountDefault(ctx, name); err != nil || r.Outcome != iwire.SelectionReadOutcomeForbidden || r.Policy != nil {
		t.Fatalf("default disclosure to ungranted app: %+v %v", r, err)
	}
	policy := iwire.SelectionPolicyV2{Name: name, Candidates: []iwire.SelectionCandidateV2{{Model: "ungranted", Placement: iwire.CandidatePlacementLocal}}}
	if r, err := o.PutAccountDefault(ctx, "", policy); err != nil || r.Outcome != iwire.EditOutcomeForbidden {
		t.Fatalf("default edit: %+v %v", r, err)
	}
	if r, err := o.DeleteAccountDefault(ctx, "", name); err != nil || r.Outcome != iwire.EditOutcomeForbidden {
		t.Fatalf("default delete: %+v %v", r, err)
	}
	decision, err := client.New(os.Getenv("OA_ACCOUNT_DEFAULT_TEST_RUNTIME")).ResolveRights(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range [][2]string{{inference.ActionComplete, "server:fixture"}, {"abstraction.inference/host.manage", "account"}, {"abstraction.credentials/apply", "credential:fixture"}} {
		got, err := decision.DecideContext(ctx, check[0], check[1])
		if err != nil || (got.Outcome.String() != "not_granted" && got.Outcome.String() != "denied") {
			t.Fatalf("default granted %s: %+v %v", check[0], got, err)
		}
	}
	// Effective lookup requires no selection-management grant. The stored pin
	// refuses here; a raw unknown policy would instead be selection:unknown.
	r, err := inferenceclient.NewChatV2(endpoint).Start(ctx, iwire.RequestV2{Target: iwire.Target{SelectionPolicy: name}, Content: iwire.RequestContent{Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "fixture"}}}}}})
	if err != nil || r.Outcome != iwire.StartOutcomeNoHost || r.Reason != "selection:provider_stale" {
		t.Fatalf("effective default: %+v %v", r, err)
	}
	r, err = inferenceclient.NewChatV2(endpoint).StartWithGuarantees(ctx, iwire.RequestV2{Target: iwire.Target{SelectionPolicy: name}, Content: iwire.RequestContent{Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "fixture"}}}}}}, []iwire.RequestGuarantee{iwire.RequestGuaranteeLocalOnly})
	if err != nil || r.Outcome != iwire.StartOutcomeNoHost || r.Reason != "selection:provider_stale" {
		t.Fatalf("narrowed effective default without management grant: %+v %v", r, err)
	}
}

func TestSelectionAccountDefaultNativePrecedenceCASRestartAndAuthority(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable")
	}
	options, _ := isolatedRuntime(t)
	stop := startInferenceRuntime(t, options)
	endpoint, err := inferenceEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o := inferenceclient.NewSelectionOperatorV2(endpoint)
	policy := iwire.SelectionPolicyV2{Name: "oa-chat-default", Candidates: []iwire.SelectionCandidateV2{{Model: "account-model", Placement: iwire.CandidatePlacementLocal, Provider: "absent-provider"}}}
	created, err := o.PutAccountDefault(ctx, "", policy)
	if err != nil || created.Outcome != iwire.EditOutcomeApplied || created.Revision == "" {
		t.Fatalf("create: %+v %v", created, err)
	}
	if r, err := o.ReadAccountDefault(ctx, policy.Name); err != nil || r.Outcome != iwire.SelectionReadOutcomeFound || r.Revision != created.Revision || r.Policy == nil || r.Policy.Candidates[0].Model != "account-model" {
		t.Fatalf("read: %+v %v", r, err)
	}
	if r, err := o.PutAccountDefault(ctx, "", policy); err != nil || r.Outcome != iwire.EditOutcomeConflict || r.Revision != created.Revision {
		t.Fatalf("stale put: %+v %v", r, err)
	}
	if r, err := o.DeleteAccountDefault(ctx, "", policy.Name); err != nil || r.Outcome != iwire.EditOutcomeConflict || r.Revision != created.Revision {
		t.Fatalf("stale delete: %+v %v", r, err)
	}
	if r, err := o.Read(ctx, "", policy.Name); err != nil || r.Outcome != iwire.SelectionReadOutcomeUnknown {
		t.Fatalf("raw read inherited default: %+v %v", r, err)
	}
	self := probeSelf()
	store := &inferenceOperator{dir: filepath.Join(options.stateDir, "inference"), credentials: &runtimeCredentials{runtimeRights: &runtimeRights{owner: self.Account}}}
	other := copyTestBinary(t, "account-default-app")
	resolve := func(program, want string) {
		t.Helper()
		p, found, err := store.ResolveSelectionV2(ctx, inference.Subject{Account: self.Account, Program: program}, policy.Name)
		if err != nil || !found || p.Candidates[0].Model != want {
			t.Fatalf("resolve %s: %+v %v %v", program, p, found, err)
		}
	}
	resolve(self.Program, "account-model")
	resolve(other, "account-model")
	readEffective := func(want, revision string) {
		t.Helper()
		before, err := os.ReadFile(store.selectionPath())
		if err != nil {
			t.Fatal(err)
		}
		read, err := o.ReadEffective(ctx, policy.Name)
		if err != nil || read.Outcome != iwire.SelectionReadOutcomeFound || read.Policy == nil || read.Policy.Candidates[0].Model != want || read.Revision != revision {
			t.Fatalf("effective read: %+v %v", read, err)
		}
		after, err := os.ReadFile(store.selectionPath())
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("effective read changed store: %v", err)
		}
	}
	readEffective("account-model", created.Revision)
	if _, found, err := store.ResolveSelectionV2(ctx, inference.Subject{Account: "foreign-account", Program: other}, policy.Name); err == nil || found {
		t.Fatalf("foreign account: %v %v", found, err)
	}
	if _, found, err := store.ResolveSelectionV2(ctx, inference.Subject{Account: self.Account, Program: other}, "another-name"); err != nil || found {
		t.Fatalf("foreign name: %v %v", found, err)
	}
	// The second native program receives the default through inference while
	// account-default management stays protected by the installation boundary.
	cmd := exec.CommandContext(ctx, other, "-test.run=^TestAccountDefaultApplicationHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "OA_ACCOUNT_DEFAULT_TEST_ENDPOINT="+endpoint, "OA_ACCOUNT_DEFAULT_TEST_RUNTIME="+options.endpoint, "OA_ACCOUNT_DEFAULT_TEST_REVISION="+created.Revision)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second program: %v\n%s", err, out)
	}
	override := iwire.SelectionPolicyV2{Name: policy.Name, Candidates: []iwire.SelectionCandidateV2{{Model: "program-model", Placement: iwire.CandidatePlacementLocal}}}
	changed, err := o.Put(ctx, self.Program, "", override)
	if err != nil || changed.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("override: %+v %v", changed, err)
	}
	resolve(self.Program, "program-model")
	resolve(other, "account-model")
	readEffective("program-model", changed.Revision)
	request := iwire.RequestV2{Target: iwire.Target{SelectionPolicy: policy.Name}, Content: iwire.RequestContent{Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "fixture"}}}}}}
	chat := inferenceclient.NewChatV2(endpoint)
	if result, err := chat.Start(ctx, request); err != nil || result.Outcome != iwire.StartOutcomeNoHost || result.Reason != "selection:no_eligible_host" {
		t.Fatalf("unavailable program policy fell through to pinned default: %+v %v", result, err)
	}
	override.Candidates[0].Provider = "missing-override"
	changed, err = o.Put(ctx, self.Program, changed.Revision, override)
	if err != nil || changed.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("pin override: %+v %v", changed, err)
	}
	policy.Candidates[0].Provider = ""
	created, err = o.PutAccountDefault(ctx, created.Revision, policy)
	if err != nil || created.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("unpin default: %+v %v", created, err)
	}
	if result, err := chat.Start(ctx, request); err != nil || result.Outcome != iwire.StartOutcomeNoHost || result.Reason != "selection:provider_stale" {
		t.Fatalf("invalid program pin fell through to unpinned default: %+v %v", result, err)
	}
	stop()
	stop = startInferenceRuntime(t, options)
	resolve(self.Program, "program-model")
	resolve(other, "account-model")
	if r, err := o.ReadAccountDefault(ctx, policy.Name); err != nil || r.Revision != created.Revision || r.Outcome != iwire.SelectionReadOutcomeFound {
		t.Fatalf("restart default: %+v %v", r, err)
	}
	if r, err := o.DeletePolicy(ctx, "", changed.Revision, policy.Name); err != nil || r.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("delete override: %+v %v", r, err)
	}
	resolve(self.Program, "account-model")
	readEffective("account-model", created.Revision)
	if r, err := o.DeleteAccountDefault(ctx, created.Revision, policy.Name); err != nil || r.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("delete default: %+v %v", r, err)
	}
	if _, found, err := store.ResolveSelectionV2(ctx, inference.Subject{Account: self.Account, Program: other}, policy.Name); err != nil || found {
		t.Fatalf("deleted default: %v %v", found, err)
	}
	if read, err := o.ReadEffective(ctx, policy.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeUnknown || read.Policy != nil || read.Revision != "" {
		t.Fatalf("deleted effective read: %+v %v", read, err)
	}
	recreated, err := o.PutAccountDefault(ctx, "", policy)
	if err != nil || recreated.Outcome != iwire.EditOutcomeApplied || recreated.Revision == created.Revision {
		t.Fatalf("recreate: %+v %v", recreated, err)
	}
	// Explicit denial applies to every account-default operation while own raw
	// reads retain their grant-free behavior.
	r, err := client.New(options.endpoint).ResolveRightsOperator(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := r.ListPolicyContext(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	deny := rights.PolicyRule{Subject: rights.Subject{Account: self.Account, Program: self.Program}, Action: inference.ActionSelectionManageFor, Resource: "account", Permit: false}
	if change, err := r.SetRuleContext(ctx, page.Revision, deny); err != nil || change.Outcome.String() != "applied" {
		t.Fatalf("deny: %+v %v", change, err)
	}
	if read, err := o.ReadAccountDefault(ctx, policy.Name); err != nil || read.Outcome != iwire.SelectionReadOutcomeForbidden || read.Policy != nil {
		t.Fatalf("denied read: %+v %v", read, err)
	}
	if change, err := o.PutAccountDefault(ctx, recreated.Revision, policy); err != nil || change.Outcome != iwire.EditOutcomeForbidden {
		t.Fatalf("denied put: %+v %v", change, err)
	}
	if change, err := o.DeleteAccountDefault(ctx, recreated.Revision, policy.Name); err != nil || change.Outcome != iwire.EditOutcomeForbidden {
		t.Fatalf("denied delete: %+v %v", change, err)
	}
	resolve(self.Program, "account-model")
	readEffective("account-model", recreated.Revision)
	stop()
}

func TestSelectionAccountDefaultCorruptionAndUnavailableNeverFallThrough(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	o := &inferenceOperator{dir: dir, credentials: &runtimeCredentials{runtimeRights: &runtimeRights{owner: "account"}}, report: func(error) {}}
	subject := inference.Subject{Account: "account", Program: filepath.Join(dir, "app.exe")}
	if read := o.ReadEffective(ctx, subject, "oa-chat-default"); read.Outcome != iwire.SelectionReadOutcomeUnknown || read.Policy != nil {
		t.Fatalf("virgin effective read: %+v", read)
	}
	if _, found, err := o.ResolveSelectionV2(ctx, subject, "oa-chat-default"); err != nil || found {
		t.Fatalf("virgin inference lookup: %v %v", found, err)
	}
	for _, caller := range []inference.Subject{{Account: "foreign", Program: subject.Program}, {Account: "account"}, {Program: subject.Program}, {}} {
		if read := o.ReadEffective(ctx, caller, "oa-chat-default"); read.Outcome != iwire.SelectionReadOutcomeForbidden || read.Policy != nil {
			t.Fatalf("unbound/foreign effective read: %+v", read)
		}
		if read := o.ReadAccountDefault(ctx, caller, "oa-chat-default"); read.Outcome != iwire.SelectionReadOutcomeForbidden || read.Policy != nil {
			t.Fatalf("unbound/foreign read: %+v", read)
		}
		if change := o.PutAccountDefault(ctx, caller, "", iwire.SelectionPolicyV2{}); change.Outcome != iwire.EditOutcomeForbidden {
			t.Fatalf("unbound/foreign put: %+v", change)
		}
		if change := o.DeleteAccountDefault(ctx, caller, "", "oa-chat-default"); change.Outcome != iwire.EditOutcomeForbidden {
			t.Fatalf("unbound/foreign delete: %+v", change)
		}
	}
	for _, raw := range []string{"", `null`, `{}`, `{"version":1}`, `{"version":1,"next":0}`, `{"version":1,"next":0,"records":null}`, `broken`, `{"version":99,"records":[]}`} {
		if err := os.WriteFile(o.selectionPath(), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, found, err := o.ResolveSelectionV2(ctx, subject, "oa-chat-default"); err == nil || found {
			t.Fatalf("corrupt index: %v %v", found, err)
		}
		if read := o.ReadEffective(ctx, subject, "oa-chat-default"); read.Outcome != iwire.SelectionReadOutcomeUnavailable || read.Policy != nil || read.Revision != "" {
			t.Fatalf("corrupt effective read: %+v", read)
		}
	}
	valid := iwire.SelectionPolicyV2{Name: "oa-chat-default", Candidates: []iwire.SelectionCandidateV2{{Model: "default", Placement: iwire.CandidatePlacementLocal}}}
	for _, corrupt := range []selectionRecord{{Account: subject.Account, Program: subject.Program, Policy: iwire.SelectionPolicyV2{Name: valid.Name}, Revision: "selection-v1:1"}, {Account: subject.Account, Program: subject.Program, Policy: valid}, {Account: subject.Account, Program: subject.Program, Policy: iwire.SelectionPolicyV2{Candidates: valid.Candidates}, Revision: "selection-v1:1"}, {Account: subject.Account, Program: "relative.exe", Policy: valid, Revision: "selection-v1:1"}, {Account: subject.Account, Policy: valid, Revision: "selection-v1:1"}} {
		index := selectionIndex{Version: 1, Records: []selectionRecord{{Account: subject.Account, Policy: valid, Revision: "selection-v1:2"}, corrupt}}
		raw, err := json.Marshal(index)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(o.selectionPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, found, err := o.ResolveSelectionV2(ctx, subject, valid.Name); err == nil || found {
			t.Fatalf("corrupt own policy fell through: %v %v", found, err)
		}
		if read := o.ReadEffective(ctx, subject, valid.Name); read.Outcome != iwire.SelectionReadOutcomeUnavailable || read.Policy != nil || read.Revision != "" {
			t.Fatalf("corrupt own effective read: %+v", read)
		}
	}
	for _, absent := range []string{"account", "program", "policy", "revision"} {
		own := map[string]any{"account": subject.Account, "program": subject.Program, "policy": valid, "revision": "selection-v1:1"}
		delete(own, absent)
		raw, err := json.Marshal(map[string]any{"version": 1, "next": 2, "records": []any{selectionRecord{Account: subject.Account, Policy: valid, Revision: "selection-v1:2"}, own}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(o.selectionPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if read := o.ReadEffective(ctx, subject, valid.Name); read.Outcome != iwire.SelectionReadOutcomeUnavailable || read.Policy != nil {
			t.Fatalf("missing %s permitted default fallback: %+v", absent, read)
		}
		if _, found, err := o.ResolveSelectionV2(ctx, subject, valid.Name); err == nil || found {
			t.Fatalf("missing %s inference fallback: %v %v", absent, found, err)
		}
	}
	if err := os.Remove(o.selectionPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(o.selectionPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, found, err := o.ResolveSelectionV2(ctx, subject, "oa-chat-default"); err == nil || found {
		t.Fatalf("unavailable index: %v %v", found, err)
	}
	if read := o.ReadEffective(ctx, subject, "oa-chat-default"); read.Outcome != iwire.SelectionReadOutcomeUnavailable || read.Policy != nil {
		t.Fatalf("unavailable effective read: %+v", read)
	}
}
