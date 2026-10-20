package main

import (
	"context"
	"slices"
	"testing"

	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A refusal names only the caller's own questions that are still pending
// (INF-W11): another program's question, an answered one and an unknown id
// are left out.
func TestPendingQuestionsNamesOnlyTheCallersPendingQuestions(t *testing.T) {
	r, a, book := inputAsks(t)
	jan := rwire.Subject{Account: r.owner, Program: appProgram("jan")}
	bob := rwire.Subject{Account: r.owner, Program: appProgram("bob")}
	ask := func(s rwire.Subject) string {
		asked := &inference.Asked{}
		if d := r.decideAsking(inference.WithAsked(context.Background(), asked), s, inference.ActionComplete, "host:deepseek"); d.Outcome != rwire.DecisionOutcomeNotGranted {
			t.Fatalf("decision %+v", d)
		}
		if ids := asked.IDs(); len(ids) == 1 {
			return ids[0]
		}
		t.Fatal("no question")
		return ""
	}
	janQ, bobQ := ask(jan), ask(bob)
	got, err := a.pendingQuestions(jan, []string{janQ, bobQ, "unknown-question", janQ})
	if err != nil || !slices.Equal(got, []string{janQ}) {
		t.Fatalf("jan's pending questions %v %v, want [%s]", got, err, janQ)
	}
	if _, _, err := book.AnswerApplication(janQ, "refuse"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.pendingQuestions(jan, []string{janQ}); err != nil || len(got) != 0 {
		t.Fatalf("an answered question is still named: %v %v", got, err)
	}
	if got, _ := a.pendingQuestions(rwire.Subject{Account: "another-account", Program: bob.Program}, []string{bobQ}); len(got) != 0 {
		t.Fatalf("another account read %v", got)
	}
}
