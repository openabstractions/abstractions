package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
)

func agent1478Fixture(t *testing.T) *selfRegisteredRun {
	t.Helper()
	run := newSelfRegisteredRun(t)
	agent1478Attach(t, run)
	return run
}

func TestApplicationAgentEnrollmentProductionScopeSurvivesBookReloadAndRetry(t *testing.T) {
	run := agent1478Fixture(t)
	run.f.a.scope = "asks-owner-program@1:" + strings.Repeat("a", 64)
	agent1478Register(t, run, "scope-agent", run.f.answerer.Program)
	view := agent1478Set(t, run, "scope-agent", "", true)
	question := agent1478Question(t, run, "scope-agent") // Actual on-disk Book reload.
	if !question.Pending() || question.Key != asks.AgentEnrollmentKey {
		t.Fatal("pending occurrence changed")
	}
	run.d.mu.Lock()
	mark := run.d.unconfirmed["scope-agent"]
	run.d.mu.Unlock()
	scope := agentEnrollmentScope(run.f.a.scope, mark.RegistrationRevision)
	if !asks.ValidRequestKey(scope) || len(scope) > 128 {
		t.Fatal("invalid generated scope", len(scope))
	}
	if scope == agentEnrollmentScope(run.f.a.scope, mark.RegistrationRevision+"1") || scope == agentEnrollmentScope(run.f.a.scope+"1", mark.RegistrationRevision) {
		t.Fatal("generation/runtime identity collapsed")
	}
	replayed := agent1478Set(t, run, "scope-agent", view.Revision, true)
	if replayed.State != applications.AgentEnrollmentStatePending || agent1478Question(t, run, "scope-agent").ID != question.ID {
		t.Fatal("retry replaced pending question")
	}
}

func expireAgentQuestionFixture(t *testing.T, run *selfRegisteredRun) {
	t.Helper()
	if err := run.f.a.book.SetPendingLifetime(time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	// A normal unrelated fixture admission persists ordinary expiry. No
	// question is answered, and no live account data is used. Its slots are
	// short: a slot longer than asks' bound refuses the ask without writing,
	// and a long temporary folder made the fixture's program path that long.
	asked, outcome, err := run.f.a.book.AskApplicationFrom("fixture-expiry", "runtime", "expiry-cleanup", asks.SelfRegisteredKey, map[string]string{"program": "expiry-fixture.exe", "title": "Expiry fixture"}, listen.Seen{}, asks.Publisher{})
	if err != nil || asked.ID == "" {
		t.Fatal("expiry fixture ask was not written", outcome, err)
	}
	if err := run.f.a.book.SetPendingLifetime(asks.DefaultPendingLifetime); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationAgentEnrollmentExpiryOnlyExplicitSetRenews(t *testing.T) {
	run := agent1478Fixture(t)
	run.f.a.scope = "asks-owner-program@1:" + strings.Repeat("a", 64)
	agent1478Register(t, run, "expiry-agent", run.f.answerer.Program)
	old := agent1478Set(t, run, "expiry-agent", "", true)
	question := agent1478Question(t, run, "expiry-agent")
	expireAgentQuestionFixture(t, run)
	if _, found, err := run.f.a.book.ApplicationRecord(question.ID); err != nil || found {
		t.Fatal("fixture did not expire", found, err)
	}
	// Reads, startup/resume and repeated background follow never renew.
	if r := run.d.readAgentEnrollment(run.ctx, run.f.panel, "expiry-agent"); r.Enrollment == nil || r.Enrollment.Revision != old.Revision {
		t.Fatal(r)
	}
	run.f.a.keepAgentEnrollments(run.ctx, run.d)
	if err := run.d.agentQuestions.ask("expiry-agent"); err != nil {
		t.Fatal(err)
	}
	run.d.mu.Lock()
	unchanged := run.d.unconfirmed["expiry-agent"]
	run.d.mu.Unlock()
	if unchanged.Question != question.ID || unchanged.RegistrationRevision != old.Revision {
		t.Fatal("startup renewed pending intent")
	}
	current := agent1478Set(t, run, "expiry-agent", old.Revision, true)
	fresh := agent1478Question(t, run, "expiry-agent")
	if current.Revision == old.Revision || fresh.ID == question.ID || !fresh.Pending() || fresh.AnsweredBy != "" {
		t.Fatal("explicit renewal reused old consent")
	}
	duplicate := agent1478Set(t, run, "expiry-agent", old.Revision, true)
	if duplicate.Revision != current.Revision || agent1478Question(t, run, "expiry-agent").ID != fresh.ID {
		t.Fatal("duplicate explicit retry created another question")
	}
	if stale := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: "expiry-agent", ExpectedRevision: "", Enabled: true}); stale.Outcome != applications.OutcomeConflict {
		t.Fatal("stale generation renewed", stale)
	}
}

