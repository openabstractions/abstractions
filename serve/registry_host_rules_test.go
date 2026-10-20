package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	logging "github.com/openabstractions/abstraction-logging/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

type hostRuleCommandService struct{ *inferenceservice.Host }

func (s hostRuleCommandService) OperatorAvailable() bool { return s.Operator != nil }

// The hosted command boundary has one declaration writer. The full runtime's
// application/Asks directory is independent of these operator-owned assertions.
func hostRuleCommandEndpoint(t *testing.T, operator *inferenceOperator) []string {
	t.Helper()
	options, _ := isolatedRuntime(t)
	endpoint, err := inferenceEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := inference.New(inference.Config{Router: operator.router, Decide: operator.credentials.decideInference,
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "unavailable"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	service, err := inferenceservice.ListenForPlacement(endpoint, provider, inference.ExecutionLocal)
	if err != nil {
		t.Fatal(err)
	}
	service.Operator = operator
	config := host.Options{Endpoint: options.endpoint, LogEndpoint: options.logEndpoint, ConfigEndpoint: options.configEndpoint,
		ConfigWithoutMachine: true, Inference: hostRuleCommandService{service}, InferenceEndpoint: endpoint,
		Sink: remoteTestSink(func(logging.Record) error { return nil })}
	config.ConfigStore, config.ConfigUserKey = isolatedConfigStore(t.TempDir())
	runtime, err := host.Listen(config)
	if err != nil {
		service.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		runtime.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{"--endpoint", options.endpoint, "--runtime-program", self, "--timeout", "10s"}
}

func TestAddHostFinalSurveySeesNewHostedCredentialRule(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "all-rules"
		if partial {
			name = "partial-rule-write"
		}
		t.Run(name, func(t *testing.T) {
			r := testRights(t, t.TempDir())
			actions := []string{ActionProviderManage, inference.ActionHostManage, credentials.ActionApply}
			if !partial {
				actions = append(actions, inference.ActionComplete)
			}
			if err := r.install(actions, []installationRule{{inference.ActionHostManage, credentials.ResourceAccount}}); err != nil {
				t.Fatal(err)
			}
			var listings, earlyRefusals atomic.Int64
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/models" || req.Header.Get("Authorization") != "Bearer fixture-secret" {
					t.Errorf("listing path/auth: %s %q", req.URL.Path, req.Header.Get("Authorization"))
					http.Error(w, "invalid", 400)
					return
				}
				listings.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-fixture"}]}`))
			}))
			defer fixture.Close()
			p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}), report: func(err error) { t.Errorf("provider: %v", err) }}
			ceilings, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "spend.json"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			modelRouter := router.New()
			modelRouter.UseCredentials(func(_ context.Context, consumer, credential, target string) (map[string]string, error) {
				if consumer != router.CredentialConsumer || credential != "deepseek" || target != "127.0.0.1" {
					t.Errorf("credential target: %q %q %q", consumer, credential, target)
				}
				rule := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: r.operators[0]}, credentials.ActionApply, credentials.ResourceFor(credential))
				if rule.Outcome != rwire.RuleReadOutcomeFound || !rule.Record.Rule.Permit {
					earlyRefusals.Add(1)
					return nil, &router.CredentialRefusal{Outcome: "not_granted", Name: credential}
				}
				return map[string]string{"Authorization": "Bearer fixture-secret"}, nil
			})
			var ruleErrors []error
			o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: r}, providers: p, router: modelRouter, ceilings: ceilings, report: func(err error) { ruleErrors = append(ruleErrors, err) }}
			p.Watch(func() {
				if err := o.refreshHosts(); err != nil {
					t.Error(err)
				}
			})
			p.remotesChanged = o.refreshHosts
			_, revision, err := p.read()
			if err != nil {
				t.Fatal(err)
			}
			caller := inference.Subject{Account: r.owner, Program: r.operators[0]}
			entry := iwire.HostEntry{Name: "deepseek", Hosted: true, Kind: router.WireOpenAICompatible, Base: fixture.URL, Credential: "deepseek", Profiles: []string{"chat"}, Ceiling: &iwire.CeilingLimit{TokensPerDay: 1000}}
			var change iwire.HostChange
			var commandEndpoint []string
			if !partial && statusTransportProvesProgram(t) {
				commandEndpoint = hostRuleCommandEndpoint(t, o)
				out, err := runInference(t, append([]string{"server", "add", entry.Name, "--base", entry.Base, "--api", entry.Kind,
					"--credential", entry.Credential, "--tokens-per-day", "1000", "--modalities", "chat", "--json"}, commandEndpoint...)...)
				if err != nil || json.Unmarshal([]byte(out), &change) != nil {
					t.Fatalf("hosted server add: %v\n%s", err, out)
				}
			} else {
				change = o.AddHost(context.Background(), caller, revision, entry)
			}
			if change.Outcome != iwire.EditOutcomeApplied || (partial != strings.HasPrefix(change.Reason, "rules:")) {
				t.Fatalf("add outcome %+v", change)
			}
			if earlyRefusals.Load() < 1 || listings.Load() != 1 {
				t.Fatalf("survey order: early refusals=%d successful listings=%d", earlyRefusals.Load(), listings.Load())
			}
			decision, _ := modelRouter.Route(router.Request{Model: "deepseek-fixture"})
			if decision == nil || decision.Verdict != router.Hosted || decision.Host != "deepseek" {
				t.Fatalf("final model route %+v", decision)
			}
			hosts := o.Hosts(context.Background(), caller)
			if commandEndpoint != nil {
				out, err := runInference(t, append([]string{"server", "list", "--json"}, commandEndpoint...)...)
				if err != nil || json.Unmarshal([]byte(out), &hosts) != nil {
					t.Fatalf("hosted server list: %v\n%s", err, out)
				}
			}
			if hosts.Outcome != iwire.ListOutcomePage || len(hosts.Hosts) != 1 || !hosts.Hosts[0].Up {
				t.Fatalf("durable host state %+v", hosts)
			}
			state := hosts.Hosts[0]
			if state.Entry.Name != entry.Name || !state.Entry.Hosted || state.Entry.Kind != entry.Kind || state.Entry.Base != entry.Base ||
				state.Entry.Credential != entry.Credential || len(state.Entry.Profiles) != 1 || state.Entry.Profiles[0] != "chat" || state.Entry.DeclaredBy != declaredByOperator ||
				state.Entry.Ceiling == nil || state.Entry.Ceiling.TokensPerDay != 1000 || state.Spend == nil || state.Spend.Day == "" {
				t.Fatalf("hosted command projection: %+v", state)
			}
			if !partial {
				for _, rule := range [][2]string{{inference.ActionComplete, inference.ResourceHost(entry.Name)}, {credentials.ActionApply, credentials.ResourceFor(entry.Credential)}} {
					read := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: r.operators[0]}, rule[0], rule[1])
					if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit || read.Record.Why != hostAddWhy {
						t.Fatalf("hosted command rule %v: %+v", rule, read)
					}
				}
			}
			if partial && len(ruleErrors) == 0 {
				t.Fatal("partial rule write was not reported")
			}
			if !partial && len(ruleErrors) != 0 {
				t.Fatalf("unexpected rule errors %v", ruleErrors)
			}
		})
	}
}

