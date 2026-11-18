package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Agent-ux 2026-11-22, Activity: a failed Connect leaves one Activity line
// naming the app and the reason, and the Catalog row reads Needs attention.
func TestFailedConnectAppearsInActivity(t *testing.T) {
	t.Cleanup(func() { noteAgentAttention("codex", "") })
	old := agentHomeIsolated
	agentHomeIsolated = func() bool { return false }
	t.Cleanup(func() { agentHomeIsolated = old })
	controller := codexPanelFixture(t)
	controller.prepare = func(context.Context) (claudePanelImage, error) { return nil, errors.New("inert prepare failure") }
	panel := &servicePanel{codexConnections: controller, appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "application-activity.json")}}
	mux := http.NewServeMux()
	panel.codexConnectionRoutes(mux, "fixture")
	request := httptest.NewRequest(http.MethodPost, "/connections/codex/add", strings.NewReader(`{"revision":"1"}`))
	request.Header.Set("X-Panel-Key", "fixture")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		t.Fatalf("the failing Connect answered %d %s", response.Code, response.Body)
	}
	lines := panel.appActivity.lines()
	if len(lines) != 1 || lines[0].App != "Codex" || lines[0].What != "Connect stopped. OpenAbstractions couldn't connect Codex." || lines[0].Outcome != "Not connected" {
		t.Fatalf("Activity lines: %+v", lines)
	}
	if got := agentAttentionFor("codex"); got == "" {
		t.Fatal("the Catalog row does not learn of the failure")
	}
	recorder := httptest.NewRecorder()
	panel.activityAppRequests(recorder, httptest.NewRequest(http.MethodGet, "/activity/app-requests", nil))
	if !strings.Contains(recorder.Body.String(), `"what":"Connect stopped. OpenAbstractions couldn't connect Codex."`) {
		t.Fatalf("the timeline read lacks the line: %s", recorder.Body)
	}
	controller.close()
}

// The Messages and Background agent runs groups speak plainly; the
// timeline's Outcome column keeps Undo in view.
func TestActivityGroupsSpeakPlainly(t *testing.T) {
	for _, jargon := range []string{"Headless", "Retained program-reported", "Inbox readiness", "retained results"} {
		if strings.Contains(agentExitActivitySection+agentExitActivityScript, jargon) {
			t.Errorf("visible jargon %q", jargon)
		}
	}
	if !strings.Contains(agentExitActivitySection, "Background agent runs") {
		t.Error("the background runs group lost its name")
	}
	css, err := os.ReadFile(filepath.Join("static", "panel.css"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "#timeline-table td:nth-child(3) { white-space: normal;") || !strings.Contains(string(css), "#timeline-table { width: 100%; }") {
		t.Error("the timeline's What happened column does not wrap, so Undo can be clipped")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	script, _ := json.Marshal(messageActivityScript)
	fixture := `const assert=require('node:assert/strict'),vm=require('node:vm');
function element(){return {hidden:false,textContent:'',children:[],append(){},replaceChildren(){}}}
const elements=new Map(),el=id=>{if(!elements.has(id))elements.set(id,element());return elements.get(id)};
const context={el,document:{hidden:false,addEventListener(){}},window:{addEventListener(){}},oaSetStatus(){},oaShowError(){},call(){return new Promise(()=>{})}};vm.createContext(context);vm.runInContext(SCRIPT,context);
const say=o=>vm.runInContext('messageHistorySentence('+JSON.stringify(o)+')',context);
assert.equal(say('not_permitted'),'Your OpenAbstractions permissions don’t let this Panel read message history. Allow it for OpenAbstractions Panel on the Permissions page.');
assert(!/not permitted/.test(say('not_permitted')));
assert.equal(say('unavailable'),'Message history can’t be read right now. It reads again shortly.');`
	fixture = strings.Replace(fixture, "SCRIPT", string(script), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, node, "-e", fixture).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
}
