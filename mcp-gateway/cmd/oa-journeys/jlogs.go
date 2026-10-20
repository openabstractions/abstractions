//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// J-LOGS (experience-matrix "Ask OpenCode about a system error" and scene 5
// "Tell me why this request failed", E6/E7; backlog/A-F19.md): a registered
// app writes a labelled fixture error through OA logging in the isolated
// runtime, and the driver writes an unrelated record of its own. The person
// asks OpenCode "why did that fail?". OpenCode reads the app's OA records
// with the tool its descriptions point to; reading asks the person once, and
// sharing the read records asks separately (A-F19: "A failure asks
// separately to read logs and to share them"). Its answer cites the fixture
// error's source and time and carries nothing from another source.
func init() {
	if raw := os.Getenv(logFixtureEnv); raw != "" {
		os.Exit(writeLogFixture(raw))
	}
	register(70, "J-LOGS", runLogs)
}

const (
	logFixtureEnv = "OA_JOURNEY_LOG_FIXTURE"
	logFixtureApp = "journey-render"
	levelError    = 8
)

type logFixture struct {
	Endpoint       string `json:"endpoint"`
	RuntimeProgram string `json:"runtime_program"`
	Label          string `json:"label"`
}

// writeLogFixture runs in the fixture app's process: one error record through
// the isolated runtime's logging service, nothing else.
func writeLogFixture(raw string) int {
	var f logFixture
	if err := json.Unmarshal([]byte(raw), &f); err != nil || f.Endpoint == "" || !filepath.IsAbs(f.RuntimeProgram) {
		fmt.Fprintln(os.Stderr, "log fixture: endpoint and absolute runtime_program required")
		return 2
	}
	machine, err := isolatedMachine(f.Endpoint, f.RuntimeProgram)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log fixture:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	log, err := machine.ResolveLog(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		fmt.Fprintln(os.Stderr, "log fixture:", err)
		return 1
	}
	if err := log.LogContext(ctx, levelError, "render failed: "+f.Label+" could not open its output folder", map[string]string{"fixture": f.Label, "step": "save"}); err != nil {
		fmt.Fprintln(os.Stderr, "log fixture:", err)
		return 1
	}
	return 0
}

func isolatedMachine(endpoint, runtime string) (*facade.Machine, error) {
	who, err := identity.Self()
	if err != nil {
		return nil, err
	}
	return facade.NewVerified(endpoint, listen.ServerExpectation{Principal: who, Program: identity.CanonicalProgramPath(filepath.Clean(runtime))}), nil
}