func TestApplicationAgentEnrollmentExpiryRetainsNativeBindingFence(t *testing.T) {
	run := agent1478Fixture(t)
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	revision := registration1459Publish(t, run.d, "expiry-image", run.f.answerer.Program, "Native image", "operator", "")
	old := agent1478Set(t, run, "expiry-image", "", true)
	image, err := run.d.currentAgentImage(run.ctx, "expiry-image")
	if err != nil {
		t.Fatal(err)
	}
	expireAgentQuestionFixture(t, run)
	registration1459Publish(t, run.d, "expiry-image", run.f.answerer.Program, "Changed native binding", "operator", revision)
	got := run.d.setAgentEnrollmentAtImage(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: "expiry-image", ExpectedRevision: old.Revision, Enabled: true}, image)
	if got.Outcome != applications.OutcomeConflict {
		t.Fatal("stale native binding renewed", got)
	}
}
func agent1478Attach(t *testing.T, run *selfRegisteredRun) {
	t.Helper()
	run.d.operator = run.f.a.rights.isOperator
	run.d.agentExcluded = func(context.Context, string, string) (bool, error) { return false, nil }
	run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		h, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			return applicationAgentImage{}, err
		}
		defer h.Close()
		return applicationAgentImage{Program: program, Key: h.FileID().Key(), Binding: binding, Publisher: asks.Publisher{}}, nil
	}
}
func agent1478Register(t *testing.T, run *selfRegisteredRun, name, program string) {
	t.Helper()
	if got := run.d.register(run.ctx, run.f.panel, wire.ApplicationDescriptor{Name: name, Title: "Fixture agent", Program: program}); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
}
func agent1478Set(t *testing.T, run *selfRegisteredRun, name, revision string, enabled bool) applications.AgentEnrollment {
	t.Helper()
	got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: name, ExpectedRevision: revision, Enabled: enabled})
	if got.Outcome != applications.OutcomeApplied || got.Enrollment == nil {
		t.Fatalf("set: %+v", got)
	}
	return *got.Enrollment
}
func agent1478Question(t *testing.T, run *selfRegisteredRun, name string) asks.Record {
	t.Helper()
	run.d.mu.Lock()
	m := run.d.unconfirmed[name]
	run.d.mu.Unlock()
	r, found, err := run.f.a.book.ApplicationRecord(m.Question)
	if err != nil || !found {
		t.Fatal("missing agent question", err)
	}
	if r.Key != asks.AgentEnrollmentKey {
		t.Fatal("wrong question", r)
	}
	return r
}
func agent1478Wait(t *testing.T, run *selfRegisteredRun, name, state string) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for {
		run.d.mu.Lock()
		m := run.d.unconfirmed[name]
		run.d.mu.Unlock()
		if m.Kind == "agent" && m.Agent.State == state {
			return
		}
		if time.Now().After(until) {
			t.Fatal("agent state timeout", state, m)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestApplicationAgentEnrollmentIsExplicitConditionalAndPersonOnly(t *testing.T) {
	run := agent1478Fixture(t)
	agent1478Register(t, run, "agent", run.f.answerer.Program)
	read := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent")
	if read.Enrollment == nil || read.Enrollment.State != applications.AgentEnrollmentStateNone || read.Enrollment.Revision != "" {
		t.Fatal(read)
	}
	if got := run.d.setAgentEnrollment(run.ctx, run.f.answerer, applications.AgentEnrollmentRequest{Application: "agent", Enabled: true}); got.Outcome != applications.OutcomeForbidden {
		t.Fatal("self enrollment", got)
	}
	pending := agent1478Set(t, run, "agent", "", true)
	q := agent1478Question(t, run, "agent")
	if pending.State != applications.AgentEnrollmentStatePending || q.AnsweredBy != "" {
		t.Fatal("admission promoted class", pending, q)
	}
	again := agent1478Set(t, run, "agent", "", true)
	if again.Revision != pending.Revision || agent1478Question(t, run, "agent").ID != q.ID {
		t.Fatal("equal pending intent duplicated")
	}
	if entries := run.d.selfEntries(); len(entries) != 0 {
		t.Fatal("agent enrollment leaked into generic Keep", entries)
	}
	if got := run.d.askConfirmation(run.ctx, run.f.panel, "agent"); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("generic AskConfirmation changed", got)
	}
	if got := run.d.readAgentEnrollment(run.ctx, run.f.answerer, "agent"); got.Outcome != applications.OutcomePage || got.Enrollment == nil || !identity.SameSubjectProgram(got.Enrollment.Program, run.f.answerer.Program) || got.Enrollment.State != applications.AgentEnrollmentStatePending {
		t.Fatal("own enrollment unavailable", got)
	}
	if effect, err := run.f.a.authorizeAnswerer(run.ctx, answerer{subject: run.f.answerer}, q, run.f.a.scope, "keep"); err == nil || effect != nil {
		t.Fatal("answerer answered agent enrollment")
	}
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "agent", "enrolled")
	q = agent1478Question(t, run, "agent")
	if got := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent"); got.Enrollment == nil || got.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
		t.Fatal(got)
	}
	if got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: "agent", ExpectedRevision: "stale", Enabled: false}); got.Outcome != applications.OutcomeConflict {
		t.Fatal("stale revoke", got)
	}
	revoked := agent1478Set(t, run, "agent", pending.Revision, false)
	if revoked.State != applications.AgentEnrollmentStateNone || revoked.Revision == pending.Revision {
		t.Fatal("revocation not fenced", revoked)
	}
	if err := run.d.applyAgentAnswer(run.ctx, "agent", pending.Revision, q); err == nil {
		t.Fatal("stale answer restored class")
	}
}