func TestRegistryHostDeclarationEstablishesInferenceRules(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install([]string{ActionProviderManage, inference.ActionComplete, credentials.ActionApply},
		[]installationRule{{ActionProviderManage, credentials.ResourceAccount}}); err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"),
		supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}),
		report: func(err error) { t.Errorf("provider: %v", err) }}
	o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: r}, providers: p,
		report: func(err error) { t.Errorf("operator: %v", err) }}
	registry := &runtimeRegistry{providers: p, operator: o}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	entry := iwire.HostEntry{Name: "fixture", Hosted: true, Kind: "openai-compatible", Base: "https://fixture.invalid", Credential: "fixture-key"}
	caller := inference.Subject{Account: r.owner, Program: r.operators[0]}
	result := registry.Declare(context.Background(), caller, revision, hostDeclaration(entry).wire())
	if result.Outcome != wire.DeclarationEditOutcomeApplied || result.Reason != "" {
		t.Fatalf("declaration: %+v", result)
	}
	for _, grant := range []struct{ action, resource string }{
		{inference.ActionComplete, inference.ResourceHost(entry.Name)},
		{credentials.ActionApply, credentials.ResourceFor(entry.Credential)},
	} {
		read := r.policy.ReadRule(rwire.Subject{Account: caller.Account, Program: caller.Program}, grant.action, grant.resource)
		if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit {
			t.Errorf("missing %s on %s: %+v", grant.action, grant.resource, read)
		}
	}
}

