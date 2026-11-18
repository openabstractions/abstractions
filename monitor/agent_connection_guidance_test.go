package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Agent-ux report 2026-11-22: the Codex page showed "Needs attention", the
// raw code "installed_gateway" and a greyed Connect. The state route now
// carries one plain sentence with the next step, and the page shows it with
// Connect hidden.
func TestCodexUnavailableStateSaysWhatIsMissing(t *testing.T) {
	old := agentHomeIsolated
	t.Cleanup(func() { agentHomeIsolated = old; noteAgentAttention("codex", "") })
	for _, tc := range []struct {
		name     string
		isolated bool
		setup    func(*codexPanelConnections)
		code     string
		want     string
	}{
		{"no codex", false, func(c *codexPanelConnections) {
			c.cli = func(context.Context) (claudePanelCLI, error) { return nil, errors.New("no Codex command") }
		}, "cli_selection", "Codex isn't installed on this computer. Install Codex, then select Check again."},
		{"gateway missing", false, func(c *codexPanelConnections) {
			c.manager = func(context.Context) (claudePanelImage, error) { return nil, os.ErrNotExist }
		}, "installed_gateway", "OpenAbstractions is missing its connection helper. Reinstall OpenAbstractions, then select Check again."},
		{"test home", true, func(c *codexPanelConnections) {
			c.manager = func(context.Context) (claudePanelImage, error) {
				return nil, errors.New("agent integration requires the installed runtime")
			}
		}, "installed_gateway", "Connecting Codex needs the installed OpenAbstractions. Open OpenAbstractions from the Start menu, then select Check again."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentHomeIsolated = func() bool { return tc.isolated }
			controller := codexPanelFixture(t)
			tc.setup(controller)
			panel := &servicePanel{codexConnections: controller}
			mux := http.NewServeMux()
			panel.codexConnectionRoutes(mux, "fixture")
			request := httptest.NewRequest(http.MethodGet, "/connections/codex/state", nil)
			request.Header.Set("X-Panel-Key", "fixture")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			var view codexConnectionView
			if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
				t.Fatal(response.Code, response.Body.String(), err)
			}
			if view.Outcome != "unavailable" || view.Reason != tc.code || view.Guidance != tc.want {
				t.Fatalf("view = %+v", view)
			}
			controller.close()
		})
	}
}

// Every stage code reads as a plain sentence; none shows the code itself.
func TestCodexUnavailableSentencesAreWordsOnly(t *testing.T) {
	old := agentHomeIsolated
	agentHomeIsolated = func() bool { return false }
	t.Cleanup(func() { agentHomeIsolated = old })
	for _, code := range []string{"", "unsupported_platform", "window_closed", "cli_selection", "installed_gateway", "setup_state", "configuration_check", "command_layout", "command_virtualized_profile", "command_start", "unknown"} {
		sentence := codexUnavailableSentence(code, errors.New("inert"))
		if sentence == "" || strings.Contains(sentence, "_") || !strings.HasSuffix(sentence, ".") {
			t.Fatalf("%q reads %q", code, sentence)
		}
	}
}

func TestAgentPageShowsGuidanceAndHidesConnectWhileUnavailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel receiver")
	}
	input, _ := json.Marshal(map[string]string{"script": agentConnectionScriptForTest(t, codexConnectionsPage, ""), "markup": strings.Split(codexConnectionsPage, "<script>")[0]})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", agentGuidanceFixture)
	command.Stdin = bytes.NewReader(input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
}

const agentGuidanceFixture = `
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const input=JSON.parse(fs.readFileSync(0,'utf8')),nodes=new Map(),pending=[];
function element(kind){return {kind,textContent:'',disabled:false,hidden:false,checked:false,children:[],replaceChildren(){this.children=[]},append(...items){this.children.push(...items)},querySelectorAll(){return []}}}
for(const match of input.markup.matchAll(/\bid="([^"]+)"/g))nodes.set(match[1],element());
const el=id=>{assert(nodes.has(id),'missing '+id);return nodes.get(id)};
const context=vm.createContext({el,AbortController,JSON,document:{createElement:element},para(){},call(path,body,signal){return new Promise((resolve,reject)=>pending.push({path,body,signal,resolve,reject}))},addEventListener(){},fetch:()=>Promise.resolve({}),key:'fixture',oaReady(){},fail(id,error){el(id).textContent=String(error)}});
const tick=()=>new Promise(resolve=>setImmediate(resolve));
(async()=>{
 vm.runInContext(input.script,context);
 const sentence="Codex isn't installed on this computer. Install Codex, then select Check again.";
 pending[0].resolve({supported:true,outcome:'unavailable',available:false,reason:'installed_gateway',guidance:sentence});await tick();
 assert.equal(el('agent-status').textContent,'Needs attention');
 assert.equal(el('agent-guidance').textContent,sentence);
 assert(!el('agent-guidance').textContent.includes('installed_gateway'));
 assert.equal(el('agent-primary').hidden,true,'Connect is hidden while setup cannot run');
 assert.equal(el('agent-refresh').hidden,false);
 assert.equal(el('agent-receiving').hidden,true);
 // Check again rereads the state.
 const again=el('agent-refresh').onclick();assert.equal(pending[1].path,'/connections/codex/state');
 pending[1].resolve({supported:true,outcome:'page',available:true,owned:false,registration:'absent'});await again;
 assert.equal(el('agent-status').textContent,'Not connected');assert.equal(el('agent-primary').hidden,false);assert.equal(el('agent-primary').disabled,false);
})().catch(error=>{console.error(error);process.exit(1)});
`
