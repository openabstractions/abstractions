package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestAgentUpdateOpenCodeAnsweringPermissionDoesNotCreateNativeKeep(t *testing.T) {
	for _, approval := range []string{"generic_allow", "responder_enrollment"} {
		t.Run(approval, func(t *testing.T) {
			run, request := agentUpdateFixture(t)
			const name = "oa-opencode-integration"
			agent1478Register(t, run, name, run.f.answerer.Program)
			if approval == "generic_allow" {
				question := run.f.ask(t, run.f.answerer, asks.AnswerAction, rights.MetaScopeApps)
				operator := policyRules{policy: run.f.a.rights.policy, by: run.f.panel, asks: run.f.a}
				metadata := askclient.RecordMetadata{Key: question.Key, About: question.About, Text: question.Text}
				answered, err := grants.AnswerQuestion(run.ctx, pendingQuestion{record: metadata, book: run.f.a.book}, operator, run.f.panel.Account, question.ID, "allow")
				if err != nil || answered.Edit == nil || answered.Edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Fatal("generic Allow did not apply", answered, err)
				}
			} else if edit := run.f.enroll(t, run.f.answerer); edit.Outcome != rwire.PolicyEditOutcomeApplied {
				t.Fatal("ASK-R5 enrollment did not apply", edit)
			}
			if got := run.f.decide(run.f.answerer, asks.AnswerAction, rights.MetaScopeApps); got != rwire.DecisionOutcomePermitted {
				t.Fatal("answering permission absent", got)
			}
			read := run.d.readAgentEnrollment(run.ctx, run.f.panel, name)
			if read.Outcome != applications.OutcomePage || read.Enrollment == nil || read.Enrollment.State != applications.AgentEnrollmentStateNone || read.Enrollment.Revision != "" {
				t.Fatal("answering permission created native enrollment", read)
			}
			request.Members = []applications.AgentUpdateMember{{Application: name, ExpectedRevision: "no-native-keep", StagedProgram: request.Members[0].StagedProgram}}
			got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
			if got.Outcome != applications.OutcomeConflict || got.Reason != "enrollment:"+name || got.Intent != "" || got.Question != "" || len(got.StagedImages) != 0 {
				t.Fatal("answering permission admitted native update", got)
			}
		})
	}
}

func TestAgentUpdateOpenCodePersonConnectionKeepAdmitsCommonBatch(t *testing.T) {
	run, request := agentUpdateFixture(t)
	const name = "oa-opencode-integration"
	if got := run.d.register(run.ctx, run.f.panel, wire.ApplicationDescriptor{Name: name, Title: "OpenCode", Program: run.f.answerer.Program}); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	pending := agent1478Set(t, run, name, "", true)
	question := agent1478Question(t, run, name)
	if question.Text != "Connect OpenCode?" {
		t.Fatal(question.Text)
	}
	answer(t, run.f, question.ID, "keep")
	agent1478Wait(t, run, name, "enrolled")
	read := run.d.readAgentEnrollment(run.ctx, run.f.panel, name)
	if read.Outcome != applications.OutcomePage || read.Enrollment == nil || read.Enrollment.State != applications.AgentEnrollmentStateEnrolled || read.Enrollment.Revision != pending.Revision {
		t.Fatal(read)
	}
	if retry := agent1478Set(t, run, name, pending.Revision, true); retry.State != applications.AgentEnrollmentStateEnrolled || agent1478Question(t, run, name).ID != question.ID {
		t.Fatal("native connection prompted again", retry)
	}
	staged := run.f.answerer.Program + ".staged"
	payload, err := os.ReadFile(request.SourceProgram)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, payload, 0600); err != nil {
		t.Fatal(err)
	}
	request.Members = append(request.Members, applications.AgentUpdateMember{Application: name, ExpectedRevision: pending.Revision, StagedProgram: staged})
	got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if got.Outcome != applications.OutcomeApplied || got.Reason != "permission_pending" || len(got.StagedImages) != 3 || got.Intent == "" || got.Question == "" {
		t.Fatal("person-kept OpenCode missed common batch", got)
	}
}

