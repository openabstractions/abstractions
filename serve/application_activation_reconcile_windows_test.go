package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func activationReconcileNativeClient(t *testing.T, run *selfRegisteredRun, operators ...string) (*applications.ApplicationsClient, string, func()) {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	operators = append(operators, program)
	prior := run.d.operator
	run.d.operator = func(candidate string) bool {
		for _, p := range operators {
			if samePrograms(candidate, p) {
				return true
			}
		}
		return prior != nil && prior(candidate)
	}
	for _, p := range operators {
		subject := rights.Subject{Account: run.d.owner, Program: identity.NormalizeSubjectProgram(p)}
		for _, rule := range []struct{ action, resource string }{{ActionApplicationManage, "account"}, {ActionApplicationRead, "app:config-agent"}} {
			if err := run.f.a.rights.policy.RegisterAction(rule.action); err != nil {
				t.Fatal(err)
			}
			if err := run.f.a.rights.policy.Set(subject, rule.action, rule.resource, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	endpoint := shortMediationEndpoint(t)
	h, err := listenApplications(endpoint, run.d, run.d.owner, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	h.v3 = &applicationV3Gateway{directory: run.d}
	ctx, cancel := context.WithCancel(run.ctx)
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			h.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(3 * time.Second):
				t.Error("native recovery host did not join")
			}
		})
	}
	t.Cleanup(stop)
	expected := runtimeExpectation(t)
	client := applications.NewApplicationsClient(listen.FrameClient{Endpoint: endpoint, Server: &expected, Timeout: 3 * time.Second, MaxFrame: 1 << 20}.WithContext(ctx))
	return client, endpoint, stop
}

func TestReconcileActivationGeneratedNativeRPCRecoversBothFaultStages(t *testing.T) {
	for _, stage := range []string{"prepared", "descriptor"} {
		t.Run(stage, func(t *testing.T) {
			run, request, before := activationConfigFixture(t)
			client, _, stop := activationReconcileNativeClient(t, run)
			run.d.activationConfigFault = func(at string) error {
				if at == stage {
					return errors.New("interrupted")
				}
				return nil
			}
			if got, err := client.SetActivation(request); err != nil || got.Outcome != wire.ApplicationOutcomeUnavailable {
				t.Fatal(got, err)
			}
			stop()
			run.start(t)
			agent1478Attach(t, run)
			client, _, _ = activationReconcileNativeClient(t, run)
			expected, _, err := run.d.agentSnapshot(run.ctx, request.Expected.Name)
			if err != nil {
				t.Fatal(err)
			}
			operator := run.d.operator
			run.d.operator = func(string) bool { return false }
			if got, err := client.ReconcileActivation(expected); err != nil || got.Outcome != wire.ApplicationOutcomeForbidden {
				t.Fatal("native nonoperator admitted", got, err)
			}
			run.d.operator = operator
			wrong := expected
			wrong.Title = "stale"
			if got, err := client.ReconcileActivation(wrong); err != nil || got.Outcome != wire.ApplicationOutcomeConflict {
				t.Fatal(got, err)
			}
			if got, err := client.ReconcileActivation(expected); err != nil || got.Outcome != wire.ApplicationOutcomeApplied {
				t.Fatal(got, err)
			}
			if after := run.d.currentAgentEnrollment(run.ctx, request.Expected.Name); !reflect.DeepEqual(before, after) {
				t.Fatal("Keep changed", before, after)
			}
			actual, _, err := run.d.agentSnapshot(run.ctx, request.Expected.Name)
			want := request.Expected
			want.Activation = request.Activation
			if err != nil || !reflect.DeepEqual(actual, want) {
				t.Fatal(actual, err)
			}
			if got, err := client.ReconcileActivation(actual); err != nil || got.Outcome != wire.ApplicationOutcomeConflict || got.Reason != "activation_not_pending" {
				t.Fatal(got, err)
			}
		})
	}
}

func TestReconcileActivationFreshC1ControllerAfterNativeRestart(t *testing.T) {
	binary := os.Getenv("OA_ACTIVATION_RECOVERY_MONITOR_BINARY")
	if binary == "" {
		t.Skip("explicit inert fresh-controller binary required")
	}
	for _, stage := range []string{"prepared", "descriptor"} {
		t.Run(stage, func(t *testing.T) {
			run, request, _ := activationConfigFixture(t)
			profile := startprofile.Profile{Name: "claude-v1", Context: "native-durable-inbox", Folder: t.TempDir(), StateDir: t.TempDir(), CLIFileID: "inert-locator", Model: "explicit-model", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 1 << 30, BudgetCents: 1}
			request.Activation.Arguments = profile.Arguments()
			client, _, stop := activationReconcileNativeClient(t, run)
			run.d.activationConfigFault = func(at string) error {
				if at == stage {
					return errors.New("interrupted")
				}
				return nil
			}
			if got, err := client.SetActivation(request); err != nil || got.Outcome != wire.ApplicationOutcomeUnavailable {
				t.Fatal(got, err)
			}
			stop()
			run.start(t)
			agent1478Attach(t, run)
			_, endpoint, _ := activationReconcileNativeClient(t, run, binary)
			server := runtimeExpectation(t)
			config, _ := json.Marshal(struct {
				Endpoint, Application, ExpectedContext string
				Server                                 *listen.ServerExpectation
			}{endpoint, request.Expected.Name, profile.Context, &server})
			ctx, cancel := context.WithTimeout(run.ctx, 25*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "-test.run=^TestClaudeStartupNativeRecoveryChild$")
			child.Env = append(os.Environ(), "OA_CLAUDE_STARTUP_RECOVERY_FIXTURE="+string(config))
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("fresh C1 controller: %v\n%s", err, output)
			}
			actual, _, err := run.d.agentSnapshot(run.ctx, request.Expected.Name)
			if err != nil || actual.Activation == nil || !reflect.DeepEqual(actual.Activation.Arguments, profile.Arguments()) {
				t.Fatal(actual, err)
			}
		})
	}
}