func TestHostRemovalDropsDeclarationBudget(t *testing.T) {
	for _, viaRegistry := range []bool{false, true} {
		name := "operator"
		if viaRegistry {
			name = "registry"
		}
		t.Run(name, func(t *testing.T) {
			r := testRights(t, t.TempDir())
			if err := r.install([]string{ActionProviderManage, inference.ActionHostManage, inference.ActionComplete, credentials.ActionApply},
				[]installationRule{{ActionProviderManage, credentials.ResourceAccount}, {inference.ActionHostManage, credentials.ResourceAccount}}); err != nil {
				t.Fatal(err)
			}
			p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"),
				supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}),
				report: func(err error) { t.Errorf("provider: %v", err) }}
			ceilings, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "spend.json"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			o := &inferenceOperator{dir: t.TempDir(), credentials: &runtimeCredentials{runtimeRights: r}, providers: p,
				router: router.New(), ceilings: ceilings, report: func(err error) { t.Errorf("operator: %v", err) }}
			p.Watch(func() {
				if err := o.refreshHosts(); err != nil {
					t.Error(err)
				}
			})
			registry := &runtimeRegistry{providers: p, operator: o}
			_, revision, err := p.read()
			if err != nil {
				t.Fatal(err)
			}
			// An owned unsupported wire has no outbound listing traffic.
			entry := iwire.HostEntry{Name: "fixture", Hosted: true, Kind: "fixture/test@1", Base: "https://fixture.invalid", Credential: "fixture-key", Ceiling: &iwire.CeilingLimit{TokensPerDay: 10}}
			caller := inference.Subject{Account: r.owner, Program: r.operators[0]}
			if viaRegistry {
				result := registry.Declare(context.Background(), caller, revision, hostDeclaration(entry).wire())
				if result.Outcome != wire.DeclarationEditOutcomeApplied {
					t.Fatalf("declare: %+v", result)
				}
				revision = result.Revision
			} else {
				result := o.AddHost(context.Background(), caller, revision, entry)
				if result.Outcome != iwire.EditOutcomeApplied {
					t.Fatalf("add: %+v", result)
				}
				revision = result.Revision
			}
			if err := ceilings.Add(entry.Credential, 10, 0); err != nil {
				t.Fatal(err)
			}
			if _, exceeded := ceilings.Exceeded(entry.Credential); !exceeded {
				t.Fatal("declared budget was not enforced")
			}
			if result := registry.Withdraw(context.Background(), caller, revision, entry.Name); result.Outcome != wire.DeclarationEditOutcomeApplied {
				t.Fatalf("withdraw: %+v", result)
			}
			if _, exceeded := ceilings.Exceeded(entry.Credential); exceeded {
				t.Fatal("removed declaration left its budget in force")
			}
			if _, tokens, _ := ceilings.Spend(entry.Credential); tokens != 10 {
				t.Fatalf("removing a budget lost spend: %d", tokens)
			}
		})
	}
}

func TestSharedCredentialKeepsEveryActiveDeclarationBudget(t *testing.T) {
	file := func(name string, tokens, images int64) providerFile {
		return newHostFile(iwire.HostEntry{Name: name, Hosted: true, Kind: "fixture/test@1", Base: "https://fixture.invalid", Credential: "shared",
			Ceiling: &iwire.CeilingLimit{TokensPerDay: tokens, ImagesPerDay: images}}, declaredByOperator)
	}
	first, second, disabled := file("first", 100, 0), file("second", 200, 5), file("disabled", 1, 1)
	disabled.Disabled = true
	for _, files := range [][]providerFile{{first, second, disabled}, {disabled, second, first}} {
		if got := declarationBudgets(files)["shared"]; got != (inference.Ceiling{TokensPerDay: 100, ImagesPerDay: 5}) {
			t.Fatalf("combined budget: %+v", got)
		}
	}
	if got := declarationBudgets([]providerFile{second})["shared"]; got != (inference.Ceiling{TokensPerDay: 200, ImagesPerDay: 5}) {
		t.Fatalf("remaining declaration: %+v", got)
	}
}
