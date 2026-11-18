package gateway

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

// naiveAgentRuntime starts the fixture runtime whose job admission routes
// each job through its router, as the inference job executor does.
func naiveAgentRuntime(t *testing.T, name string) *OA {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its existing Darwin ceiling")
	}
	if _, ok := modelhost.PinnedVideoEngine(runtime.GOOS, runtime.GOARCH); !ok {
		t.Skip("LTX-2.5 has a reviewed engine only on Windows x64")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixture.NewConfig(name)
	cfg.JobRoot = filepath.Join(t.TempDir(), "jobs")
	cfg.Allowed = []string{exe}
	cfg.Upstream = "http://127.0.0.1:1"
	cfg.RouteJobs = true
	run := fixture.Start(t, cfg)
	t.Cleanup(func() { run.Stop(t) })
	oa, err := NewOA(facade.New(cfg.RuntimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return oa
}

// backendOnly lists the base tools: no oa_setup_prepare, as Claude's helper.
type backendOnly struct{ Backend }

// R31 naive agent, 2026-11-12: a video submit for LTX-2.5 on a computer
// without it answered invalid host:router:not-here and diagnose said only
// that no snapshot was available. The submit now says in one sentence that
// LTX-2.5 isn't set up and what sets it up, naming oa_setup_prepare only
// where the connection lists it; the machine word stays in reason, and
// diagnose of the refused handle repeats the sentence. Pictures and chat
// carry their sentence through the same mapping.
func TestRefusedSubmitSaysPlainlyWhatIsNotSetUp(t *testing.T) {
	oa := naiveAgentRuntime(t, "naive-refusal")
	submit := map[string]any{"request_key": "naive-video", "profile": "video", "model": "ltx-2.5", "prompt": "a fox in snow", "hosting": "local", "duration_ms": 1000}
	notSetUp := "LTX-2.5 isn't set up on this computer. Set it up from the Catalog in OpenAbstractions Panel (about 41 GB download)."

	_, withSetup := connected(t, oa, "naive-opencode", "2025-11-25")
	_, out := call[JobSubmitOutput](t, withSetup, ToolJobSubmit, submit)
	if out.Outcome != "invalid" || !strings.HasPrefix(out.Reason, "host:router:") || !strings.HasPrefix(out.Message, notSetUp) ||
		!strings.Contains(out.Message, "oa_setup_prepare with use video") {
		t.Fatalf("submit %+v", out)
	}
	_, diagnosis := call[JobDiagnosisOutput](t, withSetup, ToolJobDiagnose, map[string]any{"handle": out.Handle})
	if diagnosis.Reason != out.Reason || diagnosis.Guidance != out.Message {
		t.Fatalf("diagnose %+v", diagnosis)
	}

	_, plain := connected(t, backendOnly{oa}, "naive-claude", "2025-11-25")
	submit["request_key"] = "naive-video-claude"
	_, out = call[JobSubmitOutput](t, plain, ToolJobSubmit, submit)
	if out.Message != notSetUp {
		t.Fatalf("submit without the setup tool %+v", out)
	}

	picture, err := oa.Submit(withSetupTool(context.Background(), true), JobSubmitInput{RequestKey: "naive-picture", Profile: "image_batch", Model: "z-image-turbo",
		Prompt: "a fox", Hosting: "local", Count: 1})
	if err != nil || picture.Outcome != "invalid" || !strings.HasPrefix(picture.Message, "Z-Image-Turbo isn't set up on this computer. Set it up from the Catalog in OpenAbstractions Panel (about") ||
		!strings.Contains(picture.Message, "oa_setup_prepare with use image") {
		t.Fatalf("picture %+v %v", picture, err)
	}

	_, chat := call[CompleteOutput](t, withSetup, ToolComplete, map[string]any{"model": "qwen-not-here", "prompt": "hi", "hosting": "local"})
	t.Logf("chat %s: %s", chat.Reason, chat.Message)
	if chat.State != "ended" || chat.Reason != "router:not-here" || !strings.HasPrefix(chat.Message, "qwen-not-here isn't set up on this computer for chat.") {
		t.Fatalf("chat %+v", chat)
	}
}

// R31 naive agent, 2026-11-12: oa_models_list named nothing about video, so
// an agent could not discover it. The list now carries the reviewed sets
// Setup sets up here as setup_needed entries with their profile, download
// and preset times, never servable.
func TestModelsListShowsWhatSetupCanSetUp(t *testing.T) {
	oa := naiveAgentRuntime(t, "naive-models")
	_, session := connected(t, oa, "naive-models", "2025-11-25")
	_, listed := call[ModelsOutput](t, session, ToolModels, map[string]any{})
	at := slices.IndexFunc(listed.Models, func(m Model) bool { return m.Family == "ltx-2.5" })
	if at < 0 {
		t.Fatalf("no LTX-2.5 in %+v", listed.Models)
	}
	ltx := listed.Models[at]
	if !ltx.SetupNeeded || ltx.Servable || ltx.Name != "LTX-2.5" || !slices.Equal(ltx.Profiles, []string{"video"}) || ltx.SetupUse != "video" ||
		ltx.DownloadBytes != modelhost.LTX25.Size() || len(ltx.PresetTimes) != 2 || ltx.PresetTimes[1] != (PresetTime{Size: "1280x704", Frames: 121, Seconds: 903}) {
		t.Fatalf("LTX-2.5 entry %+v", ltx)
	}
	for _, m := range listed.Models {
		if m.SetupNeeded && (m.Servable || len(m.Aliases) != 0) {
			t.Fatalf("a setup entry reads servable %+v", m)
		}
	}
}
