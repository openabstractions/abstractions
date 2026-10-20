package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	"github.com/openabstractions/abstraction-facade/go/grants"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

type providerSetupBudgets []inference.ProgramBudgetState

func (b providerSetupBudgets) ProgramBudgets(string, string) (string, []inference.ProgramBudgetState, error) {
	return "fixture", b, nil
}

func TestProviderGuardBudgetReadsNativeExplicitChoice(t *testing.T) {
	binding, _ := hostedcatalogue.Select("deepseek")
	budgets, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "counts.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	self := inference.Subject{Account: "account", Program: filepath.Join(t.TempDir(), "caller.exe")}
	other := inference.Subject{Account: self.Account, Program: filepath.Join(t.TempDir(), "other.exe")}
	set := func(subject inference.Subject, budget *inference.Ceiling) {
		t.Helper()
		rev, _, err := budgets.ProgramBudgets(binding.Credential, self.Account)
		if err != nil {
			t.Fatal(err)
		}
		if _, applied, err := budgets.SetProgramBudget(rev, binding.Credential, subject, budget); err != nil || !applied {
			t.Fatal(applied, err)
		}
	}
	if providerOwnBudget(budgets, binding, self) {
		t.Fatal("absent budget")
	}
	set(other, &inference.Ceiling{})
	if providerOwnBudget(budgets, binding, self) {
		t.Fatal("other beneficiary budget")
	}
	set(self, &inference.Ceiling{})
	if !providerOwnBudget(budgets, binding, self) {
		t.Fatal("explicit No limit was lost")
	}
	set(self, nil)
	if providerOwnBudget(budgets, binding, self) {
		t.Fatal("removed budget cached")
	}
	for _, flag := range []bool{false, true, false} {
		if !providerOwnBudget(providerSetupBudgets{{Program: self.Program, Budget: &inference.Ceiling{}, CostUnreported: flag}}, binding, self) {
			t.Fatal("daily cost flag changed configuration presence", flag)
		}
	}
}

func TestProviderPairOccurrenceCannotJoinLegacySingle(t *testing.T) {
	r, a, book := inputAsks(t)
	subject := rwire.Subject{Account: r.owner, Program: appProgram("requester")}
	single := a.admitPrepared(subject, nil, credentials.ActionApply, "credential:deepseek")
	permission, _ := providerPermission(subject, "deepseek")
	pair := a.admitPermissionPrepared(subject, nil, permission)
	if single == "" || pair == "" || single == pair {
		t.Fatal("different complete effects joined", single, pair)
	}
	again := a.admitPermissionPrepared(subject, nil, permission)
	records, _, err := book.ApplicationHistory()
	if err != nil || again != pair || len(records) != 2 {
		t.Fatal(again, records, err)
	}
}

func TestProviderExistingOwnHostRemainsServableWithoutProgramBudget(t *testing.T) {
	r, _, _ := inputAsks(t)
	definitions, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{Credentials: true})
	if err != nil {
		t.Fatal(err)
	}
	r.policy.Definitions = definitions
	subject := rwire.Subject{Account: r.owner, Program: appProgram("existing-requester")}
	binding, err := hostedcatalogue.Select("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	holder, err := credentials.Open(credentials.Config{Backend: &privateReleaseMemory{values: map[string][]byte{}}, Namespace: "provider-existing"})
	if err != nil {
		t.Fatal(err)
	}
	stored := holder.Store(cwire.Subject{Account: r.owner, Program: appProgram("Panel")}, "", cwire.Registration{Name: binding.Name, Kind: "bearer", Scope: cwire.Scope{Targets: []string{binding.Target}, Consumers: []string{router.CredentialConsumer, inference.Contract}}, Secret: []byte("synthetic-only")})
	if stored.Outcome != cwire.StoreOutcomeStored {
		t.Fatal(stored)
	}
	permission, _ := providerPermission(subject, binding.Name)
	for _, target := range permission.Targets {
		if err := r.policy.Set(subject, target.Action, target.Resource, true); err != nil {
			t.Fatal(err)
		}
	}
	c := &runtimeCredentials{runtimeRights: r, holder: holder}
	host := router.HostState{Host: binding.Name, Base: binding.Base, Wire: binding.Kind, Credential: binding.Credential, Hosted: true, Servable: true}
	if cause := c.subjectHostAvailability(t.Context(), inference.Subject{Account: subject.Account, Program: subject.Program}, host, nil); cause != "" {
		t.Fatal("pre-AK rights and credential lost availability", cause)
	}
}

