package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// observedCodex is testdata/codex-observed-2026-11-01.json: what Codex did
// toward the helper on 2026-11-01, as structure and timing only.
type observedCodex struct {
	Client struct {
		Initialize struct {
			Params       []string `json:"params"`
			Capabilities []string `json:"capabilities"`
			ClientInfo   []string `json:"client_info"`
		} `json:"initialize"`
		ToolsListMeta         []string `json:"tools_list_meta"`
		ToolsCallMeta         []string `json:"tools_call_meta"`
		FirstToolCallAfterMs  int      `json:"first_tool_call_after_ms"`
		ToolCallGapMs         []int    `json:"tool_call_gap_ms"`
		ThreadOnEveryToolCall bool     `json:"thread_on_every_tool_call"`
	} `json:"client"`
	Spawns struct {
		AtMs            []int `json:"at_ms"`
		GapAfterIndex   int   `json:"gap_after_index"`
		GapCompressedMs int   `json:"gap_compressed_ms"`
	} `json:"spawns"`
}

func loadObservedCodex(t *testing.T) observedCodex {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-observed-2026-11-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	var script observedCodex
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	if len(script.Spawns.AtMs) == 0 || len(script.Client.ToolsCallMeta) == 0 {
		t.Fatal("empty replay script")
	}
	return script
}

// replayedHelper is one real helper process driven over raw stdio lines with
// the message structure Codex sends.
type replayedHelper struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan map[string]any
	exited chan error
	next   int
}

func spawnReplayedHelper(t *testing.T, image, dir string) *replayedHelper {
	t.Helper()
	cmd := exec.Command(image)
	cmd.Env = append(os.Environ(), "OA_AGENT_HELPER_CHAOS_FIXTURE="+dir, "OA_CHAOS_SHARED_STATE=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &replayedHelper{cmd: cmd, in: in, lines: make(chan map[string]any, 64), exited: make(chan error, 1), next: 1}
	go func() {
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			var message map[string]any
			if json.Unmarshal(scanner.Bytes(), &message) == nil {
				h.lines <- message
			}
		}
		close(h.lines)
	}()
	go func() { h.exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
	})
	return h
}

func replayObject(keys []string, value func(string) any) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		out[key] = value(key)
	}
	return out
}

// request sends one line and, for a request, waits for its answer.
func (h *replayedHelper) request(t *testing.T, method string, params map[string]any, notify bool) map[string]any {
	t.Helper()
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		message["params"] = params
	}
	id := 0
	if !notify {
		id = h.next
		h.next++
		message["id"] = id
	}
	line, _ := json.Marshal(message)
	if _, err := h.in.Write(append(line, '\n')); err != nil {
		t.Fatalf("%s: helper stdin closed: %v", method, err)
	}
	if notify {
		return nil
	}
	timeout := time.After(30 * time.Second)
	for {
		select {
		case reply, ok := <-h.lines:
			if !ok {
				t.Fatalf("%s: helper stdout closed", method)
			}
			if got, _ := reply["id"].(float64); int(got) == id {
				return reply
			}
		case <-timeout:
			t.Fatalf("%s: no answer", method)
		}
	}
}

// introduce replays Codex's initialize, initialized and tools/list.
func (h *replayedHelper) introduce(t *testing.T, script observedCodex) {
	t.Helper()
	params := replayObject(script.Client.Initialize.Params, func(key string) any {
		switch key {
		case "capabilities":
			return replayObject(script.Client.Initialize.Capabilities, func(string) any { return map[string]any{} })
		case "clientInfo":
			return replayObject(script.Client.Initialize.ClientInfo, func(string) any { return "replay" })
		case "protocolVersion":
			return "2025-11-25"
		}
		return map[string]any{}
	})
	if reply := h.request(t, "initialize", params, false); reply["result"] == nil {
		t.Fatal("initialize refused", reply)
	}
	h.request(t, "notifications/initialized", nil, true)
	meta := replayObject(script.Client.ToolsListMeta, func(string) any { return 1 })
	if reply := h.request(t, "tools/list", map[string]any{"_meta": meta}, false); reply["result"] == nil {
		t.Fatal("tools/list refused", reply)
	}
}

