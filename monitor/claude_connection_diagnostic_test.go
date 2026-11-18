package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestPanelFakeCommandChild stands in for the connection command: it prints
// vendor-like private text and one closed diagnostic line, then exits 1.
func TestPanelFakeCommandChild(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "oa-fake-command" {
		return
	}
	fmt.Fprintln(os.Stderr, `Error: ENOENT C:\Users\private\.claude\plugins API_KEY=do-not-expose`)
	fmt.Fprintln(os.Stderr, os.Args[len(os.Args)-1])
	os.Exit(1)
}

// Each closed diagnostic a failed connection command prints reaches the
// Panel as one plain sentence and one log category line. Vendor text stays out.
func TestPanelConnectionCommandFailureSaysWhyInOneSentence(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Panel's supervised command runner is Windows-only")
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ line, operation, reason, sentence string }{
		{"OA_CLAUDE_PLUGIN_ERROR marketplace_add folder_missing", "marketplace_add", "folder_missing", "Claude Code couldn't find the OpenAbstractions plugin folder. Connect again to restore it."},
		{"OA_CLAUDE_PLUGIN_ERROR install marketplace_missing", "install", "marketplace_missing", "Claude Code doesn't list the OpenAbstractions plugin source. Connect again to add it."},
		{"OA_CLAUDE_PLUGIN_ERROR update plugin_missing", "update", "plugin_missing", "Claude Code doesn't have the OpenAbstractions plugin installed. Connect again to install it."},
		{"OA_CLAUDE_PLUGIN_ERROR marketplace_add name_conflict", "marketplace_add", "name_conflict", "Another plugin source in Claude Code already uses the OpenAbstractions name."},
		{"OA_CLAUDE_PLUGIN_ERROR validate invalid_plugin", "validate", "invalid_plugin", "Claude Code rejected the OpenAbstractions plugin files. Connect again to replace them."},
		{"OA_CLAUDE_PLUGIN_ERROR install access_denied", "install", "access_denied", "Claude Code couldn't change its plugin settings. Close Claude, then connect again."},
		{"OA_CLAUDE_PLUGIN_ERROR install exit", "install", "exit", "Claude Code stopped with an error while OpenAbstractions changed its plugin settings."},
		{"OA_CLAUDE_PLUGIN_ERROR version unavailable", "version", "unavailable", "Claude Code couldn't be started. Open Claude, then check again."},
		{"OA_CLAUDE_CONNECTIONS_ERROR bound", "connections", "bound", "OpenAbstractions found more saved Claude connections than it can list."},
		{"OA_CLAUDE_CONNECTIONS_ERROR state_folder", "connections", "state_folder", "OpenAbstractions couldn't read its Claude connection folder. Check again."},
		{"OA_CLAUDE_CONNECTIONS_ERROR marker", "connections", "marker", "A saved Claude connection record is damaged. Connect again to replace it."},
		{"OA_CLAUDE_CONNECTIONS_ERROR settings_file", "connections", "settings_file", "OpenAbstractions couldn't read Claude Code's settings file. Check again."},
		{"OA_CLAUDE_CONNECTIONS_ERROR changed", "connections", "changed", "Claude connection settings changed while OpenAbstractions read them. Check again."},
		{"OA_CLAUDE_CONNECTIONS_ERROR other", "connections", "other", "OpenAbstractions couldn't read the saved Claude connections. Check again."},
		{"OA_CLAUDE_PLUGIN_ERROR install private/path", "", "", ""},
	} {
		t.Run(tc.line, func(t *testing.T) {
			var logged bytes.Buffer
			log.SetOutput(&logged)
			defer log.SetOutput(os.Stderr)
			args := []string{"-test.run=^TestPanelFakeCommandChild$", "--", "oa-fake-command", tc.line}
			_, err := runClaudePanelCommand(t.Context(), program, args...)
			var command *panelConfigurationError
			if !errors.As(err, &command) || command.stage != "command_exit" || command.exitCode != 1 {
				t.Fatalf("runner error = %#v", err)
			}
			if command.pluginOperation != tc.operation || command.pluginReason != tc.reason {
				t.Fatalf("diagnostic = %q/%q", command.pluginOperation, command.pluginReason)
			}
			if got := panelConfigurationSentence(err); got != tc.sentence {
				t.Fatalf("sentence = %q", got)
			}
			for _, text := range []string{err.Error(), logged.String(), panelConfigurationSentence(err)} {
				if strings.Contains(text, "private") || strings.Contains(text, "API_KEY") || strings.Contains(text, "ENOENT") {
					t.Fatal("vendor text crossed the diagnostic boundary", text)
				}
			}
			want := "connection command failed: stage=command_exit exit=1 operation=" + tc.operation + " reason=" + tc.reason
			if tc.operation == "" {
				want = "connection command failed: stage=command_exit exit=1 operation=none reason=none"
			}
			if !strings.Contains(logged.String(), want) {
				t.Fatalf("log = %q", logged.String())
			}
			code, sentence := claudeConnectionFailure(err)
			if tc.sentence != "" && (sentence != tc.sentence || code != tc.operation+"_"+tc.reason) {
				t.Fatalf("view reason = %q %q", code, sentence)
			}
			if tc.sentence == "" && (code != "unavailable" || sentence != "Connection setup is unavailable. Check again.") {
				t.Fatalf("view fallback = %q %q", code, sentence)
			}
		})
	}
}