func agentUpdateFixture(t *testing.T) (*selfRegisteredRun, applications.AgentUpdateRequest) {
	return agentUpdateFixtureWithImage(t, nil)
}
func agentUpdateFixtureWithImage(t *testing.T, configure func(*selfRegisteredRun)) (*selfRegisteredRun, applications.AgentUpdateRequest) {
	t.Helper()
	run := agent1478Fixture(t)
	run.d.agentCompanionProducer = &agentCompanionFixture{}
	if configure != nil {
		configure(run)
	}
	root := t.TempDir()
	source := filepath.Join(root, "installed.exe")
	if err := os.WriteFile(source, []byte("new installed gateway"), 0600); err != nil {
		t.Fatal(err)
	}
	request := applications.AgentUpdateRequest{SourceProgram: source, Members: []applications.AgentUpdateMember{}}
	for _, name := range []string{"batch-codex", "batch-claude"} {
		program := filepath.Join(root, name+".exe")
		stage := program + ".staged"
		if err := os.WriteFile(program, []byte("old "+name), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stage, []byte("new installed gateway"), 0600); err != nil {
			t.Fatal(err)
		}
		title := map[string]string{"batch-codex": "Codex integration", "batch-claude": "Claude Code integration"}[name]
		if got := run.d.register(run.ctx, run.f.panel, wire.ApplicationDescriptor{Name: name, Program: program, Title: title}); got.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(got)
		}
		pending := agent1478Set(t, run, name, "", true)
		q := agent1478Question(t, run, name)
		answer(t, run.f, q.ID, "keep")
		agent1478Wait(t, run, name, "enrolled")
		request.Members = append(request.Members, applications.AgentUpdateMember{Application: name, ExpectedRevision: pending.Revision, StagedProgram: stage})
	}
	run.d.agentExcluded = func(_ context.Context, program, key string) (bool, error) { return identity.SameFile(program, source) }
	return run, request
}
func prepareAgentUpdateFixture(t *testing.T, run *selfRegisteredRun, request applications.AgentUpdateRequest) applications.AgentUpdateResult {
	t.Helper()
	got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if got.Outcome != applications.OutcomeApplied || got.Reason != "permission_pending" || got.Question == "" || len(got.StagedImages) != 2 {
		t.Fatalf("prepare: %+v", got)
	}
	return got
}
func publishAgentUpdateFixture(t *testing.T, request applications.AgentUpdateRequest) {
	t.Helper()
	for _, m := range request.Members {
		target := m.StagedProgram[:len(m.StagedProgram)-len(".staged")]
		if err := os.Rename(target, m.StagedProgram+".previous"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(m.StagedProgram, target); err != nil {
			t.Fatal(err)
		}
	}
}
func TestAgentUpdateBatchOnePersonOccurrenceAndConditionalCommit(t *testing.T) {
	run, request := agentUpdateFixture(t)
	if got := run.d.findAgentUpdate(run.ctx, run.f.panel, request); got.Outcome != applications.OutcomeUnknown {
		t.Fatal("read created intent", got)
	}
	pending := prepareAgentUpdateFixture(t, run, request)
	again := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if again.Intent != pending.Intent || again.Question != pending.Question {
		t.Fatal("duplicate prompt", again)
	}
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Reason != "permission_pending" {
		t.Fatal("unanswered published", got)
	}
	q, found, err := run.f.a.book.ApplicationRecord(pending.Question)
	if err != nil || !found || q.Key != asks.AgentUpdateKey {
		t.Fatal(q, found, err)
	}
	if effect, err := run.f.a.authorizeAnswerer(run.ctx, answerer{subject: run.f.answerer}, q, run.f.a.scope, "update"); err == nil || effect != nil {
		t.Fatal("assistant answered batch")
	}
	answer(t, run.f, pending.Question, "update")
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Reason != "update_ready" {
		t.Fatal("publication authority", got)
	}
	publishAgentUpdateFixture(t, request)
	got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent)
	if got.Outcome != applications.OutcomeApplied || got.Reason != "updated" || len(got.Enrollments) != 2 {
		t.Fatal("commit", got)
	}
	for _, m := range request.Members {
		read := run.d.currentAgentEnrollment(run.ctx, m.Application)
		if read.Enrollment == nil || read.Enrollment.State != applications.AgentEnrollmentStateEnrolled || run.d.unconfirmed[m.Application].agentEnrollmentIdentity() != m.ExpectedRevision {
			t.Fatal("batch Keep lost", read)
		}
	}
	replay := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent)
	if replay.Reason != "updated" || len(replay.Enrollments) != 2 {
		t.Fatal("commit replay", replay)
	}
	for _, m := range request.Members {
		found := run.d.findAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: m.Application}}})
		if found.Intent != pending.Intent || found.Reason != "updated" {
			t.Fatal("committed login discovery", found)
		}
	}
	m := request.Members[0]
	current := run.d.currentAgentEnrollment(run.ctx, m.Application)
	if got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: m.Application, ExpectedRevision: current.Enrollment.Revision, Enabled: false}); got.Outcome != applications.OutcomeApplied {
		t.Fatal("committed batch blocked revoke", got)
	}
	if run.d.unconfirmed[m.Application].agentEnrollmentIdentity() != "" {
		t.Fatal("revoked enrollment retained its identity")
	}
	reconnected := agent1478Set(t, run, m.Application, run.d.unconfirmed[m.Application].RegistrationRevision, true)
	question := agent1478Question(t, run, m.Application)
	answer(t, run.f, question.ID, "keep")
	agent1478Wait(t, run, m.Application, "enrolled")
	if got := run.d.unconfirmed[m.Application].agentEnrollmentIdentity(); got != reconnected.Revision || got == m.ExpectedRevision {
		t.Fatal("reconnected enrollment reused the old identity", got)
	}
	if r, found, err := run.f.a.book.ApplicationRecord(pending.Question); err != nil || !found || r.Pending() || r.Option != "update" {
		t.Fatal("committed question changed", r, found, err)
	}
}
func TestAgentUpdateBatchDeclinePreservesImagesAndEnrollment(t *testing.T) {
	run, request := agentUpdateFixture(t)
	pending := prepareAgentUpdateFixture(t, run, request)
	answer(t, run.f, pending.Question, "not_now")
	for i := 0; i < 2; i++ {
		got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
		if got.Question != pending.Question || got.Reason != "not_now" {
			t.Fatal("decline reasked", got)
		}
		if got = run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Reason != "not_now" {
			t.Fatal(got)
		}
	}
	for _, m := range request.Members {
		read := run.d.currentAgentEnrollment(run.ctx, m.Application)
		if read.Enrollment == nil || read.Enrollment.Revision != m.ExpectedRevision || read.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
			t.Fatal("decline changed enrollment", read)
		}
		if _, err := os.Stat(m.StagedProgram); err != nil {
			t.Fatal("stage lost", err)
		}
	}
}
func TestAgentUpdateBatchChangedInputsRefuseWithoutPublication(t *testing.T) {
	for _, change := range []string{"stage", "source", "revision", "overlap"} {
		t.Run(change, func(t *testing.T) {
			run, request := agentUpdateFixture(t)
			pending := prepareAgentUpdateFixture(t, run, request)
			answer(t, run.f, pending.Question, "update")
			switch change {
			case "stage":
				if err := os.WriteFile(request.Members[0].StagedProgram, []byte("changed staged gateway"), 0600); err != nil {
					t.Fatal(err)
				}
			case "source":
				if err := os.WriteFile(request.SourceProgram, []byte("changed source"), 0600); err != nil {
					t.Fatal(err)
				}
			case "revision":
				got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: request.Members[0].Application, ExpectedRevision: request.Members[0].ExpectedRevision, Enabled: false})
				if got.Outcome != applications.OutcomeApplied {
					t.Fatal("revoke failed", got)
				}
				if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Outcome != applications.OutcomeUnknown || len(got.Enrollments) != 0 {
					t.Fatal("retired approval reused", got)
				}
				return
			case "overlap":
				other := request
				other.Members = append([]applications.AgentUpdateMember(nil), request.Members[1:]...)
				if got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, other); got.Outcome != applications.OutcomeConflict {
					t.Fatal("overlapping intent admitted", got)
				}
				return
			}
			if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Outcome != applications.OutcomeConflict {
				t.Fatal("changed input admitted", got)
			}
			for _, m := range request.Members {
				target := m.StagedProgram[:len(m.StagedProgram)-len(".staged")]
				b, err := os.ReadFile(target)
				if err != nil || string(b) != "old "+m.Application {
					t.Fatal("old image lost", string(b), err)
				}
			}
		})
	}
}
func TestAgentUpdateBatchRejectsUnprivilegedAndStalePrepare(t *testing.T) {
	run, request := agentUpdateFixture(t)
	if got := run.d.prepareAgentUpdate(run.ctx, run.f.answerer, request); got.Outcome != applications.OutcomeForbidden {
		t.Fatal(got)
	}
	if got := run.d.findAgentUpdate(run.ctx, run.f.answerer, request); got.Outcome != applications.OutcomeForbidden {
		t.Fatal(got)
	}
	request.Members[0].ExpectedRevision = "stale"
	if got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request); got.Outcome != applications.OutcomeConflict {
		t.Fatal(got)
	}
}