// call replays one Codex tool call: the recorded _meta keys with the thread.
func (h *replayedHelper) call(t *testing.T, script observedCodex, thread string, n int) (bool, string) {
	t.Helper()
	meta := replayObject(script.Client.ToolsCallMeta, func(key string) any {
		switch key {
		case "threadId", "sessionId":
			return thread
		case "progressToken":
			return n
		case "x-codex-turn-metadata":
			return map[string]any{}
		}
		return fmt.Sprintf("replay-%d", n)
	})
	reply := h.request(t, "tools/call", map[string]any{"_meta": meta, "name": "oa_messages_wait", "arguments": map[string]any{"wait_ms": 0}}, false)
	result, _ := reply["result"].(map[string]any)
	if result == nil {
		t.Fatal("tools/call answered without a result", reply)
	}
	isError, _ := result["isError"].(bool)
	text := ""
	if content, _ := result["content"].([]any); len(content) != 0 {
		if first, _ := content[0].(map[string]any); first != nil {
			text, _ = first["text"].(string)
		}
	}
	return !isError, text
}

func (h *replayedHelper) kill(t *testing.T) {
	t.Helper()
	// Codex ends a helper this way: no stdin EOF, no chance to record an end.
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-h.exited
}

// TestReplayObservedCodexSession replays the recorded session: fifteen
// catalogue probes born in Codex's bursts and killed, beside one working
// helper that calls tools with its thread and lives through a standby-length
// outage. Every tool call gets a result, the working helper's next call
// succeeds after the outage, and the shared lifecycle file keeps the working
// row while marking the killed probes gone.
func TestReplayObservedCodexSession(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the replay drives Windows helper images")
	}
	script := loadObservedCodex(t)
	image := helperImage(t, "oa-codex-integration")
	dir := t.TempDir()
	thread := "01a10a6d-0000-4000-8000-000000000001"
	for rep := 0; rep < 3; rep++ {
		session := spawnReplayedHelper(t, image, dir)
		session.introduce(t, script)
		time.Sleep(time.Duration(script.Client.FirstToolCallAfterMs/10) * time.Millisecond)
		start := time.Now()
		var probes []*replayedHelper
		spawned := 0
		calls, successes := 0, 0
		standby := false
		for spawned < len(script.Spawns.AtMs) || calls < chaosRounds {
			at := script.Spawns.AtMs
			for spawned < len(at) {
				offset := at[spawned]
				if spawned > script.Spawns.GapAfterIndex {
					offset = at[script.Spawns.GapAfterIndex] + script.Spawns.GapCompressedMs + (at[spawned] - at[script.Spawns.GapAfterIndex+1])
				}
				if time.Since(start) < time.Duration(offset)*time.Millisecond {
					break
				}
				probe := spawnReplayedHelper(t, image, dir)
				probe.introduce(t, script)
				probes = append(probes, probe)
				spawned++
			}
			if calls < chaosRounds {
				if calls == chaosRounds/2 && !standby {
					// The standby: the runtime stops answering for two leases.
					standby = true
					if err := os.WriteFile(filepath.Join(dir, "control"), []byte(fmt.Sprintf("lapse %d", rep)), 0600); err != nil {
						t.Fatal(err)
					}
				}
				ok, text := session.call(t, script, thread, calls)
				calls++
				if ok {
					successes++
				} else if !strings.Contains(text, `"outcome"`) {
					t.Fatal("untyped refusal", text)
				}
				gap := script.Client.ToolCallGapMs[calls%len(script.Client.ToolCallGapMs)]
				time.Sleep(time.Duration(gap/20) * time.Millisecond)
			} else {
				time.Sleep(20 * time.Millisecond)
			}
		}
		// After the standby the working helper answers again.
		ok := false
		for i := 0; i < 40 && !ok; i++ {
			ok, _ = session.call(t, script, thread, 1000+i)
			if !ok {
				time.Sleep(250 * time.Millisecond)
			}
		}
		if !ok || successes == 0 {
			t.Fatalf("round %d: the working helper did not recover (%d of %d calls succeeded)", rep, successes, calls)
		}
		for _, probe := range probes {
			probe.kill(t)
		}
		select {
		case err := <-session.exited:
			t.Fatal("the working helper exited during the replay", err)
		default:
		}
		// One more helper's first write marks the killed probes gone.
		marker := spawnReplayedHelper(t, image, dir)
		marker.introduce(t, script)
		rows := readAgentDiagnostics(context.Background(), filepath.Join(dir, "state"))
		working, gone := false, 0
		for _, row := range rows {
			if row.PID == session.cmd.Process.Pid && row.Phase == "started" && !row.Gone {
				working = true
			}
			if row.Gone {
				gone++
			}
		}
		if !working || gone == 0 {
			t.Fatalf("round %d: lifecycle rows lost the working helper or kept killed probes running: %+v", rep, rows)
		}
		session.kill(t)
		marker.kill(t)
	}
}
