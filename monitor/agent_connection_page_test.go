package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAgentConnectionUpdateAndDisplayOrder(t *testing.T) {
	want := []agentHelperParty{{"codex", "Codex"}, {"claude", "Claude"}, {"opencode", "OpenCode"}}
	if len(agentHelperParties) != len(want) {
		t.Fatalf("update roster = %+v", agentHelperParties)
	}
	for i, party := range want {
		if agentHelperParties[i] != party {
			t.Fatalf("update roster at %d = %+v, want %+v", i, agentHelperParties[i], party)
		}
	}
	for _, page := range []struct {
		markup, heading, scope string
	}{
		{claudeConnectionsPage, "Connect Claude to OpenAbstractions.", "All your Claude Code projects, including local Code sessions in Claude Desktop."},
		{codexConnectionsPage, "Connect Codex to OpenAbstractions.", "This account across desktop, CLI and IDE."},
	} {
		if !strings.Contains(page.markup, page.heading) || !strings.Contains(page.markup, page.scope) {
			t.Errorf("connection page lost %q or %q", page.heading, page.scope)
		}
	}
}

// This fixture renders the real shell and page markup without receiver scripts.
func TestAgentConnectionRenderFixtures(t *testing.T) {
	out := os.Getenv("OA_AGENT_CONNECTION_FIXTURE_OUTPUT")
	if out == "" {
		t.Skip("inert screenshot output is opt-in")
	}
	for host, page := range map[string]string{"claude": claudeConnectionsPage, "codex": codexConnectionsPage, "opencode": installedConnection().page()} {
		markup := strings.Split(page, "<script>")[0]
		markup = strings.ReplaceAll(markup, `data-state="loading"`, `data-state="idle"`)
		markup = strings.ReplaceAll(markup, "Loading…", "")
		r := httptest.NewRequest("GET", "http://127.0.0.1/connections/"+host+"?platform=windows&shell=native", nil)
		doc := panelPage(r, "fixture", host+"-connections", "Agent connection", markup)
		if err := os.MkdirAll(filepath.Join(out, "connections"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "connections", host), []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
	}
	markup := strings.Split(servicePage, "<script>")[0]
	markup = strings.ReplaceAll(markup, `data-state="loading"`, `data-state="idle"`)
	doc := panelPage(httptest.NewRequest("GET", "http://127.0.0.1/?platform=windows&shell=native", nil), "fixture", "status", "Apps", markup)
	if err := os.WriteFile(filepath.Join(out, "index.html"), []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConnectionPagesShareLayoutAndCopy(t *testing.T) {
	var first []string
	for _, page := range []string{claudeConnectionsPage, codexConnectionsPage, installedConnection().page()} {
		markup := strings.Split(page, "<script>")[0]
		var ids []string
		for _, match := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(markup, -1) {
			ids = append(ids, match[1])
		}
		if first == nil {
			first = ids
		} else if strings.Join(ids, ",") != strings.Join(first, ",") {
			t.Fatal("agent page controls diverged", ids)
		}
		last := -1
		for _, section := range []string{"agent-connection", "agent-receiving", "agent-answering", "agent-details"} {
			i := strings.Index(markup, `id="`+section+`"`)
			if i <= last {
				t.Fatal("shared section order", section)
			}
			last = i
		}
		for _, caption := range regexp.MustCompile(`<span class="oa-row-caption">([^<]+)</span>`).FindAllStringSubmatch(markup, -1) {
			if len(caption[1]) > 160 {
				t.Fatal("long caption", caption[1])
			}
		}
		if !strings.Contains(markup, "Questions an assistant may answer") {
			t.Fatal("assistant wording is missing")
		}
		for _, jargon := range []string{"routine questions", "MCP", "enrollment", "hook", "oa-codex-session-start"} {
			if strings.Contains(markup, jargon) {
				t.Fatal("visible jargon", jargon)
			}
		}
		if strings.Contains(markup, "Live message reception is not observed by this page.") {
			t.Fatal("readable saved/live distinction is missing")
		}
	}
}

func testAgentConnectionController(t *testing.T, host, page string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel receiver")
	}
	input, _ := json.Marshal(map[string]string{"script": agentConnectionScriptForTest(t, page, "Agent"), "markup": strings.Split(page, "<script>")[0], "host": host})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", agentConnectionFixture)
	command.Stdin = bytes.NewReader(input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("shared controller: %v %s", err, output)
	}
}

func TestOpenCodeConnectionUsesNativeReadback(t *testing.T) {
	testAgentConnectionController(t, "opencode", installedConnection().page())
}

func agentConnectionScriptForTest(t *testing.T, page, name string) string {
	t.Helper()
	_, script, ok := strings.Cut(page, "const agentHost=")
	if !ok {
		t.Fatal("connection page has no controller configuration")
	}
	script = "const agentHost=" + strings.TrimSuffix(script, "</script>")
	if name != "" {
		title := regexp.MustCompile(`,agentName="[^"]*"`)
		quoted, _ := json.Marshal(name)
		script = title.ReplaceAllStringFunc(script, func(string) string { return ",agentName=" + string(quoted) })
	}
	return script
}

// The Update button ends every click with one sentence for its outcome:
// updated now, waiting for the app to close, or failed with a short reason.
func TestAgentUpdateButtonWritesOneSentencePerOutcome(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel receiver")
	}
	for host, page := range map[string]string{"claude": claudeConnectionsPage, "codex": codexConnectionsPage} {
		input, _ := json.Marshal(map[string]string{"script": agentConnectionScriptForTest(t, page, "Claude"), "markup": strings.Split(page, "<script>")[0], "host": host})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		command := exec.CommandContext(ctx, node, "-e", agentUpdateSentenceFixture)
		command.Stdin = bytes.NewReader(input)
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s update sentences: %v %s", host, err, output)
		}
	}
}