func TestAgentUpdateBatchPartialPublicationRetainsOriginalAuthority(t *testing.T) {
	run, request := agentUpdateFixture(t)
	pending := prepareAgentUpdateFixture(t, run, request)
	answer(t, run.f, pending.Question, "update")
	one := request
	one.Members = request.Members[:1]
	publishAgentUpdateFixture(t, one)
	got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent)
	if got.Reason != "update_ready" || len(got.Enrollments) != 0 {
		t.Fatal("partial claimed complete", got)
	}
	read := run.d.currentAgentEnrollment(run.ctx, request.Members[0].Application)
	if read.Enrollment != nil && read.Enrollment.State == applications.AgentEnrollmentStateEnrolled {
		t.Fatal("partial admitted new image", read)
	}
	two := request
	two.Members = request.Members[1:]
	publishAgentUpdateFixture(t, two)
	got = run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent)
	if got.Reason != "updated" || len(got.Enrollments) != 2 {
		t.Fatal("partial resume", got)
	}
}

func TestAgentUpdateBatchRacingCommitKeepsOneRevisionSet(t *testing.T) {
	run, request := agentUpdateFixture(t)
	pending := prepareAgentUpdateFixture(t, run, request)
	answer(t, run.f, pending.Question, "update")
	publishAgentUpdateFixture(t, request)
	var wg sync.WaitGroup
	results := make(chan applications.AgentUpdateResult, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent) }()
	}
	wg.Wait()
	close(results)
	applied := 0
	for got := range results {
		if got.Outcome == applications.OutcomeApplied && got.Reason == "updated" {
			applied++
		} else if got.Outcome != applications.OutcomeConflict {
			t.Fatal("uncertain race", got)
		}
	}
	if applied < 1 {
		t.Fatal("no commit won")
	}
	first := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent)
	second := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent)
	if first.Reason != "updated" || len(first.Enrollments) != 2 || !reflect.DeepEqual(first.Enrollments, second.Enrollments) {
		t.Fatal("racing commit reminted enrollment", first, second)
	}
}

