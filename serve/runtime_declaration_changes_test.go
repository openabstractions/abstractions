package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	inference "github.com/openabstractions/abstraction-inference/go"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// Provider declarations and inference host adds under architect ruling (3)
// of 2026-11-22: the same change gate as rights edits (RIGHTS-O17), through
// the runtime's real registry, inference operator, rights policy and asks
// Book.

func shortPendingEditWait(t *testing.T) {
	t.Helper()
	saved := pendingEditWait
	pendingEditWait = 0
	t.Cleanup(func() { pendingEditWait = saved })
}

// ruleRead reads one rule as the policy holds it.
func (g *changeGate) ruleRead(t *testing.T, program, action, resource string) string {
	t.Helper()
	read, err := g.rights(t, "read", "--program", program, "--action", action, "--resource", resource)
	if err != nil && read.Outcome != "unknown" {
		t.Fatalf("read %s on %s: %v %+v", action, resource, err, read)
	}
	return read.Outcome
}

func (g *changeGate) declared(t *testing.T, name string) bool {
	t.Helper()
	_, found := providerStates(t, []string{"--endpoint", g.options.endpoint})[name]
	return found
}

// pendingID reads the request id a pending command printed.
func pendingID(t *testing.T, err error) string {
	t.Helper()
	if exitCode(err) != exitWaiting || err == nil {
		t.Fatalf("not pending: %v", err)
	}
	_, rest, ok := strings.Cut(err.Error(), "--follow ")
	if !ok || rest == "" {
		t.Fatalf("no request id: %v", err)
	}
	return strings.TrimSpace(rest)
}

// An unelevated `provider add` is one pending request: neither the
// declaration nor its card rules exist until the person allows it; Allow
// applies both, once.
func TestCommandLineProviderAddWaitsForThePerson(t *testing.T) {
	shortPendingEditWait(t)
	g := startChangeGate(t, 0)
	bin := probeBinary(t)
	endpoint := []string{"--endpoint", g.options.endpoint}

	out, err := runProvider(t, append([]string{"add", "localai", "--program", bin, "--provider-endpoint", "localai", "--contract", "abstraction.inference/chat@1", "--card", "0"}, endpoint...)...)
	id := pendingID(t, err)
	if g.declared(t, "localai") || g.ruleRead(t, bin, resourceservice.ActionHold, ResourceCard("0")) != "unknown" {
		t.Fatalf("a pending declaration changed something: %s", out)
	}
	question := g.question(t, id)
	if question.Key != "rights.change" || !strings.Contains(question.Text, "add the provider 'localai'") || !strings.Contains(question.Text, "use the graphics card") {
		t.Fatalf("the person's question: %+v", question)
	}
	g.asPanel(t, id, "allow")
	if _, err := g.rights(t, "requests", "--follow", id, "--wait", "10s"); err != nil {
		t.Fatalf("the allowed declaration: %v", err)
	}
	if !g.declared(t, "localai") || g.ruleRead(t, bin, resourceservice.ActionHold, ResourceCard("0")) != "found" || g.ruleRead(t, bin, resourceservice.ActionHold, resourceservice.ResourceAwake) != "found" {
		t.Fatal("Allow did not apply the declaration and its rules")
	}
	if lines := g.activity(t, 1); len(lines) != 1 || !strings.HasPrefix(lines[0]["change"], "add the provider 'localai'") || lines[0]["undo"] == "" {
		t.Fatalf("the allowed declaration's Activity line: %+v", lines)
	}
}

// An elevated caller's declaration applies at once, with its Activity line.
func TestElevatedProviderAddIsExempt(t *testing.T) {
	shortPendingEditWait(t)
	g := startChangeGate(t, 0)
	g.elevated.Store(true)
	bin := probeBinary(t)
	out, err := runProvider(t, "add", "localai", "--program", bin, "--provider-endpoint", "localai", "--contract", "abstraction.inference/chat@1", "--card", "0", "--endpoint", g.options.endpoint)
	if err != nil || !g.declared(t, "localai") || g.ruleRead(t, bin, resourceservice.ActionHold, ResourceCard("0")) != "found" {
		t.Fatalf("an elevated declaration was not exempt: %v\n%s", err, out)
	}
	if lines := g.activity(t, 1); len(lines) != 1 || lines[0]["exempt"] != "true" {
		t.Fatalf("the exempt declaration's Activity line: %+v", lines)
	}
}

