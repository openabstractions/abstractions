package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
)

// ToolVideoGenerate makes one local video from a prompt and saves it in the
// person's Videos folder, shaped like oa_image_generate (architect,
// 2026-11-12). It runs the inference job path with profile video; the clip
// is saved when the job ends (video_watch.go), and the call answers its
// path when the job ends within wait_ms.
const ToolVideoGenerate = "oa_video_generate"

const (
	videoDescription = "Make a video (with sound when the model supports it) from a text prompt and save it as a file, answering its path. Waits wait_ms (default 20000, at most 50000), then answers with the phase and a handle to call again with."
	// videoPolicy is the account's video policy Setup writes when it
	// installs a video model.
	videoPolicy  = "oa-video-default"
	maxVideoWait = 50 * time.Second
	// The presets of a video model set (modelhost MODELHOST-V6): quick is
	// the model's own size and a one-second clip, quality the two-stage
	// 1280x704 and a five-second clip.
	videoQuick           = "quick"
	videoQuality         = "quality"
	videoQualitySize     = "1280x704"
	videoQuickDuration   = 1000
	videoQualityDuration = 5000
	videoPollFirst       = time.Second
	videoPollMost        = 5 * time.Second
)

// videoFinishRoom is kept from a call's budget for reading and saving a
// finished clip of up to 64 MiB.
var videoFinishRoom = 30 * time.Second

// VideoInput is one oa_video_generate call: a new video from prompt, or a
// continuation that carries only handle and optionally wait_ms.
type VideoInput struct {
	Prompt     string `json:"prompt,omitempty" jsonschema:"what the video shows and sounds like; required for a new video"`
	Model      string `json:"model,omitempty" jsonschema:"leave out for the person's video model"`
	Preset     string `json:"preset,omitempty" jsonschema:"quick (default): own size, 1 s; quality: 1280x704, 5 s"`
	Size       string `json:"size,omitempty" jsonschema:"WIDTHxHEIGHT; default: the preset's"`
	DurationMS int64  `json:"duration_ms,omitempty" jsonschema:"clip length, 1 to 600000; default: the preset's"`
	Handle     string `json:"handle,omitempty" jsonschema:"to wait for a video still being made: the handle answered, alone or with wait_ms"`
	WaitMS     int64  `json:"wait_ms,omitempty" jsonschema:"default 20000, at most 50000"`
}

// VideoOutput is the structured answer. Message is the one sentence the
// agent relays. A saved video carries path; a video still being made
// carries handle and phase; a refusal carries outcome and reason.
type VideoOutput struct {
	Outcome string `json:"outcome"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Handle  string `json:"handle,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Field   string `json:"field,omitempty"`
}

// videoModelBackend says which video model the person's video policy names,
// through a read that asks nobody. ok false carries the sentence saying why
// there is none. A backend without it lists the video tool and needs a
// model named in each call.
type videoModelBackend interface {
	VideoModel(context.Context) (model, refusal string, err error)
}

func videoSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[VideoInput](nil)
	if err != nil {
		panic(err) // Static adapter type inference, as with mcp.AddTool.
	}
	// preset names its default in its description only: the SDK and some
	// clients fill a schema default into every call, a continuation by
	// handle included (R35).
	schema.Properties["preset"].Enum = []any{videoQuick, videoQuality}
	maxWait := float64(maxVideoWait / time.Millisecond)
	schema.Properties["wait_ms"].Maximum = &maxWait
	schema.AnyOf = []*jsonschema.Schema{{Required: []string{"prompt"}}, {Required: []string{"handle"}}}
	return schema
}

// videoHandleConflict is the first argument beside handle that describes a
// new video; "" when every other argument is absent or its default (preset
// quick, size auto), as a client that fills defaults sends them.
func videoHandleConflict(in VideoInput) string {
	switch {
	case in.Prompt != "":
		return "prompt"
	case in.Model != "":
		return "model"
	case in.Preset != "" && in.Preset != videoQuick:
		return "preset"
	case in.Size != "" && in.Size != "auto":
		return "size"
	case in.DurationMS != 0:
		return "duration_ms"
	}
	return ""
}

