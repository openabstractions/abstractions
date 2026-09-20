package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func activationFixture(t *testing.T, timeout time.Duration) (*applicationDirectory, rights.Subject, rights.Subject, wire.ApplicationDescriptor) {
	t.Helper()
	state := t.TempDir()
	operator := rights.Subject{Account: "account", Program: filepath.Join(state, "operator")}
	app := rights.Subject{Account: "account", Program: filepath.Join(state, "app")}
	if err := os.WriteFile(app.Program, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := openApplications(state, operator.Account, func(_ context.Context, _ rights.Subject, _ string, _ string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	descriptor := wire.ApplicationDescriptor{Name: "editor", Program: app.Program, Title: "Editor", StartGuidance: "Open it yourself if activation is disabled", Activation: &wire.ApplicationActivationRecipe{
		Arguments: []string{"--fixture"}, Readiness: wire.ApplicationInterface{Name: "tools", Protocol: "mcp", Contract: "example/tools@1"}, ReadinessTimeoutMs: timeout.Milliseconds(),
	}}
	if result := d.register(context.Background(), operator, descriptor); result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result)
	}
	return d, operator, app, descriptor
}

func TestApplicationActivationSharesLaunchAndReusesVerifiedPresence(t *testing.T) {
	d, _, app, descriptor := activationFixture(t, time.Second)
	caller := rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "caller")}
	var launches, authorized atomic.Int32
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.decide = func(_ context.Context, _ rights.Subject, action, _ string) (bool, error) {
		if action == ActionApplicationActivate {
			authorized.Add(1)
		}
		return true, nil
	}
	d.launch = func(program string, arguments []string) error {
		launches.Add(1)
		if program != descriptor.Program || len(arguments) != 1 || arguments[0] != "--fixture" {
			t.Errorf("launch %q %q", program, arguments)
		}
		close(entered)
		<-release
		close(returned)
		return nil
	}
	results := make(chan wire.ApplicationActivationResult, 2)
	for range 2 {
		go func() { results <- d.activate(context.Background(), caller, "editor") }()
	}
	<-entered
	eventually(t, time.Second, "both callers reaching activation policy", func() bool { return authorized.Load() == 2 })
	close(release)
	<-returned
	announced := d.announce(context.Background(), app, wire.ApplicationPresence{Application: "editor", Interfaces: []wire.ApplicationInterface{descriptor.Activation.Readiness}, LeaseMs: 60000})
	if announced.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(announced)
	}
	started := 0
	for range 2 {
		result := <-results
		if result.Outcome != wire.ApplicationActivationOutcomeReady || result.Instance != announced.Instance {
			t.Fatal(result)
		}
		if result.Started {
			started++
		}
	}
	if started == 0 {
		t.Fatal("shared attempt did not report its start")
	}
	reused := d.activate(context.Background(), caller, "editor")
	if reused.Outcome != wire.ApplicationActivationOutcomeReady || reused.Started || reused.Instance != announced.Instance || launches.Load() != 1 {
		t.Fatal(reused, "launches", launches.Load())
	}
}

func TestApplicationActivationCancellationLeavesAttemptAndApplicationOwnedByUser(t *testing.T) {
	d, _, app, descriptor := activationFixture(t, time.Second)
	caller := rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "caller")}
	launched := make(chan struct{})
	var once sync.Once
	var launches atomic.Int32
	d.launch = func(string, []string) error { launches.Add(1); once.Do(func() { close(launched) }); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan wire.ApplicationActivationResult, 1)
	go func() { returned <- d.activate(ctx, caller, "editor") }()
	<-launched
	cancel()
	if result := <-returned; result.Outcome != wire.ApplicationActivationOutcomeUnavailable || result.Reason != "cancelled" {
		t.Fatal(result)
	}
	announced := d.announce(context.Background(), app, wire.ApplicationPresence{Application: "editor", Interfaces: []wire.ApplicationInterface{descriptor.Activation.Readiness}, LeaseMs: 60000})
	if announced.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(announced)
	}
	eventually(t, time.Second, "the caller-independent attempt observing readiness", func() bool {
		result := d.activate(context.Background(), caller, "editor")
		return result.Outcome == wire.ApplicationActivationOutcomeReady && result.Instance == announced.Instance && launches.Load() == 1
	})
}

