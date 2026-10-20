//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
)

const testImageModel = "sample/solid-pixel"

// J-PIC: a fresh kept agent app makes its first picture. The agent reads only
// the tool list and results: it picks the picture tool its descriptions point
// to, calls it with the person's prompt alone, and follows what the results
// say. The person answers each question after the answer delay.
func init() { register(10, "J-PIC", runPicture) }

func runPicture(ctx context.Context, e *env, o *options, r *result) {
	model := testImageModel
	if o.pytorch != "" {
		r.Note = "pytorch provider " + o.pytorch
		model = o.pytorch
	} else {
		r.Note = "test image provider (1x1 PNG); pytorch not run"
		store := filepath.Join(e.work, "image-provider")
		_ = os.MkdirAll(store, 0o700)
		out, err := e.cli(ctx, "provider", "add", "journey-image", "--program", filepath.Join(e.bin, "journey-image.exe"),
			"--provider-endpoint", "journey-image-"+newID()[:6],
			"--contract", "abstraction.inference/image@1", "--contract", "abstraction.storage/content-reader@1", "--contract", "abstraction.storage/content-writer@1",
			"--profiles", "image", "--model", testImageModel,
			"--arg", "-endpoint", "--arg", "{endpoint}", "--arg", "-runtime", "--arg", e.runtime, "--arg", "-store", "--arg", store)
		if !r.check("picture provider declared", err == nil, "%v %s", err, strings.TrimSpace(out)) {
			return
		}
		err = writePicturePolicy(ctx, e, testImageModel)
		if !r.check("picture policy written", err == nil, "%v", err) {
			return
		}
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
	tool, required := pictureTool(s.tools)
	if !r.check("a picture tool is listed", tool != nil, "tools: %s", toolNames(s.tools)) {
		return
	}
	prompt := "a small red fox in snow"
	started := time.Now()
	res, text, structured, err := s.call(ctx, tool.Name, map[string]any{"prompt": prompt})
	promptOnly := err == nil && res != nil && !res.IsError && pictureStarted(text, structured)
	r.check("prompt-only call accepted", promptOnly, "%s(prompt) required %v -> %s", tool.Name, required, clip(firstNonEmpty(text, errString(err)), 300))
	if !promptOnly {
		// The agent adds what the schema and the error ask for.
		args := map[string]any{"prompt": prompt}
		for _, name := range required {
			switch name {
			case "prompt":
			case "request_key":
				args[name] = "first-picture-" + newID()[:8]
			case "profile":
				args[name] = "image_batch"
			case "hosting":
				args[name] = "local"
			case "model":
				lres, listed, _, lerr := s.call(ctx, "oa_models_list", map[string]any{})
				refused := lerr != nil || (lres != nil && lres.IsError)
				r.check("first models list waits for the person instead of refusing", !refused, "%s", clip(firstNonEmpty(listed, errString(lerr)), 300))
				if refused {
					e.answers.waitQuiet(ctx, 60*time.Second)
					_, listed, _, lerr = s.call(ctx, "oa_models_list", map[string]any{})
				}
				r.check("models list names the picture model", lerr == nil && strings.Contains(listed, model), "%s", clip(firstNonEmpty(listed, errString(lerr)), 300))
				args[name] = model
			}
		}
		if model == testImageModel {
			args["size"] = "auto"
		}
		started = time.Now()
		res, text, structured, err = s.call(ctx, tool.Name, args)
		for tries := 0; err == nil && tries < 3; tries++ {
			outcome, _ := structured["outcome"].(string)
			if permissionPending(text, structured) {
				// The agent tells the person and ends its turn; the person
				// answers, says continue, and the agent repeats the call.
				r.check("submit told the agent the person is being asked", true, "%s", clip(text, 200))
				e.answers.waitIDs(ctx, questionIDs(structured), 60*time.Second)
			} else if outcome == "definitely_not_accepted" || outcome == "not_accepted" {
				r.check("a repeated submit after the person answered is accepted", false, "%s", clip(text, 300))
				args["request_key"] = "first-picture-" + newID()[:8]
			} else {
				break
			}
			started = time.Now()
			res, text, structured, err = s.call(ctx, tool.Name, args)
		}
	}
	if !r.check("picture request accepted after the person answered", err == nil && res != nil && !res.IsError && !strings.Contains(text, "rights:not_granted"),
		"after %.1fs: %s", time.Since(started).Seconds(), clip(firstNonEmpty(text, errString(err)), 400)) {
		return
	}
	// A picture still being made answers working with a handle; the agent
	// calls the picture tool again with only that handle, as its answer says.
	final, finalText, finalOut := res, text, structured
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); {
		outcome, _ := finalOut["outcome"].(string)
		handle, _ := finalOut["handle"].(string)
		if outcome != "working" || handle == "" {
			break
		}
		final, finalText, finalOut, err = s.call(ctx, tool.Name, map[string]any{"handle": handle})
		if !r.check("picture still readable by its handle", err == nil, "%v", err) {
			return
		}
	}
	outcome, _ := finalOut["outcome"].(string)
	r.check("picture finished", final != nil && !final.IsError && outcome == "saved", "%s %s", outcome, clip(finalText, 400))
	path, image := pictureDelivered(final, finalText)
	r.check("result names an existing picture file or carries the image", path != "" || image, "path=%q image=%v result=%s", path, image, clip(finalText, 300))
	r.check("few calls", s.calls <= 6, "%d tool calls (limit 6)", s.calls)
	keys := map[string]int{}
	for _, q := range e.answers.since(0) {
		keys[q.Text]++
	}
	repeated := 0
	for _, n := range keys {
		if n > 1 {
			repeated++
		}
	}
	r.check("no question asked twice", repeated == 0, "%d questions repeated", repeated)
}

