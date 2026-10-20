package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	"github.com/openabstractions/abstraction-facade/go/grants"
	inference "github.com/openabstractions/abstraction-inference/go"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
	"reflect"
	"strings"
	"testing"
)

func TestProviderConnectCreatesOrReusesFixedHostAndPreservesCustomConflict(t *testing.T) {
	op := &presetFakeOperator{}
	if err := configureProviderHost(t.Context(), op, "deepseek"); err != nil || len(op.hosts) != 1 {
		t.Fatal(op.hosts, err)
	}
	op.hosts[0].Entry.Ceiling = &iclient.CeilingLimit{TokensPerDay: 1000}
	before := op.hosts[0].Entry
	op.calls = nil
	if err := configureProviderHost(t.Context(), op, "deepseek"); err != nil || !reflect.DeepEqual(before, op.hosts[0].Entry) {
		t.Fatal("existing restrictions changed", err)
	}
	for _, call := range op.calls {
		if strings.HasPrefix(call, "add ") || strings.HasPrefix(call, "remove ") {
			t.Fatal("identical binding mutated", op.calls)
		}
	}
	for _, kind := range []string{"base", "credential", "wire", "local"} {
		custom := before
		switch kind {
		case "base":
			custom.Base = "https://person-owned.example/v1"
		case "credential":
			custom.Credential = "personal-key"
		case "wire":
			custom.Kind = "anthropic-messages"
		case "local":
			custom.Hosted = false
		}
		op := &presetFakeOperator{hosts: []iclient.HostState{{Entry: custom}}}
		err := configureProviderHost(t.Context(), op, "deepseek")
		if _, ok := err.(*providerHostConflict); !ok || !reflect.DeepEqual(custom, op.hosts[0].Entry) {
			t.Fatal("custom host not preserved", kind, err)
		}
		for _, call := range op.calls {
			if strings.HasPrefix(call, "add ") || strings.HasPrefix(call, "remove ") {
				t.Fatal("custom host mutated", op.calls)
			}
		}
	}
}

func TestProviderCredentialMetadataRequiresCurrentFixedBinding(t *testing.T) {
	binding, _ := hostedcatalogue.Select("deepseek")
	for _, kind := range []string{"valid", "custom-target", "expired", "other-name", "no-revision", "wrong-consumer"} {
		m := &cwire.Metadata{Name: "deepseek", Revision: "r1", State: cwire.StateActive, Kind: "bearer", Scope: cwire.Scope{Targets: []string{binding.Target}, Consumers: presetConsumers}}
		switch kind {
		case "custom-target":
			m.Scope.Targets = []string{"person-owned.example"}
		case "expired":
			m.State = cwire.StateExpired
		case "other-name":
			m.Name = "other"
		case "no-revision":
			m.Revision = ""
		case "wrong-consumer":
			m.Scope.Consumers = []string{"other.service/api@1"}
		}
		if providerMetadataMatches(m, binding) != (kind == "valid") {
			t.Fatal(kind)
		}
	}
}

type providerTestSurface struct {
	*fakeSurface
	supported bool
}

func (s *providerTestSurface) CredentialFormSupported() bool { return s.supported }

func TestProviderNativeControllerFormExposesOnlyExplicitPersonAllow(t *testing.T) {
	calls := []string{}
	step := func(name string) func(context.Context) error {
		return func(context.Context) error { calls = append(calls, name); return nil }
	}
	c := &providerConnectController{provider: "deepseek", recheck: step("recheck"), preflight: step("preflight"), configure: step("configure"), budget: func(context.Context, int64) error { calls = append(calls, "budget"); return nil }, grant: step("grant"), readback: step("readback")}
	form, err := c.form()
	if err != nil || form == nil || form.Allow == nil || form.PendingReason != "" || len(calls) != 0 {
		t.Fatal(form, err, calls)
	}
	if err := form.Allow(t.Context(), nil, form.DefaultBudget); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"recheck", "preflight", "recheck", "configure", "budget", "recheck", "grant", "readback"}) {
		t.Fatal(calls)
	}
	if form, err := nativeProviderForm(t.Context(), nil, nil, nil); err != nil || form != nil {
		t.Fatal("non-provider construction admitted", form, err)
	}
}