func TestApplicationAgentOpenCodeKeepReceiptUsesExistingPersonAnswerOwner(t *testing.T) {
	// The existing isolated owner fixture admits an owned file identity instead
	// of consulting an installed MSI. It never injects an enrollment mark.
	run := agent1478Fixture(t)
	const name = "oa-opencode-integration"
	agent1478Register(t, run, name, run.f.answerer.Program)
	pending := agent1478Set(t, run, name, "", true)
	question := agent1478Question(t, run, name)
	if question.Key != asks.AgentEnrollmentKey || question.AnsweredBy != "" {
		t.Fatal(question)
	}
	answer(t, run.f, question.ID, "refuse")
	agent1478Wait(t, run, name, "none")
	declined := run.d.readAgentEnrollment(run.ctx, run.f.panel, name)
	if declined.Enrollment == nil || declined.Enrollment.State != applications.AgentEnrollmentStateNone {
		t.Fatal(declined)
	}
	pending = agent1478Set(t, run, name, declined.Enrollment.Revision, true)
	question = agent1478Question(t, run, name)
	if effect, err := run.f.a.authorizeAnswerer(run.ctx, answerer{subject: run.f.answerer}, question, run.f.a.scope, "keep"); err == nil || effect != nil {
		t.Fatal("assistant approved its native Keep", effect, err)
	}
	if stale := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: name, ExpectedRevision: pending.Revision + "-stale", Enabled: true}); stale.Outcome != applications.OutcomeConflict {
		t.Fatal(stale)
	}
	answer(t, run.f, question.ID, "keep")
	agent1478Wait(t, run, name, "enrolled")
	read := run.d.readAgentEnrollment(run.ctx, run.f.panel, name)
	if read.Enrollment == nil || read.Enrollment.State != applications.AgentEnrollmentStateEnrolled || read.Enrollment.Revision != pending.Revision {
		t.Fatal(read)
	}
	stored := agent1478Question(t, run, name)
	if stored.AnsweredBy != asks.AnsweredByOperator || stored.Option != "keep" {
		t.Fatal(stored)
	}
}