func runLogs(ctx context.Context, e *env, o *options, r *result) {
	label := "journey-fixture-" + newID()[:8]
	noise := "journey-noise-" + newID()[:8]
	program, err := registerLogFixtureApp(ctx, e)
	if !r.check("fixture app registered", err == nil, "%v", err) {
		return
	}
	// A record from another source: the driver itself.
	if lg, lerr := e.machine.ResolveLog(ctx, facade.Requirements{Scope: facade.ScopeLocal}); lerr == nil {
		lerr = lg.LogContext(ctx, levelError, "unrelated failure "+noise, map[string]string{"fixture": noise})
		r.check("unrelated record written by another source", lerr == nil, "%v", lerr)
	} else {
		r.check("unrelated record written by another source", false, "%v", lerr)
	}
	before := time.Now().UTC().Add(-2 * time.Second)
	cfg, _ := json.Marshal(logFixture{Endpoint: e.endpoint, RuntimeProgram: e.runtime, Label: label})
	cmd := childproc.Hidden(exec.CommandContext(ctx, program))
	cmd.Env = append(os.Environ(), logFixtureEnv+"="+string(cfg))
	out, err := cmd.CombinedOutput()
	failedAt := time.Now().UTC()
	if !r.check("fixture error written through OA logging", err == nil, "%v %s", err, strings.TrimSpace(string(out))) {
		return
	}

	a, err := e.enrollAgent(ctx, "oa-opencode-integration", "OpenCode")
	if !r.check("agent app kept", err == nil, "%v", err) {
		return
	}
	s, err := e.connect(ctx, a, []string{"OA_OPENCODE_HOST_CHAT_ID=" + newUUID()})
	if !r.check("agent connected", err == nil, "%v", err) {
		return
	}
	defer s.Close()
	defer func() { r.Calls = s.calls }()

	// Part 1 (A-F19, before R21): the agent's own request fails and the
	// person asks "why did that fail?". The agent reads its own request's
	// records; reading and sharing them ask the person separately.
	ownMark := e.answers.mark()
	ownSince := time.Now().UTC().Add(-2 * time.Second).Format(time.RFC3339Nano)
	_, ftext, fout, ferr := s.call(ctx, "oa_inference_complete", map[string]any{"model": "journey-missing-model", "prompt": "hello", "hosting": "local", "max_output": 16})
	r.check("the agent's own request failed", ferr != nil || fout["outcome"] != "completed", "%s", clip(firstNonEmpty(ftext, errString(ferr)), 300))
	tool := s.tools["oa_logs_follow"]
	r.check("a log-reading tool is listed (A-F19: the agent reads its own failed request)", tool != nil, "tools: %s", toolNames(s.tools))
	if tool != nil {
		var own []map[string]any
		var ownLast string
		for step := 0; step < 4 && len(own) == 0; step++ {
			_, text, st, cerr := s.call(ctx, "oa_logs_follow", map[string]any{"about": "my_requests", "since": ownSince})
			if cerr != nil {
				ownLast = cerr.Error()
				break
			}
			ownLast = text
			own = append(own, logRecords(st)...)
			if len(own) == 0 {
				e.answers.waitIDs(ctx, questionIDs(st), 30*time.Second)
			}
		}
		read, share := logQuestions(e.answers.since(ownMark))
		r.check("reading its own request's logs asked the person (A-F19)", read != "", "%s", clip(firstNonEmpty(read, "no read question; last "+ownLast), 300))
		r.check("sharing them asked separately (A-F19)", share != "", "%s", clip(firstNonEmpty(share, "no share question"), 300))
		r.check("the failed request's records were read", len(own) > 0, "%d records; last %s", len(own), clip(ownLast, 300))
	}

	// Part 2 (E7, deferred to 0.9.0 by backlog/JOURNEY-SCOPE.md): another
	// app's logs, read with separate read and share questions. Until it is
	// built, OA must refuse it with a sentence.
	mark := e.answers.mark()
	var records []map[string]any
	var last string
	var lastRes *mcp.CallToolResult
	lastOut := map[string]any{}
	since := before.Format(time.RFC3339Nano)
	if tool != nil {
		args := map[string]any{"app": logFixtureApp, "since": since}
		for step := 0; step < 4; step++ {
			res, text, st, cerr := s.call(ctx, "oa_logs_follow", args)
			if cerr != nil {
				last = cerr.Error()
				break
			}
			last, lastRes, lastOut = text, res, st
			outcome, _ := st["outcome"].(string)
			if outcome == "not_permitted" {
				if !e.answers.waitAnswered(ctx, mark, 30*time.Second) {
					break
				}
				continue
			}
			records = append(records, logRecords(st)...)
			if c, _ := st["cursor"].(string); c != "" && len(records) == 0 {
				args["cursor"] = c
				continue
			}
			break
		}
	}
	if len(records) == 0 && s.tools["oa_api_read"] != nil {
		// The API projection's logging reader is the other route its
		// descriptions point to: one app's retained history from its start.
		_, dtext, _, _ := s.call(ctx, "oa_api_describe", map[string]any{})
		contract, method := apiLogMethod(dtext)
		if contract != "" {
			res, text, st, cerr := s.call(ctx, "oa_api_read", map[string]any{"contract": contract, "method": method,
				"arguments": map[string]any{"target": map[string]any{"selection": "one_app", "program": program}, "cursor": "", "max_records": 64, "max_bytes": 65536}})
			last, lastRes, lastOut = firstNonEmpty(text, errString(cerr)), res, st
			if v, ok := st["value"].(map[string]any); ok {
				lastOut = v
			}
			records = append(records, logRecords(st)...)
		}
	}
	readAsked, shareAsked := logQuestions(e.answers.since(mark))
	var cited map[string]any
	foreign := 0
	for _, rec := range records {
		raw, _ := json.Marshal(rec)
		if strings.Contains(string(raw), label) {
			cited = rec
		}
		if strings.Contains(string(raw), noise) {
			foreign++
		}
	}
	built := cited != nil && readAsked != "" && shareAsked != "" && foreign == 0
	r.deferred("another app's logs read with separate read and share questions (E7)", "0.9.0", built, sentence(lastRes, last, lastOut),
		"read question %q, share question %q, %d records (%d foreign); last %s", clip(readAsked, 120), clip(shareAsked, 120), len(records), foreign, clip(last, 300))
	if cited != nil {
		at, _ := firstOf(cited, "time", "Time").(string)
		t, terr := time.Parse(time.RFC3339Nano, at)
		r.check("the answer cites the fixture error's time", terr == nil && !t.Before(before) && !t.After(failedAt.Add(time.Second)), "time %q (failed between %s and %s)", at, before.Format(time.RFC3339), failedAt.Format(time.RFC3339))
	}
}

