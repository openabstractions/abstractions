package main

import (
	"context"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestCredentialBatchRefusalsRetainRightsAudit(t *testing.T) {
	for _, tc := range []struct {
		decision rwire.BatchDecision
		word     string
	}{
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeInvalid}, "rights:invalid"},
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeForbidden}, "rights:forbidden"},
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeUnavailable}, "rights:unavailable"},
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked, Revision: "one", Outcomes: []rwire.DecisionOutcome{rwire.DecisionOutcomeDenied}}, "denied"},
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked, Revision: "one", Outcomes: []rwire.DecisionOutcome{rwire.DecisionOutcomeNotGranted}}, "not_granted"},
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked, Revision: "one", Outcomes: []rwire.DecisionOutcome{rwire.DecisionOutcomeUnknownAction}}, "unknown_action"},
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked, Revision: "one", Outcomes: []rwire.DecisionOutcome{rwire.DecisionOutcomeInvalid}}, "rights:invalid"},
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked, Revision: "one", Outcomes: []rwire.DecisionOutcome{rwire.DecisionOutcomeForbidden}}, "rights:forbidden"},
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked, Revision: "one", Outcomes: []rwire.DecisionOutcome{rwire.DecisionOutcomeUnavailable}}, "rights:unavailable"},
		// A revision conflict conveys no individual Rights decision.
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeConflict, Revision: "two"}, "unavailable"},
		// A malformed checked response supplies no valid individual decision.
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked}, "unavailable"},
		{rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked, Outcomes: []rwire.DecisionOutcome{rwire.DecisionOutcomeDenied}}, "unavailable"},
	} {
		t.Run(tc.word+"/"+tc.decision.Outcome.String(), func(t *testing.T) {
			h, err := credentials.Open(credentials.Config{})
			if err != nil {
				t.Fatal(err)
			}
			s := probeSelf()
			use := cwire.Use{Subject: cwire.Subject{Account: s.Account, Program: s.Program}, Name: "alpha", Consumer: "abstraction.download/http-execution@1", Target: "127.0.0.1"}
			a := credentials.ScopedAuthorization{Admit: func() credentials.Authorization { return credentialBatchAuthorization(tc.decision) }, Check: func(string) credentials.Authorization { panic("refused batch admitted") }}
			r := h.CheckScoped(context.Background(), use, a)
			if r.Outcome != cwire.ApplyOutcomeNotPermitted && r.Outcome != cwire.ApplyOutcomeUnavailable {
				t.Fatal("Rights batch refusal admitted Use")
			}
			page := h.Audit(s.Account, "", 256)
			if len(page.Entries) != 1 || page.Entries[0].Event != "refused" || page.Entries[0].Outcome != tc.word {
				t.Fatalf("batch-to-use audit lost exact refusal: %+v", page.Entries)
			}
		})
	}
}