func TestApplicationAgentEnrollmentRestartReplacementAndOldWriterLoss(t *testing.T) {
	for _, variant := range []string{"restart", "old-writer-drop", "remove-recreate", "file-replacement", "unknown-kind"} {
		t.Run(variant, func(t *testing.T) {
			run := agent1478Fixture(t)
			agent1478Register(t, run, "agent", run.f.answerer.Program)
			pending := agent1478Set(t, run, "agent", "", true)
			q := agent1478Question(t, run, "agent")
			answer(t, run.f, q.ID, "keep")
			agent1478Wait(t, run, "agent", "enrolled")
			q = agent1478Question(t, run, "agent")
			switch variant {
			case "restart":
				run.start(t)
				agent1478Attach(t, run)
			case "old-writer-drop":
				// The old selfMark decoder knows these existing fields only. Its
				// actual JSON round trip drops the private kind/class evidence.
				path := filepath.Join(run.d.dir, selfRegisteredFile)
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				type oldMark struct {
					Question     string `json:"question,omitempty"`
					Program      string `json:"program,omitempty"`
					Publisher    string `json:"publisher,omitempty"`
					Trusted      bool   `json:"trusted,omitempty"`
					Description  string `json:"description,omitempty"`
					OriginalName string `json:"original_name,omitempty"`
				}
				old := map[string]oldMark{}
				if err = json.Unmarshal(b, &old); err != nil {
					t.Fatal(err)
				}
				b, err = json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				run.start(t)
				agent1478Attach(t, run)
			case "remove-recreate":
				if got := run.d.remove(run.ctx, run.f.panel, "agent"); got.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal(got)
				}
				agent1478Register(t, run, "agent", run.f.answerer.Program)
			case "file-replacement":
				if err := os.Rename(run.f.answerer.Program, run.f.answerer.Program+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(run.f.answerer.Program, []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown-kind":
				path := filepath.Join(run.d.dir, selfRegisteredFile)
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var marks map[string]selfMark
				if err = json.Unmarshal(b, &marks); err != nil {
					t.Fatal(err)
				}
				m := marks["agent"]
				m.Kind = "future-authority"
				marks["agent"] = m
				b, _ = json.Marshal(marks)
				if err = os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent")
			if variant == "restart" {
				if got.Enrollment == nil || got.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
					t.Fatal(got)
				}
				return
			}
			if got.Enrollment != nil && got.Enrollment.State == applications.AgentEnrollmentStateEnrolled {
				t.Fatal("class survived invalidation", variant, got)
			}
			if variant == "file-replacement" {
				// Read-only observation of K2 must leave the original K1 approval
				// intact. Explicit revocation changes the enrollment generation.
				if err := os.Remove(run.f.answerer.Program); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(run.f.answerer.Program+".old", run.f.answerer.Program); err != nil {
					t.Fatal(err)
				}
				restored := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent")
				if restored.Enrollment == nil || restored.Enrollment.State != applications.AgentEnrollmentStateEnrolled || restored.Enrollment.Revision != pending.Revision {
					t.Fatal("same K1 lost unchanged approval", restored)
				}
				agent1478Set(t, run, "agent", pending.Revision, false)
				if err := os.Rename(run.f.answerer.Program, run.f.answerer.Program+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(run.f.answerer.Program, []byte("replacement after revoke"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(run.f.answerer.Program); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(run.f.answerer.Program+".old", run.f.answerer.Program); err != nil {
					t.Fatal(err)
				}
				if got := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent"); got.Enrollment == nil || got.Enrollment.State != applications.AgentEnrollmentStateNone {
					t.Fatal("restoration bypassed explicit revoke", got)
				}
			}
			if variant != "unknown-kind" {
				if err := run.d.applyAgentAnswer(run.ctx, "agent", pending.Revision, q); err == nil {
					t.Fatal("old question restored class")
				}
			}
		})
	}
}

func TestApplicationAgentEnrollmentSharedGatewayAndUncertaintyRefuse(t *testing.T) {
	run := agent1478Fixture(t)
	agent1478Register(t, run, "agent", run.f.answerer.Program)
	run.d.agentExcluded = func(context.Context, string, string) (bool, error) { return true, nil }
	if got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: "agent", Enabled: true}); got.Outcome == applications.OutcomeApplied {
		t.Fatal("shared gateway enrolled", got)
	}
	run.d.agentExcluded = func(context.Context, string, string) (bool, error) {
		return false, errors.New("installation ambiguous")
	}
	if got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: "agent", Enabled: true}); got.Outcome == applications.OutcomeApplied {
		t.Fatal("uncertainty enrolled", got)
	}
	if yes, err := run.d.applicationV3AgentSubject(run.ctx, &identity.Peer{}); yes || err == nil {
		t.Fatal("unproved peer accepted", yes, err)
	}
}

