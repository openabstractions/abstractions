package main

import (
	"strings"
	"testing"

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