func TestAgentUpdateBatchNewInstalledSourceAsksAgainOnce(t *testing.T) {
	for _, decline := range []bool{false, true} {
		t.Run(fmt.Sprint(decline), func(t *testing.T) {
			run, request := agentUpdateFixture(t)
			pending := prepareAgentUpdateFixture(t, run, request)
			if decline {
				answer(t, run.f, pending.Question, "not_now")
			}
			if err := os.WriteFile(request.SourceProgram, []byte("next installed source"), 0600); err != nil {
				t.Fatal(err)
			}
			for i := range request.Members {
				request.Members[i].StagedProgram += ".next"
				if err := os.WriteFile(request.Members[i].StagedProgram, []byte("next installed source"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			next := prepareAgentUpdateFixture(t, run, request)
			if next.Intent == pending.Intent || next.Question == pending.Question {
				t.Fatal("new update reused old choice", next)
			}
			if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Outcome != applications.OutcomeUnknown {
				t.Fatal("superseded batch usable", got)
			}
			if _, found, err := run.f.a.book.ApplicationRecord(pending.Question); err != nil || found {
				t.Fatal("old popup retained", found, err)
			}
		})
	}
}

type agentUpdateWireHandler struct {
	applications.Applications
	run   *selfRegisteredRun
	calls int
}

func (h *agentUpdateWireHandler) PrepareAgentUpdate(r applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
	h.calls++
	return h.run.d.prepareAgentUpdate(h.run.ctx, h.run.f.panel, r), nil
}
func (h *agentUpdateWireHandler) FindAgentUpdate(r applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
	return h.run.d.findAgentUpdate(h.run.ctx, h.run.f.panel, r), nil
}
func (h *agentUpdateWireHandler) CommitAgentUpdate(id string) (applications.AgentUpdateResult, error) {
	return h.run.d.commitAgentUpdate(h.run.ctx, h.run.f.panel, id), nil
}

type agentUpdateWireMutation struct {
	dispatcher *applications.ApplicationsDispatcher
	extra      string
}

func (m agentUpdateWireMutation) ExchangeFrame(frame []byte) ([]byte, error) {
	var value map[string]any
	if err := json.Unmarshal(frame, &value); err != nil {
		return nil, err
	}
	request := value["arguments"].(map[string]any)["request"].(map[string]any)
	member := request["members"].([]any)[0].(map[string]any)
	var extra map[string]any
	if err := json.Unmarshal([]byte("{"+strings.TrimSuffix(m.extra, ",")+"}"), &extra); err != nil {
		return nil, err
	}
	for k, v := range extra {
		member[k] = v
	}
	mutated, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return m.dispatcher.ExchangeFrame(mutated)
}

func TestAgentUpdateBatchUnnamedReplacementRefusedBeforeDispatch(t *testing.T) {
	for _, field := range []string{`"replacement_application":"unnamed-helper",`, `"expected_replacement_revision":"",`} {
		t.Run(field, func(t *testing.T) {
			run, request := agentUpdateFixture(t)
			h := &agentUpdateWireHandler{run: run}
			client := applications.NewApplicationsClient(agentUpdateWireMutation{dispatcher: &applications.ApplicationsDispatcher{Handler: h}, extra: field})
			_, err := client.PrepareAgentUpdate(request)
			if err == nil || h.calls != 0 {
				t.Fatal("unnamed replacement dispatched", h.calls, err)
			}
			for _, m := range request.Members {
				got := run.d.currentAgentEnrollment(run.ctx, m.Application)
				if got.Enrollment == nil || got.Enrollment.Revision != m.ExpectedRevision || got.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
					t.Fatal("refusal changed enrollment", got)
				}
			}
		})
	}
}

func TestAgentUpdateBatchLostFinalCommitFreshClientFinalizes(t *testing.T) {
	run, request := agentUpdateFixture(t)
	pending := prepareAgentUpdateFixture(t, run, request)
	answer(t, run.f, pending.Question, "update")
	publishAgentUpdateFixture(t, request)
	for _, m := range request.Members {
		got := run.d.currentAgentEnrollment(run.ctx, m.Application)
		if got.Enrollment != nil && got.Enrollment.State == applications.AgentEnrollmentStateEnrolled {
			t.Fatal("fixture did not lose old image enrollment")
		}
	}
	before, _, err := run.f.a.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	// A fresh generated client discovers from only a stable member name. No
	// stage pathname or current enrollment revision survives this caller.
	h := &agentUpdateWireHandler{run: run}
	client := applications.NewApplicationsClient(&applications.ApplicationsDispatcher{Handler: h})
	found, err := client.FindAgentUpdate(applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: request.Members[0].Application}}})
	if err != nil || found.Outcome != applications.OutcomeApplied || found.Intent != pending.Intent {
		t.Fatal("retained intent lost", found, err)
	}
	got, err := client.CommitAgentUpdate(found.Intent)
	if err != nil || got.Outcome != applications.OutcomeApplied || got.Reason != "updated" || len(got.Enrollments) != 2 {
		t.Fatal("fresh commit did not finalize", got, err)
	}
	after, _, err := run.f.a.book.ApplicationHistory()
	if err != nil || len(before) != len(after) || got.Question != pending.Question {
		t.Fatal("recovery asked again", err)
	}
}

