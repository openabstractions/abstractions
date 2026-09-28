package main

import (
	"context"
	"path/filepath"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

func TestRegistryHostDeclarationEstablishesInferenceRules(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install([]string{ActionProviderManage, inference.ActionComplete, credentials.ActionApply},
		[]installationRule{{ActionProviderManage, credentials.ResourceAccount}}); err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"),
		supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}),
		report: func(err error) { t.Errorf("provider: %v", err) }}
	o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: r}, providers: p,
		report: func(err error) { t.Errorf("operator: %v", err) }}
	registry := &runtimeRegistry{providers: p, operator: o}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	entry := iwire.HostEntry{Name: "fixture", Hosted: true, Kind: "openai-compatible", Base: "https://fixture.invalid", Credential: "fixture-key"}
	caller := inference.Subject{Account: r.owner, Program: r.operators[0]}
	result := registry.Declare(context.Background(), caller, revision, hostDeclaration(entry).wire())
	if result.Outcome != wire.DeclarationEditOutcomeApplied || result.Reason != "" {
		t.Fatalf("declaration: %+v", result)
	}
	for _, grant := range []struct{ action, resource string }{
		{inference.ActionComplete, inference.ResourceHost(entry.Name)},
		{credentials.ActionApply, credentials.ResourceFor(entry.Credential)},
	} {
		read := r.policy.ReadRule(rwire.Subject{Account: caller.Account, Program: caller.Program}, grant.action, grant.resource)
		if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit {
			t.Errorf("missing %s on %s: %+v", grant.action, grant.resource, read)
		}
	}
}

func TestHostRemovalDropsDeclarationBudget(t *testing.T) {
	for _, viaRegistry := range []bool{false, true} {
		name := "operator"
		if viaRegistry {
			name = "registry"
		}
		t.Run(name, func(t *testing.T) {
			r := testRights(t, t.TempDir())
			if err := r.install([]string{ActionProviderManage, inference.ActionHostManage, inference.ActionComplete, credentials.ActionApply},
				[]installationRule{{ActionProviderManage, credentials.ResourceAccount}, {inference.ActionHostManage, credentials.ResourceAccount}}); err != nil {
				t.Fatal(err)
			}
			p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"),
				supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}),
				report: func(err error) { t.Errorf("provider: %v", err) }}
			ceilings, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "spend.json"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			o := &inferenceOperator{dir: t.TempDir(), credentials: &runtimeCredentials{runtimeRights: r}, providers: p,
				router: router.New(), ceilings: ceilings, report: func(err error) { t.Errorf("operator: %v", err) }}
			p.Watch(o.refreshHosts)
			registry := &runtimeRegistry{providers: p, operator: o}
			_, revision, err := p.read()
			if err != nil {
				t.Fatal(err)
			}
			// An owned unsupported wire has no outbound listing traffic.
			entry := iwire.HostEntry{Name: "fixture", Hosted: true, Kind: "fixture/test@1", Base: "https://fixture.invalid", Credential: "fixture-key", Ceiling: &iwire.CeilingLimit{TokensPerDay: 10}}
			caller := inference.Subject{Account: r.owner, Program: r.operators[0]}
			if viaRegistry {
				result := registry.Declare(context.Background(), caller, revision, hostDeclaration(entry).wire())
				if result.Outcome != wire.DeclarationEditOutcomeApplied {
					t.Fatalf("declare: %+v", result)
				}
				revision = result.Revision
			} else {
				result := o.AddHost(context.Background(), caller, revision, entry)
				if result.Outcome != iwire.EditOutcomeApplied {
					t.Fatalf("add: %+v", result)
				}
				revision = result.Revision
			}
			if err := ceilings.Add(entry.Credential, 10, 0); err != nil {
				t.Fatal(err)
			}
			if _, exceeded := ceilings.Exceeded(entry.Credential); !exceeded {
				t.Fatal("declared budget was not enforced")
			}
			if result := registry.Withdraw(context.Background(), caller, revision, entry.Name); result.Outcome != wire.DeclarationEditOutcomeApplied {
				t.Fatalf("withdraw: %+v", result)
			}
			if _, exceeded := ceilings.Exceeded(entry.Credential); exceeded {
				t.Fatal("removed declaration left its budget in force")
			}
			if _, tokens, _ := ceilings.Spend(entry.Credential); tokens != 10 {
				t.Fatalf("removing a budget lost spend: %d", tokens)
			}
		})
	}
}

func TestSharedCredentialKeepsEveryActiveDeclarationBudget(t *testing.T) {
	file := func(name string, tokens, images int64) providerFile {
		return newHostFile(iwire.HostEntry{Name: name, Hosted: true, Kind: "fixture/test@1", Base: "https://fixture.invalid", Credential: "shared",
			Ceiling: &iwire.CeilingLimit{TokensPerDay: tokens, ImagesPerDay: images}}, declaredByOperator)
	}
	first, second, disabled := file("first", 100, 0), file("second", 200, 5), file("disabled", 1, 1)
	disabled.Disabled = true
	for _, files := range [][]providerFile{{first, second, disabled}, {disabled, second, first}} {
		if got := declarationBudgets(files)["shared"]; got != (inference.Ceiling{TokensPerDay: 100, ImagesPerDay: 5}) {
			t.Fatalf("combined budget: %+v", got)
		}
	}
	if got := declarationBudgets([]providerFile{second})["shared"]; got != (inference.Ceiling{TokensPerDay: 200, ImagesPerDay: 5}) {
		t.Fatalf("remaining declaration: %+v", got)
	}
}
