package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstraction-identity/listen"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
)

type providerNativeAnswerSpy struct {
	presenterOperator
	answers int
	onRead  func()
}

func (s *providerNativeAnswerSpy) ReadQuestionAskerContext(ctx context.Context, id string) (askclient.QuestionAsker, error) {
	out, err := s.presenterOperator.ReadQuestionAskerContext(ctx, id)
	if s.onRead != nil {
		s.onRead()
	}
	return out, err
}

func (s *providerNativeAnswerSpy) AnswerQuestionIfPendingContext(ctx context.Context, id, option string) (askclient.OperatorDecision, error) {
	s.answers++
	return s.presenterOperator.AnswerQuestionIfPendingContext(ctx, id, option)
}

type providerNativeRetryRules struct {
	firstUseRules
	first string
	calls int
}

func (r *providerNativeRetryRules) ApplyFirstUseAllowContext(ctx context.Context, revision, id string) (rightsclient.PolicyEdit, error) {
	r.calls++
	if r.calls == 1 && r.first == "guard" {
		return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeUnavailable}, nil
	}
	edit, err := r.firstUseRules.ApplyFirstUseAllowContext(ctx, revision, id)
	if r.calls == 1 && r.first == "lost" && err == nil && edit.Outcome == rightsclient.PolicyEditOutcomeApplied {
		return rightsclient.PolicyEdit{}, errors.New("inert lost native completion reply")
	}
	return edit, err
}

