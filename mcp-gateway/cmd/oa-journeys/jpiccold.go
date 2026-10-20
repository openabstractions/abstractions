//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// J-PIC-COLD (owner, R29/R30, 2026-11-11): a picture asked while the chat
// model held the card starved, and each resumed call's progress said "20 s
// so far" again.
//
// The journey loads the person's chat model on the real model host with one
// chat turn, so it holds its lease on card:0. A picture engine
// (cmd/oa-journey-picture) is declared on card 0 as Setup declares an
// engine; its first picture asks for a lease that needs part of the chat's
// memory and loads for pictureCold. The agent asks for the picture with the
// prompt alone, gets working after the default wait, stays away for a few
// seconds and resumes with the handle. The resumed working answer counts the
// picture's whole time; the picture is saved.
func init() { registerTimed(16, "J-PIC-COLD", picColdBudget, runPicCold) }

const (
	pictureCold  = 40 * time.Second
	pictureModel = "journey/cold-picture"
	pictureAway  = 5 * time.Second
)

var soFar = regexp.MustCompile(`(\d+) s so far`)

func picColdBudget(o *options) time.Duration {
	return 2*time.Minute + coldLoadBudget(chatModelSize(o)) + pictureCold + 2*time.Minute
}

func runPicCold(ctx context.Context, e *env, o *options, r *result) {
	if !r.check("a chat model on this computer to hold the card", o.chatModel != "", "journeys.py --chat-model names none") {
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
	// Setup declares the picture engine beside the chat model before the
	// person's agent connects; it starts on its first picture.
	store := filepath.Join(e.work, "picture-provider")
	_ = os.MkdirAll(store, 0o700)
	out2, err := e.cli(ctx, "provider", "add", "journey-picture", "--program", filepath.Join(e.bin, "oa-journey-picture.exe"),
		"--provider-endpoint", "journey-picture-"+newID()[:6],
		"--contract", "abstraction.inference/image@1", "--contract", "abstraction.storage/content-reader@1", "--contract", "abstraction.storage/content-writer@1",
		"--profiles", "image", "--model", pictureModel, "--card", "0",
		"--arg", "-endpoint", "--arg", "{endpoint}", "--arg", "-runtime", "--arg", e.runtime, "--arg", "-store", "--arg", store,
		"--arg", "-model", "--arg", pictureModel, "--arg", "-cold", "--arg", pictureCold.String())
	if !r.check("picture engine declared on card 0", err == nil, "%v %s", err, strings.TrimSpace(out2)) {
		return
	}
	if !r.check("picture policy written", writePicturePolicy(ctx, e, pictureModel) == nil, "") {
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
	model, listed := localChatName(ctx, s, e, chat.store)
	if !r.check("oa_models_list lists the hosted chat model", model != "", "%s", clip(listed, 400)) {
		return
	}
	// One chat turn: the model loads and holds its lease.
	text, out, took := chatTurn(ctx, e, s, model, "Reply with one word: harbor.")
	outcome, _ := out["outcome"].(string)
	if !r.check("a chat turn loads the chat model", outcome == "completed", "%s after %.0fs: %s", outcome, took.Seconds(), clip(text, 300)) {
		return
	}
	lease, detail := cardLease(ctx, e, filepath.Join(o.tools, "modelhostd.exe"))
	r.check("the chat model holds the card", lease != "", "%s", detail)

	tool, _ := pictureTool(s.tools)
	if !r.check("a picture tool is listed", tool != nil, "tools: %s", toolNames(s.tools)) {
		return
	}
	var submitted time.Time
	var res map[string]any
	var resText string
	for tries := 0; tries < 3; tries++ {
		submitted = time.Now()
		_, resText, res, err = s.call(ctx, tool.Name, map[string]any{"prompt": "a lighthouse at dusk"})
		if err != nil || !permissionPending(resText, res) {
			break
		}
		e.answers.waitIDs(ctx, questionIDs(res), 60*time.Second)
	}
	handle, _ := res["handle"].(string)
	state, _ := res["outcome"].(string)
	if !r.check("the cold picture answers working with a handle", err == nil && state == "working" && handle != "",
		"after %.0fs: %s %s", time.Since(submitted).Seconds(), clip(resText, 300), errString(err)) {
		return
	}
	time.Sleep(pictureAway)
	_, workingText, working, err := s.call(ctx, tool.Name, map[string]any{"handle": handle, "wait_ms": 5000})
	elapsed := time.Since(submitted)
	seconds := -1
	if m := soFar.FindStringSubmatch(workingText); m != nil {
		seconds, _ = strconv.Atoi(m[1])
	}
	if state, _ := working["outcome"].(string); state == "working" {
		r.check("the resumed working answer counts the picture's whole time", float64(seconds) >= elapsed.Seconds()-3,
			"%d s so far after %.0fs since the submission: %s", seconds, elapsed.Seconds(), clip(workingText, 300))
	} else {
		r.check("the picture still loads when the agent resumes", false, "%s %s (cold load %s)", state, clip(workingText, 300), pictureCold)
	}
	final, finalText := working, workingText
	for deadline := time.Now().Add(pictureCold + time.Minute); time.Now().Before(deadline) && err == nil; {
		if state, _ := final["outcome"].(string); state != "working" {
			break
		}
		_, finalText, final, err = s.call(ctx, tool.Name, map[string]any{"handle": handle})
	}
	state, _ = final["outcome"].(string)
	r.check("the picture is saved", err == nil && state == "saved", "%s after %.0fs: %s %s", state, time.Since(submitted).Seconds(), clip(finalText, 300), errString(err))
	log, _ := os.ReadFile(filepath.Join(store, "picture.log"))
	asked := regexp.MustCompile(`acquired \d+ bytes after asking (\d+) holders`).FindStringSubmatch(string(log))
	r.check("the chat gave memory to the picture", asked != nil && asked[1] != "0", "%s", clip(string(log), 600))
}

// chatTurn is one chat answer: the call, the person's first-use answers,
// and resumes with the handle until the operation ends.
func chatTurn(ctx context.Context, e *env, s *session, model, prompt string) (string, map[string]any, time.Duration) {
	args := map[string]any{"model": model, "prompt": prompt, "hosting": "local"}
	text, out, started, err := startCompletion(ctx, e, s, args)
	answer := stringOf(out["text"])
	for err == nil && out != nil && stringOf(out["state"]) == "working" && ctx.Err() == nil {
		resume := map[string]any{"operation_id": stringOf(out["operation_id"])}
		if cursor, ok := out["cursor"]; ok {
			resume["cursor"] = cursor
		}
		_, text, out, err = s.call(ctx, "oa_inference_complete", resume)
		answer += stringOf(out["text"])
	}
	if out == nil {
		out = map[string]any{"outcome": errString(err)}
	}
	return firstNonEmpty(answer, text), out, time.Since(started)
}
