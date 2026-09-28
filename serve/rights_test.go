package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
)

func TestRightsCommandRegistersAndRetiresAdapterActions(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on this transport")
	}
	options, _ := credentialsRuntime(t)
	run := func(want int, args ...string) rightsReply {
		t.Helper()
		reply, err := runRightsCommand(t, append(args, "--endpoint", options.endpoint)...)
		if want == 0 && err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if want != 0 {
			assertExit(t, err, want, strings.Join(args, " "))
		}
		return reply
	}
	const action = "comfy.presentation/preview"
	if r := run(exitRefusedCall, "register-action", "--action", action, "--revision", "stale"); r.Outcome != "conflict" {
		t.Fatal(r)
	}
	if r := run(0, "register-action", "--action", action); r.Outcome != "applied" {
		t.Fatal(r)
	}
	if r := run(0, "decide", "--action", action, "--resource", "app:comfyui"); r.Outcome != "not_granted" {
		t.Fatal("registration granted authority", r)
	}
	run(0, "grant", "--action", action, "--resource", "app:comfyui", "--program", probeSelf().Program)
	if r := run(0, "decide", "--action", action, "--resource", "app:comfyui"); r.Outcome != "permitted" {
		t.Fatal(r)
	}
	run(0, "retire-action", "--action", action)
	if r := run(0, "decide", "--action", action, "--resource", "app:comfyui"); r.Outcome != "unknown_action" {
		t.Fatal(r)
	}
	if r := run(0, "read", "--action", action, "--resource", "app:comfyui", "--program", probeSelf().Program); r.Outcome != "unknown" {
		t.Fatal("retired action retained grant", r)
	}
	run(exitUsage, "register-action", "--action", action, "--program", probeSelf().Program)
	run(exitUsage, "register-action", "--action", "INVALID/action")
}

