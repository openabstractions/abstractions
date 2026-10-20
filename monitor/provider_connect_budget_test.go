package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	identity "github.com/openabstractions/abstraction-identity"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
)

func TestProviderBudgetUsesAdmittedPermissionProgram(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "beneficiary.exe")
	if err := os.WriteFile(program, []byte("inert fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := dir + string(os.PathSeparator) + "." + string(os.PathSeparator) + "beneficiary.exe"
	permission, err := asks.NewPermissionRequest(asks.PermissionTarget{Account: "fixture-account", Program: raw, Action: "abstraction.credentials/apply", Resource: "credential:deepseek"})
	if err != nil {
		t.Fatal(err)
	}
	admitted := permission.Targets[0].Program
	companion := permission.Targets[0]
	companion.Action, companion.Resource = "abstraction.inference/complete", "host:deepseek"
	permission.Targets = append(permission.Targets, companion)
	if provider, ok := asks.ProviderPermissionName(permission); !ok || provider != "deepseek" {
		t.Fatal("fixture is not the native provider permission pair", permission)
	}
	if admitted == raw || admitted != identity.NormalizeSubjectProgram(raw) || identity.NormalizeSubjectProgram(admitted) != admitted {
		t.Fatal("permission did not supply the stable native spelling", raw, admitted)
	}
	f := &providerBudgetFixture{page: iclient.ProgramBudgetPage{Outcome: iclient.ProgramBudgetReadOutcomePage, Revision: "r1", Profiles: []string{"chat"}}}
	if err := setProviderProgramBudget(t.Context(), f, "deepseek", admitted, 100); err != nil {
		t.Fatal(err)
	}
	if len(f.changes) != 1 || f.changes[0].Program != admitted {
		t.Fatal("beneficiary changed", f.changes)
	}
	amount, err := providerConfiguredBudgetChoice(f.page, "deepseek", admitted)
	if err != nil || amount == nil || *amount != 100 {
		t.Fatal(amount, err)
	}
	// A different beneficiary must still fail readback; normalization is not
	// permission to borrow another program's configured budget.
	if err := checkProviderProgramBudget(t.Context(), f, "deepseek", filepath.Join(dir, "other.exe"), 100); err == nil {
		t.Fatal("another beneficiary's budget accepted")
	}
}

type providerBudgetFixture struct {
	page           iclient.ProgramBudgetPage
	changes        []iclient.ProgramBudgetChange
	conflict       bool
	dropReadback   bool
	reads          int
	concurrentSame bool
}

func (f *providerBudgetFixture) ProgramBudgets(context.Context, string) (iclient.ProgramBudgetPage, error) {
	f.reads++
	return f.page, nil
}
func (f *providerBudgetFixture) SetProgramBudget(_ context.Context, request iclient.ProgramBudgetChange) (iclient.ProgramBudgetResult, error) {
	f.changes = append(f.changes, request)
	if f.conflict {
		f.conflict = false
		f.page.Revision = "r2"
		if f.concurrentSame {
			f.page.Entries = []iclient.ProgramBudgetEntry{{Program: request.Program, Budget: request.Budget, CostUnreported: true}}
		}
		return iclient.ProgramBudgetResult{Outcome: iclient.ProgramBudgetEditOutcomeConflict}, nil
	}
	if !f.dropReadback {
		f.page.Entries = []iclient.ProgramBudgetEntry{{Program: request.Program, Budget: request.Budget}}
	}
	return iclient.ProgramBudgetResult{Outcome: iclient.ProgramBudgetEditOutcomeApplied}, nil
}

func TestProviderConnectBudgetUsesNativeProgramAndChecksPersistedChoice(t *testing.T) {
	for _, amount := range []int64{0, 100000} {
		f := &providerBudgetFixture{page: iclient.ProgramBudgetPage{Outcome: iclient.ProgramBudgetReadOutcomePage, Revision: "r1", Profiles: []string{"chat"}}, conflict: true}
		if err := setProviderProgramBudget(t.Context(), f, "deepseek", "native-question-program", amount); err != nil {
			t.Fatal(err)
		}
		if len(f.changes) != 2 || f.changes[0].ExpectedRevision != "r1" || f.changes[1].ExpectedRevision != "r2" {
			t.Fatal(f.changes)
		}
		for _, change := range f.changes {
			if change.Program != "native-question-program" || change.Credential != "deepseek" || change.Budget == nil || change.Budget.TokensPerDay != amount || change.Budget.MicrosPerDay != 0 {
				t.Fatal(change)
			}
		}
	}
}

func TestProviderConnectBudgetRefusesUnsupportedOrUnprovedReadback(t *testing.T) {
	for _, mode := range []string{"unsupported", "uncovered", "missing-row", "usage-only", "wrong-program", "different-limit"} {
		f := &providerBudgetFixture{page: iclient.ProgramBudgetPage{Outcome: iclient.ProgramBudgetReadOutcomePage, Revision: "r1", Profiles: []string{"chat"}}, dropReadback: true}
		switch mode {
		case "unsupported":
			f.page.Outcome = iclient.ProgramBudgetReadOutcomeUnsupported
		case "uncovered":
			f.page.Profiles = []string{"image"}
		case "usage-only":
			f.page.Entries = []iclient.ProgramBudgetEntry{{Program: "native-program"}}
		case "wrong-program":
			f.page.Entries = []iclient.ProgramBudgetEntry{{Program: "other-program", Budget: &iclient.ProgramBudget{}}}
		case "different-limit":
			f.page.Entries = []iclient.ProgramBudgetEntry{{Program: "native-program", Budget: &iclient.ProgramBudget{TokensPerDay: 1}}}
		}
		if err := setProviderProgramBudget(t.Context(), f, "deepseek", "native-program", 0); err == nil {
			t.Fatal(mode, "unproved budget accepted")
		}
		if (mode == "unsupported" || mode == "uncovered") && len(f.changes) != 0 {
			t.Fatal(mode, "wrote before preflight")
		}
	}
}

func TestProviderConnectBudgetRefreshesUnreportedCostWithSameRevision(t *testing.T) {
	f := &providerBudgetFixture{page: iclient.ProgramBudgetPage{Outcome: iclient.ProgramBudgetReadOutcomePage, Revision: "same-config", Profiles: []string{"chat"}, Entries: []iclient.ProgramBudgetEntry{{Program: "native-program", Budget: &iclient.ProgramBudget{MicrosPerDay: 100000}}}}}
	if err := checkProviderProgramBudget(t.Context(), f, "openrouter", "native-program", 100000); err != nil {
		t.Fatal(err)
	}
	f.page.Entries[0].CostUnreported = true
	if err := checkProviderProgramBudget(t.Context(), f, "openrouter", "native-program", 100000); err != errProviderCostUnreported {
		t.Fatal("cached config revision hid missing cost", err)
	}
	if err := checkProviderBudgetConfiguration(t.Context(), f, "openrouter", "native-program", 100000, false); err != nil {
		t.Fatal("daily cost state changed configured setup readback", err)
	}
	f.page.Entries[0].Budget.MicrosPerDay++
	if err := checkProviderBudgetConfiguration(t.Context(), f, "openrouter", "native-program", 100000, false); err == nil {
		t.Fatal("cost flag hid a changed budget configuration")
	}
	f.page.Entries[0].Budget.MicrosPerDay--
	f.page.Entries[0].Program = "other-program"
	f.page.Entries = append(f.page.Entries, iclient.ProgramBudgetEntry{Program: "native-program", Budget: &iclient.ProgramBudget{MicrosPerDay: 100000}})
	if err := checkProviderProgramBudget(t.Context(), f, "openrouter", "native-program", 100000); err != nil {
		t.Fatal("other program cost state leaked", err)
	}
}

func TestProviderReopenPreservesConfiguredChoiceAndDailyCostFlag(t *testing.T) {
	for _, amount := range []int64{0, 123456} {
		f := &providerBudgetFixture{page: iclient.ProgramBudgetPage{Outcome: iclient.ProgramBudgetReadOutcomePage, Revision: "r1", Profiles: []string{"chat"}, Entries: []iclient.ProgramBudgetEntry{{Program: "native-program", Budget: &iclient.ProgramBudget{MicrosPerDay: amount}, CostUnreported: true}}}}
		choice, err := providerConfiguredBudgetChoice(f.page, "openrouter", "native-program")
		if err != nil || choice == nil || *choice != amount {
			t.Fatal(choice, err)
		}
		form, err := (&providerConnectController{provider: "openrouter", initialBudget: choice}).form()
		if err != nil || form.DefaultBudget != amount {
			t.Fatal("reopen guessed a different choice", form, err)
		}
		if err := setProviderProgramBudget(t.Context(), f, "openrouter", "native-program", amount); err != nil || len(f.changes) != 0 || f.reads != 1 || !f.page.Entries[0].CostUnreported {
			t.Fatal("identical choice changed native budget state", f, err)
		}
		f.page.Entries[0].Budget.TokensPerDay = 7
		if _, err := providerConfiguredBudgetChoice(f.page, "openrouter", "native-program"); err == nil {
			t.Fatal("one-field chooser would erase another configured limit")
		}
		if choice, err := providerConfiguredBudgetChoice(f.page, "openrouter", "other-program"); err != nil || choice != nil {
			t.Fatal("another program supplied initial choice", choice, err)
		}
	}
	f := &providerBudgetFixture{page: iclient.ProgramBudgetPage{Outcome: iclient.ProgramBudgetReadOutcomePage, Revision: "r1", Profiles: []string{"chat"}}, conflict: true, concurrentSame: true}
	if err := setProviderProgramBudget(t.Context(), f, "openrouter", "native-program", 123); err != nil || len(f.changes) != 1 || f.reads != 2 || f.page.Revision != "r2" || !f.page.Entries[0].CostUnreported {
		t.Fatal("CAS same-choice recovery overwrote fresh state", f, err)
	}
}