const agentUpdateSentenceFixture = `
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const input=JSON.parse(fs.readFileSync(0,'utf8')),nodes=new Map(),pending=[];
function element(kind){return {kind,textContent:'',disabled:false,hidden:false,checked:false,children:[],replaceChildren(){this.children=[]},append(...items){this.children.push(...items)},querySelectorAll(){return []}}}
for(const match of input.markup.matchAll(/\bid="([^"]+)"/g))nodes.set(match[1],element());
const el=id=>{assert(nodes.has(id),'missing '+id);return nodes.get(id)};
const context=vm.createContext({el,AbortController,JSON,document:{createElement:element},para(){},call(path,body,signal){return new Promise((resolve,reject)=>pending.push({path,body,signal,resolve,reject}))},addEventListener(){},fetch:()=>Promise.resolve({}),key:'fixture',oaReady(){},fail(id,error){el(id).textContent=String(error)}});
const claude=input.host==='claude',path='/connections/'+input.host;
function page(revision){return claude?{supported:true,outcome:'page',revision,cli_available:true,registration_available:true,background_available:true,connections:[{kind:'user',name:'OpenAbstractions',program_key:'held-program',project_key:'held-project',registration:'saved',helper_state:'update',mod_state:'current'}],background:{state:'owned'},background_asks:{state:'disabled'}}:{supported:true,outcome:'page',revision,owned:true,available:true,registration:'saved',helper_state:'update'}}
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const sentences=text=>text.split(/(?<=[.!?])\s+/).filter(Boolean);
(async()=>{
 vm.runInContext(input.script,context);
 pending[0].resolve(page('start'));await tick();
 assert.equal(el('agent-primary').textContent,'Update');
 const cases=[
  [{outcome:'updated'},'Updated. Restart Claude to use the new connector.'],
  [{outcome:'update_pending',restart_required:true},'Update waits until Claude is closed.'],
  [{outcome:'current'},'Claude already has the current connector.'],
  [{outcome:'not_now'},'Update postponed; the current connector stays in use.'],
  [{outcome:'surprise'},'Update result unknown; check again.'],
  [Error('Claude setup is in progress'),'Update failed: another change is in progress.'],
  [Error('previous image retained at C:\\x; staged image C:\\y: active connection image requires restart'),'Update waits until Claude is closed.'],
  [Error('integration update requires inspection; previous image retained at C:\\x'),'Update failed: see Details.'],
 ];
 for(const [reply,expected] of cases){
  el('agent-guidance').textContent='';
  const index=pending.length,click=el('agent-primary').onclick();
  assert.equal(pending[index].path,path+'/update');
  if(reply instanceof Error)pending[index].reject(reply);else pending[index].resolve(reply);
  await tick();pending[index+1].resolve(page('after-'+index));await click;
  const shown=el('agent-guidance').textContent;
  assert.equal(shown,expected,JSON.stringify(reply&&reply.message||reply));
  // The updated sentence is the owner's two-part wording; every other case is one sentence.
  assert.equal(sentences(shown).length,expected.startsWith('Updated.')?2:1,shown);
  assert.equal(pending.length,index+2,'one request and one readback per click');
 }
})().catch(error=>{console.error(error);process.exit(1)});
`