// The rights command administers the runtime's own decision policy: the
// installation grant lets this operator program list credentials, a deny rule
// refuses the same probe, a revoke leaves it not granted, and a grant lets it
// through again. decide never changes the policy.
func TestRightsCommandDecidesTheCredentialsProbe(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on this transport")
	}
	options, _ := credentialsRuntime(t)
	endpoint := options.endpoint
	self := probeSelf()
	rights := func(want int, args ...string) rightsReply {
		t.Helper()
		reply, err := runRightsCommand(t, append(args, "--endpoint", endpoint)...)
		if want == 0 && err != nil {
			t.Fatalf("rights %v: %v %+v", args, err, reply)
		}
		if want != 0 {
			assertExit(t, err, want, strings.Join(args, " "))
		}
		return reply
	}
	rule := []string{"--program", self.Program, "--action", credentials.ActionRead, "--resource", credentials.ResourceAccount}
	probe := func(want int) probeResult {
		t.Helper()
		results, err := runProbeCommand(t, "credentials", "list", "--endpoint", endpoint)
		if want == 0 && err != nil {
			t.Fatalf("credentials probe: %v %+v", err, results)
		}
		if want != 0 {
			assertExit(t, err, want, "credentials probe")
		}
		if len(results) != 1 || results[0].Rule == nil || results[0].Rule.Action != credentials.ActionRead {
			t.Fatalf("credentials probe printed %+v", results)
		}
		return results[0]
	}

	list := rights(0, "list")
	if list.Outcome != "page" || list.Revision == "" || !strings.Contains(strings.Join(list.Catalog, " "), credentials.ActionRead) {
		t.Fatalf("rights list %+v", list)
	}
	read := rights(0, append([]string{"read"}, rule...)...)
	if read.Outcome != "found" || read.Permit == nil || !*read.Permit || read.Record == nil {
		t.Fatalf("installation grant %+v", read)
	}
	if allowed := probe(0); allowed.Outcome != "page" {
		t.Fatalf("operator credentials probe %+v", allowed)
	}
	if d := rights(0, "decide", "--action", credentials.ActionRead, "--resource", credentials.ResourceAccount); d.Outcome != "permitted" || d.Subject.Program != self.Program {
		t.Fatalf("decide for this program %+v", d)
	}
	other := rights(exitRefusedCall, append([]string{"decide"}, rule...)...)
	if other.Outcome != "forbidden" || other.RuleOnFile == nil || other.RuleOnFile.Outcome != "found" {
		t.Fatalf("DecideFor from a program that is not an enforcer %+v", other)
	}

	stale := rights(exitRefusedCall, append([]string{"grant", "--deny", "--revision", "not-the-revision"}, rule...)...)
	if stale.Outcome != "conflict" {
		t.Fatalf("grant at a stale revision %+v", stale)
	}
	denied := rights(0, append([]string{"grant", "--deny", "--why", "probe refusal"}, rule...)...)
	if denied.Outcome != "applied" || denied.Current == nil || denied.Current.Permit {
		t.Fatalf("deny rule %+v", denied)
	}
	if refused := probe(exitRefusedCall); refused.Outcome == "page" {
		t.Fatalf("credentials probe under a deny rule %+v", refused)
	}
	if d := rights(0, "decide", "--action", credentials.ActionRead, "--resource", credentials.ResourceAccount); d.Outcome != "denied" {
		t.Fatalf("decide under a deny rule %+v", d)
	}
	rights(0, append([]string{"revoke"}, rule...)...)
	if gone := rights(0, append([]string{"read"}, rule...)...); gone.Outcome != "unknown" {
		t.Fatalf("revoked rule %+v", gone)
	}
	if refused := probe(exitRefusedCall); refused.Outcome == "page" {
		t.Fatalf("credentials probe with no rule %+v", refused)
	}
	rights(0, append([]string{"grant"}, rule...)...)
	if allowed := probe(0); allowed.Outcome != "page" {
		t.Fatalf("credentials probe after a grant %+v", allowed)
	}
	if jobs, err := runProbeCommand(t, "jobs", "inventory", "--endpoint", endpoint); err != nil || jobs[0].Outcome != "page" {
		t.Fatalf("jobs inventory %v %+v", err, jobs)
	}

	for _, bad := range [][]string{
		{"grant", "--program", "relative/app", "--action", credentials.ActionRead, "--resource", "account"},
		{"grant", "--program", self.Program, "--action", "noslash", "--resource", "account"},
		{"grant", "--program", self.Program, "--action", credentials.ActionRead},
		{"revoke", "--deny", "--program", self.Program, "--action", credentials.ActionRead, "--resource", "account"},
		{"list", "--limit", "65"},
		{"read", "--action", credentials.ActionRead, "--resource", "account"},
		{"forget"},
		{"list", "extra"},
	} {
		_, err := runRightsCommand(t, append(bad, "--endpoint", endpoint)...)
		assertExit(t, err, exitUsage, strings.Join(bad, " "))
	}
	_, err := runRightsCommand(t, "list", "--endpoint", unreachableEndpoint(t), "--timeout", "2s")
	assertExit(t, err, exitNotResolved, "rights list with no runtime")
}

// With neither --endpoint nor --runtime-program, ABSTRACTION_RUNTIME_ENDPOINT
// alone would make options.machine() resolve through client.Discover(), which
// verifies the installed runtime's registration regardless of which endpoint
// the variable names (bootstrap.SelectInstalled reads it only for the
// endpoint, never for the server it expects to find there) — silently
// applying the wrong identity check to a different server
// (research/packaged-activation/GATEWAY-REFUSAL-2026-09-22.md). rightsMachine
// instead connects to the named endpoint unverified and says so once.
func TestRightsMachineConnectsUnverifiedWhenOnlyVariableNamesEndpoint(t *testing.T) {
	t.Setenv(runtimeEndpointVar, `\\.\pipe\oa-rightsmachine-test`)
	var diagnostics strings.Builder
	machine, source, err := rightsMachine(probeOptions{timeout: time.Second}, &diagnostics, "rights list", rightsSubUsage("list"))
	if err != nil {
		t.Fatal(err)
	}
	if machine == nil {
		t.Fatal("nil machine")
	}
	if source != endpointFromVar {
		t.Fatalf("source = %v, want endpointFromVar", source)
	}
	got := diagnostics.String()
	for _, want := range []string{runtimeEndpointVar, `\\.\pipe\oa-rightsmachine-test`, "unverified"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice %q does not mention %q", got, want)
		}
	}
}

