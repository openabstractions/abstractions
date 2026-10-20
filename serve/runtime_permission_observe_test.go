package main

import (
	"context"
	"fmt"
	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestRuntimePermissionAnsweringRequestJoinsNativeEnrollment(t *testing.T) {
	r, a, _ := inputAsks(t)
	r.operators = []string{appProgram("runtime")}
	who := answerer{subject: rwire.Subject{Account: r.owner, Program: appProgram("answerer")}}
	if got := a.askForAnswering(t.Context(), who); got.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatal(got)
	}
	first, err := a.joinFirstUse(t.Context(), who.subject, func() asks.Publisher { return who.publisher }, asks.AnswerAction, rights.MetaScopeApps)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Joined == nil {
		t.Fatal(first, err)
	}
	second, err := a.joinFirstUse(t.Context(), who.subject, func() asks.Publisher { return who.publisher }, asks.AnswerAction, rights.MetaScopeApps)
	if err != nil || second.Joined == nil || second.Joined.ID != first.Joined.ID {
		t.Fatal(second, err)
	}
	for _, test := range []struct {
		subject  rwire.Subject
		resource string
		want     askswire.PermissionOutcome
	}{
		{who.subject, "another-scope", askswire.PermissionOutcomeInvalid},
		{rwire.Subject{Account: "foreign", Program: who.subject.Program}, rights.MetaScopeApps, askswire.PermissionOutcomeForbidden},
	} {
		got, err := a.joinFirstUse(t.Context(), test.subject, func() asks.Publisher { return asks.Publisher{} }, asks.AnswerAction, test.resource)
		if err != nil || got.Outcome != test.want || got.Joined != nil {
			t.Fatal(got, err)
		}
	}
}

func TestRuntimePermissionObservationLegacyAndPairedCurrentRevision(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("fixture uses native non-XPC Program transport")
	}
	for _, test := range []struct {
		name, action, resource string
		actions                []string
	}{{"model", grants.ModelLookupAction, "fixture", []string{grants.ModelLookupAction}}, {"paired", contentWriteAction, "input:image", []string{contentWriteAction, contentReadAction}}} {
		t.Run(test.name, func(t *testing.T) {
			r, a, book := inputAsks(t)
			catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
			if err != nil {
				t.Fatal(err)
			}
			r.policy.Definitions = catalog
			a.useTierFacts(nil, nil, []string{"fixture"})
			endpoint := listen.Endpoint(fmt.Sprintf("permission-observe-%d-%d", os.Getpid(), endpointSeq.Add(1)))
			h, err := asksservice.Listen(endpoint, book)
			if err != nil {
				t.Fatal(err)
			}
			if err = h.EnablePermission(a.preparePermission); err != nil {
				t.Fatal(err)
			}
			if err = h.EnablePermissionObservation(a.observePermission); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- h.Serve(t.Context()) }()
			t.Cleanup(func() {
				h.Close()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(time.Second):
					t.Error("host did not drain")
				}
			})
			client := askclient.NewPermission(endpoint)
			target := []byte(test.resource)
			request, err := client.RequestContext(t.Context(), askswire.PermissionIntent{RequestKey: "native-first-use", Selection: &rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: test.action, ResourceProfile: rights.LegacyExactResourceProfile}, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindOne, Target: target}}})
			if err != nil || request.Outcome != askswire.PermissionOutcomePending || request.Answer == nil {
				t.Fatal(request, err)
			}
			id := request.Answer.ID
			if _, _, err = book.AnswerApplication(id, "allow"); err != nil {
				t.Fatal(err)
			}
			program, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			subject := rwire.Subject{Account: r.owner, Program: identity.CanonicalProgramPath(program)}
			check := func(want askswire.PermissionOutcome) {
				t.Helper()
				out, err := client.ObserveQuestionContext(context.Background(), id, 0)
				if err != nil || out.Observation.Outcome != want || want == askswire.PermissionOutcomePermitted && out.Observation.PolicyRevision == "" {
					t.Fatal(out, err)
				}
			}
			check(askswire.PermissionOutcomePending)
			for i, action := range test.actions {
				if err := r.policy.Set(subject, action, test.resource, true); err != nil {
					t.Fatal(err)
				}
				if i < len(test.actions)-1 {
					check(askswire.PermissionOutcomePending)
				}
			}
			check(askswire.PermissionOutcomePermitted)
			if err := r.policy.Revoke(subject, test.actions[0], test.resource); err != nil {
				t.Fatal(err)
			}
			check(askswire.PermissionOutcomePending)
		})
	}
}
