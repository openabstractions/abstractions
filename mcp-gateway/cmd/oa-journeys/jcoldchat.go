//go:build windows

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// J-COLD-CHAT (owner, R29/R30, 2026-11-11): OpenCode asked the local Qwen
// through oa_inference_complete while it was not loaded. The call answered
// working after its wait; the agent came back more than 10 s later and the
// operation had been cancelled as idle. A Qwen answer also carried its
// reasoning.
//
// The journey hosts the person's chat model on the real model host, unloaded.
// The agent calls oa_inference_complete with a short wait, receives working
// with a handle, stays away for coldChatAway without observing, then resumes
// with the handle until the answer completes. The answer holds the asked word
// and none of the model's reasoning.
func init() { registerTimed(15, "J-COLD-CHAT", coldChatBudget, runColdChat) }

// coldChatAway is how long the agent stays away after working: longer than
// the provider's 10 s idle.
const coldChatAway = 15 * time.Second

// coldChatWait is the first call's wait_ms. The default 20 s can see a load
// from a warm file cache finish; 5 s keeps the call answering working while
// the engine loads, and holds the operation at its wait cap the same way.
const coldChatWait = 5000

// coldChatWord is the one word the person asks the model to answer with.
const coldChatWord = "lighthouse"

// reasoningMarks are text a model's reasoning carries when it leaks into
// the answer.
var reasoningMarks = []string{"<think>", "</think>", "Thinking Process", "<|im_start|>", "<|im_end|>"}

func coldChatBudget(o *options) time.Duration {
	return 2*time.Minute + coldLoadBudget(chatModelSize(o)) + coldChatAway + time.Minute
}

func runColdChat(ctx context.Context, e *env, o *options, r *result) {
	if !r.check("a chat model on this computer to load cold", o.chatModel != "", "journeys.py --chat-model names none") {
		return
	}
	busy, err := gpuHolders(ctx, nil, e.logf)
	if !r.check("nothing else holds the card", err == nil && busy == "", "%s%s", busy, errString(err)) {
		return
	}
	chat, err := hostLocalChat(ctx, e, o)
	if !r.check("the model host hosts the chat model", err == nil, "%v", err) {
		return
	}
	r.Note = fmt.Sprintf("%s (%.1f GiB) on %s", chat.file, float64(chat.bytes)/(1<<30), o.tools)
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
	model, listed := localChatName(ctx, s, e, chat.store)
	if !r.check("oa_models_list lists the hosted chat model", model != "", "%s", clip(listed, 400)) {
		return
	}
	lease, detail := cardLease(ctx, e, filepath.Join(o.tools, "modelhostd.exe"))
	r.check("the chat model is unloaded before the call", lease == "", "%s", detail)

	text, out, took, err := completeCold(ctx, e, r, s, model, "Reply with one word: "+coldChatWord+".", coldChatWait)
	if err != nil {
		return
	}
	outcome, _ := out["outcome"].(string)
	reason, _ := out["reason"].(string)
	r.check("the resumed call completes", outcome == "completed", "%s %s after %.0fs: %s", outcome, reason, took.Seconds(), clip(text, 300))
	r.check("the operation was not ended as idle", !strings.Contains(reason, "idle"), "%s %s", outcome, reason)
	r.check("the answer holds the asked word", strings.Contains(strings.ToLower(text), coldChatWord), "%q", clip(text, 300))
	leak := ""
	for _, mark := range reasoningMarks {
		if strings.Contains(text, mark) {
			leak = mark
		}
	}
	r.check("no reasoning in the answer", leak == "", "%q in %q", leak, clip(text, 400))
}

// completeCold is the agent's cold oa_inference_complete: the call with
// wait_ms, the person's first-use answers, working with a handle, the agent
// away for coldChatAway, then resumes with the handle and cursor until the
// operation ends. It returns the answer's text, the last result and the
// time from the call to the answer.
func completeCold(ctx context.Context, e *env, r *result, s *session, model, prompt string, wait int64) (string, map[string]any, time.Duration, error) {
	args := map[string]any{"model": model, "prompt": prompt, "hosting": "local", "wait_ms": wait}
	text, out, started, err := startCompletion(ctx, e, s, args)
	if !r.check("the call reaches the model", err == nil, "%v", err) {
		return "", nil, 0, fmt.Errorf("call failed")
	}
	state, _ := out["state"].(string)
	handle, _ := out["operation_id"].(string)
	if !r.check("a cold model answers working with a handle", state == "working" && handle != "", "after %.1fs: %s", time.Since(started).Seconds(), clip(text, 300)) {
		return "", nil, 0, fmt.Errorf("no working answer")
	}
	answer := stringOf(out["text"])
	// The agent ends its turn; the person comes back later.
	time.Sleep(coldChatAway)
	for state == "working" && ctx.Err() == nil {
		resume := map[string]any{"operation_id": handle}
		if cursor, ok := out["cursor"]; ok {
			resume["cursor"] = cursor
		}
		_, text, out, err = s.call(ctx, "oa_inference_complete", resume)
		if !r.check("the handle resumes", err == nil && out != nil, "%v %s", err, clip(text, 300)) {
			return answer, out, time.Since(started), fmt.Errorf("resume failed")
		}
		answer += stringOf(out["text"])
		state, _ = out["state"].(string)
	}
	return answer, out, time.Since(started), nil
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// startCompletion is the agent's first oa_inference_complete as the results
// direct it: after a first-use question the person answers and the agent
// repeats the call; while the model host itself is starting (unavailable
// activating:<host>) the agent repeats it shortly after. started is when the
// call that was answered began.
func startCompletion(ctx context.Context, e *env, s *session, args map[string]any) (string, map[string]any, time.Time, error) {
	var text string
	var out map[string]any
	var err error
	started := time.Now()
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline) && ctx.Err() == nil; {
		started = time.Now()
		_, text, out, err = s.call(ctx, "oa_inference_complete", args)
		switch {
		case err != nil:
			return text, out, started, err
		case permissionPending(text, out) || strings.Contains(text, rights.DecisionOutcomeNotGranted.String()):
			e.answers.waitIDs(ctx, questionIDs(out), 60*time.Second)
		case strings.HasPrefix(stringOf(out["reason"]), "activating:"):
			time.Sleep(2 * time.Second)
		default:
			return text, out, started, nil
		}
	}
	return text, out, started, err
}
