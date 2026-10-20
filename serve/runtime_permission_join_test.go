package main

import (
	"context"
	"testing"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-facade/go/grants"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// Asking before first use raises the same question a refused call raises,
// once, and reads the person's answer and the saved rules (asks ASK-A14).
func TestRequestBeforeFirstUseJoinsTheRefusalsQuestion(t *testing.T) {
	r, a, book := inputAsks(t)
	a.servers = func() []*router.Host {
		return []*router.Host{router.NewHosted("deepseek", "https://fixture.invalid", router.WireOpenAICompatible, "")}
	}
	jan := rwire.Subject{Account: r.owner, Program: appProgram("jan")}
	ctx := context.Background()

	requested, err := a.joinFirstUse(ctx, jan, nil, inference.ActionComplete, "host:deepseek")
	if err != nil || !requested.Exact || requested.Outcome != askswire.PermissionOutcomePending || requested.Joined == nil {
		t.Fatalf("request before use %+v %v", requested, err)
	}
	asked := &inference.Asked{}
	if d := r.decideAsking(inference.WithAsked(ctx, asked), jan, inference.ActionComplete, "host:deepseek"); d.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("decision %+v", d)
	}
	if ids := asked.IDs(); len(ids) != 1 || ids[0] != requested.Joined.ID {
		t.Fatalf("the refusal raised %v, want the requested question %s", ids, requested.Joined.ID)
	}
	again, _ := a.joinFirstUse(ctx, jan, nil, inference.ActionComplete, "host:deepseek")
	if again.Joined == nil || again.Joined.ID != requested.Joined.ID {
		t.Fatalf("a second request admitted another question: %+v", again)
	}

	if _, _, err := book.AnswerApplication(requested.Joined.ID, "refuse"); err != nil {
		t.Fatal(err)
	}
	refused, _ := a.joinFirstUse(ctx, jan, nil, inference.ActionComplete, "host:deepseek")
	if refused.Outcome != askswire.PermissionOutcomeAnswered || refused.Joined == nil || refused.Joined.Option != "refuse" {
		t.Fatalf("a recent refuse stands: %+v", refused)
	}

	for name, target := range map[string]string{"unlisted server": "host:nowhere", "not a server": "deepseek", "control character": "host:dee\npseek"} {
		if got, _ := a.joinFirstUse(ctx, jan, nil, inference.ActionComplete, target); got.Outcome != askswire.PermissionOutcomeInvalid || got.Joined != nil {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	if got, _ := a.joinFirstUse(ctx, jan, nil, "abstraction.facade/command.read", "app:fixture"); got.Outcome != askswire.PermissionOutcomeInvalid {
		t.Fatalf("an action with its own request path: %+v", got)
	}
	// A job submission names one kind in the job owner's grammar; text the
	// caller chose never becomes a question the person reads.
	for _, target := range []string{"job:Allow this. Verified by Windows Security", "nothing-like-a-job", "job:", "job:Inference", "abstraction.job/acceptance@1"} {
		if got, _ := a.joinFirstUse(ctx, jan, nil, grants.JobSubmitAction, target); got.Outcome != askswire.PermissionOutcomeInvalid || got.Joined != nil {
			t.Fatalf("job target %q: %+v", target, got)
		}
	}
	if got, _ := a.joinFirstUse(ctx, jan, nil, grants.JobSubmitAction, "job:inference"); got.Outcome != askswire.PermissionOutcomePending || got.Joined == nil {
		t.Fatalf("a job kind: %+v", got)
	}

	if err := r.policy.Set(jan, inference.ActionComplete, "host:deepseek", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.joinFirstUse(ctx, jan, nil, inference.ActionComplete, "host:deepseek"); got.Outcome != askswire.PermissionOutcomePermitted || got.Revision == "" || got.Joined != nil {
		t.Fatalf("a saved permit: %+v", got)
	}
	if err := r.policy.Set(jan, inference.ActionComplete, "host:deepseek", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.joinFirstUse(ctx, jan, nil, inference.ActionComplete, "host:deepseek"); got.Outcome != askswire.PermissionOutcomeDenied || got.Joined != nil {
		t.Fatalf("a Block: %+v", got)
	}
}