func TestApplicationActivationRetainsTimeoutAndReplacementUncertainty(t *testing.T) {
	d, operator, app, descriptor := activationFixture(t, 100*time.Millisecond)
	caller := rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "caller")}
	var launches atomic.Int32
	d.launch = func(string, []string) error { launches.Add(1); return nil }
	first := d.activate(context.Background(), caller, "editor")
	second := d.activate(context.Background(), caller, "editor")
	if first.Outcome != wire.ApplicationActivationOutcomeNotReady || !first.Started || second != first || launches.Load() != 1 {
		t.Fatal(first, second, "launches", launches.Load())
	}
	if removed := d.remove(context.Background(), operator, "editor"); removed.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(removed)
	}
	descriptor.Activation.Arguments = []string{"--replacement"}
	if registered := d.register(context.Background(), operator, descriptor); registered.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(registered)
	}
	if replaced := d.activate(context.Background(), caller, "editor"); replaced.Outcome != wire.ApplicationActivationOutcomeNotReady && replaced.Outcome != wire.ApplicationActivationOutcomeDisabled {
		t.Fatal(replaced)
	}
	if launches.Load() != 1 {
		t.Fatal("replacement duplicated an uncertain launch", launches.Load())
	}
}

func TestApplicationActivationBoundsAStalledPlatformLaunch(t *testing.T) {
	d, _, app, _ := activationFixture(t, 100*time.Millisecond)
	caller := rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "caller")}
	release := make(chan struct{})
	d.launch = func(string, []string) error { <-release; return nil }
	started := time.Now()
	first := d.activate(context.Background(), caller, "editor")
	if first.Outcome != wire.ApplicationActivationOutcomeUnavailable || first.Reason != "launch_timeout" || time.Since(started) > time.Second {
		t.Fatal(first, time.Since(started))
	}
	second := d.activate(context.Background(), caller, "editor")
	if second != first {
		t.Fatal("stalled launch uncertainty was not retained", first, second)
	}
	close(release)
}

func TestApplicationActivationBoundsExecutableInspectionInsideRecipeBudget(t *testing.T) {
	d, _, app, _ := activationFixture(t, 100*time.Millisecond)
	caller := rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "caller")}
	release := make(chan struct{})
	d.stat = func(string) (os.FileInfo, error) { <-release; return nil, errors.New("released") }
	var launches atomic.Int32
	d.launch = func(string, []string) error { launches.Add(1); return nil }
	started := time.Now()
	first := d.activate(context.Background(), caller, "editor")
	if first.Outcome != wire.ApplicationActivationOutcomeUnavailable || first.Reason != "identity_timeout" || time.Since(started) > time.Second {
		t.Fatal(first, time.Since(started))
	}
	if second := d.activate(context.Background(), caller, "editor"); second != first || launches.Load() != 0 || len(d.attempts) != 1 {
		t.Fatal(second, "launches", launches.Load(), "attempts", len(d.attempts))
	}
	close(release)
}