// No notice, and options.machine()'s own resolution applies unchanged, once
// --endpoint names the connection explicitly: the variable no longer selects
// it alone, so it names no identity gap to disclose.
func TestRightsMachineSilentWithExplicitEndpoint(t *testing.T) {
	t.Setenv(runtimeEndpointVar, `\\.\pipe\oa-rightsmachine-test`)
	var diagnostics strings.Builder
	machine, source, err := rightsMachine(probeOptions{endpoint: `\\.\pipe\oa-rightsmachine-explicit`, timeout: time.Second}, &diagnostics, "rights list", rightsSubUsage("list"))
	if err != nil {
		t.Fatal(err)
	}
	if machine == nil {
		t.Fatal("nil machine")
	}
	if source != endpointExplicit {
		t.Fatalf("source = %v, want endpointExplicit", source)
	}
	if got := diagnostics.String(); got != "" {
		t.Fatalf("notice %q with an explicit --endpoint, want none", got)
	}
}

// No unverified-endpoint notice when --runtime-program is named without
// --endpoint: that is a caller mistake, not the variable-only case
// rightsMachine discloses. It is options.machine()'s own usage refusal
// (round 6: routed through flagMistake, the same three-line shape every
// other exit-2 mistake uses), not a silent one.
func TestRightsMachineSilentWhenRuntimeProgramNamedAlone(t *testing.T) {
	t.Setenv(runtimeEndpointVar, `\\.\pipe\oa-rightsmachine-test`)
	var diagnostics strings.Builder
	if _, _, err := rightsMachine(probeOptions{runtimeProgram: `C:\oa\openabstractions.exe`, timeout: time.Second}, &diagnostics, "rights list", rightsSubUsage("list")); err == nil {
		t.Fatal("--runtime-program without --endpoint accepted")
	}
	got := diagnostics.String()
	if strings.Contains(got, runtimeEndpointVar) || strings.Contains(got, "unverified") {
		t.Fatalf("notice %q names the unverified-endpoint case, want only the --runtime-program mistake", got)
	}
	for _, want := range []string{"rights list: --runtime-program verifies an --endpoint; give both", "--help"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice %q does not mention %q", got, want)
		}
	}
}

// TestRightsSubcommandHelpGoesToStdoutAndIsItsOwn is item 3's own test:
// `rights <sub> --help`, for every subcommand rights has, prints to stdout
// (not stderr, unlike the bug this fixes), exits 0, and shows that
// subcommand's own usage line, flags and one example — not the generic
// rights catalogue every subcommand's page used to fall back to.
func TestRightsSubcommandHelpGoesToStdoutAndIsItsOwn(t *testing.T) {
	subs := []string{"list", "grant", "revoke", "read", "decide", "register-action", "retire-action"}
	for _, sub := range subs {
		t.Run(sub, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			if err := rightsCommand([]string{sub, "--help"}, &output, &diagnostics); err != nil {
				t.Fatalf("%s --help: %v", sub, err)
			}
			if diagnostics.Len() != 0 {
				t.Fatalf("%s --help: diagnostics %q, want none (help goes to stdout)", sub, diagnostics.String())
			}
			got := output.String()
			if !strings.HasPrefix(got, "Usage: openabstractions rights "+sub) {
				t.Fatalf("%s --help: does not start with its own usage line: %q", sub, got)
			}
			if !strings.Contains(got, "Example:") {
				t.Fatalf("%s --help: no example: %q", sub, got)
			}
			if got == rightsUsage {
				t.Fatalf("%s --help: falls back to the generic rights catalogue", sub)
			}
			// Every other subcommand's own usage line is absent from this one's
			// page: each subcommand shows only its own page, not all seven.
			for _, other := range subs {
				if other == sub {
					continue
				}
				if strings.Contains(got, "Usage: openabstractions rights "+other+" ") {
					t.Fatalf("%s --help: also carries %s's own usage line: %q", sub, other, got)
				}
			}
		})
	}
	// rights --help (no subcommand) keeps printing the whole catalogue, to
	// stdout, unchanged.
	var output bytes.Buffer
	if err := rightsCommand([]string{"--help"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != rightsUsage {
		t.Fatalf("rights --help: %q, want the catalogue", output.String())
	}
}
