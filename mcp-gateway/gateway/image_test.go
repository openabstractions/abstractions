package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"

	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
	contentwire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// pictureBytes is a PNG-signed picture and its digest.
func pictureBytes(seed string) ([]byte, string) {
	data := append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, []byte(seed)...)
	sum := sha256.Sum256(data)
	return data, "sha256:" + hex.EncodeToString(sum[:])
}

// fakePictures is a fake job backend: every accepted job finishes when ready
// says so, with one PNG delivery. Each picture call gets its own handle, and a
// permission retry keeps it.
type fakePictures struct {
	mu        sync.Mutex
	folder    string
	refusal   error
	data      []byte
	digest    string
	ready     atomic.Bool
	refuseFor int // submissions answered with a first-use refusal
	submitted []JobSubmitInput
	handles   map[string]string // request key to handle
	policies  []string
	inputs    map[string]string // digest to media type; absent is not_found
	sniffed   []string
	models    atomic.Int32
	reads     atomic.Int32
	records   map[string][2]string
	now       time.Time
}

func newFakePictures(t *testing.T) *fakePictures {
	data, digest := pictureBytes("fox")
	f := &fakePictures{folder: filepath.Join(t.TempDir(), "Pictures"), data: data, digest: digest, now: time.Date(2026, 11, 10, 14, 5, 9, 0, time.Local),
		handles: map[string]string{}, inputs: map[string]string{}, records: map[string][2]string{}}
	if err := os.MkdirAll(f.folder, 0o700); err != nil {
		t.Fatal(err)
	}
	f.ready.Store(true)
	return f
}

func (f *fakePictures) steps() pictureSteps {
	return pictureSteps{
		folder: func() (string, error) { return f.folder, f.refusal },
		model: func(_ context.Context, policy string) (string, *ImageOutput, error) {
			f.models.Add(1)
			f.mu.Lock()
			f.policies = append(f.policies, policy)
			f.mu.Unlock()
			return "starter-picture", nil, nil
		},
		inputType: func(_ context.Context, digest string) (string, *ImageOutput, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.sniffed = append(f.sniffed, digest)
			media, ok := f.inputs[digest]
			if !ok {
				return "", &ImageOutput{Outcome: "not_found", Message: "Input image unavailable: not_found."}, nil
			}
			return media, nil, nil
		},
		submit: func(_ context.Context, in JobSubmitInput) (JobSubmitOutput, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.submitted = append(f.submitted, in)
			handle, ok := f.handles[in.RequestKey]
			if !ok {
				handle = fmt.Sprintf("%032d", len(f.handles)+1)
				f.handles[in.RequestKey] = handle
			}
			if len(f.submitted) <= f.refuseFor {
				return JobSubmitOutput{Handle: handle, Outcome: "forbidden", Reason: "rights:not_granted rights:question_pending:q1"}, nil
			}
			return JobSubmitOutput{Handle: handle, Outcome: "accepted"}, nil
		},
		status: func(_ context.Context, handle string) (JobStatusOutput, error) {
			if !f.ready.Load() {
				return JobStatusOutput{Outcome: "observed", State: "running", Progress: map[string]int64{"done": 5, "total": 9}}, nil
			}
			return JobStatusOutput{Outcome: "observed", State: "complete", ResultOutcome: "data", Result: &jobwire.JobResult{
				Profile: jobwire.ProfileImageBatch, Host: "pytorch-image", Model: "starter-picture",
				Deliveries: []jobwire.Delivery{{Digest: f.digest, MediaType: "image/png", Size: int64(len(f.data)), Location: "local"}}}}, nil
		},
		read: func(context.Context, jobwire.Delivery) ([]byte, error) {
			f.reads.Add(1)
			return f.data, nil
		},
		now: func() time.Time { return f.now },
		saved: func(handle string) (string, string, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			record, ok := f.records[handle]
			return record[0], record[1], ok
		},
		remember: func(handle, path, digest string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.records[handle] = [2]string{path, digest}
		},
	}
}

// pictureBackend serves the ordinary fixture tools plus a picture tool over
// the fake job backend.
type pictureBackend struct {
	*fixtureBackend
	pictures  *fakePictures
	awaits    atomic.Int32
	listReads atomic.Int32
	answer    string
	image     bool
	servedErr error
}

