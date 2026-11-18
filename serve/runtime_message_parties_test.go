package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestMessagingPartiesBookCallbacks(t *testing.T) {
	for _, kind := range []string{"first-use", "choice"} {
		t.Run(kind, func(t *testing.T) {
			run := messagePartiesFixture(t)
			f := run.f
			messagePartyGrant(t, run, false, true)
			if kind == "first-use" {
				q := f.ask(t, f.comfy, inference.ActionComplete, "host:lemonade")
				prepared, err := f.a.rights.policy.PrepareMetaHolderContext(run.ctx)
				if err != nil {
					t.Fatal(err)
				}
				old := run.d.agentImage
				run.d.agentImage = func(context.Context, string, string) (applicationAgentImage, error) {
					t.Error("native enrollment lookup inside Book")
					return applicationAgentImage{}, errors.New("locked inventory")
				}
				defer func() { run.d.agentImage = old }()
				_, _, err = f.a.book.AnswerApplicationAsResponder(q.ID, "allow", f.answerer.Program, func(record asks.Record, scope string) error {
					_, err := f.a.authorizeAnswerer(prepared, f.me(), record, scope, "allow")
					return err
				})
				if !errors.Is(err, asks.ErrAnswererForbidden) {
					t.Fatal(err)
				}
				return
			}
			q := askswire.ChoiceRequest{RequestRef: askswire.RequestRef{Key: "messaging-party", ReplayUntilMs: f.clock.Add(time.Hour).UnixMilli()}, Title: "Choose output", Options: []askswire.ChoiceOption{{ID: "keep", Label: "Keep"}}, PendingUntilMs: f.clock.Add(time.Minute).UnixMilli()}
			pending, err := f.a.book.AskChoiceAs("choice-owner", f.comfy.Program, q, []string{"keep"}, listen.Seen{}, asks.Publisher{}, nil)
			if err != nil || pending.Outcome != askswire.ChoiceOutcomePending {
				t.Fatal(pending, err)
			}
			prepared, err := f.a.rights.policy.PrepareMetaHolderContext(run.ctx)
			if err != nil {
				t.Fatal(err)
			}
			old := run.d.agentImage
			run.d.agentImage = func(context.Context, string, string) (applicationAgentImage, error) {
				t.Error("native enrollment lookup inside Book")
				return applicationAgentImage{}, errors.New("locked inventory")
			}
			defer func() { run.d.agentImage = old }()
			_, recorded, err := f.a.book.AnswerSharedChoice(pending.ID, "keep", f.answerer.Program, func(c asks.ChoiceCandidate) error {
				_, err := f.a.authorizeChoiceAnswerer(prepared, f.me(), c)
				return err
			})
			if recorded || !errors.Is(err, asks.ErrAnswererForbidden) {
				t.Fatal(recorded, err)
			}
		})
	}
}

func TestMessagingPartyDecisionMetadataFailsClosed(t *testing.T) {
	for _, outcome := range []rwire.DecisionOutcome{0, rwire.DecisionOutcomePermitted, rwire.DecisionOutcomeDenied, rwire.DecisionOutcomeNotGranted, rwire.DecisionOutcomeUnknownAction, rwire.DecisionOutcomeInvalid, rwire.DecisionOutcomeForbidden, rwire.DecisionOutcomeUnavailable, 99} {
		permit, err := messagePartyPermit(outcome)
		known := outcome == rwire.DecisionOutcomePermitted || outcome == rwire.DecisionOutcomeDenied || outcome == rwire.DecisionOutcomeNotGranted
		if (err == nil) != known || permit != (outcome == rwire.DecisionOutcomePermitted) {
			t.Fatal(outcome, permit, err)
		}
	}
	run := messagePartiesFixture(t)
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "unknown-action.json"), []string{"example/unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	run.f.a.rights.policy = policy
	if got := run.f.a.rights.decide(run.ctx, run.f.answerer, ActionApplicationMessage, "app:receiver"); got.Outcome != rwire.DecisionOutcomeUnknownAction {
		t.Fatal(got)
	}
	if _, err := run.f.a.rights.messageRelatedParties(run.ctx, run.f.comfy.Program); err == nil {
		t.Fatal("unknown action became an absent relationship")
	}
}

