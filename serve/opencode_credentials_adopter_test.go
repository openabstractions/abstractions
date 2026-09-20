package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
)

// This opt-in adopter proof runs the official OpenCode executable through the
// native OA transport. The credentials command stores a secret through Holder;
// OpenCode retains its name; the inference runtime's Applier supplies the
// Authorization header to the controlled upstream. Revocation prevents a
// later OpenCode request from reaching that upstream.
func TestOpenCodeUsesNamedCredentialThroughNativeHolderAndApplier(t *testing.T) {
	opencode, addon, library := os.Getenv("OA_OPENCODE_BINARY"), os.Getenv("OA_IPC_NODE"), os.Getenv("OA_IPC_LIBRARY")
	if opencode == "" || addon == "" || library == "" {
		t.Skip("set OA_OPENCODE_BINARY, OA_IPC_NODE, and OA_IPC_LIBRARY for the native OpenCode credential proof")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	for _, path := range []string{opencode, addon, library} {
		if !filepath.IsAbs(path) {
			t.Fatalf("fixture path must be absolute: %s", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	opencode = filepath.Clean(opencode)

	upstream := newHostedUpstream(t)
	options, _ := isolatedRuntime(t)
	hosts := fmt.Sprintf(`{"local":[],"hosted":[{"name":"openrouter","base":%q,"wire":"openai-compatible","credential":"openrouter"}],"ceilings":{"openrouter":{"tokens_per_day":100000}}}`, upstream.URL+"/api/v1")
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })
	startInferenceRuntime(t, options)
	runtimeProgram, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runtimeProgram = filepath.Clean(runtimeProgram)

	var transcript strings.Builder
	recordCredentialCommand := func(stdin string, args ...string) error {
		out, diagnostics, err := runCredentials(t, stdin, args...)
		transcript.WriteString(out)
		transcript.WriteString(diagnostics)
		if err != nil {
			transcript.WriteString(err.Error())
		}
		return err
	}
	err = recordCredentialCommand(testCredentialSecret+"\n", "add", "openrouter", "--target", "127.0.0.1",
		"--for", inference.Contract, "--for", "abstraction.router/router@1", "--from-stdin", "--use-by", opencode,
		"--use-by", runtimeProgram,
		"--endpoint", options.endpoint, "--timeout", "30s")
	if strings.Contains(transcript.String(), testCredentialSecret) {
		t.Fatal("credential setup output contains the credential value")
	}
	requireUserScopeAdd(t, err, transcript.String())
	setCompleteRule(t, options.endpoint, opencode, "openrouter", true)

	providerIndex := stageOpenCodeProvider(t)
	project := t.TempDir()
	config := map[string]any{
		"provider": map[string]any{"openabstractions": map[string]any{
			"name": "OpenAbstractions", "npm": fileURL(providerIndex),
			"options": map[string]any{"runtimeEndpoint": options.endpoint, "scope": "remote",
				"guarantees": []string{inference.GuaranteeHosted}, "credential": "openrouter"},
			"models": map[string]any{"anthropic/claude-sonnet-5": map[string]any{"name": "Controlled hosted fixture",
				"limit": map[string]any{"context": 32768, "output": 4096}}},
		}},
		"model": "openabstractions/anthropic/claude-sonnet-5",
	}
	rawConfig, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawConfig, []byte(testCredentialSecret)) {
		t.Fatal("OpenCode configuration contains the credential value")
	}
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	env := isolatedOpenCodeEnvironment(t, t.TempDir(), addon, library)
	run := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, opencode, "--pure", "run", "--auto", "--print-logs", "--log-level", "DEBUG",
			"--model", "openabstractions/anthropic/claude-sonnet-5", "--format", "json", "--dir", project,
			"Reply with the controlled provider response.")
		cmd.Dir, cmd.Env = project, env
		return cmd.CombinedOutput()
	}

	completed, err := run()
	if bytes.Contains(completed, []byte(testCredentialSecret)) {
		t.Fatal("OpenCode output contains the credential value")
	}
	if err != nil || !bytes.Contains(completed, []byte("Hello!")) {
		t.Fatalf("OpenCode hosted credential request: %v\n%s", err, completed)
	}
	beforeRevoke := upstream.authorizations()
	if len(beforeRevoke) == 0 {
		t.Fatal("OpenCode made no request to the controlled upstream")
	}
	for _, header := range beforeRevoke {
		if header != "Bearer "+testCredentialSecret {
			t.Fatalf("controlled upstream received %d unexpected Authorization headers", len(beforeRevoke))
		}
	}

	revokeErr := recordCredentialCommand("", "revoke", "openrouter", "--endpoint", options.endpoint, "--timeout", "30s")
	if strings.Contains(transcript.String(), testCredentialSecret) {
		t.Fatal("credential revocation output contains the credential value")
	}
	if revokeErr != nil {
		t.Fatalf("credentials revoke: %v", revokeErr)
	}
	refused, _ := run()
	if bytes.Contains(refused, []byte(testCredentialSecret)) {
		t.Fatal("revoked OpenCode output contains the credential value")
	}
	if !bytes.Contains(refused, []byte("credential:revoked:openrouter")) && !bytes.Contains(refused, []byte("router:not-here")) {
		t.Fatalf("OpenCode output lacks the post-revocation refusal:\n%s", refused)
	}
	if got := upstream.authorizations(); len(got) != len(beforeRevoke) {
		t.Fatalf("revoked request changed controlled upstream call count from %d to %d", len(beforeRevoke), len(got))
	}

	credentialAudit, diagnostics, err := runCredentials(t, "", "audit", "--endpoint", options.endpoint, "--json", "--timeout", "30s")
	transcript.WriteString(credentialAudit + diagnostics)
	if strings.Contains(credentialAudit+diagnostics, testCredentialSecret) {
		t.Fatal("credential audit contains the credential value")
	}
	var credentialRecords struct {
		Entries []struct {
			Event, Outcome string
			Subject        struct{ Program string }
		}
	}
	if err != nil || json.Unmarshal([]byte(credentialAudit), &credentialRecords) != nil {
		t.Fatalf("credential audit lacks the OpenCode apply and revoked refusal: %v\n%s", err, credentialAudit)
	}
	var applied, revoked bool
	for _, entry := range credentialRecords.Entries {
		if samePrograms(entry.Subject.Program, opencode) {
			applied = applied || entry.Event == "applied" && entry.Outcome == "applied"
			revoked = revoked || entry.Event == "refused" && entry.Outcome == "revoked"
		}
	}
	if !applied || !revoked {
		t.Fatalf("credential audit lacks the OpenCode apply and revoked refusal:\n%s", credentialAudit)
	}
	inferenceAudit, err := runInference(t, "audit", "--endpoint", options.endpoint, "--json", "--timeout", "30s")
	transcript.WriteString(inferenceAudit)
	if strings.Contains(inferenceAudit, testCredentialSecret) {
		t.Fatal("inference audit contains the credential value")
	}
	var inferenceRecords struct {
		Entries []struct{ Program, Outcome string }
	}
	if err != nil || json.Unmarshal([]byte(inferenceAudit), &inferenceRecords) != nil {
		t.Fatalf("inference audit lacks the attributed OpenCode completion: %v\n%s", err, inferenceAudit)
	}
	completedByOpenCode := false
	for _, entry := range inferenceRecords.Entries {
		completedByOpenCode = completedByOpenCode || samePrograms(entry.Program, opencode) && entry.Outcome == "completed"
	}
	if !completedByOpenCode {
		t.Fatalf("inference audit lacks the attributed OpenCode completion:\n%s", inferenceAudit)
	}
	if strings.Contains(transcript.String(), testCredentialSecret) {
		t.Fatal("a command or audit transcript contains the credential value")
	}
	if log, err := os.ReadFile(options.out); err == nil && bytes.Contains(log, []byte(testCredentialSecret)) {
		t.Fatal("runtime log contains the credential value")
	}
	if err := filepath.WalkDir(options.stateDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || strings.Contains(path, string(filepath.Separator)+"items"+string(filepath.Separator)) {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte(testCredentialSecret)) {
			t.Errorf("%s contains the credential value", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
