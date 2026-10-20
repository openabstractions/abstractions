package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestConfigNativeMessagesStartReadsEffectiveTypedSettings(t *testing.T) {
	options := runtimeFlags{stateDir: t.TempDir()}
	path := filepath.Join(t.TempDir(), "config.json")
	serveRuntimeNASConfig(t, &options, path)
	applications := &applicationsHost{}
	configureRuntimeMessageStartSettings(applications, options)
	initial, err := applications.readMessageStartSettings(context.Background(), "fixture")
	if err != nil || initial != defaultMessageStartSettings() {
		t.Fatal(initial, err)
	}
	if err := os.WriteFile(path, []byte(`{"messages_start_questions":false,"messages_start_notes":true,"messages_start_per_hour":"4","messages_start_open_app":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := applications.readMessageStartSettings(context.Background(), "fixture")
	if err != nil || changed.Questions || !changed.Notes || !changed.OpenApp || !changed.Receiver || changed.PerHour != 4 {
		t.Fatal(changed, err)
	}
	// Invalid saved known values resolve through Config's ignored/default rule.
	if err := os.WriteFile(path, []byte(`{"messages_start_notes":"true","messages_start_per_hour":"3"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ignored, err := applications.readMessageStartSettings(context.Background(), "fixture")
	if err != nil || ignored != defaultMessageStartSettings() {
		t.Fatal(ignored, err)
	}
	if err := os.WriteFile(path, []byte("!"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := applications.readMessageStartSettings(context.Background(), "fixture"); err == nil {
		t.Fatal("corrupt settings admitted a start policy")
	}
}

func TestConfigNativeOperatorReadIsIndependentAndRevocationSurvivesRestart(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("current native Program proof limitation")
	}
	state := t.TempDir()
	rights := testRights(t, state)
	ctx := context.Background()
	options := host.Options{}
	gateByRights(&options, rights.decider())
	configureRuntimeTypedConfig(&options, nil, rights, filepath.Join(state, "records.jsonl"))
	rights.configure(&options, nil)
	rules := installationRules(options, nil)
	var actions []string
	for _, rule := range rules {
		if !slices.Contains(actions, rule.Action) {
			actions = append(actions, rule.Action)
		}
	}
	if err := rights.install(actions, rules); err != nil {
		t.Fatal(err)
	}
	// Startup has no kept providers yet. Its cleanup must not retire the
	// independently installed settings-client read rules.
	if err := rights.retireProviderConfig(ctx, map[appliedRule]bool{}); err != nil {
		t.Fatal(err)
	}
	if rule := rights.policy.ReadRule(rights.self(), host.ConfigKeyReadAction, "messages_start_questions"); rule.Outcome != rightswire.RuleReadOutcomeFound || !rule.Record.Rule.Permit {
		t.Fatal("provider cleanup retired independent operator read", rule)
	}
	endpoint := listen.Endpoint(fmt.Sprintf("config-native-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	provider, err := configservice.ListenWithStore(endpoint, casapi.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}, filepath.Join(state, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.OmitMachineRung(); err != nil {
		t.Fatal(err)
	}
	if err := provider.EnableTypedConfig(options.ConfigTyped); err != nil {
		t.Fatal(err)
	}
	if err := provider.EnableEditPolicy(options.ConfigEditPolicy); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- provider.Serve(ctx) }()
	t.Cleanup(func() {
		provider.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("native config fixture did not stop")
		}
	})
	transport := listen.FrameClient{Endpoint: endpoint, Timeout: 2 * time.Second, MaxFrame: 1 << 20}
	reader := wire.NewConfigReaderV2Client(transport)
	first, err := reader.Read([]string{"messages_start_questions"})
	if err != nil || first.Outcome != wire.ReadOutcomeRead || len(first.Entries) != 1 || !first.Entries[0].Value.Boolean {
		t.Fatal("native operator could not read independently granted key", first, err)
	}
	if err := rights.keepProviderConfig(ctx, rights.self().Program, []string{"messages_start_questions"}); err != nil {
		t.Fatal(err)
	}
	if err := rights.removeProviderConfig(ctx, rights.self().Program); err != nil {
		t.Fatal(err)
	}
	if result, err := reader.Read([]string{"messages_start_questions"}); err != nil || len(result.Entries) != 1 {
		t.Fatal("overlapping provider removal retired operator read", result, err)
	}
	if err := rights.policy.Revoke(rights.self(), host.ConfigKeyReadAction, "messages_start_questions"); err != nil {
		t.Fatal(err)
	}
	if rule := rights.policy.ReadRule(rights.self(), host.ConfigEditAction, host.ConfigEditResource); rule.Outcome != rightswire.RuleReadOutcomeFound || !rule.Record.Rule.Permit {
		t.Fatal("read revocation changed independent edit grant", rule)
	}
	checkDenied := func() {
		t.Helper()
		result, err := reader.Read([]string{"messages_start_questions"})
		if err != nil || result.Outcome != wire.ReadOutcomeRead || len(result.Entries) != 0 || len(result.Refused) != 1 || result.Refused[0].Reason != wire.KeyRefusalReasonNotGranted {
			t.Fatal("write grant bypassed revoked read", result, err)
		}
	}
	checkDenied()
	again := testRights(t, state)
	again.configure(&host.Options{}, nil)
	if err := again.install(actions, rules); err != nil {
		t.Fatal(err)
	}
	checkDenied()
}

func TestConfigNativeDynamicDefaultUsesLoggingOwner(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "records.jsonl")
	options := host.Options{}
	configureRuntimeTypedConfig(&options, nil, nil, path)
	value, err := options.ConfigTyped.DynamicDefault("logs_max_use")
	if err != nil {
		t.Fatal(err)
	}
	want, err := logging.DefaultLogsRetention(path)
	if err != nil {
		t.Fatal(err)
	}
	if value == nil || value.Kind != wire.ValueKindBytes || value.Bytes != want.MaxUse {
		t.Fatal("default diverged from logging owner", value, want)
	}
	if value, err := options.ConfigTyped.DynamicDefault("unknown_dynamic_key"); err == nil || value != nil {
		t.Fatal("wrong owner supplied a default", value, err)
	}
}