func providerPresenterDetails(t *testing.T, record askclient.RecordMetadata, target askclient.PermissionTarget) askclient.QuestionDetails {
	t.Helper()
	details := typedPresenterDetails(t, record, target)
	companion := target
	companion.Action, companion.Resource = inference.ActionComplete, "host:deepseek"
	details.Permission.Targets = append(details.Permission.Targets, companion)
	return details
}

type providerRetryRules struct {
	*fakeRules
	outcomes  []rightsclient.PolicyEditOutcome
	calls     int
	failFirst bool
}

func (r *providerRetryRules) ApplyFirstUseAllowContext(context.Context, string, string) (rightsclient.PolicyEdit, error) {
	out := r.outcomes[r.calls]
	r.calls++
	if r.failFirst && r.calls == 1 {
		return rightsclient.PolicyEdit{}, errors.New("inert rights reply lost after known native answer")
	}
	revision := "r1"
	if out == rightsclient.PolicyEditOutcomeConflict {
		revision = "r2"
	}
	return rightsclient.PolicyEdit{Outcome: out, Revision: revision}, nil
}

func TestProviderPresenterRetriesOwnAnsweredOccurrenceAndPostsOnlyAfterReadback(t *testing.T) {
	for _, variant := range []string{"explicit-retry", "rights-error", "cas-retry", "not-now", "notice-retry"} {
		t.Run(variant, func(t *testing.T) {
			record := firstUseFixture("provider-retry", nativePresenterProgram("opencode.exe"), credentials.ActionApply, "credential:deepseek")
			target := askclient.PermissionTarget{Account: "S-1-5-21-test", Program: record.About, Action: credentials.ActionApply, Resource: "credential:deepseek"}
			details := providerPresenterDetails(t, record, target)
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}, details: map[string]askclient.QuestionDetails{record.ID: details}, contents: map[string]askclient.QuestionContent{record.ID: {Question: askclient.QuestionAsker{Details: details}}}}
			operator.beforeAnswer = func() {
				details.Question.Question.Record.Option, details.Question.Question.Record.Answered = "allow", "2026-11-04T08:00:00Z"
			}
			surface := &providerTestSurface{fakeSurface: &fakeSurface{option: "allow"}, supported: true}
			base := &fakeRules{}
			p := testPresenter(unlockedSession(), surface, operator, base)
			rules := &providerRetryRules{fakeRules: base, outcomes: []rightsclient.PolicyEditOutcome{rightsclient.PolicyEditOutcomeUnavailable, rightsclient.PolicyEditOutcomeApplied}}
			rules.failFirst = variant == "rights-error"
			if variant == "cas-retry" {
				rules.outcomes = []rightsclient.PolicyEditOutcome{rightsclient.PolicyEditOutcomeConflict, rightsclient.PolicyEditOutcomeConflict, rightsclient.PolicyEditOutcomeApplied}
			}
			if variant == "notice-retry" {
				rules.outcomes = []rightsclient.PolicyEditOutcome{rightsclient.PolicyEditOutcomeApplied}
			}
			p.rules = func() grants.RuleOperator { return rules }
			stores, readbacks, notices := 0, 0, 0
			p.providerComplete = func(context.Context, askclient.RecordMetadata, *askclient.PermissionRequest) error {
				notices++
				if variant == "notice-retry" && notices == 1 {
					return errors.New("inert lost post")
				}
				return nil
			}
			p.providerForms = func(_ context.Context, _ *askclient.PermissionRequest, recheck, grant func(context.Context) error) (*providerKeyForm, error) {
				return (&providerConnectController{provider: "deepseek", missing: true, recheck: recheck, grant: grant, validate: func(context.Context, string, []byte) router.CandidateValidation { return router.CandidateValid }, preflight: func(context.Context) error { return nil }, store: func(context.Context, []byte) error { stores++; return nil }, configure: func(context.Context) error { return nil }, budget: func(context.Context, int64) error { return nil }, readback: func(context.Context) error { readbacks++; return nil }}).form()
			}
			if variant == "not-now" {
				surface.option = "refuse"
				operator.beforeAnswer = nil
			} else {
				surface.during = func() {
					form := surface.shown[0].CredentialForm
					first := form.Allow(t.Context(), []byte("inert-candidate"), 1000)
					if variant == "cas-retry" {
						if first != nil {
							t.Fatal(first)
						}
					} else {
						if first == nil {
							t.Fatal("first attempt should remain unresolved")
						}
						if err := form.Allow(t.Context(), nil, 1000); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			result := p.present(t.Context(), record)
			if result.Outcome != presentAnswered || len(operator.sent()) != 1 {
				t.Fatal(result, operator.sent())
			}
			if variant == "not-now" {
				if rules.calls != 0 || stores != 0 || readbacks != 0 || notices != 0 {
					t.Fatal(rules.calls, stores, readbacks, notices)
				}
				return
			}
			wantCalls := 2
			if variant == "cas-retry" {
				wantCalls = 3
			}
			if variant == "notice-retry" {
				wantCalls = 1
			}
			wantNotices := 1
			wantReadbacks := 1
			if variant == "notice-retry" {
				wantNotices = 2
				wantReadbacks = 2
			}
			if rules.calls != wantCalls || stores != 1 || readbacks != wantReadbacks || notices != wantNotices {
				t.Fatal(rules.calls, stores, readbacks, notices)
			}
		})
	}
}

func TestPresenterProviderMissingFormCannotDowngradeIntoPlainAllow(t *testing.T) {
	for _, supported := range []bool{false, true} {
		for _, factory := range []bool{false, true} {
			record := firstUseFixture("provider-no-form", nativePresenterProgram("opencode.exe"), credentials.ActionApply, "credential:deepseek")
			target := askclient.PermissionTarget{Account: "S-1-5-21-test", Program: record.About, Action: credentials.ActionApply, Resource: "credential:deepseek"}
			details := providerPresenterDetails(t, record, target)
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}, details: map[string]askclient.QuestionDetails{record.ID: details}, contents: map[string]askclient.QuestionContent{record.ID: {Question: askclient.QuestionAsker{Details: details}}}}
			surface := &providerTestSurface{fakeSurface: &fakeSurface{option: "allow"}, supported: supported}
			p := testPresenter(unlockedSession(), surface, operator, &fakeRules{})
			if factory {
				p.providerForms = func(context.Context, *askclient.PermissionRequest, func(context.Context) error, func(context.Context) error) (*providerKeyForm, error) {
					return nil, nil
				}
			}
			result := p.present(t.Context(), record)
			if result.Outcome != presentUnavailable || len(operator.sent()) != 0 || len(surface.shown) != 0 {
				t.Fatal(supported, factory, result, operator.sent(), surface.shown)
			}
		}
	}
}

