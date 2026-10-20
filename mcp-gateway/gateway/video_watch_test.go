package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
)

// scriptedJob answers a job's reads in order and repeats the last answer;
// an answer with an error stands for a runtime that is away.
type scriptedJob struct {
	mu      sync.Mutex
	answers []scriptedRead
	reads   atomic.Int32
}

type scriptedRead struct {
	out    JobStatusOutput
	result *jobwire.JobResult
	err    error
}

func (j *scriptedJob) read(context.Context, JobHandleInput) (JobStatusOutput, *jobwire.JobResult, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := int(j.reads.Add(1)) - 1
	a := j.answers[min(n, len(j.answers)-1)]
	return a.out, a.result, a.err
}

func testClip(t *testing.T) ([]byte, jobwire.Delivery) {
	t.Helper()
	clip := append([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x86, 0x8D}, []byte("A_PCM/INT/LIT")...)
	clip = append(clip, bytes.Repeat([]byte("clip"), 1000)...)
	sum := sha256.Sum256(clip)
	return clip, jobwire.Delivery{Digest: "sha256:" + hex.EncodeToString(sum[:]), MediaType: "video/webm", Size: int64(len(clip)), Location: "local"}
}

func running(waiting string, done, total int64) scriptedRead {
	return scriptedRead{out: JobStatusOutput{Outcome: "observed", State: "running", Waiting: waiting,
		Progress: map[string]int64{"done": done, "total": total}, Phase: jobPhase("running", waiting, done, total, true)}}
}

// A local video job's clip is saved when the job ends, with no status call
// from any agent: a gateway that starts follows the unsaved local video job
// its state names, through a runtime restart that left reads failing and
// the job sent again, and saves the clip once. A later status read answers
// the same path without reading the clip again.
func TestFinishedVideoIsSavedWithoutAStatusCallAcrossARestart(t *testing.T) {
	videoWatchPoll, videoWatchErrorPause = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { videoWatchPoll, videoWatchErrorPause = 5*time.Second, 60*time.Second })
	clip, delivery := testClip(t)
	folder, state := t.TempDir(), t.TempDir()
	handle := strings.Repeat("A", 32)
	record, _ := json.Marshal(handleRecord{Version: handleRecordVersion, Schema: jobSchema, LocalVideo: true})
	if err := os.WriteFile(filepath.Join(state, handle+".json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	// A picture job's handle is left alone.
	other, _ := json.Marshal(handleRecord{Version: handleRecordVersion, Schema: jobSchema})
	if err := os.WriteFile(filepath.Join(state, strings.Repeat("B", 32)+".json"), other, 0o600); err != nil {
		t.Fatal(err)
	}
	result := &jobwire.JobResult{Profile: jobwire.ProfileVideo, Deliveries: []jobwire.Delivery{delivery}}
	job := &scriptedJob{answers: []scriptedRead{
		running("model:loading:40", 0, 0),
		{err: errors.New("the runtime is restarting")},
		{err: errors.New("the runtime is restarting")},
		running(restartingWord, 0, 0),
		running("", 3, 8),
		{out: JobStatusOutput{Outcome: "observed", State: "complete", Phase: "done"}, result: result},
	}}
	var clipReads atomic.Int32
	var mu sync.Mutex
	var seen []string
	o := &OA{stateDir: state, videosAt: func() (string, error) { return folder, nil },
		statusAt: func(ctx context.Context, in JobHandleInput) (JobStatusOutput, *jobwire.JobResult, error) {
			mu.Lock()
			seen = append(seen, in.Handle)
			mu.Unlock()
			return job.read(ctx, in)
		},
		readClipAt: func(context.Context, jobwire.Delivery) ([]byte, error) { clipReads.Add(1); return clip, nil }}
	o.WatchUnsavedVideos()
	saved := filepath.Join(folder, pictureFolderName)
	var path string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		entries, _ := os.ReadDir(saved)
		if len(entries) == 1 {
			path = filepath.Join(saved, entries[0].Name())
			break
		}
	}
	if path == "" || !strings.HasPrefix(filepath.Base(path), "video-") {
		t.Fatalf("no clip was saved after %d reads", job.reads.Load())
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, clip) {
		t.Fatal("the saved file is not the clip")
	}
	mu.Lock()
	for _, h := range seen {
		if h != handle {
			t.Fatalf("the watch read handle %s", h)
		}
	}
	mu.Unlock()
	out, err := o.Status(context.Background(), JobHandleInput{Handle: handle})
	if err != nil || out.Path != path || clipReads.Load() != 1 {
		t.Fatalf("a later status answered %q (%v) after %d clip reads", out.Path, err, clipReads.Load())
	}
	if entries, _ := os.ReadDir(saved); len(entries) != 1 {
		t.Fatalf("%d files saved", len(entries))
	}
}