func TestProviderSetupAnswerCommitsBothTargetsOnlyAfterGuard(t *testing.T) {
	for _, variant := range []string{"ready", "no-budget", "usage-only", "other-program", "disabled", "duplicate", "no-key", "binding-race", "budget-race", "retry"} {
		t.Run(variant, func(t *testing.T) {
			r, a, book := inputAsks(t)
			definitions, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{Credentials: true})
			if err != nil {
				t.Fatal(err)
			}
			r.policy.Definitions = definitions
			subject := rwire.Subject{Account: r.owner, Program: appProgram("requester")}
			panel := rwire.Subject{Account: r.owner, Program: appProgram("Panel")}
			binding, err := hostedcatalogue.Select("deepseek")
			if err != nil {
				t.Fatal(err)
			}
			permission, ok := providerPermission(subject, binding.Name)
			if !ok {
				t.Fatal("provider permission")
			}
			a.admitPermissionPrepared(subject, nil, permission)
			records, _, err := book.ApplicationHistory()
			if err != nil || len(records) != 1 {
				t.Fatalf("admission: %v %v", records, err)
			}
			for _, option := range []string{"allow", "refuse"} {
				if _, err := a.authorizeAnswerer(t.Context(), answerer{}, records[0], a.scope, option); !errors.Is(err, asksservice.ErrResponderForbidden) {
					t.Fatal("provider pair admitted a responder", option, err)
				}
			}
			holder, err := credentials.Open(credentials.Config{Backend: &privateReleaseMemory{values: map[string][]byte{}}, Namespace: "provider-guard"})
			if err != nil {
				t.Fatal(err)
			}
			if variant != "no-key" {
				stored := holder.Store(cwire.Subject{Account: r.owner, Program: panel.Program}, "", cwire.Registration{Name: binding.Name, Kind: "bearer", Scope: cwire.Scope{Targets: []string{binding.Target}, Consumers: []string{router.CredentialConsumer, inference.Contract}}, Secret: []byte("synthetic-only")})
				if stored.Outcome != cwire.StoreOutcomeStored {
					t.Fatal(stored)
				}
			}
			c := &runtimeCredentials{runtimeRights: r, holder: holder}
			files := []providerFile{{Declaration: providerDeclaration{Name: binding.Name, Host: &providerHost{Base: binding.Base, Kind: binding.Kind, Credential: binding.Credential, Hosted: true}}}}
			if variant == "disabled" {
				files[0].Disabled = true
			}
			if variant == "duplicate" {
				files = append(files, files[0])
			}
			budgets := providerSetupBudgets{{Program: subject.Program, Budget: &inference.Ceiling{}}}
			if variant == "no-budget" || variant == "retry" {
				budgets = nil
			}
			if variant == "usage-only" {
				budgets[0].Budget = nil
			}
			if variant == "other-program" {
				budgets[0].Program = appProgram("other")
			}
			hosts := []*router.Host{router.NewHosted(binding.Name, binding.Base, binding.Kind, binding.Credential)}
			reads := 0
			r.providerSetupGuard = func(ctx context.Context, p asks.PermissionRequest) (func() error, error) {
				return c.providerSetupGuard(ctx, p, func() []*router.Host { return hosts }, func() []providerFile {
					reads++
					if reads > 1 && variant == "binding-race" {
						return nil
					}
					if reads > 1 && variant == "budget-race" {
						budgets[0].Budget = nil
					}
					return files
				}, budgets)
			}
			answered, err := grants.AnswerQuestion(context.Background(), pendingQuestion{book: book}, policyRules{policy: r.policy, by: panel, asks: a}, r.owner, records[0].ID, "allow")
			if err != nil || answered.Edit == nil {
				t.Fatalf("answer: %+v %v", answered, err)
			}
			want := rwire.PolicyEditOutcomeUnavailable
			if variant == "ready" {
				want = rwire.PolicyEditOutcomeApplied
			}
			if answered.Edit.Outcome != want {
				t.Fatalf("edit: %+v want %v", answered.Edit, want)
			}
			for _, target := range permission.Targets {
				decision := r.policy.Decide(subject, target.Action, target.Resource)
				if (decision.Outcome == rwire.DecisionOutcomePermitted) != (variant == "ready") {
					t.Fatalf("target %+v: %+v", target, decision)
				}
			}
			if variant == "ready" {
				view := router.HostState{Host: binding.Name, Base: binding.Base, Wire: binding.Kind, Credential: binding.Credential, Hosted: true, Servable: true}
				if cause := c.subjectHostAvailability(t.Context(), inference.Subject{Account: subject.Account, Program: subject.Program}, view, nil); cause != "" {
					t.Fatal("committed pair not usable by its own beneficiary", cause)
				}
				current, _ := r.policy.OperatorSnapshot()
				replay, err := a.applyOperatorAllow(t.Context(), panel, current.Revision, records[0].ID, func() error { return nil })
				if err != nil || replay.Outcome != rwire.PolicyEditOutcomeConflict || replay.Revision != current.Revision || replay.Current == nil {
					t.Fatal("completed receipt lost revision/current", replay, err)
				}
				stale, err := a.applyOperatorAllow(t.Context(), panel, "sha256:stale", records[0].ID, func() error { return nil })
				if err != nil || stale.Outcome != rwire.PolicyEditOutcomeConflict || stale.Revision == "sha256:stale" || stale.Revision != current.Revision {
					t.Fatal(stale, err)
				}
				for _, target := range permission.Targets {
					if err := r.policy.Revoke(subject, target.Action, target.Resource); err != nil {
						t.Fatal(err)
					}
				}
				files = nil
				budgets = nil // Completed receipt recovery needs no new setup guard.
				current, _ = r.policy.OperatorSnapshot()
				replay, err = a.applyOperatorAllow(t.Context(), panel, current.Revision, records[0].ID, func() error { return nil })
				if err != nil || replay.Outcome != rwire.PolicyEditOutcomeConflict || replay.Revision != current.Revision || replay.Current != nil {
					t.Fatal("revoked completed receipt lost revision", replay, err)
				}
				reconciled, err := grants.RetryFirstUseRule(t.Context(), pendingQuestion{book: book}, policyRules{policy: r.policy, by: panel, asks: a}, r.owner, records[0].ID)
				if err != nil || reconciled.RuleOutcome != grants.RuleCompleted {
					t.Fatal(reconciled, err)
				}
				for _, target := range permission.Targets {
					if got := r.policy.Decide(subject, target.Action, target.Resource); got.Outcome == rwire.DecisionOutcomePermitted {
						t.Fatal("receipt restored revoked permit", target, got)
					}
				}
			}
			if variant == "retry" {
				budgets = providerSetupBudgets{{Program: subject.Program, Budget: &inference.Ceiling{}}}
				retried, err := grants.RetryFirstUseRule(t.Context(), pendingQuestion{book: book}, policyRules{policy: r.policy, by: panel, asks: a}, r.owner, records[0].ID)
				if err != nil || retried.Edit == nil || retried.Edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Fatal(retried, err)
				}
				current, _, _ := book.ApplicationHistory()
				if len(current) != 1 || current[0].ID != records[0].ID {
					t.Fatal("retry changed occurrence", current)
				}
				for _, target := range permission.Targets {
					if got := r.policy.Decide(subject, target.Action, target.Resource); got.Outcome != rwire.DecisionOutcomePermitted {
						t.Fatal(target, got)
					}
				}
			}
		})
	}
}

func TestProviderNotNowAnswersWithoutSetupOrPermits(t *testing.T) {
	r, a, book := inputAsks(t)
	subject := rwire.Subject{Account: r.owner, Program: appProgram("requester")}
	p, _ := providerPermission(subject, "deepseek")
	id := a.admitPermissionPrepared(subject, nil, p)
	r.providerSetupGuard = func(context.Context, asks.PermissionRequest) (func() error, error) {
		t.Fatal("Not now prepared a grant")
		return nil, nil
	}
	answered, err := grants.AnswerQuestion(t.Context(), pendingQuestion{book: book}, policyRules{policy: r.policy, by: rwire.Subject{Account: r.owner, Program: appProgram("Panel")}, asks: a}, r.owner, id, "refuse")
	if err != nil || answered.Edit != nil || answered.Decision.Record == nil || answered.Decision.Record.Option != "refuse" {
		t.Fatal(answered, err)
	}
	for _, target := range p.Targets {
		if got := r.policy.Decide(subject, target.Action, target.Resource); got.Outcome == rwire.DecisionOutcomePermitted {
			t.Fatal(got)
		}
	}
}