func TestMessagingPartiesLateMetadataFailureRefusesEffects(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-use", true: "typed"}[typed], func(t *testing.T) {
			run := messagePartiesFixture(t)
			f := run.f
			var q asks.Record
			if typed {
				configureApplicationPermissionRuntime(t, f)
				p, err := grants.ApplicationPermission(f.comfy, "app:editor", "Editor", "read")
				if err != nil {
					t.Fatal(err)
				}
				var outcome string
				q, outcome, err = f.a.book.AskTypedPermissionOccurrenceAuthorizedFrom(f.a.scope, "runtime", "late-message-metadata", p, listen.Seen{}, asks.Publisher{}, func() error { return nil })
				if err != nil || outcome != "pending" {
					t.Fatal(outcome, err)
				}
			} else {
				q = f.ask(t, f.comfy, inference.ActionComplete, "host:lemonade")
			}
			effect, err := f.a.authorizeAnswerer(run.ctx, f.me(), q, f.a.scope, "allow")
			if err != nil {
				t.Fatal(err)
			}
			f.a.rights.applications = nil
			result, _ := effect(run.ctx, func() error { return nil }, true)
			if result != askswire.ResponderEffectOutcomeUnavailable {
				t.Fatal("late metadata failure", result)
			}
		})
	}
}

func messagePartiesFixture(t *testing.T) *selfRegisteredRun {
	t.Helper()
	run := agent1478Fixture(t)
	run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	run.f.a.rights.applications = run.d
	if err := run.f.a.rights.policy.RegisterAction(ActionApplicationMessage); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ name, program string }{{"sender", run.f.answerer.Program}, {"receiver", run.f.comfy.Program}, {"unrelated", run.f.cli.Program}} {
		agent1478Register(t, run, row.name, row.program)
		agent1478Set(t, run, row.name, "", true)
		answer(t, run.f, agent1478Question(t, run, row.name).ID, "keep")
		agent1478Wait(t, run, row.name, "enrolled")
	}
	run.f.enroll(t, run.f.answerer)
	return run
}

func TestMessagingPartiesRecheckFirstUseAndTypedEffectGuards(t *testing.T) {
	for _, kind := range []string{"first-use", "typed"} {
		for _, option := range []string{"allow", "refuse"} {
			t.Run(kind+"/"+option, func(t *testing.T) {
				run := messagePartiesFixture(t)
				f := run.f
				var q asks.Record
				if kind == "typed" {
					configureApplicationPermissionRuntime(t, f)
					p, err := grants.ApplicationPermission(f.comfy, "app:editor", "Editor", "read")
					if err != nil {
						t.Fatal(err)
					}
					var outcome string
					q, outcome, err = f.a.book.AskTypedPermissionOccurrenceAuthorizedFrom(f.a.scope, "runtime", "message-party", p, listen.Seen{}, asks.Publisher{}, func() error { return nil })
					if err != nil || outcome != "pending" {
						t.Fatal(outcome, err)
					}
				} else {
					q = f.ask(t, f.comfy, inference.ActionComplete, "host:lemonade")
				}
				prepared, err := f.a.rights.policy.PrepareMetaHolderContext(run.ctx)
				if err != nil {
					t.Fatal(err)
				}
				effect, err := f.a.authorizeAnswerer(prepared, f.me(), q, f.a.scope, option)
				if err != nil || effect == nil {
					t.Fatal("initial admission", err)
				}
				messagePartyGrant(t, run, false, true)
				if _, err := f.a.authorizeAnswerer(prepared, f.me(), q, f.a.scope, option); !errors.Is(err, asks.ErrAnswererForbidden) {
					t.Fatal("current admission", err)
				}
				oldRefresh := f.a.rights.policy.RefreshMetaHolder
				f.a.rights.policy.RefreshMetaHolder = func(context.Context) error {
					t.Error("eligibility refresh inside policy effect")
					return errors.New("locked refresh")
				}
				defer func() { f.a.rights.policy.RefreshMetaHolder = oldRefresh }()
				result, _ := effect(prepared, func() error { return nil }, true)
				if result != askswire.ResponderEffectOutcomeForbidden {
					t.Fatal("late relation", result)
				}
				if kind == "first-use" && f.decide(f.comfy, inference.ActionComplete, "host:lemonade") == rwire.DecisionOutcomePermitted {
					t.Fatal("late relation wrote permit")
				}
			})
		}
	}
}

func messagePartyGrant(t *testing.T, run *selfRegisteredRun, reverse, permit bool) {
	t.Helper()
	subject, name := run.f.answerer, "receiver"
	if reverse {
		subject, name = run.f.comfy, "sender"
	}
	if err := run.f.a.rights.policy.Set(subject, ActionApplicationMessage, "app:"+name, permit); err != nil {
		t.Fatal(err)
	}
}