func TestAgentUpdateBatchDeclinedSingletonThenNewMemberApplied(t *testing.T) {
	run, request := agentUpdateFixture(t)
	claude := request.Members[1]
	none := agent1478Set(t, run, claude.Application, claude.ExpectedRevision, false)
	one := request
	one.Members = request.Members[:1]
	prior := run.d.prepareAgentUpdate(run.ctx, run.f.panel, one)
	if prior.Outcome != applications.OutcomeApplied {
		t.Fatal(prior)
	}
	answer(t, run.f, prior.Question, "not_now")
	kept := agent1478Set(t, run, claude.Application, none.Revision, true)
	answer(t, run.f, agent1478Question(t, run, claude.Application).ID, "keep")
	agent1478Wait(t, run, claude.Application, "enrolled")
	request.Members[1].ExpectedRevision = kept.Revision
	next := prepareAgentUpdateFixture(t, run, request)
	if next.Intent == prior.Intent {
		t.Fatal("broader membership reused old answer")
	}
	if _, found, err := run.f.a.book.ApplicationRecord(prior.Question); err != nil || found {
		t.Fatal("declined orphan popup retained", found, err)
	}
}

func TestAgentUpdateBatchHistoricalOrphanReservationIsFreed(t *testing.T) {
	run, request := agentUpdateFixture(t)
	prior := prepareAgentUpdateFixture(t, run, request)
	// Historical writer counterexample: only the anchor's intent was dropped,
	// leaving reservations and its now-unusable popup. Current writers refuse it.
	run.d.mu.Lock()
	marks, _, err := run.d.readSelfMarksAtDescriptorLocked(run.ctx)
	if err != nil {
		run.d.mu.Unlock()
		t.Fatal(err)
	}
	for name, m := range marks {
		if m.Agent != nil && m.Agent.Update != nil {
			a := *m.Agent
			a.Update = nil
			m.Agent = &a
			marks[name] = m
		}
	}
	raw, err := json.Marshal(marks)
	run.d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(run.d.dir, selfRegisteredFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	next := prepareAgentUpdateFixture(t, run, request)
	if next.Intent == prior.Intent {
		t.Fatal("orphan reused as approval")
	}
	if _, found, err := run.f.a.book.ApplicationRecord(prior.Question); err != nil || found {
		t.Fatal("orphan question retained", found, err)
	}
}

func TestAgentUpdateBatchMemberMutationRetiresWholeIntent(t *testing.T) {
	for _, option := range []string{"", "not_now", "update"} {
		for _, action := range []string{"revoke", "remove"} {
			for member := range 2 {
				t.Run(fmt.Sprintf("%s/%s/%d", option, action, member), func(t *testing.T) {
					run, request := agentUpdateFixture(t)
					pending := prepareAgentUpdateFixture(t, run, request)
					if option != "" {
						answer(t, run.f, pending.Question, option)
					}
					m := request.Members[member]
					if action == "revoke" {
						got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: m.Application, ExpectedRevision: m.ExpectedRevision, Enabled: false})
						if got.Outcome != applications.OutcomeApplied {
							t.Fatal("revoke", got)
						}
					} else if got := run.d.remove(run.ctx, run.f.panel, m.Application); got.Outcome != wire.ApplicationOutcomeApplied {
						t.Fatal("remove", got)
					}
					r, found, err := run.f.a.book.ApplicationRecord(pending.Question)
					if err != nil || found != (option != "") || found && r.Pending() {
						t.Fatal("retired batch question state", r, found, err)
					}
					if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Outcome != applications.OutcomeUnknown || len(got.Enrollments) != 0 {
						t.Fatal("retired intent committed", got)
					}
					if got := run.d.findAgentUpdate(run.ctx, run.f.panel, request); got.Outcome != applications.OutcomeUnknown {
						t.Fatal("retired intent found", got)
					}
					other := request.Members[1-member]
					got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: other.Application, ExpectedRevision: other.ExpectedRevision, Enabled: false})
					if got.Outcome != applications.OutcomeApplied {
						t.Fatal("other member still reserved", got)
					}
				})
			}
		}
	}
}