// A declaration that repeats OA's shipped declaration (its name and program
// pair has product standing, and its arguments, endpoint and resources match),
// as Setup and the installer make them, applies without a question. The same
// declaration with altered arguments, and a shipped program under a name it
// does not ship, ask (security review 2026-11-22, F4).
func TestProductStandingDeclarationIsExempt(t *testing.T) {
	shortPendingEditWait(t)
	tools, dir := newInstallationDir(t)
	program := filepath.Join(tools, "modelhostd")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	if err := os.WriteFile(program, []byte("shipped"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeBundledDeclaration(t, dir, product.ModelHost, filepath.Base(program), "modelhost", []string{"serve"})
	saved := standingInstallFolders
	standingInstallFolders = func() []string { return []string{tools} }
	t.Cleanup(func() { standingInstallFolders = saved })
	g := startChangeGate(t, 0)
	add := func(name string, args ...string) error {
		t.Helper()
		_, err := runProvider(t, append([]string{"add", name, "--program", program, "--provider-endpoint", "modelhost", "--contract", inference.Contract, "--endpoint", g.options.endpoint}, args...)...)
		return err
	}
	pendingID(t, add(product.ModelHost, "--arg", "serve", "--arg", "--debug-port=9222"))
	// The person removed OA's declaration; declaring it again as shipped asks
	// nothing.
	if out, err := runProvider(t, "remove", product.ModelHost, "--endpoint", g.options.endpoint); err != nil {
		t.Fatalf("remove: %v %s", err, out)
	}
	if err := add(product.ModelHost, "--arg", "serve"); err != nil || !g.declared(t, product.ModelHost) {
		t.Fatalf("OA's shipped declaration was asked about: %v", err)
	}
	if n := len(g.activity(t, 0)); n != 0 {
		t.Fatalf("a product-standing declaration wrote a change line: %d", n)
	}
	pendingID(t, add("localai", "--arg", "serve"))
	if g.declared(t, "localai") {
		t.Fatal("a shipped program under a name it does not ship was declared without a question")
	}
}

// A hosted server's credential apply rule is never written before the
// person's Allow: an unelevated `inference server add` is pending and the
// runtime may not yet apply the saved key; Allow writes it.
func TestCredentialApplyRuleWaitsForAllow(t *testing.T) {
	shortPendingEditWait(t)
	g := startChangeGate(t, 0)
	runtimeProgram := probeSelf().Program
	apply := credentials.ResourceFor("saved-key")

	_, err := runInference(t, "server", "add", "attacker", "--base", "https://attacker.invalid/v1", "--api", "openai-compatible",
		"--credential", "saved-key", "--modalities", "chat", "--endpoint", g.options.endpoint)
	id := pendingID(t, err)
	if g.ruleRead(t, runtimeProgram, credentials.ActionApply, apply) != "unknown" || g.ruleRead(t, runtimeProgram, inference.ActionComplete, inference.ResourceHost("attacker")) != "unknown" {
		t.Fatal("a pending host add wrote its rules")
	}
	if question := g.question(t, id); !strings.Contains(question.Text, "use your saved key for it") || !strings.Contains(question.Text, "at https://attacker.invalid/v1") || !strings.Contains(question.Text, "Resource https://attacker.invalid/v1") {
		t.Fatalf("the person's question: %+v", question)
	}
	g.asPanel(t, id, "allow")
	if _, err := g.rights(t, "requests", "--follow", id, "--wait", "10s"); err != nil {
		t.Fatalf("the allowed host add: %v", err)
	}
	if g.ruleRead(t, runtimeProgram, credentials.ActionApply, apply) != "found" {
		t.Fatal("Allow did not write the credential apply rule")
	}
}
