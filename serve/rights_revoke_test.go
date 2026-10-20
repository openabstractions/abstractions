package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
)

// rights revoke says plainly when no rule matched, and a delegated
// responder's first-use answer scope can be named without the JSON array
// Windows PowerShell 5.1 cannot pass (live walkthrough 2026-10-02, D14: the
// walkthrough read "applied at revision … no exact rule" for a revoke that
// removed nothing).
func TestRightsRevokeSaysWhetherARuleMatched(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on this transport")
	}
	options, _ := credentialsRuntime(t)
	text := func(args ...string) string {
		t.Helper()
		var out, diagnostics bytes.Buffer
		if err := rightsCommand(append(args, "--endpoint", options.endpoint), &out, &diagnostics); err != nil {
			t.Fatalf("rights %v: %v\n%s%s", args, err, out.String(), diagnostics.String())
		}
		return out.String()
	}
	responder := filepath.Join(t.TempDir(), "responder.exe")
	target := filepath.Join(t.TempDir(), "app.exe")
	scope := []string{"--program", responder, "--answer-program", target, "--answer-action", "abstraction.inference/complete",
		"--answer-resource", "host:modelhost", "--answer-option", "allow"}

	if got := text(append([]string{"revoke"}, scope...)...); !strings.HasPrefix(got, "rights revoke: nothing matched: ") || strings.Contains(got, "applied") {
		t.Fatalf("a revoke with no rule printed %q", got)
	}
	reply, err := runRightsCommand(t, append([]string{"revoke", "--endpoint", options.endpoint}, scope...)...)
	if err != nil || reply.Outcome != "applied" || reply.Matched == nil || *reply.Matched {
		t.Fatalf("a revoke with no rule answered %+v %v", reply, err)
	}

	granted, err := runRightsCommand(t, append([]string{"grant", "--endpoint", options.endpoint}, scope...)...)
	if err != nil || granted.Outcome != "applied" {
		t.Fatalf("grant by the --answer-* flags: %+v %v", granted, err)
	}
	want, err := asks.FirstUseAnswerResource(canonicalSubjectProgram(target), "abstraction.inference/complete", "host:modelhost", "allow")
	if err != nil {
		t.Fatal(err)
	}
	read, err := runRightsCommand(t, "read", "--endpoint", options.endpoint, "--program", responder, "--action", asks.AnswerAction, "--resource", want)
	if err != nil || read.Outcome != "found" {
		t.Fatalf("the rule the --answer-* flags wrote is not %s on %s: %+v %v", asks.AnswerAction, want, read, err)
	}
	if got := text(append([]string{"revoke"}, scope...)...); !strings.HasPrefix(got, "rights revoke: removed the rule ") {
		t.Fatalf("a revoke that removed a rule printed %q", got)
	}
	if got := text(append([]string{"revoke"}, scope...)...); !strings.HasPrefix(got, "rights revoke: nothing matched: ") {
		t.Fatalf("a second revoke printed %q", got)
	}

	for _, bad := range [][]string{
		append([]string{"revoke", "--resource", "x"}, scope...),
		{"revoke", "--program", responder, "--answer-program", target, "--answer-action", "abstraction.inference/complete", "--answer-resource", "host:modelhost", "--answer-option", "maybe"},
		{"revoke", "--program", responder, "--answer-program", "relative.exe", "--answer-action", "abstraction.inference/complete", "--answer-resource", "host:modelhost", "--answer-option", "allow"},
		append([]string{"revoke", "--action", "abstraction.credentials/apply"}, scope...),
	} {
		_, err := runRightsCommand(t, append(bad, "--endpoint", options.endpoint)...)
		assertExit(t, err, exitUsage, strings.Join(bad, " "))
	}
}