func TestPresenterProviderFormCannotDowngradeIntoPlainAllow(t *testing.T) {
	for _, supported := range []bool{false, true} {
		record := firstUseFixture("provider-form", nativePresenterProgram("opencode.exe"), credentials.ActionApply, "credential:deepseek")
		target := askclient.PermissionTarget{Account: "S-1-5-21-test", Program: record.About, Action: credentials.ActionApply, Resource: "credential:deepseek"}
		details := providerPresenterDetails(t, record, target)
		operator := &fakeOperator{records: []askclient.RecordMetadata{record}, details: map[string]askclient.QuestionDetails{record.ID: details}, contents: map[string]askclient.QuestionContent{record.ID: {Question: askclient.QuestionAsker{Details: details}}}}
		surface := &providerTestSurface{fakeSurface: &fakeSurface{option: "allow"}, supported: supported}
		rules := &fakeRules{}
		p := testPresenter(unlockedSession(), surface, operator, rules)
		p.providerForms = func(context.Context, *askclient.PermissionRequest, func(context.Context) error, func(context.Context) error) (*providerKeyForm, error) {
			return (&providerConnectController{provider: "deepseek", missing: true}).form()
		}
		result := p.present(t.Context(), record)
		if result.Outcome != presentStale || len(operator.sent()) != 0 || len(surface.shown) != 1 {
			t.Fatal(supported, result, operator.sent())
		}
		shown := surface.shown[0]
		if supported {
			if shown.CredentialForm == nil || shown.CredentialForm.PendingReason == "" {
				t.Fatal("missing budget was hidden")
			}
			if len(shown.Choices) != 2 || shown.Choices[1].Option != "refuse" || shown.Choices[1].Label != "Not now" {
				t.Fatal("provider choice projection changed", shown.Choices)
			}
		} else {
			if shown.CredentialForm != nil || !strings.Contains(shown.Content, "Allow is disabled") {
				t.Fatal(shown)
			}
			for _, choice := range shown.Choices {
				if choice.Option == "allow" {
					t.Fatal("unsupported native surface offered Allow")
				}
			}
		}
	}
}