func TestMessagingPartiesLateEnrollmentActivationFencesEveryEffect(t *testing.T) {
	for _, kind := range []string{"enrolled", "exact", "typed"} {
		t.Run(kind, func(t *testing.T) {
			run := messagePartiesFixture(t)
			f := run.f
			current := run.d.currentAgentEnrollment(run.ctx, "receiver")
			if current.Enrollment == nil {
				t.Fatal(current)
			}
			disabled := agent1478Set(t, run, "receiver", current.Enrollment.Revision, false)
			if kind == "exact" {
				f.stop(t)
			}
			messagePartyGrant(t, run, false, true)
			var q asks.Record
			if kind == "typed" {
				configureApplicationPermissionRuntime(t, f)
				p, err := grants.ApplicationPermission(f.comfy, "app:editor", "Editor", "read")
				if err != nil {
					t.Fatal(err)
				}
				var outcome string
				q, outcome, err = f.a.book.AskTypedPermissionOccurrenceAuthorizedFrom(f.a.scope, "runtime", "late-class", p, listen.Seen{}, asks.Publisher{}, func() error { return nil })
				if err != nil || outcome != "pending" {
					t.Fatal(outcome, err)
				}
			} else {
				q = f.ask(t, f.comfy, inference.ActionComplete, "host:lemonade")
			}
			if kind == "exact" {
				resource, err := asks.FirstUseAnswerResource(canonicalSubjectProgram(f.comfy.Program), inference.ActionComplete, "host:lemonade", "allow")
				if err != nil {
					t.Fatal(err)
				}
				permit := true
				if _, err := f.a.rights.policy.ChangeRule(f.revision(t), f.answerer, asks.AnswerAction, resource, rights.RuleEdit{Permit: &permit, By: f.panel, TTL: 5 * time.Minute}, func() error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := f.a.rights.policy.PrepareMetaHolderContext(run.ctx)
			if err != nil {
				t.Fatal(err)
			}
			effect, err := f.a.authorizeAnswerer(prepared, f.me(), q, f.a.scope, "allow")
			if err != nil || effect == nil {
				t.Fatal("initial admission", err)
			}
			revision := f.revision(t)
			agent1478Set(t, run, "receiver", disabled.Revision, true)
			answer(t, f, agent1478Question(t, run, "receiver").ID, "keep")
			agent1478Wait(t, run, "receiver", "enrolled")
			if f.revision(t) != revision {
				t.Fatal("class mutation unexpectedly changed rights")
			}
			result, _ := effect(prepared, func() error { return nil }, true)
			if result != askswire.ResponderEffectOutcomeUnavailable && result != askswire.ResponderEffectOutcomeForbidden {
				t.Fatal("late class activation", result)
			}
			if kind != "typed" && f.decide(f.comfy, inference.ActionComplete, "host:lemonade") == rwire.DecisionOutcomePermitted {
				t.Fatal("late class wrote a permit")
			}
		})
	}
}

func TestMessagingFactsOldRefreshCannotPublishAfterRegistrationMutation(t *testing.T) {
	run := messagePartiesFixture(t)
	old := run.d.agentImage
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		once.Do(func() { close(entered); <-release })
		return old(ctx, program, binding)
	}
	// The seam changed no file stamp; drop the fixture's cached verdicts.
	run.d.agentImages.forget()
	done := make(chan error, 1)
	go func() { done <- run.d.refreshMessageParties(run.ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not reach unlocked native validation")
	}
	got := run.d.register(run.ctx, run.f.panel, wire.ApplicationDescriptor{Name: "new-entry", Title: "New entry", Program: run.f.lemonade.Program})
	close(release)
	if got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old refresh published changed inputs")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not finish")
	}
	run.d.agentImage = old
	if roles := run.d.roles.Load(); roles != nil && roles.messaging != nil {
		t.Fatal("old facts restored after mutation")
	}
	if _, err := run.f.a.rights.policy.PrepareMetaHolderContext(run.ctx); err != nil {
		t.Fatal("fresh projection", err)
	}
	if roles := run.d.roles.Load(); roles == nil || roles.messaging == nil {
		t.Fatal("fresh complete projection unavailable")
	}
}

func TestMessagingFactsVerifiedReadsRetainIdenticalProjection(t *testing.T) {
	run := messagePartiesFixture(t)
	if _, err := run.f.a.rights.policy.PrepareMetaHolderContext(run.ctx); err != nil {
		t.Fatal(err)
	}
	prior := run.d.roles.Load()
	if prior == nil || prior.messaging == nil {
		t.Fatal("no prepared facts")
	}
	if got := run.d.currentAgentEnrollment(run.ctx, "sender"); got.Outcome != applications.OutcomePage {
		t.Fatal(got)
	}
	if run.d.roles.Load() != prior {
		t.Fatal("pure enrollment read invalidated unchanged facts")
	}
	if err := run.d.refreshRoles(run.ctx); err != nil {
		t.Fatal(err)
	}
	if run.d.roles.Load() != prior {
		t.Fatal("pure role refresh invalidated unchanged facts")
	}
	if err := run.d.refreshMessageParties(run.ctx); err != nil {
		t.Fatal(err)
	}
	if run.d.roles.Load() != prior {
		t.Fatal("identical validated refresh replaced live facts")
	}
}