func (b *pictureBackend) GenerateImage(ctx context.Context, in ImageInput) (ImageOutput, error) {
	return makePicture(ctx, b.pictures.steps(), in)
}

func (b *pictureBackend) ServesPictures(context.Context) (bool, error) {
	b.listReads.Add(1)
	return b.image, b.servedErr
}

func (b *pictureBackend) AwaitPermissionQuestions(ctx context.Context, ids []string) (string, error) {
	b.awaits.Add(1)
	if b.answer == "" {
		return "waiting", nil
	}
	return b.answer, ctx.Err()
}

func newPictureBackend(t *testing.T) *pictureBackend {
	return &pictureBackend{fixtureBackend: &fixtureBackend{principal: "a", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}},
		pictures: newFakePictures(t), image: true}
}

func savedFiles(t *testing.T, folder string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(folder, pictureFolderName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestPicturePromptOnlySavesOneFileAndAnswersItsPath(t *testing.T) {
	b := newPictureBackend(t)
	_, cs := connected(t, b, "picture-client", "2026-07-28")
	result, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox"})
	if result.IsError || out.Outcome != "saved" || out.Digest != b.pictures.digest {
		t.Fatalf("%+v %+v", result, out)
	}
	want := filepath.Join(b.pictures.folder, pictureFolderName, "image-20261110-140509.png")
	if real, _ := filepath.EvalSymlinks(b.pictures.folder); out.Path != filepath.Join(real, pictureFolderName, "image-20261110-140509.png") && out.Path != want {
		t.Fatalf("path %q", out.Path)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; text != "Saved to "+out.Path+". To edit it, pass inputImage "+out.Digest+"." || text != out.Message {
		t.Fatalf("answer %q", text)
	}
	saved, err := os.ReadFile(out.Path)
	if err != nil || string(saved) != string(b.pictures.data) {
		t.Fatal("saved bytes", err)
	}
	submitted := b.pictures.submitted
	if len(submitted) != 1 || submitted[0].Profile != "image_batch" || submitted[0].Model != "starter-picture" || submitted[0].Hosting != "local" ||
		submitted[0].Credential != "" || submitted[0].Size != "auto" || submitted[0].Count != 1 || !strings.HasPrefix(submitted[0].RequestKey, "picture-") {
		t.Fatalf("submitted %+v", submitted)
	}
	if b.pictures.models.Load() != 1 {
		t.Fatal("the picture policy was not read")
	}
	// A named model is used as it is, without reading the policy.
	_, named := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox", "model": "other", "size": "512x512"})
	if named.Outcome != "saved" || b.pictures.models.Load() != 1 || b.pictures.submitted[1].Model != "other" || b.pictures.submitted[1].Size != "512x512" {
		t.Fatalf("named model %+v %+v", named, b.pictures.submitted)
	}
	if names := savedFiles(t, b.pictures.folder); !slices.Equal(names, []string{"image-20261110-140509-2.png", "image-20261110-140509.png"}) {
		t.Fatalf("files %v", names)
	}
}

func TestPictureParallelCallsSaveThreeFiles(t *testing.T) {
	b := newPictureBackend(t)
	_, cs := connected(t, b, "picture-client", "2026-07-28")
	var wg sync.WaitGroup
	paths := make([]string, 3)
	errs := make([]error, 3)
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolImageGenerate, Arguments: map[string]any{"prompt": "a fox"}})
			if err == nil && r.IsError {
				err = errors.New(r.Content[0].(*mcp.TextContent).Text)
			}
			if err == nil {
				paths[i] = r.StructuredContent.(map[string]any)["path"].(string)
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(paths)
	if len(slices.Compact(paths)) != 3 {
		t.Fatalf("paths %v", paths)
	}
	if names := savedFiles(t, b.pictures.folder); len(names) != 3 {
		t.Fatalf("files %v", names)
	}
	keys := map[string]bool{}
	for _, in := range b.pictures.submitted {
		keys[in.RequestKey] = true
	}
	if len(keys) != 3 {
		t.Fatal("parallel pictures shared a request key")
	}
}

func TestPictureWithoutAPicturesFolderStartsNothing(t *testing.T) {
	b := newPictureBackend(t)
	b.pictures.refusal = &layout.Refusal{Reason: layout.NoProfileFolder, Detail: "switched off"}
	_, cs := connected(t, b, "picture-client", "2026-07-28")
	result, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox"})
	if !result.IsError || out.Outcome != "no_pictures_folder" || out.Reason != "layout:no_profile_folder" || !strings.Contains(out.Message, "no Pictures folder (no_profile_folder)") {
		t.Fatalf("%+v", out)
	}
	if b.pictures.models.Load() != 0 || len(b.pictures.submitted) != 0 || b.awaits.Load() != 0 {
		t.Fatal("a call with no Pictures folder asked or started something")
	}
	// A resolved folder that does not exist is refused as well.
	b.pictures.refusal = nil
	b.pictures.folder = filepath.Join(t.TempDir(), "gone")
	if _, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox"}); out.Outcome != "no_pictures_folder" || len(b.pictures.submitted) != 0 {
		t.Fatalf("%+v", out)
	}
}

func TestPictureFirstUseAllowWithinTheWaitSucceeds(t *testing.T) {
	b := newPictureBackend(t)
	b.pictures.refuseFor = 1
	b.answer = "allowed"
	_, cs := connected(t, b, "picture-client", "2026-07-28")
	result, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox"})
	if result.IsError || out.Outcome != "saved" || b.awaits.Load() != 1 {
		t.Fatalf("%+v awaits %d", out, b.awaits.Load())
	}
	submitted := b.pictures.submitted
	if len(submitted) != 2 || submitted[0].RequestKey != submitted[1].RequestKey {
		t.Fatalf("the retry after Allow changed the request key: %+v", submitted)
	}
	if names := savedFiles(t, b.pictures.folder); len(names) != 1 {
		t.Fatalf("files %v", names)
	}
	// A person who has not answered yet leaves the existing waiting result.
	b.answer = ""
	b.pictures.refuseFor = 10
	result, _ = call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox"})
	if state, _ := result.StructuredContent.(map[string]any); state["outcome"] != "awaiting_permission" {
		t.Fatalf("%+v", result.StructuredContent)
	}
}

func TestPictureSlowerThanTheWaitAnswersAHandleToCallAgainWith(t *testing.T) {
	b := newPictureBackend(t)
	b.pictures.ready.Store(false)
	_, cs := connected(t, b, "picture-client", "2026-07-28")
	started := time.Now()
	result, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox", "wait_ms": 1000})
	if result.IsError || out.Outcome != "working" || out.Handle == "" || time.Since(started) > 3*time.Second {
		t.Fatalf("%+v after %s", out, time.Since(started))
	}
	if out.Message != `Making the picture: step 5 of 9, 1 s so far. To wait for this picture, call oa_image_generate with {"handle":"`+out.Handle+`"}.` {
		t.Fatalf("answer %q", out.Message)
	}
	if names := savedFiles(t, b.pictures.folder); len(names) != 0 {
		t.Fatalf("an unfinished picture saved %v", names)
	}
	b.pictures.ready.Store(true)
	result, again := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"handle": out.Handle})
	if result.IsError || again.Outcome != "saved" || len(b.pictures.submitted) != 1 {
		t.Fatalf("%+v submitted %d", again, len(b.pictures.submitted))
	}
	if _, err := os.Stat(again.Path); err != nil {
		t.Fatal(err)
	}
	// The same handle again answers the file already saved.
	_, repeat := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"handle": out.Handle})
	if repeat.Outcome != "saved" || repeat.Path != again.Path || repeat.Message != again.Message {
		t.Fatalf("repeat %+v, first %+v", repeat, again)
	}
	if names := savedFiles(t, b.pictures.folder); len(names) != 1 || b.pictures.reads.Load() != 1 {
		t.Fatalf("a repeated handle saved again: %v, reads %d", names, b.pictures.reads.Load())
	}
}

