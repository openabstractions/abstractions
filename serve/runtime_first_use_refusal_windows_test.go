package main

import (
	"context"
	"errors"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	inference "github.com/openabstractions/abstraction-inference/go"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

func TestRuntimeFirstUseRequireRetainsQuestionAndDecisionShape(t *testing.T) {
	peer, _ := applicationV3NativePeer(t)
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _ := inputAsks(t)
	calls := 0
	r.firstUse = func(got rightswire.Subject, _ func() asks.Publisher, action, resource string) string {
		calls++
		if got != subject || action != grants.JobSubmitAction || resource != "job:inference" {
			t.Fatal(got, action, resource)
		}
		return "q1"
	}
	asked := new(inference.Asked)
	err = r.Require(inference.WithAsked(t.Context(), asked), peer, grants.JobSubmitAction, "job:inference")
	var decision *rightsclient.DecisionError
	var pending interface{ PermissionRefusalReason() string }
	if !errors.As(err, &decision) || decision.Outcome != "not_granted" || !errors.As(err, &pending) || pending.PermissionRefusalReason() != "rights:not_granted rights:question_pending:q1" || calls != 1 || len(asked.IDs()) != 1 {
		t.Fatal(err, decision, calls, asked.IDs())
	}
	if err := r.policy.Set(subject, grants.JobSubmitAction, "job:inference", true); err != nil {
		t.Fatal(err)
	}
	if err := r.Require(t.Context(), peer, grants.JobSubmitAction, "job:inference"); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	if err := r.policy.Set(subject, grants.JobSubmitAction, "job:inference", false); err != nil {
		t.Fatal(err)
	}
	err = r.Require(t.Context(), peer, grants.JobSubmitAction, "job:inference")
	if !errors.As(err, &decision) || decision.Outcome != "denied" || errors.As(err, &pending) || calls != 1 {
		t.Fatal("explicit deny raised a pending question", err, calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Require(ctx, peer, grants.JobSubmitAction, "job:inference"); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal(err, calls)
	}
}