func TestProviderConnectPresenterCommitsOnlyAfterCheckedNativeAllow(t *testing.T) {
	for _, ready := range []bool{true, false} {
		record := firstUseFixture("provider-allow", nativePresenterProgram("opencode.exe"), credentials.ActionApply, "credential:deepseek")
		target := askclient.PermissionTarget{Account: "S-1-5-21-test", Program: record.About, Action: credentials.ActionApply, Resource: "credential:deepseek"}
		details := providerPresenterDetails(t, record, target)
		operator := &fakeOperator{records: []askclient.RecordMetadata{record}, details: map[string]askclient.QuestionDetails{record.ID: details}, contents: map[string]askclient.QuestionContent{record.ID: {Question: askclient.QuestionAsker{Details: details}}}}
		operator.beforeAnswer = func() {
			details.Question.Question.Record.Option, details.Question.Question.Record.Answered = "allow", "2026-09-30T09:01:00Z"
		}
		surface := &providerTestSurface{fakeSurface: &fakeSurface{option: "allow"}, supported: true}
		p := testPresenter(unlockedSession(), surface, operator, &fakeRules{})
		var effects []string
		p.providerForms = func(_ context.Context, permission *askclient.PermissionRequest, recheck, grant func(context.Context) error) (*providerKeyForm, error) {
			if permission.Targets[0].Program != target.Program || permission.Targets[0].Account != target.Account {
				t.Fatal("requester was replaced")
			}
			return (&providerConnectController{provider: "deepseek", missing: true, recheck: recheck,
				validate: func(_ context.Context, _ string, key []byte) router.CandidateValidation {
					defer clear(key)
					effects = append(effects, "check")
					return router.CandidateValid
				},
				preflight: func(context.Context) error { return nil },
				configure: func(context.Context) error { effects = append(effects, "host"); return nil },
				readback: func(context.Context) error {
					effects = append(effects, "readback")
					if !ready {
						return errors.New("not ready")
					}
					return nil
				},
				budget: func(context.Context, int64) error { effects = append(effects, "budget"); return nil },
				store:  func(context.Context, []byte) error { effects = append(effects, "store"); return nil },
				grant:  func(ctx context.Context) error { effects = append(effects, "grant"); return grant(ctx) }}).form()
		}
		var allowErr error
		surface.during = func() { allowErr = surface.shown[0].CredentialForm.Allow(t.Context(), []byte("inert-candidate"), 1000) }
		result := p.present(t.Context(), record)
		want := presentAnswered
		if !ready {
			want = presentUnavailable
		}
		if result.Outcome != want || (allowErr == nil) != ready || (result.Err == nil) != ready || len(operator.sent()) != 1 || !reflect.DeepEqual(effects, []string{"check", "store", "host", "budget", "grant", "readback"}) {
			t.Fatal(result, effects, operator.sent())
		}
	}
}