// logQuestions names the read and share questions the person saw.
func logQuestions(seen []asked) (read, share string) {
	for _, q := range seen {
		t := strings.ToLower(q.Text + " " + q.About)
		switch {
		case strings.Contains(t, "share") || strings.Contains(t, "log-source"):
			share = q.Text + " -> " + q.Answer
		case strings.Contains(t, "log") || strings.Contains(t, "history.read") || strings.Contains(t, "records about its own requests"):
			read = q.Text + " -> " + q.Answer
		}
	}
	return read, share
}

// registerLogFixtureApp places a copy of this driver as the fixture app and
// registers it under its name.
func registerLogFixtureApp(ctx context.Context, e *env) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(e.work, "apps", logFixtureApp)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	program := filepath.Join(dir, logFixtureApp+".exe")
	if _, err := os.Stat(program); err != nil {
		if err := copyFile(self, program); err != nil {
			return "", err
		}
	}
	directory, err := e.machine.ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return "", fmt.Errorf("resolve applications: %w", err)
	}
	change, err := directory.Register(ctx, fwire.ApplicationDescriptor{Name: logFixtureApp, Program: identity.CanonicalProgramPath(program), Title: "Journey Render"})
	if err != nil {
		return "", err
	}
	if change.Outcome != fwire.ApplicationOutcomeApplied {
		return "", fmt.Errorf("register %s: %s %s", logFixtureApp, change.Outcome, change.Reason)
	}
	return identity.CanonicalProgramPath(program), nil
}

// logRecords finds the log records in a tool result.
func logRecords(st map[string]any) []map[string]any {
	var out []map[string]any
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if _, ok := firstOf(t, "msg", "Msg").(string); ok {
				out = append(out, t)
				return
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(st)
	return out
}

// apiLogMethod picks the projected one-app history read from oa_api_describe.
func apiLogMethod(text string) (string, string) {
	var out struct {
		Methods []struct {
			Contract string `json:"contract"`
			Method   string `json:"method"`
		} `json:"methods"`
	}
	_ = json.Unmarshal([]byte(text), &out)
	for _, m := range out.Methods {
		if strings.HasPrefix(m.Contract, "abstraction.logging/") && m.Method == "Read" && strings.Contains(m.Contract, "@2") {
			return m.Contract, m.Method
		}
	}
	return "", ""
}
