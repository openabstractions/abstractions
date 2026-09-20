package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

const applicationCommandAnnouncerArg = "-oa-applications-command-announcer"

func init() {
	if len(os.Args) == 3 && os.Args[1] == applicationCommandAnnouncerArg {
		os.Exit(runApplicationCommandAnnouncer(os.Args[2]))
	}
}

func runApplicationCommandAnnouncer(endpoint string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	apps, err := client.New(endpoint).ResolveApplications(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return 2
	}
	result, err := apps.Announce(ctx, wire.ApplicationPresence{Application: "editor", LeaseMs: 60000,
		Interfaces: []wire.ApplicationInterface{{Name: "tools", Protocol: "mcp", Contract: "example/tools@1"}},
		Contexts:   []wire.ApplicationContext{{Name: "document-1", Title: "Example document", Revision: "r1"}}})
	if err != nil || result.Outcome != wire.ApplicationOutcomeApplied {
		return 3
	}
	return 0
}

func setApplicationCommandRule(t *testing.T, options runtimeFlags, program, action, resource string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	operator, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveRightsOperator(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	page, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("policy revision: %+v %v", page, err)
	}
	rule := rights.PolicyRule{Subject: rights.Subject{Account: probeSelf().Account, Program: filepath.Clean(program)}, Action: action, Resource: resource, Permit: true}
	edit, err := operator.SetRuleContext(ctx, page.Revision, rule)
	if err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("set %s for %s: %+v %v", action, program, edit, err)
	}
}

func TestApplicationsCommandListsFilteredMetadataAndReusesReadyPresence(t *testing.T) {
	options := gatedRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	apps, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveApplications(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	program := copyTestBinary(t, "oa-applications-command-fixture")
	descriptor := wire.ApplicationDescriptor{Name: "editor", Title: "Example editor", Program: program, StartGuidance: "private guidance",
		Activation: &wire.ApplicationActivationRecipe{Arguments: []string{"--must-not-launch"}, Readiness: wire.ApplicationInterface{Name: "tools", Protocol: "mcp", Contract: "example/tools@1"}, ReadinessTimeoutMs: 5000}}
	if change, err := apps.Register(ctx, descriptor); err != nil || change.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("register: %+v %v", change, err)
	}

	var hidden bytes.Buffer
	if err := applicationsCommand([]string{"list", "--endpoint", options.endpoint, "--json", "--timeout", "10s"}, &hidden, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hidden.String(), "editor") || strings.Contains(hidden.String(), program) {
		t.Fatalf("permission-filtered listing disclosed editor: %s", hidden.String())
	}

	var denied bytes.Buffer
	err = applicationsCommand([]string{"activate", "editor", "--endpoint", options.endpoint, "--json", "--timeout", "10s"}, &denied, io.Discard)
	assertExit(t, err, exitRefusedCall, "applications activate without a grant")
	if !strings.Contains(denied.String(), `"outcome":"forbidden"`) {
		t.Fatalf("typed activation refusal missing: %s %v", denied.String(), err)
	}
	if !applicationTransportProvesSession(t) {
		t.Skip("application session proof unavailable on this host")
	}

	setApplicationCommandRule(t, options, program, ActionApplicationAnnounce, "app:editor")
	cmd := exec.Command(program, applicationCommandAnnouncerArg, options.endpoint)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("announcer: %v\n%s", err, output)
	}
	self := probeSelf().Program
	setApplicationCommandRule(t, options, self, ActionApplicationRead, "app:editor")
	setApplicationCommandRule(t, options, self, ActionApplicationActivate, "app:editor")

	var listed bytes.Buffer
	if err := applicationsCommand([]string{"list", "--endpoint", options.endpoint, "--json", "--timeout", "10s"}, &listed, io.Discard); err != nil {
		t.Fatal(err)
	}
	var page applicationListOutput
	if err := json.Unmarshal(listed.Bytes(), &page); err != nil || len(page.Applications) != 1 || len(page.Applications[0].Instances) != 1 {
		t.Fatalf("list: %v %s", err, listed.String())
	}
	if page.Applications[0].Name != "editor" || page.Applications[0].Instances[0].Contexts[0].Title != "Example document" {
		t.Fatalf("projected list: %+v", page)
	}
	if strings.Contains(listed.String(), program) || strings.Contains(listed.String(), "private guidance") || strings.Contains(listed.String(), "must-not-launch") {
		t.Fatalf("listing disclosed operator-only fields: %s", listed.String())
	}

	var activated bytes.Buffer
	if err := applicationsCommand([]string{"activate", "editor", "--endpoint", options.endpoint, "--json", "--timeout", "10s"}, &activated, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result applicationActivationOutput
	if err := json.Unmarshal(activated.Bytes(), &result); err != nil || result.Outcome != "ready" || result.Started || result.Instance == "" {
		t.Fatalf("activate reused presence: %+v %v %s", result, err, activated.String())
	}
}

func TestApplicationsCommandUsageAndAbsentRuntime(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"list", "extra"}, {"activate"}, {"activate", "../editor"}, {"activate", "editor", "extra"}, {"list", "--timeout", "-1s"}} {
		err := applicationsCommand(args, io.Discard, io.Discard)
		assertExit(t, err, exitUsage, strings.Join(args, " "))
	}
	var help bytes.Buffer
	if err := applicationsCommand([]string{"--help"}, &help, io.Discard); err != nil || !strings.Contains(help.String(), "activate <name>") {
		t.Fatalf("help: %v %s", err, help.String())
	}
	err := applicationsCommand([]string{"list", "--endpoint", unreachableEndpoint(t), "--timeout", "2s"}, io.Discard, io.Discard)
	assertExit(t, err, exitNotResolved, "applications list without runtime")
}

func TestCredentialAndInferenceHelpNameCurrentHostedRequirements(t *testing.T) {
	var credentialsHelp bytes.Buffer
	if err := credentialsCommand([]string{"--help"}, strings.NewReader(""), &credentialsHelp, io.Discard); err != nil ||
		!strings.Contains(credentialsHelp.String(), "abstraction.router/router@1") {
		t.Fatalf("credentials help: %v\n%s", err, credentialsHelp.String())
	}
	var inferenceHelp bytes.Buffer
	if err := inferenceCommand([]string{"--help"}, &inferenceHelp, io.Discard); err != nil ||
		!strings.Contains(inferenceHelp.String(), "image, live") || !strings.Contains(inferenceHelp.String(), "all six") {
		t.Fatalf("inference help: %v\n%s", err, inferenceHelp.String())
	}
}