func TestProviderCredentialFormNeverEntersTrayJSON(t *testing.T) {
	c := &providerConnectController{provider: "deepseek", missing: true}
	form, err := c.form()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(trayQuestionPresentation{Title: "permission", CredentialForm: form})
	if err != nil || bytes.Contains(raw, []byte("key_page")) || bytes.Contains(raw, []byte("api_keys")) || bytes.Contains(raw, []byte("CredentialForm")) {
		t.Fatal(string(raw), err)
	}
}

func TestProviderConnectControllerRequiresValidKeyAndExplicitOwnedBudget(t *testing.T) {
	for _, mode := range []string{"missing-budget", "bad-key", "unreachable", "changed-question", "host-refused", "budget-refused", "readback-missing", "allow", "stored-key"} {
		t.Run(mode, func(t *testing.T) {
			var effects []string
			c := &providerConnectController{provider: "deepseek", missing: mode != "stored-key",
				recheck: func(context.Context) error {
					if mode == "changed-question" {
						return errors.New("stale")
					}
					return nil
				},
				validate: func(_ context.Context, id string, key []byte) router.CandidateValidation {
					defer clear(key)
					effects = append(effects, "validate")
					if mode == "bad-key" {
						return router.CandidateRejected
					}
					if mode == "unreachable" {
						return router.CandidateUnreachable
					}
					return router.CandidateValid
				},
				preflight: func(context.Context) error { return nil },
				configure: func(context.Context) error {
					effects = append(effects, "host")
					if mode == "host-refused" {
						return errors.New("conflict")
					}
					return nil
				},
				readback: func(context.Context) error {
					effects = append(effects, "readback")
					if mode == "readback-missing" {
						return errors.New("missing")
					}
					return nil
				},
				store: func(_ context.Context, key []byte) error { effects = append(effects, "store"); return nil },
				budget: func(context.Context, int64) error {
					effects = append(effects, "budget")
					if mode == "budget-refused" {
						return errors.New("offline")
					}
					return nil
				},
				grant: func(context.Context) error { effects = append(effects, "grant"); return nil }}
			if mode == "missing-budget" {
				c.budget = nil
			}
			form, err := c.form()
			if err != nil || form.Label != "DeepSeek" || form.NeedsKey != (mode != "stored-key") {
				t.Fatal(form, err)
			}
			key := []byte("inert-fixture-secret")
			if mode == "stored-key" {
				key = nil
			}
			err = form.Allow(t.Context(), key, 1000)
			switch mode {
			case "allow", "unreachable":
				if err != nil || !reflect.DeepEqual(effects, []string{"validate", "store", "host", "budget", "grant", "readback"}) {
					t.Fatal(effects, err)
				}
				if mode == "unreachable" && form.Warning() != "Couldn't check the key right now." {
					t.Fatal("unreachable blocked or hid the warning")
				}
			case "stored-key":
				if err != nil || !reflect.DeepEqual(effects, []string{"host", "budget", "grant", "readback"}) {
					t.Fatal(effects, err)
				}
			default:
				if err == nil {
					t.Fatal("incomplete connection succeeded")
				}
				for _, e := range effects {
					if (e == "store" && mode != "host-refused" && mode != "budget-refused" && mode != "readback-missing") || (e == "grant" && mode != "readback-missing") {
						t.Fatal(effects)
					}
				}
			}
			if !bytes.Equal(key, make([]byte, len(key))) {
				t.Fatal("candidate retained")
			}
			if mode == "readback-missing" {
				before := len(effects)
				c.readback = func(context.Context) error { effects = append(effects, "fresh-readback"); return nil }
				if err := form.Allow(t.Context(), nil, 2000); err != nil || !reflect.DeepEqual(effects[before:], []string{"fresh-readback"}) {
					t.Fatal("retry replayed a write or trusted a cached completion", effects[before:], err)
				}
			}
		})
	}
}