// pictureTool is the tool whose description says it makes pictures; among
// several, the one asking the fewest arguments.
// writePicturePolicy makes the account default oa-image-default name one
// local candidate, as Setup does when its picture model becomes ready
// (monitor/setup_step.go setupImageDefault).
func writePicturePolicy(ctx context.Context, e *env, model string) error {
	operator, err := e.machine.ResolveInferenceSelectionOperatorV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	read, err := operator.ReadAccountDefault(ctx, "oa-image-default")
	if err != nil {
		return err
	}
	if read.Outcome != iwire.SelectionReadOutcomeFound && read.Outcome != iwire.SelectionReadOutcomeUnknown {
		return fmt.Errorf("the picture default cannot be read: %s", read.Outcome)
	}
	change, err := operator.PutAccountDefault(ctx, read.Revision, iclient.SelectionPolicyV2{Name: "oa-image-default",
		Candidates: []iclient.SelectionCandidateV2{{Model: model, Placement: iwire.CandidatePlacementLocal}}})
	if err != nil {
		return err
	}
	if change.Outcome != iwire.EditOutcomeApplied {
		return fmt.Errorf("the picture default was not written: %s %s", change.Outcome, change.Reason)
	}
	return nil
}

func pictureTool(tools map[string]*mcp.Tool) (*mcp.Tool, []string) {
	var best *mcp.Tool
	var bestRequired []string
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t := tools[n]
		d := strings.ToLower(t.Name + " " + t.Description)
		if !strings.Contains(d, "image") && !strings.Contains(d, "picture") {
			continue
		}
		if strings.Contains(t.Name, "status") || strings.Contains(t.Name, "cancel") || strings.Contains(t.Name, "diagnose") || strings.Contains(t.Name, "follow") {
			continue
		}
		req := requiredOf(t)
		if best == nil || len(req) < len(bestRequired) {
			best, bestRequired = t, req
		}
	}
	return best, bestRequired
}

func requiredOf(t *mcp.Tool) []string {
	raw, _ := json.Marshal(t.InputSchema)
	var s struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(raw, &s)
	return s.Required
}

func toolNames(tools map[string]*mcp.Tool) string {
	var n []string
	for k := range tools {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, " ")
}

// pictureStarted says whether a picture call made or is making the picture:
// oa_image_generate's structured outcome saved or working. An answer with no
// structured outcome is read by its text.
func pictureStarted(text string, structured map[string]any) bool {
	if outcome, ok := structured["outcome"].(string); ok {
		return outcome == "saved" || outcome == "working"
	}
	return !refusedShape(text)
}

func refusedShape(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "invalid") || strings.Contains(t, "required") || strings.Contains(t, "missing")
}

func permissionPending(text string, structured map[string]any) bool {
	o, _ := structured["outcome"].(string)
	return o == "awaiting_permission" || strings.Contains(text, "question_pending") || (structured["waiting"] != nil && structured["handle"] == nil)
}

var windowsFile = regexp.MustCompile(`[A-Za-z]:\\\\?(?:[^"\\\r\n<>|?*]+\\\\?)*[^"\\\r\n<>|?*]+\.(?:png|jpg|jpeg|webp)`)

// pictureDelivered finds a file path the agent can show, or image content.
func pictureDelivered(res *mcp.CallToolResult, text string) (string, bool) {
	image := false
	if res != nil {
		for _, c := range res.Content {
			if _, ok := c.(*mcp.ImageContent); ok {
				image = true
			}
		}
	}
	raw := text
	if res != nil && res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		raw += string(b)
	}
	for _, m := range windowsFile.FindAllString(raw, -1) {
		p := strings.ReplaceAll(m, `\\`, `\`)
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			return p, image
		}
	}
	return "", image
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func questionIDs(structured map[string]any) []string {
	var ids []string
	if list, ok := structured["question_ids"].([]any); ok {
		for _, v := range list {
			if id, ok := v.(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}