func TestApplicationAgentEnrollmentPendingRestartRefusalAndReplacement(t *testing.T) {
	for _, variant := range []string{"restart-keep", "refuse", "replacement"} {
		t.Run(variant, func(t *testing.T) {
			run := agent1478Fixture(t)
			agent1478Register(t, run, "agent", run.f.answerer.Program)
			pending := agent1478Set(t, run, "agent", "", true)
			q := agent1478Question(t, run, "agent")
			if variant == "replacement" {
				if got := run.d.remove(run.ctx, run.f.panel, "agent"); got.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal(got)
				}
				agent1478Register(t, run, "agent", run.f.answerer.Program)
				answer(t, run.f, q.ID, "keep")
				q, _, _ = run.f.a.book.ApplicationRecord(q.ID)
				if err := run.d.applyAgentAnswer(run.ctx, "agent", pending.Revision, q); err == nil {
					t.Fatal("removed generation accepted answer")
				}
				if got := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent"); got.Enrollment == nil || got.Enrollment.State != applications.AgentEnrollmentStateNone {
					t.Fatal(got)
				}
				return
			}
			if variant == "refuse" {
				answer(t, run.f, q.ID, "refuse")
				agent1478Wait(t, run, "agent", "none")
				return
			}
			run.start(t)
			agent1478Attach(t, run)
			if got := agent1478Question(t, run, "agent"); got.ID != q.ID {
				t.Fatal("restart replaced pending question", got)
			}
			answer(t, run.f, q.ID, "keep")
			agent1478Wait(t, run, "agent", "enrolled")
		})
	}
}

func TestApplicationAgentEnrollmentV3DisableDoesNotRestoreOldApproval(t *testing.T) {
	run := agent1478Fixture(t)
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	revision := registration1459Publish(t, run.d, "agent", run.f.answerer.Program, "Agent", "operator", "")
	pending := agent1478Set(t, run, "agent", "", true)
	q := agent1478Question(t, run, "agent")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "agent", "enrolled")
	if got := run.d.changeApplicationV3(run.ctx, run.f.panel, "agent", revision, "disabled"); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	if got := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent"); got.Enrollment == nil || got.Enrollment.State != applications.AgentEnrollmentStateNone {
		t.Fatal("disabled registration kept class", got)
	}
	if got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: "agent", ExpectedRevision: pending.Revision, Enabled: true}); got.Outcome != applications.OutcomeConflict {
		t.Fatal("disabled registration admitted enrollment", got)
	}
	if got := run.d.changeApplicationV3(run.ctx, run.f.panel, "agent", run.d.v3.entries["agent"].Revision, "enabled"); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	if got := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent"); got.Enrollment == nil || got.Enrollment.State != applications.AgentEnrollmentStateNone {
		t.Fatal("reenabling restored old class", got)
	}
}

