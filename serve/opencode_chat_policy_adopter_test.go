package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
)

// chatPolicyProbe runs the staged OpenCode plugin's start under Node with its
// own selection read (only the connector is the Node addon's in place of
// Bun's) and the router inventory stubbed to one family, then prints the
// model the start selected and the plugin's log lines.
const chatPolicyProbe = `
import {NativeConnector} from '@openabstractions/ipc';
import OpenAbstractionsPlugin from './plugin.js';
const fakeTool = Object.assign((definition) => definition, {schema: {
  object: (value) => value,
  string: () => ({optional() { return this; }}),
  number: () => ({optional() { return this; }}),
  boolean: () => ({optional() { return this; }}),
  enum: (value) => ({value, optional() { return this; }}),
  array: (value) => ({value, optional() { return this; }}),
}});
const offer = {family: 'fixture-chat', names: ['fixture-chat'], servers: ['local'], context: 32768n, contextSource: 'effective',
  input: 28672n, unknown: []};
const logs = [];
const plugin = await OpenAbstractionsPlugin({client: {app: {log: async ({body}) => { logs.push(body); }}}}, {}, {
  tool: fakeTool, connector: new NativeConnector(), readCapability: async () => false,
  readInventory: async () => ({offers: [offer]}),
});
const config = {model: 'openabstractions/fixture-chat', provider: {openabstractions: {npm: '@openabstractions/opencode',
  options: {scope: 'local', runtimeEndpoint: process.env.OA_PROBE_ENDPOINT}, models: {}}}};
await plugin.config(config);
console.log(JSON.stringify({model: config.model, listed: Object.hasOwn(config.provider.openabstractions.models, 'oa-chat'), logs}));
`

// The OpenCode plugin reads the chat policy the Panel's chooser stored for
// OpenCode's program through the real selection service, as an application
// holding only the installation's grants, and makes it OpenCode's model at
// start (INF-R1: a program reads its own policies with no grant; adopter
// boundary move plan step 5). Before that rule the read was forbidden and the
// start kept the configured model.
func TestOpenCodePluginReadsItsOwnChatPolicyUnderInstallationGrants(t *testing.T) {
	if _, err := findOpenCodeProviderSources(filepath.Clean("..")); err != nil {
		if !privateWorkspace(t) && strings.Contains(err.Error(), "generated JavaScript packages absent") {
			t.Skip("OpenCode provider integration requires facade, inference and identity JavaScript checkouts")
		}
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable for the staged OpenCode plugin")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	provider := filepath.Dir(stageOpenCodeProvider(t))
	if _, err := os.Stat(filepath.Join(provider, "node_modules", "@openabstractions", "ipc", "native", "oa_ipc_node.node")); err != nil {
		t.Skip("the Node IPC addon is not built: run py adopters/opencode/test/run_node_tests.py --run or scripts/build_node_addon.py")
	}
	script := filepath.Join(provider, "chat-policy-probe.mjs")
	if err := os.WriteFile(script, []byte(chatPolicyProbe), 0o600); err != nil {
		t.Fatal(err)
	}
	execPath, err := exec.Command(node, "-p", "process.execPath").Output()
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Clean(strings.TrimSpace(string(execPath)))

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint, err := inferenceEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// What the Panel's "Chat model for <app>" row stores for the app's program.
	policy := iwire.SelectionPolicyV2{Name: "oa-chat-default", Candidates: []iwire.SelectionCandidateV2{{Model: "fixture-chat", Placement: iwire.CandidatePlacementLocal}}}
	if change, err := inferenceclient.NewSelectionOperatorV2(endpoint).Put(ctx, program, "", policy); err != nil || change.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("store the app's chat policy: %+v %v", change, err)
	}

	command := exec.CommandContext(ctx, node, script)
	command.Dir = provider
	command.Env = append(environmentWithout(os.Environ(), "ABSTRACTION_IPC_LIBRARY", "ABSTRACTION_IPC_NODE"), "OA_PROBE_ENDPOINT="+options.endpoint)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("plugin start: %v\n%s", err, output)
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(string(output)), "\r\n", "\n"), "\n")
	var result struct {
		Model  string `json:"model"`
		Listed bool   `json:"listed"`
		Logs   []struct {
			Message string         `json:"message"`
			Extra   map[string]any `json:"extra"`
		} `json:"logs"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
		t.Fatalf("plugin output %q: %v", output, err)
	}
	if !result.Listed || result.Model != "openabstractions/oa-chat" {
		t.Fatalf("the plugin did not use the chat model stored for it: model %q, listed %v, logs %+v", result.Model, result.Listed, result.Logs)
	}
}
