package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

// Setup's question and the Panel's Update button share one pending check: an
// update the person already approved reads as a restart of the running apps.
func TestAgentHelperPendingSharesSetupApproval(t *testing.T) {
	all := []applications.AgentUpdateImage{{Application: "oa-claude-integration"}, {Application: "oa-codex-integration"}, {Application: "oa-opencode-integration"}}
	approved := applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready", Intent: "intent", StagedImages: all}
	for _, c := range []struct {
		name, currency string
		retained       applications.AgentUpdateResult
		state          string
		restart        []string
		stopped        string // the background retry's recorded stop
		failure        string // the sentence clause the Panel shows
	}{
		{"published by Setup", "current", approved, "current", nil, "", ""},
		{"no Setup question", "update", applications.AgentUpdateResult{Outcome: applications.OutcomeUnknown, Reason: "intent"}, "update", nil, "", ""},
		{"question still open", "update", applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "permission_pending", Intent: "intent", StagedImages: all}, "update", nil, "", ""},
		{"declined in Setup", "update", applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "not_now", Intent: "intent", StagedImages: all}, "update", nil, "", ""},
		{"approved, apps running", "update", approved, "restart", []string{"Codex", "Claude", "OpenCode"}, "", ""},
		{"approved for Codex only", "update", applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "update_ready", Intent: "intent", StagedImages: all[1:2]}, "restart", []string{"Codex"}, "", ""},
		{"unreadable image", "unknown", approved, "unknown", nil, "", ""},
		{"approved, retry stopped for another reason", "update", approved, "update", nil, "a connection changed since the question", "a connection changed since the question"},
		{"declined, stale retry stop", "update", applications.AgentUpdateResult{Outcome: applications.OutcomeApplied, Reason: "not_now", Intent: "intent", StagedImages: all}, "update", nil, "ignored", ""},
	} {
		got := agentHelperPending(c.currency, c.retained, c.stopped)
		if got.State != c.state || strings.Join(got.Restart, ",") != strings.Join(c.restart, ",") || got.Failure != c.failure {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestConnectionViewsCarryRestartHosts(t *testing.T) {
	restart := agentHelperReading{State: "restart", Restart: []string{"Codex", "Claude"}}
	c := codexPanelFixture(t)
	c.helperState = func(context.Context, string, string) agentHelperReading { return restart }
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Owned: true, Registration: "saved", Program: "owned-helper"})
	}
	view, err := c.read(context.Background())
	if err != nil || view.HelperState != "restart" || strings.Join(view.RestartHosts, ",") != "Codex,Claude" {
		t.Fatal(view, err)
	}
	root := t.TempDir()
	rows := []claudeSavedConnection{{Project: root, ProjectKey: "project", Program: filepath.Join(root, "helper.exe"), ProgramKey: "image", Name: "OpenAbstractions", Kind: "user", Registration: "saved"}}
	claude := &claudePanelConnections{state: func() (string, error) { return root, nil }, image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }}
	claude.helperState = func(context.Context, string, string) agentHelperReading { return restart }
	claude.command = func(context.Context, string, ...string) ([]byte, error) {
		return json.Marshal(map[string]any{"outcome": "page", "connections": rows})
	}
	page, err := claude.read(context.Background())
	if err != nil || page.Connections[0].HelperState != "restart" || strings.Join(page.Connections[0].RestartHosts, ",") != "Codex,Claude" {
		t.Fatal(page, err)
	}
}

func TestAgentConnectionPageShowsUpdateOnlyWhenPending(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel receiver")
	}
	for host, page := range map[string]string{"claude": claudeConnectionsPage, "codex": codexConnectionsPage, "opencode": installedConnection().page()} {
		name := map[string]string{"claude": "Claude", "codex": "Codex", "opencode": "OpenCode"}[host]
		input, _ := json.Marshal(map[string]string{"script": agentConnectionScriptForTest(t, page, name), "markup": strings.Split(page, "<script>")[0], "host": host})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		command := exec.CommandContext(ctx, node, "-e", agentPendingFixture)
		command.Stdin = bytes.NewReader(input)
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s pending states: %v %s", host, err, output)
		}
	}
}

const agentPendingFixture = `
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const input=JSON.parse(fs.readFileSync(0,'utf8')),nodes=new Map();
function element(kind){return {kind,textContent:'',disabled:false,hidden:false,checked:false,children:[],replaceChildren(){this.children=[]},append(...items){this.children.push(...items)},querySelectorAll(){return []}}}
for(const match of input.markup.matchAll(/\bid="([^"]+)"/g))nodes.set(match[1],element());
const el=id=>{assert(nodes.has(id),'missing '+id);return nodes.get(id)};
const context=vm.createContext({el,AbortController,JSON,document:{createElement:element},para(){},call(){return new Promise(()=>{})},addEventListener(){},fetch:()=>Promise.resolve({}),key:'fixture',oaReady(){},fail(id,error){el(id).textContent=String(error)}});
vm.runInContext(input.script,context);
const host=input.host;
function view(state,hosts,mod,failure){
 const row={helper_state:state};if(hosts)row.restart_hosts=hosts;if(failure)row.update_failure=failure;
 if(host==='claude')return {supported:true,outcome:'page',revision:'r',cli_available:true,registration_available:true,background_available:true,connections:[{kind:'user',name:'OpenAbstractions',program_key:'p',project_key:'k',registration:'saved',mod_state:mod||'current',...row}],background:{state:'owned'},background_asks:{state:'disabled'}};
 if(host==='codex')return {supported:true,outcome:'page',revision:'r',owned:true,available:true,registration:'saved',...row};
 return {supported:true,outcome:'page',revision:'r',owned:state==='current',available:true,registration:state==='current'?'saved':'absent',host_registration:'registered',helper_enrollment:'enrolled',...row};
}
const shown=()=>el('agent-primary').hidden?'':el('agent-primary').textContent;
// After Setup's update of all three published, nothing is pending.
context.v=view('current');vm.runInContext('drawAgent(v)',context);
assert.notEqual(shown(),'Update');assert.equal(el('agent-status').textContent,'Connected');
// A genuinely pending image change offers Update.
context.v=view('update');vm.runInContext('drawAgent(v)',context);
assert.equal(shown(),'Update');assert.equal(el('agent-status').textContent,'Update ready');
// Setup's approved update waits for the running apps: one sentence, no Update.
context.v=view('restart',['Codex','Claude','OpenCode'],'update');vm.runInContext('drawAgent(v)',context);
assert.notEqual(shown(),'Update');assert.notEqual(shown(),'Connect');
assert.equal(el('agent-status').textContent,'Restart needed');
assert.equal(el('agent-guidance').textContent,'Restart Codex, Claude and OpenCode to finish updating.');
// An approved update whose retry stopped for another reason offers Update again.
context.v=view('update',null,null,'a connection changed since the question');vm.runInContext('drawAgent(v)',context);
assert.equal(shown(),'Update');assert.equal(el('agent-guidance').textContent,'Update stopped: a connection changed since the question.');
context.v=view('restart',['Claude']);vm.runInContext('drawAgent(v)',context);
assert.equal(el('agent-guidance').textContent,'Restart Claude to finish updating.');
context.v=view('restart');vm.runInContext('drawAgent(v)',context);
assert.equal(el('agent-guidance').textContent,'Restart '+agentNameOf()+' to finish updating.');
function agentNameOf(){return vm.runInContext('agentName',context)}
`