const agentConnectionFixture = `
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const input=JSON.parse(fs.readFileSync(0,'utf8')),nodes=new Map(),events={},pending=[];
function element(kind){return {kind,textContent:'',disabled:false,hidden:false,checked:false,children:[],replaceChildren(){this.children=[]},append(...items){this.children.push(...items)},querySelectorAll(){return this.children.flatMap(child=>[...(child.kind==='button'?[child]:[]),...child.querySelectorAll()])}}}
for(const match of input.markup.matchAll(/\bid="([^"]+)"/g)){assert(!nodes.has(match[1]));nodes.set(match[1],element())}
const el=id=>{assert(nodes.has(id),'missing '+id);return nodes.get(id)};
const context=vm.createContext({el,AbortController,JSON,document:{createElement:element},para(parent,text){const item=element();item.textContent=text;parent.append(item)},call(path,body,signal){return new Promise((resolve,reject)=>pending.push({path,body,signal,resolve,reject}))},addEventListener(type,fn){events[type]=fn},fetch:()=>Promise.resolve({}),key:'fixture',oaReady(){},fail(id,error){el(id).textContent=String(error)}});
const host=input.host,path='/connections/'+host,claude=host==='claude';
function page(revision,helper='current'){return claude?{supported:true,outcome:'page',revision,cli_available:true,registration_available:true,background_available:true,connections:[{kind:'user',name:'OpenAbstractions',program_key:'held-program',project_key:'held-project',registration:'saved',helper_state:helper,mod_state:'current'}],background:{state:'owned'},background_asks:{state:'disabled'}}:{supported:true,outcome:'page',revision,owned:true,available:true,registration:'saved',helper_state:helper}}
const tick=()=>new Promise(resolve=>setImmediate(resolve));
(async()=>{
 vm.runInContext(input.script,context);
 if(host==='opencode'){
  assert.equal(pending[0].path,path+'/state');pending[0].resolve({...page('start'),owned:false,registration:'absent',helper_enrollment:'none',host_registration:'absent'});await tick();
  assert.equal(el('agent-status').textContent,'Not connected');assert.equal(el('agent-primary').disabled,false);
  const change=el('agent-primary').onclick();assert.equal(pending[1].path,path+'/connect');assert.equal(pending[1].body.revision,'start');pending[1].resolve({...page('pending'),outcome:'connect_pending',owned:false,registration:'absent'});await change;
  assert.equal(el('agent-status').textContent,'Needs attention');assert(el('agent-guidance').textContent.includes('pending'));assert.equal(pending.length,2);
  const refresh=vm.runInContext('refreshAgent()',context);pending[2].resolve(page('native'));await refresh;
  assert.equal(el('agent-status').textContent,'Connected');assert.equal(el('agent-primary').hidden,true);
  assert.equal(el('agent-receiving').hidden,false);assert.equal(el('agent-answering').hidden,false);assert.equal(el('agent-saved').children[0].children[1].textContent,'Saved');
  for(const id of ['receive','observe'])assert.equal(el('agent-'+id+'-control').hidden,true);
  assert.equal(el('agent-observe-row').hidden,true);assert.equal(nodes.has('agent-assistant'),false);
  const native={supported:true,outcome:'page',available:true,owned:false,registration:'absent',helper_enrollment:'enrolled',host_registration:'registered'};
  for(const state of ['update','update_available']){
   context.native={...native,revision:'old-'+state,helper_state:state};vm.runInContext('drawAgent(native)',context);assert.equal(el('agent-status').textContent,'Update ready');assert.equal(el('agent-primary').textContent,'Update');assert.equal(el('agent-primary').disabled,false);
   const index=pending.length,update=el('agent-primary').onclick();assert.equal(pending[index].path,path+'/connect');assert.equal(pending[index].body.revision,'old-'+state);pending[index].resolve({...native,outcome:'conflict',helper_state:'current'});await update;assert.equal(el('agent-status').textContent,'Needs attention');assert(el('agent-guidance').textContent.includes('check again'));
  }
  context.native={...native,helper_state:'unknown'};vm.runInContext('drawAgent(native)',context);assert.equal(el('agent-status').textContent,'Needs attention');assert.equal(el('agent-primary').disabled,true);
  context.native={...native,revision:'reviewed-conflict',helper_state:'update_available',host_registration:'conflict'};vm.runInContext('drawAgent(native)',context);assert.equal(el('agent-primary').disabled,false);assert.equal(el('agent-primary').textContent,'Connect');assert.equal(el('agent-guidance').textContent,"Agent's saved entry changed. Connect replaces it.");
  const replacement=pending.length,replace=el('agent-primary').onclick();assert.equal(pending[replacement].path,path+'/connect');assert.equal(pending[replacement].body.revision,'reviewed-conflict');pending[replacement].resolve({...native,outcome:'unknown'});await replace;assert.equal(el('agent-guidance').textContent,'The change outcome is unknown. Check again.');
  const index=pending.length,failure=vm.runInContext('refreshAgent()',context);pending[index].resolve({supported:false,outcome:'unavailable'});await failure;assert.equal(el('agent-status').textContent,'Needs attention');assert.equal(el('agent-primary').disabled,true);
  // Setup unavailable: the guidance says it once and the rows that need setup are hidden (R19b).
  assert.equal(el('agent-guidance').textContent,'Connection setup is unavailable.');assert.equal(el('agent-receiving').hidden,true);assert.equal(el('agent-answering').hidden,true);events.pagehide();return
 }
 assert.equal(pending[0].path,path+'/state');
 const later=vm.runInContext('refreshAgent()',context);assert(pending[0].signal.aborted);pending[1].resolve(page('new'));await later;pending[0].resolve(page('old','update'));await tick();
 assert.equal(el('agent-primary').textContent,'Disconnect');assert.equal(el('agent-status').textContent,'Connected');assert.equal(nodes.has('agent-assistant'),false);assert.equal(el('agent-guidance').textContent,'');
 if(claude){assert.equal(el('agent-receive').checked,true);assert.equal(el('agent-receive').disabled,false);el('agent-observe').checked=true;const observe=el('agent-observe').onchange();assert.equal(pending[2].path,path+'/background-asks');assert.equal(pending[2].body.action,'enable');assert.equal(pending[2].body.revision,'new');pending[2].resolve({outcome:'saved'});await tick();pending[3].resolve(page('observed'));await observe;}
 if(claude){const index=pending.length;el('agent-receive').checked=false;const failure=el('agent-receive').onchange();assert.equal(pending[index].body.action,'disable');pending[index].reject(Error('inert failure'));await failure;assert.equal(el('agent-receive').checked,true);assert.equal(el('agent-receive').disabled,false);assert.equal(el('agent-receive-control').hidden,false);assert.equal(el('agent-receive-status').textContent,'Last confirmed: On.');assert.equal(el('agent-receiving-guidance').textContent,'The change could not be confirmed. Check again.');assert(!el('agent-receive-status').textContent.includes('Reopen'));assert.equal(el('agent-answering-guidance').textContent,'Reopen Agent after changes.');assert.equal(pending.length,index+1)}else{assert.equal(el('agent-receive-control').hidden,true);assert.equal(el('agent-observe-row').hidden,true)}
 const offset=pending.length,update=vm.runInContext('refreshAgent()',context);pending[offset].resolve(page('update','update'));await update;assert.equal(el('agent-primary').textContent,'Update');
 const action=el('agent-primary').onclick();assert.equal(pending[offset+1].path,path+'/update');assert.equal(pending[offset+1].body.revision,'update');if(claude)assert.equal(pending[offset+1].body.program_key,'held-program');pending[offset+1].resolve({outcome:'permission_pending'});await tick();pending[offset+2].resolve(page('after'));await action;assert.equal(el('agent-status').textContent,'Needs attention');
 for(const [outcome,notice] of [['permission_error','Permission review failed'],['cleanup_pending','Earlier permissions need cleanup'],['declined','Your permissions were not changed']]){const index=pending.length,change=vm.runInContext("agentChange('update')",context);assert.equal(el('agent-open').disabled,true);for(const control of el('agent-saved').querySelectorAll('button'))assert.equal(control.disabled,true);pending[index].resolve({outcome,permission_reason:'retained-fixture'});await tick();pending[index+1].resolve(page(outcome));await change;assert(el('agent-guidance').textContent.includes(notice));assert(el('agent-technical').textContent.includes('retained-fixture'));if(outcome!=='declined')assert.equal(el('agent-status').textContent,'Needs attention')}
 const refreshed=vm.runInContext('refreshAgent()',context);pending[pending.length-1].resolve(page('confirmed'));await refreshed;assert.equal(el('agent-guidance').textContent,'Reopen Agent after changes.');
 assert.equal(el('agent-receiving').hidden,false);assert.equal(el('agent-answering').hidden,false);
 // A state that cannot be read hides the rows it would fill and says so once.
 const unread=vm.runInContext('refreshAgent()',context);pending[pending.length-1].reject(Error('inert read failure'));await unread;assert.equal(el('agent-status').textContent,'Needs attention');assert.equal(el('agent-guidance').textContent,'The connection could not be read. Check again.');assert.equal(el('agent-receiving').hidden,true);assert.equal(el('agent-answering').hidden,true);
 const reread=vm.runInContext('refreshAgent()',context);pending[pending.length-1].resolve({...page('unsupported'),supported:false});await reread;assert.equal(el('agent-guidance').textContent,'Connection setup is unavailable.');assert.equal(el('agent-receiving').hidden,true);
 const back=vm.runInContext('refreshAgent()',context);pending[pending.length-1].resolve(page('back'));await back;assert.equal(el('agent-receiving').hidden,false);assert.equal(el('agent-answering').hidden,false);
 const late=vm.runInContext('refreshAgent()',context),last=pending[pending.length-1];events.pagehide();assert(last.signal.aborted);last.resolve(page('closed'));await late;assert.equal(el('agent-primary').disabled,true);
})().catch(error=>{console.error(error);process.exit(1)});
`