func TestProviderFinishSetupReopensNativeOccurrenceAndReconcilesReceipt(t *testing.T) {
	for _, scenario := range []string{"guard-closed", "lost-reply", "lost-reply-revoked"} {
		t.Run(scenario, func(t *testing.T) {
			book, policy, program := panelFirstUseRuntime(t, "abstraction.credentials/apply", "abstraction.inference/complete")
			account := probe.Self().Account
			primary := asks.PermissionTarget{Account: account, Program: program, Action: "abstraction.credentials/apply", Resource: "credential:deepseek"}
			permission, err := asks.NewPermissionRequest(primary)
			if err != nil {
				t.Fatal(err)
			}
			companion := permission.Targets[0]
			companion.Action, companion.Resource = "abstraction.inference/complete", "host:deepseek"
			permission.Targets = append(permission.Targets, companion)
			question, _, err := book.AskPermissionFrom("runtime-scope", "runtime", "provider-native-reopen", permission, listen.Seen{Why: "inert native fixture"}, asks.Publisher{})
			if err != nil {
				t.Fatal(err)
			}
			op, err := defaultPresenterOperator(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			spy := &providerNativeAnswerSpy{presenterOperator: op}
			rules := &providerNativeRetryRules{firstUseRules: &panelRules{}, first: "lost"}
			if scenario == "guard-closed" {
				rules.first = "guard"
			}
			readRecord := func() askclient.RecordMetadata {
				t.Helper()
				read, err := op.ReadQuestionAskerContext(t.Context(), question.ID)
				if err != nil || read.Details.Question.Question.Record == nil {
					t.Fatal(read, err)
				}
				return *read.Details.Question.Question.Record
			}
			key, budget := false, false
			stores := 0
			noticeIDs := []string{}
			makePresenter := func(first bool) *questionPresenter {
				surface := &providerTestSurface{fakeSurface: &fakeSurface{option: "allow", dismissed: make(chan struct{})}, supported: true}
				p := &questionPresenter{session: unlockedSession(), surface: surface, operator: func(context.Context) (presenterOperator, error) { return spy, nil }, rules: func() grants.RuleOperator { return rules }, account: account, budget: time.Second}
				var showing atomic.Bool
				reads := make(chan struct{}, 8)
				spy.onRead = func() {
					if showing.Load() {
						select {
						case reads <- struct{}{}:
						default:
						}
					}
				}
				if !first {
					p.watchEvery = 2 * time.Millisecond
				}
				p.providerForms = func(_ context.Context, _ *askclient.PermissionRequest, recheck, grant func(context.Context) error) (*providerKeyForm, error) {
					return (&providerConnectController{provider: "deepseek", missing: !key, recheck: recheck, grant: grant, validate: func(context.Context, string, []byte) router.CandidateValidation { return router.CandidateValid }, preflight: func(context.Context) error { return nil }, store: func(context.Context, []byte) error { key = true; stores++; return nil }, configure: func(context.Context) error { return nil }, budget: func(context.Context, int64) error { budget = true; return nil }, readback: func(context.Context) error {
						if !budget {
							return errProviderBudget
						}
						for _, target := range permission.Targets {
							if policy.Decide(rightsclient.Subject{Account: target.Account, Program: target.Program}, target.Action, target.Resource).Outcome != rightsclient.DecisionOutcomePermitted {
								return errProviderPermissionRemoved
							}
						}
						return nil
					}}).form()
				}
				p.providerComplete = func(ctx context.Context, _ askclient.RecordMetadata, targets *askclient.PermissionRequest) error {
					read, err := op.ReadQuestionAskerContext(ctx, question.ID)
					if err != nil || read.Details.Question.Question.Record == nil {
						return errProviderReadback
					}
					record := *read.Details.Question.Question.Record
					request, err := providerCompletionRequest(record, targets, trayPublisher{})
					if err != nil {
						return err
					}
					posted, _, err := book.PostNotificationFrom("panel-native-fixture", program, request, listen.Seen{Why: "inert native notice"}, asks.Publisher{}, func() error { return ctx.Err() })
					if err != nil || posted.ID == "" {
						return errProviderReadback
					}
					noticeIDs = append(noticeIDs, posted.ID)
					return nil
				}
				surface.during = func() {
					if !first {
						showing.Store(true)
						for tick := 0; tick < 2; tick++ {
							select {
							case <-reads:
							case <-surface.dismissed:
								t.Error("answered provider form dismissed before Allow")
								return
							case <-time.After(2 * time.Second):
								t.Error("answered provider watch did not stay open across ticks")
								return
							}
						}
						select {
						case <-surface.dismissed:
							t.Error("answered provider form dismissed while unchanged")
							return
						default:
						}
						showing.Store(false)
					}
					var candidate []byte
					if !key {
						candidate = []byte("inert-candidate")
					}
					err := surface.shown[0].CredentialForm.Allow(t.Context(), candidate, 1000)
					if first && err == nil {
						t.Error("first unavailable/lost completion should be retained")
					}
				}
				return p
			}
			first := makePresenter(true).present(t.Context(), readRecord())
			if first.Outcome != presentUnavailable || spy.answers != 1 || len(noticeIDs) != 0 {
				t.Fatal(first, spy.answers, noticeIDs)
			}
			if scenario == "guard-closed" {
				if state := grants.ListedFirstUseRuleState(t.Context(), op, &panelRules{}, account, readRecord()); state != grants.RuleNotWritten {
					t.Fatal(state)
				}
			}
			if scenario == "lost-reply-revoked" {
				for _, target := range permission.Targets {
					if err := policy.Revoke(rightsclient.Subject{Account: target.Account, Program: target.Program}, target.Action, target.Resource); err != nil {
						t.Fatal(err)
					}
				}
			}
			oldOpen, oldSupported := providerSetupReopen, providerSetupReopenSupported
			defer func() { providerSetupReopen, providerSetupReopenSupported = oldOpen, oldSupported }()
			finished := make(chan presentResult, 1)
			providerSetupReopenSupported = func() bool { return true }
			// The presenter program's -present mode, in process: it draws
			// the handed occurrence and its exit code carries the outcome.
			providerSetupReopen = func(ctx context.Context, id string) (string, error) {
				if id != question.ID {
					t.Error("reopen changed native occurrence", id)
				}
				result := makePresenter(false).presentOccurrence(ctx, id)
				finished <- result
				return occurrenceFromExitCode(occurrenceExitCode(occurrenceOutcome(result))), nil
			}
			panel := (&servicePanel{}).handler("test-key")
			pageResponse := panelRequest(t, panel, "/questions", nil)
			page := decodePanel[questionPage](t, pageResponse.Code, pageResponse.Body.Bytes())
			if !page.NativePrompts[question.ID] {
				t.Fatal("native provider row lost its action hint", page)
			}
			if scenario == "lost-reply" && page.RuleStates[question.ID] != grants.RuleWritten {
				t.Fatal("lost completion must remain reachable from written history", page.RuleStates)
			}
			response := panelRequest(t, panel, "/questions", questionAction{Action: "finish_provider", ID: question.ID})
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			var actionResult map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &actionResult); err != nil {
				t.Fatal(err)
			}
			wantOutcome := "completed"
			if scenario == "guard-closed" {
				wantOutcome = "applied"
			}
			if scenario == "lost-reply-revoked" {
				wantOutcome = "removed"
			}
			if actionResult["Outcome"] != wantOutcome || actionResult["Message"] == "" {
				t.Fatal(actionResult)
			}
			var result presentResult
			select {
			case result = <-finished:
			case <-time.After(10 * time.Second):
				t.Fatal("native fixture did not finish")
			}
			if scenario == "lost-reply-revoked" {
				if result.Outcome != presentUnavailable || !errors.Is(result.Err, errProviderPermissionRemoved) || len(noticeIDs) != 0 {
					t.Fatal(result, noticeIDs)
				}
				for _, target := range permission.Targets {
					if policy.Decide(rightsclient.Subject{Account: target.Account, Program: target.Program}, target.Action, target.Resource).Outcome == rightsclient.DecisionOutcomePermitted {
						t.Fatal("Finish setup restored removed permission")
					}
				}
			} else {
				if result.Outcome != presentAnswered || len(noticeIDs) != 1 {
					t.Fatal(result, noticeIDs)
				}
				if scenario == "lost-reply" && result.Answered.RuleOutcome != grants.RuleCompleted {
					t.Fatal("lost reply was not receipt-reconciled", result)
				}
				replayed := makePresenter(false).reopenProvider(t.Context(), question.ID)
				if replayed.Outcome != presentAnswered || replayed.Answered.RuleOutcome != grants.RuleCompleted || len(noticeIDs) != 2 || noticeIDs[0] != noticeIDs[1] {
					t.Fatal("notice replay changed occurrence", replayed, noticeIDs)
				}
			}
			if spy.answers != 1 || stores != 1 {
				t.Fatal("reopen answered or stored again", spy.answers, stores)
			}
			history, _, err := book.ApplicationHistory()
			if err != nil || len(history) != 1 || history[0].ID != question.ID {
				t.Fatal(history, err)
			}
		})
	}
}
