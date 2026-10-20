package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// No credential holder or saved key exists in this fixture. Request may ask
// the person to establish use; it returns no secret or standing authority.
func TestProviderCredentialRequestBeforeKeyIsStored(t *testing.T) {
	_, a, _ := inputAsks(t)
	endpoint := listen.Endpoint(fmt.Sprintf("provider-key-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	h, err := asksservice.Listen(endpoint, a.book)
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePermission(a.preparePermission); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(context.Background()) }()
	t.Cleanup(func() {
		h.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("permission host did not drain")
		}
	})
	selection := rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: credentials.ActionApply, ResourceProfile: rights.LegacyExactResourceProfile}, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindOne, Target: []byte("credential:deepseek")}}
	intent := askswire.PermissionIntent{RequestKey: "use-unstored-provider", Selection: &selection}
	app := askclient.NewPermission(endpoint)
	first, err := app.RequestContext(t.Context(), intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatalf("unstored key request %+v %v", first, err)
	}
	second, err := app.RequestContext(t.Context(), intent)
	if err != nil || second.Answer == nil || second.Answer.ID != first.Answer.ID {
		t.Fatalf("request did not join %+v %v", second, err)
	}
	record, found, err := a.book.ApplicationRecord(first.Answer.ID)
	if err != nil || !found || !record.Pending() || record.Permission == nil || record.Permission.Targets[0].Resource != "credential:deepseek" {
		t.Fatalf("question %+v %v", record, err)
	}
	if grants.FirstUseTier(credentials.ActionApply, "credential:deepseek", a.tierFacts) != asks.TierSensitive {
		t.Fatal("provider key use lost person-only tier")
	}
	bound := record.Permission.Targets[0]
	if rule := a.rights.policy.ReadRule(rwire.Subject{Account: bound.Account, Program: bound.Program}, bound.Action, bound.Resource); rule.Record != nil {
		t.Fatal("request granted use before a native answer")
	}
	intent.RequestKey = "another-provider-tool-call"
	again, err := app.RequestContext(t.Context(), intent)
	if err != nil || again.Answer == nil || again.Answer.ID != first.Answer.ID {
		t.Fatal("tool chain raised another question", again, err)
	}
	answered, err := grants.AnswerQuestion(t.Context(), pendingQuestion{book: a.book}, policyRules{policy: a.rights.policy, by: rwire.Subject{Account: a.rights.owner, Program: appProgram("Panel")}, asks: a}, a.rights.owner, record.ID, "allow")
	if err != nil || answered.Edit == nil || answered.Edit.Outcome != rwire.PolicyEditOutcomeUnavailable {
		t.Fatal("missing setup guard did not retain answer", answered, err)
	}
	intent.RequestKey = "provider-after-unapplied-answer"
	standing, err := app.RequestContext(t.Context(), intent)
	if err != nil || standing.Outcome != askswire.PermissionOutcomeAnswered || standing.Answer == nil || standing.Answer.ID != first.Answer.ID || standing.Answer.Option != "allow" {
		t.Fatal("Request replaced unapplied standing answer", standing, err)
	}
	if history, _, err := a.book.ApplicationHistory(); err != nil || len(history) != 1 {
		t.Fatal("Request duplicated occurrence", history, err)
	}
	intent.Purpose = "custom text has no native presentation owner"
	if out, err := app.RequestContext(t.Context(), intent); err != nil || out.Outcome != askswire.PermissionOutcomeInvalid || out.Answer != nil {
		t.Fatal("legacy Purpose restriction changed", out, err)
	}
	intent.Purpose = ""
	for _, target := range []string{"credential:missing-provider", "credential:github-download", "credential:openai-compatible", "credential:*"} {
		selection.Selector.Target = []byte(target)
		if out, err := app.RequestContext(t.Context(), intent); err != nil || out.Outcome != askswire.PermissionOutcomeInvalid || out.Answer != nil {
			t.Fatal("unsupported provider resource admitted", target, out, err)
		}
	}
}
