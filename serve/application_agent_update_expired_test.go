package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
)

// R24 (2026-11-11): an R23 update question expired unanswered (ASK-A7). Login
// discovery kept returning its intent as question_gone, the Panel's batch
// stopped there, and no later upgrade asked or published again.
func TestAgentUpdateExpiredQuestionNeitherBlocksDiscoveryNorLaterUpdate(t *testing.T) {
	run, request := agentUpdateFixture(t)
	first := prepareAgentUpdateFixture(t, run, request)
	expireAgentQuestionFixture(t, run)
	if _, found, err := run.f.a.book.ApplicationRecord(first.Question); err != nil || found {
		t.Fatal("fixture did not expire", found, err)
	}
	for _, m := range request.Members {
		found := run.d.findAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: m.Application}}})
		if found.Outcome != applications.OutcomeUnknown || found.Intent != "" {
			t.Fatal("expired question still retained for discovery", found)
		}
	}
	// A newer installed connector arrives with new staged copies.
	next := applications.AgentUpdateRequest{SourceProgram: filepath.Join(filepath.Dir(request.SourceProgram), "installed-next.exe")}
	if err := os.WriteFile(next.SourceProgram, []byte("newer installed gateway"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, m := range request.Members {
		stage := m.StagedProgram + "-next"
		if err := os.WriteFile(stage, []byte("newer installed gateway"), 0600); err != nil {
			t.Fatal(err)
		}
		next.Members = append(next.Members, applications.AgentUpdateMember{Application: m.Application, ExpectedRevision: m.ExpectedRevision, StagedProgram: stage})
	}
	run.d.agentExcluded = func(_ context.Context, program, key string) (bool, error) { return identity.SameFile(program, next.SourceProgram) }
	asked := prepareAgentUpdateFixture(t, run, next)
	if asked.Intent == first.Intent || asked.Question == first.Question {
		t.Fatal("later update reused the expired intent", asked)
	}
	answer(t, run.f, asked.Question, "update")
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, asked.Intent); got.Reason != "update_ready" {
		t.Fatal("later update not ready", got)
	}
}

// The same staged copies after an expired question ask once more under the
// same intent; an expiry is no person's answer.
func TestAgentUpdateExpiredQuestionAsksAgainForSameStagedCopies(t *testing.T) {
	run, request := agentUpdateFixture(t)
	first := prepareAgentUpdateFixture(t, run, request)
	expireAgentQuestionFixture(t, run)
	exact := run.d.findAgentUpdate(run.ctx, run.f.panel, request)
	if exact.Intent != first.Intent || len(exact.StagedImages) != 2 {
		t.Fatal("exact lookup lost the staged capture", exact)
	}
	again := prepareAgentUpdateFixture(t, run, request)
	if again.Intent != first.Intent || again.Question == first.Question {
		t.Fatal("expired question not asked again", again)
	}
	answer(t, run.f, again.Question, "update")
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, again.Intent); got.Reason != "update_ready" {
		t.Fatal("reasked update not ready", got)
	}
	publishAgentUpdateFixture(t, request)
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, again.Intent); got.Reason != "updated" {
		t.Fatal("reasked update not committed", got)
	}
}