// The Connection section says why the saved connections could not be read,
// and a failed Connect says the command's plain sentence. The raw chain stays
// in Technical details.
func TestClaudeConnectionPageShowsThePlainReason(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel receiver")
	}
	input, _ := json.Marshal(map[string]string{"script": agentConnectionScriptForTest(t, claudeConnectionsPage, "Claude"), "markup": strings.Split(claudeConnectionsPage, "<script>")[0]})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", claudeConnectionReasonFixture)
	command.Stdin = bytes.NewReader(input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("connection reason: %v %s", err, output)
	}
}

const claudeConnectionReasonFixture = `
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const input=JSON.parse(fs.readFileSync(0,'utf8')),nodes=new Map(),pending=[];
function element(kind){return {kind,textContent:'',disabled:false,hidden:false,checked:false,children:[],replaceChildren(){this.children=[]},append(...items){this.children.push(...items)},querySelectorAll(){return []}}}
for(const match of input.markup.matchAll(/\bid="([^"]+)"/g))nodes.set(match[1],element());
const el=id=>{assert(nodes.has(id),'missing '+id);return nodes.get(id)};
const context=vm.createContext({el,AbortController,JSON,document:{createElement:element},para(){},call(path,body,signal){return new Promise((resolve,reject)=>pending.push({path,body,signal,resolve,reject}))},addEventListener(){},fetch:()=>Promise.resolve({}),key:'fixture',oaReady(){},fail(id,error){el(id).textContent=String(error)}});
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const empty={supported:true,outcome:'page',revision:'r',cli_available:true,registration_available:true,picker_available:true,connections:[],background:{state:'absent'},background_asks:{state:'absent'}};
(async()=>{
 vm.runInContext(input.script,context);
 const reason='OpenAbstractions found more saved Claude connections than it can list.';
 pending[0].resolve({...empty,outcome:'unavailable',reason,reason_code:'connections_bound'});await tick();
 assert.equal(el('agent-status').textContent,'Needs attention');
 assert.equal(el('agent-guidance').textContent,reason);
 const refresh=vm.runInContext('refreshAgent()',context);pending[1].resolve(empty);await refresh;
 assert.equal(el('agent-primary').textContent,'Connect');assert(!el('agent-primary').disabled);
 const cases=[
  [Error("Claude Code couldn't find the OpenAbstractions plugin folder. Connect again to restore it.\nconnection command could not be completed (stage: command_exit; exit: 1; Claude plugin: marketplace_add; reason: folder_missing)"),"Claude Code couldn't find the OpenAbstractions plugin folder. Connect again to restore it."],
  [Error('connection command could not be completed (stage: command_exit; exit: 1)'),'The change could not be confirmed. Check again.'],
 ];
 for(const [error,expected] of cases){
  const index=pending.length,click=el('agent-primary').onclick();
  assert.equal(pending[index].path,'/connections/claude/connect');
  pending[index].reject(error);await tick();pending[index+1].resolve(empty);await click;
  assert.equal(el('agent-guidance').textContent,expected);
  assert(el('agent-technical').textContent.includes('command_exit'),'raw chain belongs in Technical details');
 }
})().catch(error=>{console.error(error);process.exit(1)});
`