func TestAgentUpdateBatchFailedRetirementCASPreservesQuestion(t *testing.T) {
	run, request := agentUpdateFixture(t)
	pending := prepareAgentUpdateFixture(t, run, request)
	run.d.mu.Lock()
	err := run.d.withRegistrationStorageLocked(run.ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := run.d.readSelfMarksAtDescriptorLocked(run.ctx)
		if err != nil {
			return err
		}
		next := maps.Clone(marks)
		retireAgentUpdateMarks(next, map[string]bool{pending.Intent: true})
		run.d.selfBase = base
		run.d.selfBase.Data = append([]byte(" "), base.Data...)
		return run.d.persistAgentUpdate(run.ctx, next, legacy, map[string]bool{pending.Intent: true})
	})
	run.d.unlock()
	if err == nil {
		t.Fatal("stale CAS retired the batch")
	}
	if r, found, err := run.f.a.book.ApplicationRecord(pending.Question); err != nil || !found || !r.Pending() {
		t.Fatal("failed CAS changed the question", found, err)
	}
	if got := run.d.findAgentUpdate(run.ctx, run.f.panel, request); got.Intent != pending.Intent {
		t.Fatal("failed CAS changed the intent", got)
	}
}

func TestAgentUpdateBatchPreviouslyUnhookedCloseDrainsCommittedRetirement(t *testing.T) {
	run, request := agentUpdateFixture(t)
	pending := prepareAgentUpdateFixture(t, run, request)
	// Leave an actual successful-CAS retirement queued, as a writer without
	// the former function-level drain hook could do. Closing the native inbox
	// is an existing mutator that never had that hook.
	run.d.mu.Lock()
	err := run.d.withRegistrationStorageLocked(run.ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := run.d.readSelfMarksAtDescriptorLocked(run.ctx)
		if err != nil {
			return err
		}
		next := maps.Clone(marks)
		retireAgentUpdateMarks(next, map[string]bool{pending.Intent: true})
		run.d.selfBase = base
		return run.d.persistAgentUpdate(run.ctx, next, legacy, map[string]bool{pending.Intent: true})
	})
	run.d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if r, found, err := run.f.a.book.ApplicationRecord(pending.Question); err != nil || !found || !r.Pending() {
		t.Fatal("fixture did not retain its pending cleanup", found, err)
	}
	run.d.closeNativeInboxes()
	if _, found, err := run.f.a.book.ApplicationRecord(pending.Question); err != nil || found {
		t.Fatal("common unlock owner left the pending question", found, err)
	}
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Outcome != applications.OutcomeUnknown {
		t.Fatal("retired intent became usable", got)
	}
}