func TestPictureBadArgumentsAreRefusedAtOnceWithTheirValues(t *testing.T) {
	b := newPictureBackend(t)
	_, cs := connected(t, b, "picture-client", "2026-07-28")
	for _, c := range []struct {
		args    map[string]any
		field   string
		message string
	}{
		{map[string]any{}, "prompt", "prompt is required: describe the picture."},
		{map[string]any{"prompt": "  "}, "prompt", "prompt is required: describe the picture."},
		{map[string]any{"prompt": "a fox", "count": 2}, "count", "Unknown argument count; allowed: prompt, size, model, selectionPolicy, inputImage, mask, handle, wait_ms."},
		{map[string]any{"prompt": "a fox", "size": "big"}, "size", "size must be auto or WIDTHxHEIGHT such as 512x512; leave it out for auto."},
		{map[string]any{"prompt": "a fox", "model": ""}, "model", "model must be a model name; leave it out for the person's picture model."},
		{map[string]any{"prompt": "a fox", "handle": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "prompt", fmt.Sprintf(pictureHandleUse, "prompt")},
		{map[string]any{"handle": "short"}, "handle", "handle must be the handle an earlier oa_image_generate call answered with."},
		{map[string]any{"prompt": "a fox", "selectionPolicy": "Not A Name"}, "selectionPolicy", "selectionPolicy must be a saved policy name; leave it out for the person's picture model."},
		{map[string]any{"prompt": "a fox", "model": "paint", "selectionPolicy": "pictures"}, "selectionPolicy", "Give model or selectionPolicy, not both; leave both out for the person's picture model."},
		{map[string]any{"prompt": "a fox", "inputImage": "C:/cat.png"}, "inputImage", "inputImage must be an OA image digest sha256:<64 hex>."},
		{map[string]any{"prompt": "a fox", "mask": "sha256:" + strings.Repeat("a", 64)}, "mask", "mask must be a sha256 digest and goes only with inputImage."},
	} {
		result, out := call[ImageOutput](t, cs, ToolImageGenerate, c.args)
		if !result.IsError || out.Outcome != "invalid" || out.Field != c.field || out.Message != c.message {
			t.Fatalf("%v: %+v", c.args, out)
		}
		if c.field == "size" && !slices.Equal(out.Allowed, []string{"auto", "WIDTHxHEIGHT"}) || c.field == "count" && !slices.Equal(out.Allowed, imageArguments) {
			t.Fatalf("%v: allowed %v", c.args, out.Allowed)
		}
	}
	if len(b.pictures.submitted) != 0 || b.pictures.models.Load() != 0 {
		t.Fatal("a bad argument started work")
	}
}

func TestPictureFailingVerificationWritesNoFile(t *testing.T) {
	f := newFakePictures(t)
	f.data = append([]byte(nil), f.data...)
	f.data[len(f.data)-1] ^= 1 // the digest names other bytes
	out, err := makePicture(context.Background(), f.steps(), ImageInput{Prompt: "a fox", Size: "auto"})
	if err != nil || out.Outcome != "invalid" || out.Path != "" {
		t.Fatalf("%+v %v", out, err)
	}
	if names := savedFiles(t, f.folder); len(names) != 0 {
		t.Fatalf("files %v", names)
	}
}

func TestPictureFailureNamesTheSizesTheEngineMakes(t *testing.T) {
	out := pictureFailure("1024x1024", "unsupported_feature engine:size:512x512")
	if out.Outcome != "invalid" || out.Field != "size" || !slices.Equal(out.Allowed, []string{"auto", "512x512"}) ||
		out.Message != "This picture model does not make 1024x1024. Allowed sizes: auto, 512x512. Leave size out for auto." {
		t.Fatalf("%+v", out)
	}
	if out := pictureFailure("auto", "engine stopped"); out.Outcome != "failed" || out.Message != "The picture could not be made: engine stopped." {
		t.Fatalf("%+v", out)
	}
}

func TestPicturePolicyAnswers(t *testing.T) {
	local := inferenceclient.SelectionReadV2{Outcome: inferenceclient.SelectionReadOutcomeFound, Policy: &inferenceclient.SelectionPolicyV2{Name: picturePolicy,
		Candidates: []inferenceclient.SelectionCandidateV2{{Model: "starter-picture", Placement: inferenceclient.CandidatePlacementLocal}}}}
	if model, answer := pictureCandidate(picturePolicy, local); model != "starter-picture" || answer != nil {
		t.Fatal(model, answer)
	}
	if _, answer := pictureCandidate(picturePolicy, inferenceclient.SelectionReadV2{Outcome: inferenceclient.SelectionReadOutcomeUnknown}); answer == nil || answer.Message != "Pictures are not set up yet." {
		t.Fatal(answer)
	}
	if _, answer := pictureCandidate("pictures", inferenceclient.SelectionReadV2{Outcome: inferenceclient.SelectionReadOutcomeUnknown}); answer == nil || answer.Message != "The saved policy pictures names no picture model." {
		t.Fatal(answer)
	}
	hosted := local
	hosted.Policy = &inferenceclient.SelectionPolicyV2{Name: picturePolicy, Candidates: []inferenceclient.SelectionCandidateV2{{Model: "flux", Placement: inferenceclient.CandidatePlacementHosted, Credential: "fal"}}}
	if _, answer := pictureCandidate(picturePolicy, hosted); answer == nil || answer.Message != "Pictures are set to a hosted model, which this tool does not use yet." {
		t.Fatal(answer)
	}
}

func TestToolVisibilityFollowsThePicturePolicy(t *testing.T) {
	// The follow-up job tools stay listed; oa_inference_job_submit is never
	// listed (architect, 2026-11-12).
	jobTools := []string{ToolJobCancel, ToolJobDiagnose, ToolJobStatus}
	for _, c := range []struct {
		name   string
		image  bool
		err    error
		listed bool
	}{
		{"policy names a local model", true, nil, true},
		{"no picture policy", false, nil, false},
		{"unanswered", false, errors.New("runtime away"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newPictureBackend(t)
			b.image, b.servedErr = c.image, c.err
			_, cs := connected(t, b, "visibility-client", "2026-07-28")
			for range 2 {
				listed, err := cs.ListTools(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, tool := range listed.Tools {
					names = append(names, tool.Name)
				}
				if slices.Contains(names, ToolImageGenerate) != c.listed {
					t.Fatalf("picture tool listed=%t in %v", !c.listed, names)
				}
				for _, name := range jobTools {
					if !slices.Contains(names, name) {
						t.Fatalf("%s is not listed", name)
					}
				}
				if slices.Contains(names, ToolJobSubmit) {
					t.Fatal("oa_inference_job_submit is listed")
				}
			}
			if b.listReads.Load() != 1 {
				t.Fatalf("the picture policy was read %d times in one session", b.listReads.Load())
			}
			// A call answers for itself, listed or not.
			_, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox"})
			if out.Outcome != "saved" {
				t.Fatalf("%+v", out)
			}
		})
	}
}

// Listing tools never raises a question: with no rules, a session's
// tools/list reads only the picture policy, which consults no right, and
// starts, asks and waits for nothing.
func TestToolsListWithNoRulesRaisesNoQuestion(t *testing.T) {
	b := newPictureBackend(t)
	b.image = false
	_, cs := connected(t, b, "no-rules-client", "2026-07-28")
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if b.listReads.Load() != 1 || b.awaits.Load() != 0 || b.providerEffects != 0 || b.pictures.models.Load() != 0 ||
		len(b.pictures.submitted) != 0 || len(b.pictures.sniffed) != 0 {
		t.Fatalf("tools/list did more than read the picture policy: reads %d awaits %d effects %d", b.listReads.Load(), b.awaits.Load(), b.providerEffects)
	}
	// The OA backend's own read is the policy read alone: its listing read is
	// ServesPictures, built on readPolicy (selection-operator@2 ReadEffective).
	var listing picturesBackend = &OA{}
	if _, ok := listing.(interface {
		ServedWork(context.Context) (bool, bool, error)
	}); ok {
		t.Fatal("the OA backend still offers a router read for listing")
	}
}

// The job tools and the picture tool carry the final descriptions of ruling 4.
func TestPictureAndJobToolsCarryTheRulingDescriptions(t *testing.T) {
	b := newPictureBackend(t)
	_, cs := connected(t, b, "description-client", "2026-07-28")
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		ToolImageGenerate: "Make one picture from a prompt and save it as a file, answering its path. When outcome is working, follow the exact call in message using the returned handle. Waits wait_ms (default 20000; Codex ends a call at 60 s), then answers with progress and a handle to call again with.",
		ToolVideoGenerate: videoDescription,
		ToolJobStatus:     jobStatusDescription,
		ToolJobDiagnose:   "Say why a video job failed, by its handle.",
		ToolJobCancel:     "Stop a video job by its handle.",
	}
	for _, tool := range listed.Tools {
		if description, ok := want[tool.Name]; ok {
			if tool.Description != description {
				t.Fatalf("%s: %q", tool.Name, tool.Description)
			}
			delete(want, tool.Name)
		}
		if tool.Name == ToolImageGenerate && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.IdempotentHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint) {
			t.Fatalf("picture tool is not the effect class: %+v", tool.Annotations)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing %v", want)
	}
}

// Edits go through the same durable job and save into Pictures: the inputs'
// media types come from their first bytes, a mask must be PNG, and the job
// document carries edit mode with both digests.
func TestPictureEditRunsTheSameJobAndSavesToPictures(t *testing.T) {
	b := newPictureBackend(t)
	jpeg := "sha256:" + strings.Repeat("1", 64)
	mask := "sha256:" + strings.Repeat("2", 64)
	b.pictures.inputs[jpeg], b.pictures.inputs[mask] = "image/jpeg", "image/png"
	_, cs := connected(t, b, "edit-client", "2026-07-28")
	result, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "snow on the rocks", "inputImage": jpeg, "mask": mask, "selectionPolicy": "pictures"})
	if result.IsError || out.Outcome != "saved" || !strings.Contains(out.Path, pictureFolderName) {
		t.Fatalf("%+v", out)
	}
	submitted := b.pictures.submitted[0]
	if submitted.InputImage != jpeg || submitted.InputMediaType != "image/jpeg" || submitted.Mask != mask || submitted.MaskMediaType != "image/png" ||
		!slices.Equal(b.pictures.policies, []string{"pictures"}) {
		t.Fatalf("submitted %+v policies %v", submitted, b.pictures.policies)
	}
	document, err := jobwire.Decode(jobSpec(submitted))
	if err != nil || document.Request.Image.Mode != jobwire.ModeEdit || document.Request.Image.ImageDigest != jpeg ||
		document.Request.Image.ImageMediaType != "image/jpeg" || document.Request.Image.MaskDigest != mask {
		t.Fatalf("job document %+v %v", document.Request, err)
	}
	// A JPEG mask, an unreadable input and a non-picture input stop before any job.
	b.pictures.inputs[mask] = "image/jpeg"
	if _, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "x", "inputImage": jpeg, "mask": mask}); out.Outcome != "invalid" || out.Message != "The mask must be a PNG image." {
		t.Fatalf("%+v", out)
	}
	if _, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "x", "inputImage": "sha256:" + strings.Repeat("3", 64)}); out.Outcome != "not_found" || out.Message != "Input image unavailable: not_found." {
		t.Fatalf("%+v", out)
	}
	b.pictures.inputs[jpeg] = ""
	if _, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "x", "inputImage": jpeg}); out.Outcome != "invalid" || out.Message != "The input image must be a PNG, JPEG or WebP image." {
		t.Fatalf("%+v", out)
	}
	if len(b.pictures.submitted) != 1 {
		t.Fatalf("a refused edit started a job: %d", len(b.pictures.submitted))
	}
	// The job submit tool takes no edit input of its own.
	for _, field := range []string{"input_image", "input_media_type", "mask", "mask_media_type"} {
		if _, ok := jobSubmitSchema().Properties[field]; ok {
			t.Fatalf("job submit offers %s", field)
		}
	}
}