func invalidVideo(field, message string) *VideoOutput {
	return &VideoOutput{Outcome: "invalid", Field: field, Message: message}
}

// videoRequest checks one call's arguments; each wrong one is refused with
// one sentence naming the values it takes.
func videoRequest(in VideoInput) (VideoInput, *VideoOutput) {
	if in.WaitMS < 0 || in.WaitMS > int64(maxVideoWait/time.Millisecond) {
		return in, invalidVideo("wait_ms", "wait_ms must be 0 to 50000 milliseconds; leave it out for 20000.")
	}
	if in.Handle != "" {
		if field := videoHandleConflict(in); field != "" {
			return in, invalidVideo(field, fmt.Sprintf("%s describes a new video, and handle continues one already started: to wait for it, call oa_video_generate with the handle and optionally wait_ms; to start another, leave out handle.", field))
		}
		in = VideoInput{Handle: in.Handle, WaitMS: in.WaitMS}
		if decoded, err := base64.RawURLEncoding.DecodeString(in.Handle); err != nil || len(in.Handle) != 32 || len(decoded) != 24 {
			return in, invalidVideo("handle", "handle must be the handle an earlier oa_video_generate call answered with.")
		}
		return in, nil
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return in, invalidVideo("prompt", "prompt is required: describe the video.")
	}
	if len(in.Prompt) > maxPicturePrompt || !utf8.ValidString(in.Prompt) {
		return in, invalidVideo("prompt", fmt.Sprintf("prompt is too long: at most %d bytes.", maxPicturePrompt))
	}
	if len(in.Model) > 256 || in.Model != "" && strings.TrimSpace(in.Model) == "" {
		return in, invalidVideo("model", "model must be a model name; leave it out for the person's video model.")
	}
	switch in.Preset {
	case "", videoQuick:
		in.Preset = videoQuick
	case videoQuality:
	default:
		return in, invalidVideo("preset", "preset must be quick or quality; leave it out for quick.")
	}
	if in.Size != "" && (in.Size == "auto" || !pictureSize.MatchString(in.Size)) {
		return in, invalidVideo("size", "size must be WIDTHxHEIGHT such as 1280x704; leave it out for the preset's size.")
	}
	if in.DurationMS < 0 || in.DurationMS > 600000 {
		return in, invalidVideo("duration_ms", "duration_ms must be 1 to 600000 milliseconds; leave it out for the preset's length.")
	}
	if in.Size == "" {
		in.Size = "auto"
		if in.Preset == videoQuality {
			in.Size = videoQualitySize
		}
	}
	if in.DurationMS == 0 {
		in.DurationMS = videoQuickDuration
		if in.Preset == videoQuality {
			in.DurationMS = videoQualityDuration
		}
	}
	return in, nil
}

func videoToolResult(out VideoOutput) *mcp.CallToolResult {
	ok := out.Outcome == "saved" || out.Outcome == "working" || out.Outcome == "made"
	return &mcp.CallToolResult{IsError: !ok, StructuredContent: out, Content: []mcp.Content{&mcp.TextContent{Text: out.Message}}}
}

func addVideoTool(s *mcp.Server, backend Backend, bounded func(context.Context, func(context.Context) error) error, effect *mcp.ToolAnnotations) {
	output, err := jsonschema.For[VideoOutput](nil)
	if err != nil {
		panic(err)
	}
	mcp.AddTool(s, &mcp.Tool{Name: ToolVideoGenerate, Description: videoDescription, Annotations: effect, InputSchema: videoSchema(), OutputSchema: output},
		func(ctx context.Context, request *mcp.CallToolRequest, in VideoInput) (*mcp.CallToolResult, VideoOutput, error) {
			in, refused := videoRequest(in)
			if refused != nil {
				return videoToolResult(*refused), *refused, nil
			}
			ctx = withProgress(ctx, request)
			var out VideoOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = makeVideo(ctx, backend, in); return err })
			if err != nil {
				if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, out, err
				}
				out = VideoOutput{Outcome: "unavailable", Message: err.Error()}
			}
			return videoToolResult(out), out, nil
		})
}

