package opencode_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

// chatPolicyProbe runs the staged OpenCode plugin's start under Node with its
// own selection read (only the connector is the Node addon's in place of
// Bun's) and the router inventory stubbed to two families, then prints the
// model the start selected and the plugin's log lines. Without the policy the
// start would take OA's default family, the first by name (alpha-chat).
const chatPolicyProbe = `
import {NativeConnector} from '@openabstractions/ipc';
import {tool as hostTool} from '@opencode-ai/plugin/tool';
import OpenAbstractionsPlugin from './plugin.js';
const family = (name) => ({family: name, names: [name], servers: ['local'], context: 32768n, contextSource: 'effective',
  input: 28672n, unknown: []});
const offers = [family('alpha-chat'), family('fixture-chat')];
const logs = [];
const plugin = await OpenAbstractionsPlugin({client: {app: {log: async ({body}) => { logs.push(body); }}}}, {}, {
  tool: hostTool, connector: new NativeConnector(), readCapability: async () => false,
  readInventory: async () => ({offers}),
});
const config = {model: 'openabstractions/oa-chat', provider: {openabstractions: {npm: '@openabstractions/opencode',
  options: {scope: 'local', runtimeEndpoint: process.env.OA_PROBE_ENDPOINT,
    server: JSON.parse(process.env.OA_PROBE_RUNTIME_SERVER)}, models: {}}}};
await plugin.config(config);
console.log(JSON.stringify({model: config.model, listed: Object.hasOwn(config.provider.openabstractions.models, 'oa-chat'), logs}));
`

// The OpenCode plugin reads the chat policy the Panel's chooser stored for
// OpenCode's program through the real selection service, as an application
// holding only the installation's grants, and makes the real model it chooses
// OpenCode's model at a start configured on the app's own oa-chat route (INF-R1:
// a program reads its own policies with no grant; adopter boundary move plan
// step 5; since bb4bec7d2 a model the person configured stays).
func TestOpenCodePluginReadsItsOwnChatPolicyUnderInstallationGrants(t *testing.T) {
	if _, err := findOpenCodeProviderSources(adopterRoot(t)); err != nil {
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
	addon := filepath.Join(provider, "node_modules", "@openabstractions", "ipc", "native", "oa_ipc_node.node")
	if _, err := os.Stat(addon); err != nil {
		t.Fatal("the current Node IPC addon is required: run py adopters/opencode/test/run_node_tests.py --run")
	}
	// The normal gate's preceding Node owner prepares this output. Receive its
	// required native capability before building a runtime; a stale source-tree
	// addon must fail as a prerequisite, rather than look like unavailable policy.
	preflight, stopPreflight := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopPreflight()
	const capability = `const api=require(process.argv[1]);
const features=typeof api.features==='function'?api.features():0;
if(!Number.isInteger(features)||(features&4)===0)throw new Error('native single-exchange feature 4 is required');`
	if output, err := exec.CommandContext(preflight, node, "-e", capability, addon).CombinedOutput(); err != nil {
		t.Fatalf("current Node IPC addon prerequisite: %v; run py adopters/opencode/test/run_node_tests.py --run\n%s", err, output)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// What the Panel's "Chat model for <app>" row stores for the app's program.
	policy := iwire.SelectionPolicyV2{Name: "oa-chat-default", Candidates: []iwire.SelectionCandidateV2{{Model: "fixture-chat", Placement: iwire.CandidatePlacementLocal}}}
	operator, err := client.NewVerified(options.endpoint, runtimeExpectation(t, options.program)).ResolveInferenceSelectionOperatorV2(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	if change, err := operator.Put(ctx, program, "", policy); err != nil || change.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("store the app's chat policy: %+v %v", change, err)
	}
	// This isolated runtime has no installed-runtime selection record. Supply
	// the same independently known principal and child image used by the Go
	// operator, keeping the Node connector's native server proof intact.
	expected := runtimeExpectation(t, options.program)
	server := map[string]any{"program": expected.Program}
	switch expected.Principal.Kind {
	case "windows":
		server["principalKind"], server["principal"] = 1, expected.Principal.SID
	case "posix":
		server["principalKind"], server["principal"] = 2, strconv.Itoa(expected.Principal.UID)
	default:
		t.Fatalf("unsupported fixture principal %q", expected.Principal.Kind)
	}
	serverJSON, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(ctx, node, script)
	command.Dir = provider
	command.Env = append(environmentWithout(os.Environ(), "ABSTRACTION_IPC_LIBRARY", "ABSTRACTION_IPC_NODE", "OA_PROBE_ENDPOINT", "OA_PROBE_RUNTIME_SERVER"),
		"OA_PROBE_ENDPOINT="+options.endpoint, "OA_PROBE_RUNTIME_SERVER="+string(serverJSON))
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
	if !result.Listed || result.Model != "openabstractions/fixture-chat" {
		t.Fatalf("the plugin did not use the chat model stored for it: model %q, listed %v, logs %+v", result.Model, result.Listed, result.Logs)
	}
}