// Each phase of a video job reads as one short line.
func TestVideoJobPhaseLines(t *testing.T) {
	for _, c := range []struct {
		state, waiting string
		done, total    int64
		want           string
	}{
		{"pending", "activating:video-provider", 0, 0, "preparing the video engine"},
		{"pending", "engine:starting", 0, 0, "starting the video engine"},
		{"running", "model:loading:40", 0, 0, "loading the model"},
		{"running", "model:loading", 0, 0, "loading the model"},
		{"running", "queue:1", 0, 0, "waiting for the video engine"},
		{"running", "queue:3", 0, 0, "waiting for the video engine: place 3 in its queue"},
		{"running", restartingWord, 0, 0, "restarted after an OpenAbstractions update"},
		{"running", "", 3, 8, "generating: step 3 of 8"},
		{"running", "", 37, 100, "generating: 37%"},
		{"complete", "", 1, 1, "done"},
	} {
		if got := jobPhase(c.state, c.waiting, c.done, c.total, true); got != c.want {
			t.Errorf("%s %s %d/%d: %q", c.state, c.waiting, c.done, c.total, got)
		}
	}
}

// videoBackend is a gateway backend whose one video job reads in order.
type videoBackend struct {
	*fixtureBackend
	job       *scriptedJob
	model     string
	submitted []JobSubmitInput
	mu        sync.Mutex
	listReads atomic.Int32
}

func (b *videoBackend) Submit(_ context.Context, in JobSubmitInput) (JobSubmitOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.submitted = append(b.submitted, in)
	return JobSubmitOutput{Handle: strings.Repeat("C", 32), Outcome: "accepted"}, nil
}

func (b *videoBackend) Status(ctx context.Context, in JobHandleInput) (JobStatusOutput, error) {
	out, _, err := b.job.read(ctx, in)
	return out, err
}

func (b *videoBackend) VideoModel(context.Context) (string, string, error) {
	b.listReads.Add(1)
	if b.model == "" {
		return "", videoNotSetUp, nil
	}
	return b.model, "", nil
}

func newVideoBackend(model string, answers ...scriptedRead) *videoBackend {
	return &videoBackend{fixtureBackend: &fixtureBackend{principal: "a", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}},
		job: &scriptedJob{answers: answers}, model: model}
}

// oa_video_generate is listed while the person's video policy names a local
// model, and oa_inference_job_submit is never listed; both stay callable.
func TestVideoToolIsListedWhileVideoIsServed(t *testing.T) {
	for _, model := range []string{"ltx-2.5", ""} {
		b := newVideoBackend(model, running("", 0, 0))
		_, cs := connected(t, b, "video-list-client", "2026-07-28")
		listed, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range listed.Tools {
			names = append(names, tool.Name)
		}
		if slices.Contains(names, ToolVideoGenerate) != (model != "") || slices.Contains(names, ToolJobSubmit) ||
			!slices.Contains(names, ToolJobStatus) || !slices.Contains(names, ToolJobCancel) || !slices.Contains(names, ToolJobDiagnose) {
			t.Fatalf("model %q listed %v", model, names)
		}
		if b.listReads.Load() != 1 {
			t.Fatalf("the video policy was read %d times", b.listReads.Load())
		}
		if _, out := call[JobSubmitOutput](t, cs, ToolJobSubmit, map[string]any{"request_key": "k", "profile": "video", "model": "ltx-2.5", "prompt": "a fox", "hosting": "local", "duration_ms": 1000}); out.Outcome != "accepted" {
			t.Fatalf("unlisted submit: %+v", out)
		}
	}
}