// fakeContent is a content reader holding digests' bytes.
type fakeContent struct {
	bytes  map[string][]byte
	closed int
}

func (f *fakeContent) Open(_ context.Context, digest string) (contentwire.OpenResult, error) {
	data, ok := f.bytes[digest]
	if !ok {
		return contentwire.OpenResult{Outcome: contentwire.OpenOutcomeForbidden}, nil
	}
	return contentwire.OpenResult{Outcome: contentwire.OpenOutcomeOpened, Resource: &contentwire.Resource{Handle: digest, Digest: digest, Size: int64(len(data))}}, nil
}

func (f *fakeContent) Read(_ context.Context, resource contentwire.Resource, offset, limit int64) (contentwire.ReadResult, error) {
	data := f.bytes[resource.Handle][offset:]
	data = data[:min(int64(len(data)), limit)]
	return contentwire.ReadResult{Outcome: contentwire.ReadOutcomeData, Chunk: &contentwire.Chunk{Offset: offset, Total: resource.Size, Data: data, EOF: offset+int64(len(data)) == resource.Size}}, nil
}

func (f *fakeContent) Close(context.Context, contentwire.Resource) (contentwire.CloseResult, error) {
	f.closed++
	return contentwire.CloseResult{}, nil
}

func TestEditInputTypeComesFromItsFirstBytes(t *testing.T) {
	png, pngDigest := pictureBytes("mask")
	reader := &fakeContent{bytes: map[string][]byte{pngDigest: png, "sha256:jpeg": {255, 216, 255, 224, 0, 16}, "sha256:text": []byte("hello")}}
	for digest, want := range map[string]string{pngDigest: "image/png", "sha256:jpeg": "image/jpeg", "sha256:text": ""} {
		media, answer, err := sniffContent(context.Background(), reader, digest)
		if err != nil || answer != nil || media != want {
			t.Fatalf("%s: %q %+v %v", digest, media, answer, err)
		}
	}
	if _, answer, _ := sniffContent(context.Background(), reader, "sha256:other"); answer == nil || answer.Outcome != "forbidden" {
		t.Fatalf("%+v", answer)
	}
	if reader.closed != 3 {
		t.Fatalf("closed %d handles", reader.closed)
	}
}

