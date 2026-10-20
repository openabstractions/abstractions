package main

import (
	credentials "github.com/openabstractions/abstraction-credentials/go"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"path/filepath"
	"testing"
)

func TestProgramBudgetRequiresObservedUnitOnEveryCredentialBinding(t *testing.T) {
	file := providerFile{Declaration: providerDeclaration{Name: "openrouter", Host: &providerHost{Base: "https://openrouter.ai/api/v1", Kind: "openai-compatible", Hosted: true, Credential: "openrouter"}}}
	money := inference.Ceiling{MicrosPerDay: 100}
	if reason := unreportedProgramBudgetUnit([]providerFile{file}, "openrouter", money); reason != "" {
		t.Fatal(reason)
	}
	for _, mutate := range []func(*providerFile){
		func(f *providerFile) { f.Declaration.Host.Base = "https://custom.invalid/v1" },
		func(f *providerFile) { f.Declaration.Name = "custom" },
		func(f *providerFile) { f.Disabled = true },
	} {
		copy := file
		host := *file.Declaration.Host
		copy.Declaration.Host = &host
		mutate(&copy)
		if reason := unreportedProgramBudgetUnit([]providerFile{copy}, "openrouter", money); reason != "micros_per_day:unreported" {
			t.Fatal(reason)
		}
	}
	custom := file
	host := *file.Declaration.Host
	custom.Declaration.Host = &host
	host.Base = "https://custom.invalid/v1"
	if reason := unreportedProgramBudgetUnit([]providerFile{file, custom}, "openrouter", money); reason != "micros_per_day:unreported" {
		t.Fatal("shared binding bypass", reason)
	}
	if reason := unreportedProgramBudgetUnit(nil, "missing", inference.Ceiling{}); reason != "" {
		t.Fatal("No limit refused", reason)
	}
	if reason := unreportedProgramBudgetUnit(nil, "missing", inference.Ceiling{RequestsPerDay: 1}); reason != "" {
		t.Fatal("locally observed requests refused", reason)
	}
}

func TestInferenceProgramBudgetUsesExistingOperatorAuthority(t *testing.T) {
	f := newAnswererFixture(t)
	if err := f.a.rights.policy.RegisterAction(inference.ActionHostManage); err != nil {
		t.Fatal(err)
	}
	c, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "counts.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: f.a.rights}, ceilings: c, report: func(err error) { t.Error(err) }}
	caller := inference.Subject{Account: f.panel.Account, Program: f.panel.Program}
	if page := o.ProgramBudgets(t.Context(), caller, "fixture"); page.Outcome != wire.ProgramBudgetReadOutcomeForbidden || page.Revision != "" || len(page.Entries) != 0 {
		t.Fatal(page)
	}
	if err := f.a.rights.policy.Set(f.panel, inference.ActionHostManage, credentials.ResourceAccount, true); err != nil {
		t.Fatal(err)
	}
	page := o.ProgramBudgets(t.Context(), caller, "fixture")
	if page.Outcome != wire.ProgramBudgetReadOutcomePage || page.Revision == "" {
		t.Fatal(page)
	}
	if empty := o.ProgramBudgets(t.Context(), caller, ""); empty.Outcome != wire.ProgramBudgetReadOutcomeInvalid {
		t.Fatal("empty credential", empty)
	}
	for _, request := range []wire.ProgramBudgetChange{
		{Credential: "", Program: f.answerer.Program},
		{Credential: "fixture", Program: ""},
	} {
		result := o.SetProgramBudget(t.Context(), caller, request)
		want := "program"
		if request.Credential == "" {
			want = "credential"
		}
		if result.Outcome != wire.ProgramBudgetEditOutcomeInvalid || result.Reason != want {
			t.Fatal(result)
		}
	}
	if result := o.SetProgramBudget(t.Context(), caller, wire.ProgramBudgetChange{Credential: "fixture", Program: f.answerer.Program}); result.Outcome != wire.ProgramBudgetEditOutcomeConflict || result.Revision != page.Revision {
		t.Fatal("empty revision must retain conflict", result)
	}
	unsupported := wire.ProgramBudgetChange{ExpectedRevision: page.Revision, Credential: "fixture", Program: f.answerer.Program, Budget: &wire.ProgramBudget{MicrosPerDay: 100}}
	if result := o.SetProgramBudget(t.Context(), caller, unsupported); result.Outcome != wire.ProgramBudgetEditOutcomeInvalid || result.Reason != "micros_per_day:unreported" {
		t.Fatal(result)
	}
	if after := o.ProgramBudgets(t.Context(), caller, "fixture"); after.Revision != page.Revision || len(after.Entries) != 0 {
		t.Fatal("invalid unit wrote configuration", after)
	}
	request := wire.ProgramBudgetChange{ExpectedRevision: page.Revision, Credential: "fixture", Program: f.answerer.Program, Budget: &wire.ProgramBudget{RequestsPerDay: 1}}
	changed := o.SetProgramBudget(t.Context(), caller, request)
	if changed.Outcome != wire.ProgramBudgetEditOutcomeApplied {
		t.Fatal(changed)
	}
	page = o.ProgramBudgets(t.Context(), caller, "fixture")
	if len(page.Entries) != 1 || !identity.SameSubjectProgram(page.Entries[0].Program, f.answerer.Program) || page.Entries[0].Budget == nil || page.Entries[0].Budget.RequestsPerDay != 1 {
		t.Fatal(page)
	}
	if changed := o.SetProgramBudget(t.Context(), caller, request); changed.Outcome != wire.ProgramBudgetEditOutcomeConflict {
		t.Fatal(changed)
	}
	packaged := identity.PackagedProgramPrefix + "Fixture.App_123456789abcd"
	if !identity.ValidSubjectProgram(packaged) {
		t.Fatal("invalid package fixture")
	}
	packageChoice := wire.ProgramBudgetChange{ExpectedRevision: page.Revision, Credential: "fixture", Program: packaged, Budget: &wire.ProgramBudget{}}
	if changed := o.SetProgramBudget(t.Context(), caller, packageChoice); changed.Outcome != wire.ProgramBudgetEditOutcomeApplied {
		t.Fatal(changed)
	}
	page = o.ProgramBudgets(t.Context(), caller, "fixture")
	found := false
	for _, entry := range page.Entries {
		if entry.Program == packaged && entry.Budget != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("native package identity was converted into a path", page)
	}
	agent := inference.Subject{Account: f.answerer.Account, Program: f.answerer.Program}
	if changed := o.SetProgramBudget(t.Context(), agent, request); changed.Outcome != wire.ProgramBudgetEditOutcomeForbidden {
		t.Fatal("agent gained operator authority", changed)
	}
	if err := f.a.rights.policy.Revoke(f.panel, inference.ActionHostManage, credentials.ResourceAccount); err != nil {
		t.Fatal(err)
	}
	if page := o.ProgramBudgets(t.Context(), caller, "fixture"); page.Outcome != wire.ProgramBudgetReadOutcomeForbidden || len(page.Entries) != 0 {
		t.Fatal("revoked operator read budgets", page)
	}
	request.ExpectedRevision = page.Revision
	if changed := o.SetProgramBudget(t.Context(), caller, request); changed.Outcome != wire.ProgramBudgetEditOutcomeForbidden {
		t.Fatal("revoked operator wrote budget", changed)
	}
}