func TestApplicationAgentEnrollmentReadsOneRegistrationSnapshot(t *testing.T) {
	run := agent1478Fixture(t)
	var decodes atomic.Int32
	decode := func(raw []byte, program string, held *heldApplicationManifest) (wire.ApplicationDescriptor, string, error) {
		decodes.Add(1)
		return registration1459Decoder(raw, program, held)
	}
	if err := run.d.attachApplicationV3(run.ctx, decode); err != nil {
		t.Fatal(err)
	}
	registration1459Publish(t, run.d, "agent", run.f.answerer.Program, "Agent", "operator", "")
	agent1478Set(t, run, "agent", "", true)
	answer(t, run.f, agent1478Question(t, run, "agent").ID, "keep")
	agent1478Wait(t, run, "agent", "enrolled")
	_, binding, err := run.d.agentSnapshot(run.ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	before := decodes.Load()
	if _, _, err := run.d.agentSnapshot(run.ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	wantDecodes := decodes.Load() - before
	before = decodes.Load()
	var image applicationAgentImage
	result := run.d.currentAgentEnrollmentImage(run.ctx, "agent", &image)
	if result.Enrollment == nil || result.Enrollment.State != applications.AgentEnrollmentStateEnrolled || image.Binding != binding {
		t.Fatal(result, image.Binding, binding)
	}
	if got := decodes.Load() - before; wantDecodes == 0 || got != wantDecodes {
		t.Fatalf("enrollment decoded %d manifests; one registration snapshot decoded %d", got, wantDecodes)
	}
}

func TestApplicationAgentEnrollmentRecoversAnsweredAdmissionBeforeQuestionPublication(t *testing.T) {
	for _, variant := range []string{"admission-retry", "retry", "restart"} {
		t.Run(variant, func(t *testing.T) {
			run := agent1478Fixture(t)
			agent1478Register(t, run, "agent", run.f.answerer.Program)
			actualBook := run.f.a.book
			unavailableBook, err := asks.LoadBook(filepath.Join(t.TempDir(), "unavailable-application-owner.json"))
			if err != nil {
				t.Fatal(err)
			}
			run.f.a.book = unavailableBook
			failed := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: "agent", Enabled: true})
			if failed.Outcome != applications.OutcomeUnavailable || failed.Reason != "question" {
				t.Fatal("admission failure not reported", failed)
			}
			run.d.mu.Lock()
			m := run.d.unconfirmed["agent"]
			run.d.mu.Unlock()
			if m.Kind != "agent" || m.Agent.State != "pending" || m.Question != "" {
				t.Fatal("pending intent not durable before admission", m)
			}
			run.f.a.book = actualBook
			if variant == "admission-retry" {
				pending := agent1478Set(t, run, "agent", "", true)
				if pending.Revision != m.RegistrationRevision || pending.State != applications.AgentEnrollmentStatePending || agent1478Question(t, run, "agent").ID == "" {
					t.Fatal("failed admission retry replaced durable intent", pending, m)
				}
				return
			}
			v, _, err := run.d.agentSnapshot(run.ctx, "agent")
			if err != nil {
				t.Fatal(err)
			}
			// Reproduce the actual two-owner crash interval: Asks commits its
			// deterministic occurrence and answers it before metadata has its ID.
			q, out, err := actualBook.AskApplicationFrom(agentEnrollmentScope(run.f.a.scope, m.RegistrationRevision), run.f.a.display, "agent-enrollment:"+m.RegistrationRevision, asks.AgentEnrollmentKey, map[string]string{"program": selfRegisteredSlot(m.Program), "title": selfRegisteredTitle(v)}, listen.Seen{Why: "explicit operator request for person-kept native agent enrollment"}, m.publisher())
			if err != nil || out != "pending" {
				t.Fatal(q, out, err)
			}
			answer(t, run.f, q.ID, "keep")
			if variant == "restart" {
				run.start(t)
				agent1478Attach(t, run)
			}
			got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: "agent", Enabled: true})
			if got.Outcome != applications.OutcomeApplied {
				t.Fatal("same pending intent did not recover", got)
			}
			agent1478Wait(t, run, "agent", "enrolled")
			if current := agent1478Question(t, run, "agent"); current.ID != q.ID {
				t.Fatal("recovery replaced actual answered occurrence", current, q)
			}
		})
	}
}

func TestApplicationAgentEnrollmentHasOneOwnerPerRightsSubject(t *testing.T) {
	run := agent1478Fixture(t)
	agent1478Register(t, run, "agent-a", run.f.answerer.Program)
	agent1478Register(t, run, "agent-b", run.f.answerer.Program)
	a := agent1478Set(t, run, "agent-a", "", true)
	request := applications.AgentEnrollmentRequest{Application: "agent-b", Enabled: true}
	if got := run.d.setAgentEnrollment(run.ctx, run.f.panel, request); got.Outcome != applications.OutcomeConflict || got.Reason != "enrollment:agent-a" {
		t.Fatal("second pending owner", got)
	}
	q := agent1478Question(t, run, "agent-a")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "agent-a", "enrolled")
	if got := run.d.setAgentEnrollment(run.ctx, run.f.panel, request); got.Outcome != applications.OutcomeConflict || got.Reason != "enrollment:agent-a" {
		t.Fatal("second kept owner", got)
	}
	agent1478Set(t, run, "agent-a", a.Revision, false)
	if got := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent-b"); got.Enrollment == nil || got.Enrollment.State != applications.AgentEnrollmentStateNone {
		t.Fatal("revoked owner transferred class", got)
	}
	// A new explicit request may begin only after the original owner revoked.
	b := agent1478Set(t, run, "agent-b", "", true)
	if b.State != applications.AgentEnrollmentStatePending || b.Revision == a.Revision {
		t.Fatal("new owner reused approval", b)
	}
}