func TestAgentUpdateBatchStagedBytesMustEqualInstalled(t *testing.T) {
	run, request := agentUpdateFixture(t)
	if err := os.WriteFile(request.Members[0].StagedProgram, []byte("other native executable bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if got.Outcome != applications.OutcomeConflict || got.Reason != "staged_image" {
		t.Fatal("different executable admitted", got)
	}
}

func TestAgentUpdateBatchQuestionNamesOnlyRegisteredMembers(t *testing.T) {
	for i, absent := range []string{"Claude Code", "Codex"} {
		t.Run(absent, func(t *testing.T) {
			run, request := agentUpdateFixture(t)
			request.Members = request.Members[i : i+1]
			got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
			if got.Outcome != applications.OutcomeApplied {
				t.Fatal(got)
			}
			record, found, err := run.f.a.book.ApplicationRecord(got.Question)
			if err != nil || !found {
				t.Fatal(err)
			}
			if strings.Contains(record.Text, absent) || strings.Contains(record.Text, "batch-") || !strings.Contains(record.Text, "integration connections?") {
				t.Fatal("question differs from actual membership", record.Text)
			}
		})
	}
}

func TestAgentUpdateBatchReservedSignedSourceWriterRefusesBeforeIndex(t *testing.T) {
	for _, tc := range []struct {
		block  bool
		option string
	}{{false, ""}, {true, ""}, {false, "not_now"}, {true, "not_now"}} {
		t.Run(fmt.Sprintf("%v/%s", tc.block, tc.option), func(t *testing.T) {
			run := agent1478Fixture(t)
			root := t.TempDir()
			old := filepath.Join(root, "old.exe")
			source := filepath.Join(root, "installed.exe")
			stage := filepath.Join(root, "staged.exe")
			for name, body := range map[string]string{old: "old helper", source: "new helper", stage: "new helper"} {
				if err := os.WriteFile(name, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
				t.Fatal(err)
			}
			revision := registration1459Publish(t, run.d, "signed-agent", old, "Registered assistant", "operator", "")
			pending := agent1478Set(t, run, "signed-agent", "", true)
			answer(t, run.f, agent1478Question(t, run, "signed-agent").ID, "keep")
			agent1478Wait(t, run, "signed-agent", "enrolled")
			run.d.agentExcluded = func(_ context.Context, program, key string) (bool, error) { return identity.SameFile(program, source) }
			request := applications.AgentUpdateRequest{SourceProgram: source, Members: []applications.AgentUpdateMember{{Application: "signed-agent", ExpectedRevision: pending.Revision, StagedProgram: stage}}}
			batch := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
			if batch.Outcome != applications.OutcomeApplied {
				t.Fatal(batch)
			}
			v, f := registration1459File(t, "signed-agent", old, "Changed title")
			got := run.d.publishApplicationV3(run.ctx, v, f, "operator", revision)
			if got.Outcome == wire.ApplicationOutcomeApplied || run.d.v3.entries["signed-agent"].Revision != revision || run.d.v3.entries["signed-agent"].State != "enabled" {
				t.Fatal("signed source writer changed reserved index", got, run.d.v3.entries["signed-agent"])
			}
			found := run.d.findAgentUpdate(run.ctx, run.f.panel, request)
			if found.Intent != batch.Intent {
				t.Fatal("signed source writer orphaned intent", found)
			}
			if tc.option != "" {
				answer(t, run.f, batch.Question, tc.option)
			}
			blocked := false
			if tc.block {
				run.d.messageBlock = func(context.Context, string) error { blocked = true; return nil }
			}
			if removed := run.d.changeApplicationV3(run.ctx, run.f.panel, "signed-agent", revision, "remove"); removed.Outcome != wire.ApplicationOutcomeApplied {
				t.Fatal("declined signed batch blocked removal", removed)
			}
			if blocked != tc.block {
				t.Fatal("message removal owner skipped", blocked)
			}
			if _, found, err := run.f.a.book.ApplicationRecord(batch.Question); err != nil || found != (tc.option != "") {
				t.Fatal("signed batch question state", found, err)
			}
			if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, batch.Intent); got.Outcome != applications.OutcomeUnknown || len(got.Enrollments) != 0 {
				t.Fatal("removed signed intent committed", got)
			}
		})
	}
}

func TestAgentUpdateBatchMismatchedQuestionNeverRetiresOtherProof(t *testing.T) {
	run, request := agentUpdateFixture(t)
	one := request
	one.Members = request.Members[:1]
	prior := run.d.prepareAgentUpdate(run.ctx, run.f.panel, one)
	if prior.Outcome != applications.OutcomeApplied {
		t.Fatal(prior)
	}
	run.d.mu.Lock()
	marks, _, err := run.d.readSelfMarksAtDescriptorLocked(run.ctx)
	if err != nil {
		run.d.mu.Unlock()
		t.Fatal(err)
	}
	mark := marks[one.Members[0].Application]
	keep := mark.Question
	a := *mark.Agent
	u := *a.Update
	u.Question = keep
	a.Update = &u
	mark.Agent = &a
	marks[one.Members[0].Application] = mark
	raw, err := json.Marshal(marks)
	run.d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(run.d.dir, selfRegisteredFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if got.Outcome != applications.OutcomeConflict {
		t.Fatal("mismatched question treated as dead intent", got)
	}
	if _, found, err := run.f.a.book.ApplicationRecord(keep); err != nil || !found {
		t.Fatal("another enrollment proof was retired", found, err)
	}
}

// Owner R14, 2026-11-09: the ninth installed connector stopped asking because
// the anchor's retained history was full; Update clicks showed no popup.
func TestAgentUpdateBatchKeepsAskingAfterHistoryFills(t *testing.T) {
	run, request := agentUpdateFixture(t)
	programs := map[string]string{}
	for _, m := range request.Members {
		programs[m.Application] = m.StagedProgram[:len(m.StagedProgram)-len(".staged")]
	}
	for cycle := 1; cycle <= agentUpdateHistoryLimit+3; cycle++ {
		body := []byte(fmt.Sprintf("installed gateway %d", cycle))
		if err := os.WriteFile(request.SourceProgram, body, 0600); err != nil {
			t.Fatal(err)
		}
		for i, m := range request.Members {
			stage := fmt.Sprintf("%s.%d.tmp", programs[m.Application], cycle)
			if err := os.WriteFile(stage, body, 0600); err != nil {
				t.Fatal(err)
			}
			current := run.d.currentAgentEnrollment(run.ctx, m.Application)
			if current.Enrollment == nil {
				t.Fatal("enrollment lost", cycle, current)
			}
			request.Members[i].StagedProgram, request.Members[i].ExpectedRevision = stage, current.Enrollment.Revision
		}
		pending := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
		if pending.Outcome != applications.OutcomeApplied || pending.Reason != "permission_pending" || pending.Question == "" {
			t.Fatalf("update %d asked no question: %+v", cycle, pending)
		}
		answer(t, run.f, pending.Question, "update")
		if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Reason != "update_ready" {
			t.Fatal("publication authority", cycle, got)
		}
		for _, m := range request.Members {
			program := programs[m.Application]
			if err := os.Rename(program, m.StagedProgram+".previous"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(m.StagedProgram, program); err != nil {
				t.Fatal(err)
			}
		}
		if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Outcome != applications.OutcomeApplied || got.Reason != "updated" {
			t.Fatal("commit", cycle, got)
		}
	}
}