// Owner report 2026-11-10: after a Claude Code self-update the saved command
// no longer verifies. The page says why; Connect is on when the running
// Claude Code verifies and off with the reason when it does not.
func TestClaudeConnectionOffersReconnectAfterUnverifiableUpdate(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel receiver")
	}
	input, _ := json.Marshal(map[string]string{"script": agentConnectionScriptForTest(t, claudeConnectionsPage, ""), "markup": strings.Split(claudeConnectionsPage, "<script>")[0]})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", claudeReconnectFixture)
	command.Stdin = bytes.NewReader(input)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reconnect offer: %v %s", err, output)
	}
}

const claudeReconnectFixture = `
const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
const input=JSON.parse(fs.readFileSync(0,'utf8')),nodes=new Map(),pending=[];
function element(kind){return {kind,textContent:'',disabled:false,hidden:false,checked:false,children:[],replaceChildren(){this.children=[]},append(...items){this.children.push(...items)},querySelectorAll(){return []}}}
for(const match of input.markup.matchAll(/\bid="([^"]+)"/g))nodes.set(match[1],element());
const el=id=>{assert(nodes.has(id),'missing '+id);return nodes.get(id)};
const context=vm.createContext({el,AbortController,JSON,document:{createElement:element},para(){},call(path,body,signal){return new Promise((resolve,reject)=>pending.push({path,body,signal,resolve,reject}))},addEventListener(){},fetch:()=>Promise.resolve({}),key:'fixture',oaReady(){},fail(id,error){el(id).textContent=String(error)}});
const reason='Claude Code changed since you connected it. Open Claude Code, then return here.';
function page(revision,reconnect){return {supported:true,outcome:'page',revision,cli_available:false,cli_reason_code:'cli_saved_verification',cli_reason:reason,reconnect_available:reconnect,open_available:true,registration_available:true,background_available:false,connections:[{kind:'user',name:'OpenAbstractions',program_key:'held-program',project_key:'held-project',registration:'saved',helper_state:'current',mod_state:'unknown'}],background:{state:'owned'},background_asks:{state:'disabled'}}}
const tick=()=>new Promise(resolve=>setImmediate(resolve));
(async()=>{
 vm.runInContext(input.script,context);
 pending[0].resolve(page('stale',false));await tick();
 assert.equal(el('agent-status').textContent,'Needs attention');assert.equal(el('agent-guidance').textContent,reason);assert.equal(el('agent-primary').disabled,true);assert.equal(el('agent-open').hidden,false);
 const again=vm.runInContext('refreshAgent()',context);pending[1].resolve(page('running',true));await again;
 assert.equal(el('agent-status').textContent,'Needs attention');assert.equal(el('agent-primary').textContent,'Connect');assert(!el('agent-primary').disabled);
 assert.equal(el('agent-guidance').textContent,'Claude Code changed since you connected it. Connect again to use it.');
 el('agent-primary').onclick();assert.equal(pending[2].path,'/connections/claude/connect');
})().catch(error=>{console.error(error);process.exit(1)});
`