// A saved-picture record answers only while its file holds the picture.
func TestSavedRecordFollowsItsFile(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "Pictures")
	if err := os.MkdirAll(filepath.Join(folder, pictureFolderName), 0o700); err != nil {
		t.Fatal(err)
	}
	data, digest := pictureBytes("fox")
	picture := filepath.Join(folder, pictureFolderName, "West pier Łódź (2).png")
	if err := os.WriteFile(picture, data, 0o600); err != nil {
		t.Fatal(err)
	}
	o := &OA{stateDir: dir, picturesAt: func() (string, error) { return folder, nil }}
	handle := strings.Repeat("A", 32)
	o.rememberPicture(handle, picture, digest)
	if path, got, ok := o.savedPicture(handle); !ok || path != picture || got != digest {
		t.Fatal(path, got, ok)
	}
	if err := os.WriteFile(picture, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := o.savedPicture(handle); ok {
		t.Fatal("a changed file still answers as the saved picture")
	}
}

// A record whose name is no longer a safe file name is refused by both readers.
func TestSavedRecordRefusesTamperedName(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "Pictures")
	savedFolder := filepath.Join(folder, pictureFolderName)
	if err := os.MkdirAll(savedFolder, 0o700); err != nil {
		t.Fatal(err)
	}
	data, digest := pictureBytes("fox")
	outside := filepath.Join(dir, "x")
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	const leaf = "West pier Łódź (2).png"
	if err := os.WriteFile(filepath.Join(savedFolder, leaf), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		// POSIX can actually store these names. Saved records still use
		// Identity's portable file-name grammar.
		for _, name := range []string{leaf + ".", leaf + " ", "CON.png"} {
			if err := os.WriteFile(filepath.Join(savedFolder, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	o := &OA{stateDir: dir, picturesAt: func() (string, error) { return folder, nil }}
	handle := strings.Repeat("B", 32)
	record, ok := o.savedPath(handle)
	if !ok {
		t.Fatal("no record path")
	}
	for _, name := range []string{`..\x`, "../x", outside, ".", "..", "", `C:picture.png`,
		"CON.png", "LPT¹.png", leaf + ":stream", leaf + ".", leaf + " ", leaf + "\x00"} {
		raw, _ := json.Marshal(savedRecord{Name: name, Digest: digest})
		if err := os.WriteFile(record, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if path, _, ok := o.savedPicture(handle); ok {
			t.Fatalf("name %q answered %s", name, path)
		}
		if path, ok := savedVideo(folder, record, digest); ok {
			t.Fatalf("video name %q answered %s", name, path)
		}
	}
}

// A junction at OpenAbstractions cannot carry the picture out of Pictures.
func TestPictureJunctionIsNotFollowed(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows link")
	}
	dir := t.TempDir()
	folder := filepath.Join(dir, "Pictures")
	outside := filepath.Join(dir, "elsewhere")
	for _, d := range []string{folder, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(folder, pictureFolderName), outside).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	f := newFakePictures(t)
	f.folder = folder
	out, err := makePicture(context.Background(), f.steps(), ImageInput{Prompt: "a fox", Size: "auto"})
	if err != nil || out.Outcome != "not_saved" || out.Path != "" {
		t.Fatalf("%+v %v", out, err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside holds %v %v", entries, err)
	}
}

// J-PIC on 2026-11-10: a picture policy naming a model no host here serves
// was refused by the executor as invalid host:router:not-here, and the agent
// saw only "did not start the picture: invalid.". The sentence names the
// model; another invalid refusal carries its reason.
func TestPictureRefusalNamesTheUnservedModelOrItsReason(t *testing.T) {
	for _, c := range []struct{ outcome, reason, want string }{
		{"invalid", "host:router:not-here", "No picture model named starter-picture is served on this computer. Setup or Models installs one; leave model out for the person's picture model."},
		{"invalid", "host:router:no-host", "No picture model named starter-picture is served on this computer. Setup or Models installs one; leave model out for the person's picture model."},
		{"invalid", "host:unsupported", "OpenAbstractions did not start the picture: invalid (host:unsupported)."},
		{"invalid", "host:start_failed:lemonade-image", "Lemonade isn't set up on this computer. OpenAbstractions Setup sets it up."},
		{"unavailable", "rights:unavailable", "OpenAbstractions did not start the picture: unavailable."},
	} {
		f := newFakePictures(t)
		steps := f.steps()
		steps.submit = func(context.Context, JobSubmitInput) (JobSubmitOutput, error) {
			return JobSubmitOutput{Handle: "h", Outcome: c.outcome, Reason: c.reason}, nil
		}
		out, err := makePicture(context.Background(), steps, ImageInput{Prompt: "a fox", Size: "auto"})
		if err != nil || out.Outcome != c.outcome || out.Reason != c.reason || out.Message != c.want {
			t.Fatalf("%s %s: %+v %v", c.outcome, c.reason, out, err)
		}
		if names := savedFiles(t, f.folder); len(names) != 0 {
			t.Fatalf("files %v", names)
		}
	}
}

func TestPictureWithoutWaitFollowsTheDefault(t *testing.T) {
	wait := defaultCallWait
	defaultCallWait = 700 * time.Millisecond
	t.Cleanup(func() { defaultCallWait = wait })
	b := newPictureBackend(t)
	b.pictures.ready.Store(false)
	_, cs := connected(t, b, "picture-client", "2026-07-28")
	started := time.Now()
	_, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox"})
	if elapsed := time.Since(started); out.Outcome != "working" || elapsed < defaultCallWait || elapsed > 3*time.Second {
		t.Fatalf("%+v after %s", out, elapsed)
	}
}

// A job slower than the default but within wait_ms saves in one call: the
// fake's two seconds stand for a sixty-second picture.
func TestPictureLongWaitSavesInOneCall(t *testing.T) {
	wait := defaultCallWait
	defaultCallWait = 300 * time.Millisecond
	t.Cleanup(func() { defaultCallWait = wait })
	b := newPictureBackend(t)
	b.pictures.ready.Store(false)
	time.AfterFunc(2*time.Second, func() { b.pictures.ready.Store(true) })
	_, cs := connected(t, b, "picture-client", "2026-07-28")
	result, out := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox", "wait_ms": 10000})
	if result.IsError || out.Outcome != "saved" || out.Path == "" {
		t.Fatalf("%+v", out)
	}
	_, refused := call[ImageOutput](t, cs, ToolImageGenerate, map[string]any{"prompt": "a fox", "wait_ms": -1})
	if refused.Outcome != "invalid" || refused.Field != "wait_ms" {
		t.Fatalf("%+v", refused)
	}
}

func TestPictureProgressNotificationsFollowTheToken(t *testing.T) {
	b := newPictureBackend(t)
	b.pictures.ready.Store(false)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewServer(b).Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []*mcp.ProgressNotificationParams
	client := mcp.NewClient(&mcp.Implementation{Name: "picture-client", Version: "forged"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			seen = append(seen, r.Params)
			mu.Unlock()
		}})
	cs, err := client.Connect(context.Background(), ct, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	params := &mcp.CallToolParams{Name: ToolImageGenerate, Arguments: map[string]any{"prompt": "a fox", "wait_ms": 800}}
	params.SetProgressToken("picture-1")
	if _, err := cs.CallTool(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[0].ProgressToken != "picture-1" || seen[0].Total != 9 || seen[0].Progress != 5 ||
		!strings.HasPrefix(seen[0].Message, "Making the picture: step 5 of 9") {
		t.Fatalf("progress %+v", seen)
	}
}