// makeVideo runs one video call: the model, the durable job, at most the
// call's wait of following it, and the answer.
func makeVideo(ctx context.Context, backend Backend, in VideoInput) (VideoOutput, error) {
	handle := in.Handle
	if handle == "" {
		model := in.Model
		if model == "" {
			chooser, ok := backend.(videoModelBackend)
			if !ok {
				return *invalidVideo("model", "Name a video model: this connection has no saved video model."), nil
			}
			chosen, refusal, err := chooser.VideoModel(ctx)
			if err != nil {
				return VideoOutput{}, err
			}
			if refusal != "" {
				return VideoOutput{Outcome: "not_set_up", Message: refusal}, nil
			}
			model = chosen
		}
		key, err := callRequestKeyFor(ctx, "video-")
		if err != nil {
			return VideoOutput{}, err
		}
		request := JobSubmitInput{RequestKey: key, Profile: profileWord(jobwire.ProfileVideo), Model: model, Prompt: in.Prompt,
			Hosting: "local", Size: in.Size, Count: 1, DurationMS: in.DurationMS}
		submitted, err := backend.Submit(ctx, request)
		if err != nil {
			return VideoOutput{}, err
		}
		if submitted.Outcome != "accepted" {
			message := submitted.Message
			if message == "" {
				message = fmt.Sprintf("OpenAbstractions did not start the video: %s.", strings.TrimSpace(submitted.Outcome+" "+submitted.Reason))
			}
			return VideoOutput{Outcome: submitted.Outcome, Reason: submitted.Reason, Handle: submitted.Handle, Message: message}, nil
		}
		handle = submitted.Handle
	}
	status, err := followJob(ctx, backend.Status, handle, callWait(in.WaitMS, defaultCallWait))
	if err != nil {
		return VideoOutput{}, err
	}
	return videoAnswer(handle, status), nil
}

// videoAnswer is the video tool's answer for a job's state.
func videoAnswer(handle string, status JobStatusOutput) VideoOutput {
	if status.Outcome != "observed" {
		return VideoOutput{Outcome: status.Outcome, Handle: handle, Message: fmt.Sprintf("OpenAbstractions could not read the video's job: %s.", status.Outcome)}
	}
	switch status.State {
	case "complete":
		if status.Path == "" {
			return VideoOutput{Outcome: "made", Handle: handle, Message: "The video was made. " + status.Message}
		}
		message := "Saved the video to " + status.Path + "."
		if status.Message != "" {
			message += " " + status.Message
		}
		return VideoOutput{Outcome: "saved", Path: status.Path, Message: message}
	case "failed":
		if status.Failure == "" {
			return VideoOutput{Outcome: "failed", Handle: handle, Message: "The video could not be made."}
		}
		return VideoOutput{Outcome: "failed", Handle: handle, Message: fmt.Sprintf("The video could not be made: %s.", strings.TrimSuffix(status.Failure, "."))}
	case "cancelled":
		return VideoOutput{Outcome: "cancelled", Handle: handle, Message: "The video was cancelled before it was made."}
	}
	phase := status.Phase
	if phase == "" {
		phase = "starting"
	}
	message := fmt.Sprintf("Still making the video: %s. Call oa_video_generate with handle %q to wait for it.", phase, handle)
	if status.Message != "" {
		message = status.Message + " " + message
	}
	return VideoOutput{Outcome: "working", Handle: handle, Phase: phase, Message: message}
}

