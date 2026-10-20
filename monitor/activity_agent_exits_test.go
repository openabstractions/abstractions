package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type activityExitImage struct {
	changed        bool
	checks, closed int
}

func TestActivityAgentExitScriptSelectionAndEmptyProgress(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	script, _ := json.Marshal(agentExitActivityScript)
	fixture := `const assert=require('node:assert/strict'),vm=require('node:vm');
const elements=new Map(),pending=[];function element(){return {value:'',hidden:false,children:[],textContent:'',append(x){this.children.push(x)},replaceChildren(){this.children=[]},querySelector(){return this}}}function el(id){if(!elements.has(id))elements.set(id,element());return elements.get(id)}
const context={el,document:{hidden:false,createElement:element},URLSearchParams,Date,call:url=>new Promise(resolve=>pending.push({url,resolve})),oaSetStatus:(id,state,text)=>{el(id).textContent=text;el(id).state=state},fail:(id,e)=>{throw e},oaLocalTime:x=>x};vm.createContext(context);vm.runInContext(SCRIPT,context);
(async()=>{await vm.runInContext('readAgentExits()',context);assert.equal(pending.length,0);el('agent-exit-agent').value='claude';const first=vm.runInContext('readAgentExits()',context);el('agent-exit-agent').value='codex';const second=vm.runInContext('readAgentExits()',context);pending[1].resolve({outcome:'page',events:[],next:'sealed',complete:false});await second;assert.equal(el('agent-exit-more').hidden,false);pending[0].resolve({outcome:'page',events:[{at:1,result:'stale'}],complete:true});await first;assert.equal(el('agent-exit-table').children.length,0);const next=vm.runInContext('readAgentExits(true)',context);assert.match(pending[2].url,/agent=codex&cursor=sealed/);pending[2].resolve({outcome:'not_permitted'});await next;assert.match(el('agent-exit-status').textContent,/Permission/);assert.equal(el('agent-exit-more').hidden,true)})().catch(e=>{console.error(e);process.exitCode=1});`
	fixture = strings.Replace(fixture, "SCRIPT", string(script), 1)
	if out, err := exec.Command(node, "-e", fixture).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
}

func (i *activityExitImage) Program() string { return "kept-helper" }
func (i *activityExitImage) Check(context.Context) error {
	i.checks++
	if i.changed {
		return errors.New("enrollment changed")
	}
	return nil
}
func (i *activityExitImage) Close() error { i.closed++; return nil }

type activityExitReader struct {
	t      *testing.T
	page   logclient.ApplicationLogPage
	image  *activityExitImage
	change bool
	calls  int
}

func (f *activityExitReader) ReadContext(_ context.Context, target logclient.ApplicationLogTarget, cursor string, records, bytes int64) (logclient.ApplicationLogPage, error) {
	f.calls++
	if target.Selection != wire.ApplicationLogSelectionOneApp || target.Program != "kept-helper" || records != 64 || bytes != 65536 || cursor != strings.Repeat("c", 1024) {
		f.t.Fatal(target, cursor, records, bytes)
	}
	if f.change {
		f.image.changed = true
	}
	return f.page, nil
}
func TestActivityAgentExitsScopedKeptSelectionAndRefusals(t *testing.T) {
	oldHold, oldReader := activityAgentHold, activityAgentReader
	t.Cleanup(func() { activityAgentHold, activityAgentReader = oldHold, oldReader })
	for _, mode := range []string{"page", "not_permitted", "changed", "not_kept", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			image := &activityExitImage{}
			reader := &activityExitReader{t: t, image: image, page: logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, AtEnd: true}, change: mode == "changed"}
			if mode == "not_permitted" {
				reader.page.Outcome = wire.HistoryPageOutcomeNotPermitted
			}
			held := 0
			activityAgentHold = func(_ context.Context, party setup.AgentParty) (activityAgentImage, error) {
				held++
				if party != setup.AgentClaude {
					t.Fatal(party)
				}
				if mode == "not_kept" {
					return nil, errors.New("not kept")
				}
				return image, nil
			}
			activityAgentReader = func(context.Context) (activityAgentHistory, error) { return reader, nil }
			agent := "claude"
			if mode == "invalid" {
				agent = "all_apps"
			}
			response := httptest.NewRecorder()
			(&servicePanel{}).activityAgentExits(response, httptest.NewRequest("GET", "/activity/agent-exits?agent="+agent+"&cursor="+strings.Repeat("c", 1024), nil))
			if mode == "invalid" {
				if held != 0 || reader.calls != 0 || response.Code != 400 {
					t.Fatal(held, reader.calls, response.Code)
				}
				return
			}
			if mode == "not_kept" {
				if reader.calls != 0 {
					t.Fatal("read unkept image")
				}
				return
			}
			if image.closed != 1 {
				t.Fatal("image leaked")
			}
			if mode == "changed" {
				if response.Code == 200 {
					t.Fatal("changed enrollment returned results")
				}
				return
			}
			var page activityPage
			if json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Outcome != mode || reader.calls != 1 || image.checks != 2 {
				t.Fatal(response.Body.String(), reader.calls, image.checks)
			}
		})
	}
}

func TestActivityAgentExitPrivacyProjection(t *testing.T) {
	metadata := `{"schema":"oa.agent.exit@1","provenance":"program_reported","vendor_status":"success","exit_code":0,"duration_ms":2,"stdout_bytes":3,"stderr_bytes":4,"usage":{"input_tokens":5,"output_tokens":6,"secret":7},"estimated_cost_usd":"0.1","truncated":true,"result":"private answer","prompt":"private prompt","tool":"private tool"}`
	record := wire.Record{Time: "2026-10-29T00:00:00Z", Msg: "private message", Attrs: map[string]string{"oa_event": "agent.start.exit", "exit_metadata": metadata, "secret": "private auth"}}
	event, ok := activityAgentExit(record)
	if !ok {
		t.Fatal("valid capture omitted")
	}
	shown := event.What + event.Result
	for _, secret := range []string{"private", "secret", "prompt", "tool", "auth"} {
		if strings.Contains(shown, secret) {
			t.Fatal("private value displayed", shown)
		}
	}
	for _, want := range []string{"Program reported success", "exit 0", "input tokens 5", "output tokens 6", "estimated USD 0.100000", "output truncated"} {
		if !strings.Contains(shown, want) {
			t.Fatal(want, shown)
		}
	}
	for _, bad := range []string{strings.Replace(metadata, "success", "private status", 1), strings.Replace(metadata, "oa.agent.exit@1", "unknown", 1), strings.Replace(metadata, `"duration_ms":2`, `"duration_ms":-1`, 1), strings.Replace(metadata, `"duration_ms":2`, `"duration_ms":9007199254740993`, 1)} {
		record.Attrs["exit_metadata"] = bad
		if _, ok := activityAgentExit(record); ok {
			t.Fatal("invalid metadata displayed")
		}
	}
}