func TestMessagingPartiesBothDirectionsRevocationAndConsumers(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			run := messagePartiesFixture(t)
			f := run.f
			q := f.ask(t, f.comfy, inference.ActionComplete, "host:lemonade")
			choice := asks.ChoiceCandidate{Record: askswire.ChoiceRecord{ID: "choice", Asker: "receiver"}, AskerProgram: f.comfy.Program, Shared: []string{"keep"}}
			if _, err := f.a.authorizeAnswerer(run.ctx, f.me(), q, f.a.scope, "allow"); err != nil {
				t.Fatal("before relation", err)
			}
			messagePartyGrant(t, run, reverse, true)
			if _, err := f.a.authorizeAnswerer(run.ctx, f.me(), q, f.a.scope, "allow"); !errors.Is(err, asks.ErrAnswererForbidden) {
				t.Fatal("first use", err)
			}
			if _, err := f.a.authorizeChoiceAnswerer(run.ctx, f.me(), choice); !errors.Is(err, asks.ErrAnswererForbidden) {
				t.Fatal("shared choice", err)
			}
			if err := f.a.checkAnswerParties(run.ctx, answerer{subject: f.cli}, asks.Parties{Target: f.comfy.Program}); err != nil {
				t.Fatal("unrelated agent", err)
			}
			alias := filepath.Join(t.TempDir(), "sender-alias.exe")
			if err := os.Link(f.answerer.Program, alias); err != nil {
				t.Fatal(err)
			}
			if err := f.a.checkAnswerParties(run.ctx, answerer{subject: rwire.Subject{Account: f.answerer.Account, Program: alias}}, asks.Parties{Target: f.comfy.Program}); !errors.Is(err, asks.ErrAnswererForbidden) {
				t.Fatal("native file alias", err)
			}
			messagePartyGrant(t, run, reverse, false)
			if _, err := f.a.authorizeAnswerer(run.ctx, f.me(), q, f.a.scope, "allow"); err != nil {
				t.Fatal("revoked first use", err)
			}
			if _, err := f.a.authorizeChoiceAnswerer(run.ctx, f.me(), choice); err != nil {
				t.Fatal("revoked shared choice", err)
			}
		})
	}
}

func TestMessagingPartyUncertaintyNeverMeansNoRelation(t *testing.T) {
	run := messagePartiesFixture(t)
	f := run.f
	q := f.ask(t, f.comfy, inference.ActionComplete, "host:lemonade")
	choice := asks.ChoiceCandidate{Record: askswire.ChoiceRecord{ID: "choice", Asker: "receiver"}, AskerProgram: f.comfy.Program, Shared: []string{"keep"}}
	for _, failure := range []string{"directory", "enrollment", "policy"} {
		t.Run(failure, func(t *testing.T) {
			var restore func()
			switch failure {
			case "directory":
				old := f.a.rights.applications
				f.a.rights.applications = nil
				restore = func() { f.a.rights.applications = old }
			case "enrollment":
				old := run.d.agentImage
				run.d.agentImage = func(context.Context, string, string) (applicationAgentImage, error) {
					return applicationAgentImage{}, errors.New("image unavailable")
				}
				// The seam changed no file stamp; drop the cached verdicts.
				run.d.agentImages.forget()
				restore = func() { run.d.agentImage = old }
			case "policy":
				old := f.a.rights.policy
				f.a.rights.policy = nil
				restore = func() { f.a.rights.policy = old }
			}
			defer restore()
			if failure != "policy" {
				if _, err := f.a.rights.policy.PrepareMetaHolderContext(run.ctx); err == nil {
					t.Fatal("unavailable outer facts accepted")
				}
			}
			if err := f.a.checkAnswerParties(run.ctx, f.me(), asks.Parties{Target: f.comfy.Program}); err == nil {
				t.Fatal("uncertainty admitted")
			}
			if failure != "policy" {
				if effect, err := f.a.authorizeAnswerer(run.ctx, f.me(), q, f.a.scope, "allow"); err == nil || effect != nil {
					t.Fatal("first use uncertainty", err)
				}
				if effect, err := f.a.authorizeChoiceAnswerer(run.ctx, f.me(), choice); err == nil || effect != nil {
					t.Fatal("choice uncertainty", err)
				}
			}
		})
	}
}
