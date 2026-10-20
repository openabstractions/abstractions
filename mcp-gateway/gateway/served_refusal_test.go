package gateway

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

// R34 owner machine, 2026-11-12: OpenCode asked oa_image_generate for
// ltx-2.5 and was told "LTX-2.5 isn't set up on this computer", though
// modelhost declares video:ltx-2.5 and the coordinator's video submit of
// ltx-2.5 ran. The router answered no-host for the picture profile, and
// every not-here or no-host read as "isn't set up". Through the real router
// with modelhost's declared set: the family ltx2.5, the alias ltx-2.5 and
// its title start a video, a set-up model asked through the wrong tool names the
// right call, a near spelling names the model's name, and "isn't set up"
// stays for a model nothing here declares.
func TestRefusalNamesWhereASetUpModelIsServed(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its existing Darwin ceiling")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	declared, err := router.NewNative("modelhost", "unused", listen.ServerExpectation{},
		[]string{"video:ltx-2.5", "chat:qwen3.6-35b-a3b"}, []string{router.ProfileChat, router.ProfileVideo})
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixture.NewConfig("served-refusal")
	cfg.JobRoot = filepath.Join(t.TempDir(), "jobs")
	cfg.Allowed = []string{exe}
	cfg.Upstream = "http://127.0.0.1:1"
	cfg.RouteJobs = true
	cfg.Hosts = []*router.Host{declared}
	run := fixture.Start(t, cfg)
	t.Cleanup(func() { run.Stop(t) })
	oa, err := NewOA(facade.New(cfg.RuntimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, session := connected(t, oa, "naive-opencode", "2025-11-25")
	video := func(key, model string) JobSubmitOutput {
		_, out := call[JobSubmitOutput](t, session, ToolJobSubmit, map[string]any{"request_key": key, "profile": "video", "model": model,
			"prompt": "rain on a tin roof", "hosting": "local", "duration_ms": 1000})
		return out
	}

	for i, name := range []string{"ltx-2.5", "ltx2.5", "LTX-2.5", "LTX 2.5"} {
		if out := video("video-"+name, name); out.Outcome != "accepted" {
			t.Fatalf("video %d %s: %+v", i, name, out)
		}
	}

	picture, err := oa.Submit(context.Background(), JobSubmitInput{RequestKey: "picture-ltx", Profile: "image_batch", Model: "ltx-2.5", Prompt: "rain", Hosting: "local", Count: 1})
	// The reviewed setup offer supplies its display title where available.
	// Else this declaration has no title and the router lists its family.
	display := "ltx2.5"
	if _, hasSetup := modelhost.PinnedVideoEngine(runtime.GOOS, runtime.GOARCH); hasSetup {
		display = "LTX-2.5"
	}
	wantPicture := display + " is a video model. Call oa_video_generate with model ltx-2.5."
	if err != nil || picture.Outcome != "invalid" || picture.Message != wantPicture || pictureRefusal("ltx-2.5", picture) != wantPicture {
		t.Fatalf("picture of a video model %+v %v", picture, err)
	}

	_, chat := call[CompleteOutput](t, session, ToolComplete, map[string]any{"model": "ltx-2.5", "prompt": "hi", "hosting": "local"})
	if chat.State != "ended" || chat.Message != wantPicture {
		t.Fatalf("chat with a video model %+v", chat)
	}

	if out := video("video-qwen", "qwen3.6-35b-a3b"); out.Outcome != "invalid" ||
		out.Message != "qwen3.6-35b-a3b is a chat model. Call oa_inference_complete with model qwen3.6-35b-a3b." {
		t.Fatalf("video of a chat model %+v", out)
	}

	if out := video("video-spelling", "ltx25"); out.Outcome != "invalid" ||
		out.Message != "ltx25 isn't a model name here; ask for ltx-2.5." {
		t.Fatalf("near spelling %+v", out)
	}

	if out := video("video-wan", "wan-2.2"); out.Outcome != "invalid" || !strings.HasPrefix(out.Message, "wan-2.2 isn't set up on this computer for video.") {
		t.Fatalf("model nothing here declares %+v", out)
	}
}
