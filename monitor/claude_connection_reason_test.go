package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// Agent-ux report 2026-11-22: Claude's page looped on "Connection setup is
// unavailable. Check again." while the real reason sat in Technical
// details, and the Catalog row still read Not connected. The state route
// now says the real reason, and the Catalog row reads Needs attention with
// it until a read works.
func TestClaudeUnavailableStateSaysRealReasonAndCatalogFollows(t *testing.T) {
	old := agentHomeIsolated
	t.Cleanup(func() { agentHomeIsolated = old; noteAgentAttention("claude", "") })
	noteAgentAttention("codex", "")
	agentHomeIsolated = func() bool { return true }
	state := t.TempDir()
	broken := true
	controller := &claudePanelConnections{
		state: func() (string, error) {
			if broken {
				return "", errors.New("connections require the installed OpenAbstractions")
			}
			return state, nil
		},
		image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil },
		command: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"outcome":"page","connections":[]}`), nil
		},
	}
	stubOverviewMachine(t)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{setupJournal: &journal, claudeConnections: controller}
	stubOfficialComfy(t)
	stubEnrolled(t)
	mux := http.NewServeMux()
	p.claudeConnectionRoutes(mux, "fixture")
	read := func() claudePanelView {
		request := httptest.NewRequest(http.MethodGet, "/connections/claude/state", nil)
		request.Header.Set("X-Panel-Key", "fixture")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		var view claudePanelView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(response.Code, response.Body.String(), err)
		}
		return view
	}
	want := "Connecting Claude needs the installed OpenAbstractions. Open OpenAbstractions from the Start menu, then select Check again."
	if view := read(); view.Outcome != "unavailable" || view.Reason != want || view.ReasonCode != "installed_runtime" {
		t.Fatalf("view = %+v", view)
	}
	if got := readIntegrations(t, p)["claude"]; got.State != "connect" || got.Attention != "Connecting Claude needs the installed OpenAbstractions." {
		t.Fatalf("Catalog row after the failure: %+v", got)
	}
	if got := readIntegrations(t, p)["codex"]; got.Attention != "" {
		t.Fatalf("Codex has no failure: %+v", got)
	}
	agentHomeIsolated = func() bool { return false }
	broken = false
	if view := read(); view.Outcome != "page" || view.Reason != "" {
		t.Fatalf("view after repair = %+v", view)
	}
	if got := readIntegrations(t, p)["claude"]; got.Attention != "" {
		t.Fatalf("a working read clears the Catalog's attention: %+v", got)
	}
}

// Open Claude stays hidden until Claude is connected; the guidance names
// the step instead.
func TestClaudeOpenHiddenWhileNotConnected(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel receiver")
	}
	input, _ := json.Marshal(map[string]string{"script": agentConnectionScriptForTest(t, claudeConnectionsPage, ""), "markup": strings.Split(claudeConnectionsPage, "<script>")[0]})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", claudeOpenFixture)
	command.Stdin = bytes.NewReader(input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
}

const claudeOpenFixture = `
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const input=JSON.parse(fs.readFileSync(0,'utf8')),nodes=new Map(),pending=[];
function element(kind){return {kind,textContent:'',disabled:false,hidden:false,checked:false,children:[],replaceChildren(){this.children=[]},append(...items){this.children.push(...items)},querySelectorAll(){return []}}}
for(const match of input.markup.matchAll(/\bid="([^"]+)"/g))nodes.set(match[1],element());
const el=id=>{assert(nodes.has(id),'missing '+id);return nodes.get(id)};
const context=vm.createContext({el,AbortController,JSON,document:{createElement:element},para(){},call(path,body,signal){return new Promise((resolve,reject)=>pending.push({path,body,signal,resolve,reject}))},addEventListener(){},fetch:()=>Promise.resolve({}),key:'fixture',oaReady(){},fail(id,error){el(id).textContent=String(error)}});
const tick=()=>new Promise(resolve=>setImmediate(resolve));
(async()=>{
 vm.runInContext(input.script,context);
 const reason='Connecting Claude needs the installed OpenAbstractions. Open OpenAbstractions from the Start menu, then select Check again.';
 pending[0].resolve({supported:true,outcome:'unavailable',revision:'1',open_available:true,cli_available:false,connections:[],reason,reason_code:'installed_runtime'});await tick();
 assert.equal(el('agent-guidance').textContent,reason);assert.equal(el('agent-open').hidden,true);assert.equal(el('agent-primary').hidden,true);
 const again=vm.runInContext('refreshAgent()',context);pending[1].resolve({supported:true,outcome:'page',revision:'2',open_available:true,cli_available:false,registration_available:true,connections:[]});await again;
 assert.equal(el('agent-status').textContent,'Not connected');assert.equal(el('agent-open').hidden,true,'Open Claude is hidden while Claude is not connected');
 const saved=vm.runInContext('refreshAgent()',context);pending[2].resolve({supported:true,outcome:'page',revision:'3',open_available:true,cli_available:false,registration_available:true,connections:[{kind:'user',name:'OpenAbstractions',program_key:'p',project_key:'q',registration:'saved',helper_state:'current',mod_state:'current'}]});await saved;
 assert.equal(el('agent-open').hidden,false,'a connected Claude offers Open Claude');
})().catch(error=>{console.error(error);process.exit(1)});
`