// A video call submits the preset's job and, when the job ends within
// wait_ms, answers the saved path; a job still loading answers its handle
// and phase line when the wait passes, with progress on the way.
func TestVideoToolAnswersThePathOrAHandleWithThePhase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video-1.webm")
	b := newVideoBackend("ltx-2.5", running("activating:video-provider", 0, 0), running("", 2, 8),
		scriptedRead{out: JobStatusOutput{Outcome: "observed", State: "complete", Path: path}})
	_, cs := connected(t, b, "video-client", "2026-07-28")
	_, out := call[VideoOutput](t, cs, ToolVideoGenerate, map[string]any{"prompt": "a fox in snow", "preset": "quality", "wait_ms": 20000})
	if out.Outcome != "saved" || out.Path != path || !strings.Contains(out.Message, path) {
		t.Fatalf("%+v", out)
	}
	if len(b.submitted) != 1 || b.submitted[0].Model != "ltx-2.5" || b.submitted[0].Size != videoQualitySize ||
		b.submitted[0].DurationMS != videoQualityDuration || b.submitted[0].Profile != "video" || b.submitted[0].Hosting != "local" {
		t.Fatalf("submitted %+v", b.submitted)
	}

	loading := newVideoBackend("ltx-2.5", running("activating:video-provider", 0, 0))
	var lines []string
	var mu sync.Mutex
	client := mcp.NewClient(&mcp.Implementation{Name: "progress-client", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			lines = append(lines, r.Params.Message)
			mu.Unlock()
		}})
	st, ct := mcp.NewInMemoryTransports()
	server := NewServer(loading)
	ss, err := server.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	session, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	started := time.Now()
	params := &mcp.CallToolParams{Name: ToolVideoGenerate, Arguments: map[string]any{"prompt": "a fox", "wait_ms": 1500}}
	params.SetProgressToken("p")
	r, err := session.CallTool(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	var working VideoOutput
	raw, _ := json.Marshal(r.StructuredContent)
	if err := json.Unmarshal(raw, &working); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 1400*time.Millisecond || elapsed > 10*time.Second {
		t.Fatalf("the call answered after %s", elapsed)
	}
	if working.Outcome != "working" || working.Handle == "" || working.Phase != "preparing the video engine" || !strings.Contains(working.Message, working.Handle) {
		t.Fatalf("%+v", working)
	}
	if loading.submitted[0].Size != "auto" || loading.submitted[0].DurationMS != videoQuickDuration {
		t.Fatalf("quick preset submitted %+v", loading.submitted[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "preparing the video engine") {
		t.Fatalf("progress %q", lines)
	}
}

// oa_inference_job_status with wait_ms answers when the job ends, before the
// wait passes, and at the wait while it runs; without wait_ms it answers at
// once as before.
func TestJobStatusWaitEndsAtCompletionOrAtTheCap(t *testing.T) {
	handle := strings.Repeat("C", 32)
	done := newVideoBackend("ltx-2.5", running("", 1, 8), running("", 5, 8),
		scriptedRead{out: JobStatusOutput{Outcome: "observed", State: "complete", Path: "x"}})
	_, cs := connected(t, done, "status-client", "2026-07-28")
	started := time.Now()
	_, out := call[JobStatusOutput](t, cs, ToolJobStatus, map[string]any{"handle": handle, "wait_ms": 30000})
	if out.State != "complete" || out.Path != "x" || time.Since(started) > 15*time.Second {
		t.Fatalf("%+v after %s", out, time.Since(started))
	}

	runningJob := newVideoBackend("ltx-2.5", running("", 2, 8))
	_, cs = connected(t, runningJob, "status-client", "2026-07-28")
	started = time.Now()
	_, out = call[JobStatusOutput](t, cs, ToolJobStatus, map[string]any{"handle": handle, "wait_ms": 1200})
	if elapsed := time.Since(started); out.State != "running" || out.Phase != "generating: step 2 of 8" || elapsed < 1100*time.Millisecond || elapsed > 10*time.Second {
		t.Fatalf("%+v after %s", out, elapsed)
	}
	reads := runningJob.job.reads.Load()
	_, out = call[JobStatusOutput](t, cs, ToolJobStatus, map[string]any{"handle": handle})
	if out.State != "running" || runningJob.job.reads.Load() != reads+1 {
		t.Fatalf("a call without wait_ms read %d times", runningJob.job.reads.Load()-reads)
	}

	// Submit with wait_ms follows the accepted job the same way.
	_, submitted := call[JobSubmitOutput](t, cs, ToolJobSubmit, map[string]any{"request_key": "k", "profile": "video", "model": "ltx-2.5", "prompt": "a fox", "hosting": "local", "duration_ms": 1000, "wait_ms": 1000})
	if submitted.Outcome != "accepted" || submitted.Status == nil || submitted.Status.Phase != "generating: step 2 of 8" {
		t.Fatalf("%+v", submitted)
	}
}