func TestApplicationActivationReadinessIsBoundToTheActivatingSession(t *testing.T) {
	d, _, app, descriptor := activationFixture(t, 150*time.Millisecond)
	caller := rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "caller")}
	d.launch = func(string, []string) error { return nil }
	result := make(chan wire.ApplicationActivationResult, 1)
	go func() { result <- d.activateInSession(context.Background(), caller, "session-a", "editor") }()
	eventually(t, time.Second, "the activation attempt starting", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.attempts["editor"] != nil
	})
	foreign := d.announceInSession(context.Background(), app, "session-b", wire.ApplicationPresence{Application: "editor", Interfaces: []wire.ApplicationInterface{descriptor.Activation.Readiness}, LeaseMs: 60000})
	if foreign.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(foreign)
	}
	if activated := <-result; activated.Outcome != wire.ApplicationActivationOutcomeNotReady {
		t.Fatal("another session satisfied readiness", activated)
	}
	if activated := d.activateInSession(context.Background(), caller, "session-b", "editor"); activated.Outcome != wire.ApplicationActivationOutcomeForbidden || activated.Reason != "session" {
		t.Fatal("another session joined the attempt", activated)
	}
	local := d.announceInSession(context.Background(), app, "session-a", wire.ApplicationPresence{Application: "editor", Interfaces: []wire.ApplicationInterface{descriptor.Activation.Readiness}, LeaseMs: 60000})
	if local.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(local)
	}
	if withdrawn := d.withdrawInSession(context.Background(), app, "session-b", "editor", local.Instance); withdrawn.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal("another session withdrew readiness", withdrawn)
	}
	if activated := d.activateInSession(context.Background(), caller, "session-a", "editor"); activated.Outcome != wire.ApplicationActivationOutcomeReady || activated.Instance != local.Instance {
		t.Fatal(activated)
	}
}

func TestApplicationActivationBookkeepingStaysBoundedAcrossDescriptorChurn(t *testing.T) {
	state := t.TempDir()
	operator := rights.Subject{Account: "account", Program: filepath.Join(state, "operator")}
	program := filepath.Join(state, "app")
	d, err := openApplications(state, operator.Account, func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	for i := range 256 {
		name := "app-" + strconv.Itoa(i)
		descriptor := wire.ApplicationDescriptor{Name: name, Program: program, Title: name}
		if result := d.register(context.Background(), operator, descriptor); result.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(i, result)
		}
		if result := d.remove(context.Background(), operator, name); result.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(i, result)
		}
	}
	if len(d.generations) != 0 || len(d.attempts) != 0 {
		t.Fatal("descriptor churn retained bookkeeping", len(d.generations), len(d.attempts))
	}
	_, _, app, _ := activationFixture(t, time.Second)
	for i := range 64 {
		done := make(chan struct{})
		close(done)
		d.attempts["retired-"+strconv.Itoa(i)] = &applicationActivationAttempt{done: done, completed: true}
	}
	descriptor := wire.ApplicationDescriptor{Name: "editor", Program: app.Program, Title: "Editor", Activation: &wire.ApplicationActivationRecipe{Readiness: wire.ApplicationInterface{Name: "tools", Protocol: "mcp"}, ReadinessTimeoutMs: 1000}}
	if result := d.register(context.Background(), operator, descriptor); result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result)
	}
	if result := d.activate(context.Background(), rights.Subject{Account: operator.Account, Program: filepath.Join(state, "caller")}, "editor"); result.Outcome != wire.ApplicationActivationOutcomeUnavailable || result.Reason != "capacity" || len(d.attempts) != 64 {
		t.Fatal(result, len(d.attempts))
	}
}

func TestApplicationActivationRemovalLinearizesAfterIssuedLaunchAndRejectsItsReadiness(t *testing.T) {
	d, operator, app, descriptor := activationFixture(t, time.Second)
	caller := rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "caller")}
	entered, release := make(chan struct{}), make(chan struct{})
	var launches atomic.Int32
	d.launch = func(string, []string) error {
		launches.Add(1)
		close(entered)
		<-release
		return nil
	}
	activated := make(chan wire.ApplicationActivationResult, 1)
	go func() { activated <- d.activate(context.Background(), caller, "editor") }()
	<-entered
	removed := make(chan wire.ApplicationChange, 1)
	go func() { removed <- d.remove(context.Background(), operator, "editor") }()
	if result := <-removed; result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result)
	}
	if result := <-activated; result.Outcome != wire.ApplicationActivationOutcomeDisabled {
		t.Fatal(result)
	}
	close(release)
	descriptor.Activation.Arguments = []string{"--replacement"}
	if result := d.register(context.Background(), operator, descriptor); result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result)
	}
	// Even if the old launched program announces after replacement, its prior
	// generation cannot become readiness for the new recipe.
	d.announce(context.Background(), app, wire.ApplicationPresence{Application: "editor", Interfaces: []wire.ApplicationInterface{descriptor.Activation.Readiness}, LeaseMs: 60000})
	if result := d.activate(context.Background(), caller, "editor"); result.Outcome != wire.ApplicationActivationOutcomeDisabled || launches.Load() != 1 {
		t.Fatal(result, "launches", launches.Load())
	}
}

