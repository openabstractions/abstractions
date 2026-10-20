package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	facade "github.com/openabstractions/abstraction-facade/go"
	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type activityAgentImage interface {
	Program() string
	Check(context.Context) error
	Close() error
}
type activityAgentHistory interface {
	ReadContext(context.Context, logclient.ApplicationLogTarget, string, int64, int64) (logclient.ApplicationLogPage, error)
}

var activityAgentHold = func(ctx context.Context, party setup.AgentParty) (activityAgentImage, error) {
	return setup.HoldEnrolledAgentImage(ctx, panelMachine(), party)
}
var activityAgentReader = func(ctx context.Context) (activityAgentHistory, error) {
	return panelMachine().ResolveLogApplicationReader(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// Read only the selected, currently kept helper through the scoped @2 owner.
// Its persisted native emitter stamp supplies attribution; record text does not.
func (p *servicePanel) activityAgentExits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	// reader@2 seals native target and caller evidence into its continuation.
	if len(cursor) > 128<<10 || !utf8.ValidString(cursor) {
		http.Error(w, "Invalid history cursor", http.StatusBadRequest)
		return
	}
	party := setup.AgentParty(r.URL.Query().Get("agent"))
	if party != setup.AgentClaude && party != setup.AgentCodex && party != setup.AgentOpenCode {
		http.Error(w, "Choose an agent connection", http.StatusBadRequest)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	image, err := activityAgentHold(ctx, party)
	if err != nil {
		panelJSON(w, activityPage{Outcome: "unavailable", Events: []activityEvent{}})
		return
	}
	defer image.Close()
	if err := image.Check(ctx); err != nil {
		panelError(w, err)
		return
	}
	reader, err := activityAgentReader(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := reader.ReadContext(ctx, logclient.ApplicationLogTarget{Selection: wire.ApplicationLogSelectionOneApp, Program: image.Program()}, cursor, 64, 65536)
	if err != nil {
		panelError(w, err)
		return
	}
	if err := image.Check(ctx); err != nil {
		panelError(w, err)
		return
	}
	out := activityPage{Outcome: page.Outcome.String(), Events: []activityEvent{}, Next: page.Next, Complete: page.AtEnd}
	if page.Outcome == wire.HistoryPageOutcomePage {
		for _, record := range page.Records {
			if event, ok := activityAgentExit(record); ok {
				out.Events = append(out.Events, event)
			}
		}
	}
	panelJSON(w, out)
}

// This display projection accepts only the capture schema's bounded numeric
// metadata and closed status vocabulary. Raw result, prompt and tool text never
// enter Activity, including unknown fields a writer adds to its record.
func activityAgentExit(record wire.Record) (activityEvent, bool) {
	if record.Attrs["oa_event"] != "agent.start.exit" {
		return activityEvent{}, false
	}
	raw := record.Attrs["exit_metadata"]
	if len(raw) > 4096 {
		return activityEvent{}, false
	}
	var m struct {
		Schema           string            `json:"schema"`
		Provenance       string            `json:"provenance"`
		VendorStatus     string            `json:"vendor_status"`
		ExitCode         *int              `json:"exit_code"`
		DurationMS       uint64            `json:"duration_ms"`
		StdoutBytes      uint64            `json:"stdout_bytes"`
		StderrBytes      uint64            `json:"stderr_bytes"`
		Truncated        bool              `json:"truncated"`
		Usage            map[string]uint64 `json:"usage"`
		EstimatedCostUSD string            `json:"estimated_cost_usd"`
	}
	if json.Unmarshal([]byte(raw), &m) != nil || m.Schema != "oa.agent.exit@1" || m.Provenance != "program_reported" {
		return activityEvent{}, false
	}
	status := map[string]string{"success": "success", "error": "error", "error_during_execution": "execution error", "error_max_budget_usd": "cost limit", "error_max_turns": "turn limit", "error_max_structured_output_retries": "output retry limit", "unrecognized": "unrecognized status"}[m.VendorStatus]
	if status == "" || m.DurationMS > 1<<53 || m.StdoutBytes > 1<<53 || m.StderrBytes > 1<<53 {
		return activityEvent{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, record.Time)
	if err != nil {
		return activityEvent{}, false
	}
	parts := []string{"Program reported " + status}
	if m.ExitCode != nil {
		parts = append(parts, fmt.Sprintf("exit %d", *m.ExitCode))
	}
	parts = append(parts, fmt.Sprintf("%d ms; output %d bytes; errors %d bytes", m.DurationMS, m.StdoutBytes, m.StderrBytes))
	for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		if n, ok := m.Usage[key]; ok && n <= 1<<53 {
			parts = append(parts, strings.ReplaceAll(key, "_", " ")+" "+strconv.FormatUint(n, 10))
		}
	}
	if cost, err := strconv.ParseFloat(m.EstimatedCostUSD, 64); err == nil && cost >= 0 && cost <= 1000000 {
		parts = append(parts, fmt.Sprintf("estimated USD %.6f", cost))
	}
	if m.Truncated {
		parts = append(parts, "output truncated")
	}
	return activityEvent{At: at.UnixMilli(), What: "Headless agent exited", Result: strings.Join(parts, "; ")}, true
}

var agentExitActivitySection = oaGroup("agent-exits", "Headless agent status", "Retained program-reported status and usage. Inbox readiness and message delivery are recorded separately.",
	`<label>Connection <select id="agent-exit-agent"><option value="">Choose a connection</option><option value="claude">Claude</option><option value="codex">Codex</option></select></label>`,
	oaStatus("agent-exit-status"), oaTable("agent-exit-table", "Time", "Status"),
	`<button id="agent-exit-more" type="button" hidden>Show more</button>`)

const agentExitActivityScript = `
let agentExitCursor='',agentExitGeneration=0,agentExitReadAt=0;
function refreshAgentExits(){if(!agentExitReadAt||el('agent-exit-agent').value&&Date.now()-agentExitReadAt>30000){agentExitReadAt=Date.now();return readAgentExits()}}
async function readAgentExits(more=false){const agent=el('agent-exit-agent').value,revision=++agentExitGeneration,body=el('agent-exit-table').querySelector('tbody');if(!more){if(body)body.replaceChildren();agentExitCursor=''}el('agent-exit-more').hidden=true;if(!agent){oaSetStatus('agent-exit-status','ok','Choose a connection to read its retained results.');return}try{const p=await call('/activity/agent-exits?'+new URLSearchParams({agent,cursor:agentExitCursor}));if(revision!==agentExitGeneration||document.hidden)return;if(p.outcome!=='page'){oaSetStatus('agent-exit-status','ok',p.outcome==='not_permitted'?'Permission to read this connection’s results is unavailable.':p.outcome==='unavailable'?'This kept connection or its history is unavailable.':'History outcome: '+p.outcome);return}for(const event of p.events||[]){const row=document.createElement('tr');for(const text of [oaLocalTime(new Date(event.at).toISOString()),event.result]){const cell=document.createElement('td');cell.textContent=text;row.append(cell)}body.append(row)}agentExitCursor=p.next||'';el('agent-exit-more').hidden=p.complete||!agentExitCursor;oaSetStatus('agent-exit-status','ok',p.complete?'At the current end of retained results.':'More history remains.')}catch(e){if(revision===agentExitGeneration)fail('agent-exit-status',e)}}
el('agent-exit-agent').onchange=()=>{agentExitReadAt=Date.now();return readAgentExits()};el('agent-exit-more').onclick=()=>readAgentExits(true);
`