// followJob reads a job until it ends or wait passes, whichever is first,
// and answers its last state. The wait ends early enough to save a finished
// clip within the call's deadline. Each read reports one progress line. A
// read that fails, as while the runtime restarts, is tried again until the
// wait passes.
func followJob(ctx context.Context, status func(context.Context, JobHandleInput) (JobStatusOutput, error), handle string, wait time.Duration) (JobStatusOutput, error) {
	started := time.Now()
	until := started.Add(wait)
	if deadline, ok := ctx.Deadline(); ok {
		room := min(videoFinishRoom, time.Until(deadline)/4)
		if last := deadline.Add(-room); last.Before(until) {
			until = last
		}
	}
	pause := videoPollFirst
	for {
		out, err := status(ctx, JobHandleInput{Handle: handle})
		if err == nil && (out.Outcome != "observed" || out.State == "complete" || out.State == "failed" || out.State == "cancelled") {
			return out, nil
		}
		if err == nil {
			seconds := int64(time.Since(started).Round(time.Second) / time.Second)
			done, total := float64(seconds), 0.0
			if out.Progress["total"] > 0 {
				done, total = float64(out.Progress["done"]), float64(out.Progress["total"])
			}
			reportProgress(ctx, fmt.Sprintf("%s, %d s so far", phaseOrStarting(out.Phase), seconds), done, total)
		}
		left := time.Until(until)
		if left <= 0 || ctx.Err() != nil {
			return out, err
		}
		timer := time.NewTimer(min(pause, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			return out, err
		case <-timer.C:
		}
		pause = min(pause*2, videoPollMost)
	}
}

func phaseOrStarting(phase string) string {
	if phase == "" {
		return "starting"
	}
	return phase
}

// VideoModel is the first candidate of the person's video policy when it is
// local. A call falls back to the one video model this computer serves when
// the policy names none or cannot be read (fail open, R35). A policy naming a
// hosted model stays the person's choice. The runtime's ReadEffective
// consults no right and asks nothing; the router listing a call reads may
// ask, so the tools/list read stops at the policy.
func (o *OA) VideoModel(ctx context.Context) (string, string, error) {
	read, readErr := o.readPolicy(ctx, videoPolicy)
	refusal := videoNotSetUp
	if readErr == nil {
		var model string
		model, refusal = videoCandidate(read)
		if refusal == "" || refusal == videoHosted {
			return model, refusal, nil
		}
	}
	if listing, _ := ctx.Value(listingReadKey{}).(bool); listing {
		return "", refusal, readErr
	}
	served, listErr := o.servedVideoModels(ctx)
	switch {
	case listErr == nil && len(served) == 1:
		log.Printf("gateway: video: the video policy names no local model (%v %s); using %s, the one video model served here", readErr, read.Outcome, served[0])
		return served[0], "", nil
	case listErr == nil && len(served) > 1 && refusal == videoNotSetUp:
		return "", fmt.Sprintf("Name a video model with model: %s.", strings.Join(served, ", ")), nil
	case readErr != nil:
		return "", "", readErr
	}
	return "", refusal, nil
}

// servedVideoModels is one alias of each model the router serves for video
// on this computer, sorted.
func (o *OA) servedVideoModels(ctx context.Context) ([]string, error) {
	router, err := o.machine.ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	snapshot, err := router.ModelsContext(ctx, false)
	if err != nil {
		return nil, err
	}
	return videoAliases(modelsFrom(snapshot.Models)), nil
}

// videoAliases is the first alias of each servable model listed for video.
func videoAliases(models []Model) []string {
	var aliases []string
	for _, m := range models {
		if m.Servable && len(m.Aliases) > 0 && slices.Contains(m.Profiles, "video") {
			aliases = append(aliases, m.Aliases[0])
		}
	}
	slices.Sort(aliases)
	return aliases
}

// videoNotSetUp is the sentence while no local video model is the person's.
const videoNotSetUp = "Video is not set up yet. Setup installs a video model; or name a served video model with model."

// videoHosted is the sentence while the person's video policy names a hosted
// model.
const videoHosted = "Video is set to a hosted model, which this tool does not use yet."

func videoCandidate(read inferenceclient.SelectionReadV2) (string, string) {
	switch read.Outcome {
	case inferenceclient.SelectionReadOutcomeFound:
	case inferenceclient.SelectionReadOutcomeUnknown:
		return "", videoNotSetUp
	default:
		return "", fmt.Sprintf("OpenAbstractions could not read the video setting: %s.", read.Outcome)
	}
	if read.Policy == nil || len(read.Policy.Candidates) == 0 || read.Policy.Candidates[0].Model == "" {
		return "", videoNotSetUp
	}
	first := read.Policy.Candidates[0]
	if first.Placement != inferenceclient.CandidatePlacementLocal {
		return "", videoHosted
	}
	return first.Model, ""
}