func TestApplicationActivationReportsIdentityMissingDisabledAndLaunchFailures(t *testing.T) {
	d, _, app, descriptor := activationFixture(t, time.Second)
	caller := rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "caller")}
	launched := make(chan struct{})
	d.launch = func(string, []string) error { close(launched); return nil }
	result := make(chan wire.ApplicationActivationResult, 1)
	go func() { result <- d.activate(context.Background(), caller, "editor") }()
	<-launched
	wrong := rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "wrong")}
	if announced := d.announce(context.Background(), wrong, wire.ApplicationPresence{Application: "editor", Interfaces: []wire.ApplicationInterface{descriptor.Activation.Readiness}, LeaseMs: 1000}); announced.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal(announced)
	}
	if activated := <-result; activated.Outcome != wire.ApplicationActivationOutcomeIdentityRefused {
		t.Fatal(activated)
	}
	if unknown := d.activate(context.Background(), caller, "missing"); unknown.Outcome != wire.ApplicationActivationOutcomeUnknown {
		t.Fatal(unknown)
	}
	state := t.TempDir()
	disabled, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	disabledDescriptor := wire.ApplicationDescriptor{Name: "viewer", Program: filepath.Join(state, "viewer"), Title: "Viewer", StartGuidance: "Start Viewer"}
	if change := disabled.register(context.Background(), rights.Subject{Account: "account", Program: filepath.Join(state, "operator")}, disabledDescriptor); change.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(change)
	}
	if activation := disabled.activate(context.Background(), rights.Subject{Account: "account", Program: caller.Program}, "viewer"); activation.Outcome != wire.ApplicationActivationOutcomeDisabled {
		t.Fatal(activation)
	}
	missingFile := descriptor
	missingFile.Name, missingFile.Program = "absent", filepath.Join(t.TempDir(), "absent")
	if change := d.register(context.Background(), rights.Subject{Account: app.Account, Program: filepath.Join(t.TempDir(), "operator")}, missingFile); change.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(change)
	}
	if activation := d.activate(context.Background(), caller, "absent"); activation.Outcome != wire.ApplicationActivationOutcomeUnknown || activation.Reason != "not_installed" {
		t.Fatal(activation)
	}
	refused, _, refusedApp, _ := activationFixture(t, time.Second)
	refused.launch = func(string, []string) error { return errors.New("fixture refusal") }
	if activation := refused.activate(context.Background(), rights.Subject{Account: refusedApp.Account, Program: caller.Program}, "editor"); activation.Outcome != wire.ApplicationActivationOutcomeLaunchRefused {
		t.Fatal(activation)
	}
	if activation := refused.activate(context.Background(), rights.Subject{Account: "other", Program: caller.Program}, "editor"); activation.Outcome != wire.ApplicationActivationOutcomeForbidden {
		t.Fatal(activation)
	}
}

func TestApplicationActivationRecipeBounds(t *testing.T) {
	state := t.TempDir()
	descriptor := wire.ApplicationDescriptor{Name: "editor", Program: filepath.Join(state, "editor"), Title: "Editor", Activation: &wire.ApplicationActivationRecipe{
		Readiness: wire.ApplicationInterface{Name: "tools", Protocol: "mcp"}, ReadinessTimeoutMs: 99,
	}}
	if validApplicationDescriptor(descriptor) {
		t.Fatal("short readiness timeout accepted")
	}
	descriptor.Activation.ReadinessTimeoutMs = 100
	descriptor.Activation.Arguments = make([]string, 64)
	for i := range descriptor.Activation.Arguments {
		descriptor.Activation.Arguments[i] = strings.Repeat("x", 4096)
	}
	if validApplicationDescriptor(descriptor) {
		t.Fatal("recipe exceeding the descriptor's 8192-byte aggregate limit accepted")
	}
}
