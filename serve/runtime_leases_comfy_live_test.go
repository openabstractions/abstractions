package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
)

// ComfyUI as an attached holder, asked to yield, live (TODO.md resource §6
// remainder; research/resources/COMFY-CARD-2026-09-22.md's "2026-09-22,
// later" section, which proved this fixture-level with
// TestFixtureComfyUIAttachedHolderIsAskedWhenOverTheFloor and left the live
// proof owed: "The live proof this section owes is the ask reaching ComfyUI
// at all.").
//
// This is that proof: a real ComfyUI loads real weights through one render,
// with no oa_card hook installed and no lease of its own — it holds the card
// the plain way any process does. A second, isolated runtime with no other
// host declared then asks for more of card:0 than is free, with only
// host:comfyui registered to answer for it, and the ask has to reach
// ComfyUI's own /api/free (abstraction-router/go/unload.go's unloadComfyUI)
// for anything to be freed at all.
func TestLiveComfyUIYieldsWhenAskedByAnotherAsker(t *testing.T) {
	if os.Getenv("OA_LIVE_YIELD_COMFY") != "1" {
		t.Skip("set OA_LIVE_YIELD_COMFY=1 to load real weights into a real ComfyUI and ask an isolated runtime to yield them")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	comfy := comfyYieldSetup(t)
	address := freeLoopbackPort(t)
	comfy.start(t, address)

	began := time.Now()
	run := comfyYieldRender(t, comfy.url, 11)
	t.Logf("the render read %s in %s", run.Status.StatusStr, time.Since(began).Round(time.Millisecond))
	if run.Status.StatusStr != "success" || !run.Status.Completed {
		t.Fatalf("the render did not complete: %s %+v", run.Status.StatusStr, run.Errors())
	}
	if len(run.Images()) == 0 {
		t.Fatal("the render completed with no image")
	}
	t.Logf("the render saved %v", run.Images())

	// A second, independent runtime: nothing about ComfyUI's own process
	// knows this one exists, the way a render and the arbiter asking it to
	// yield are two different programs in production.
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	if out, err := runInference(t, "host", "add", "comfyui", "--base", comfy.url,
		"--endpoint", options.endpoint, "--timeout", "60s"); err != nil {
		t.Fatalf("host add comfyui: %v\n%s", err, out)
	}

	call, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	machine := client.New(options.endpoint)
	me := liveSubject(t)
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	setLiveRule(t, call, operator, me, resourceservice.ActionTableRead, resourceservice.ResourceAccount, true)
	setLiveRule(t, call, operator, me, resourceservice.ActionHold, instrument.Card0, true)
	// Installation already permits yielding host:comfyui (TestInstallationWritesTheLeaseRules);
	// written again here so the permit this ask depends on is not merely a
	// default nobody chose. No other unloadable host was ever declared to
	// this runtime, so host:comfyui is the only attached holder Book.order
	// can offer regardless.
	setLiveRule(t, call, operator, me, resourceservice.ActionYield, claimedHostPrefix+"comfyui", true)

	leases, err := machine.ResolveResourceLeases(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	table, err := machine.ResolveResourceTable(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}

	before, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	comfyBefore := liveHeldBy(before, "comfyui")
	t.Logf("card:0 held %.2f GiB, of which ComfyUI's own process holds %.2f GiB",
		float64(before.Held)/(1<<30), float64(comfyBefore)/(1<<30))
	if comfyBefore <= 0 {
		t.Fatalf("the render loaded weights and the table reports no verified row for %s: %+v", comfy.python, before.Holders)
	}
	pool := before.Capacity
	if pool == 0 {
		pool = instrument.MachineMemory()
	}
	if pool <= before.Held {
		t.Fatalf("the pool is %d and %d is held; there is nothing to ask for", pool, before.Held)
	}
	// Small enough that ComfyUI's own hold alone covers the gap, so a grant
	// can only mean host:comfyui was asked and answered.
	const headroom = 2 << 30
	if headroom >= comfyBefore {
		t.Fatalf("the %d-byte headroom is not smaller than ComfyUI's own %d-byte hold; this ask would not prove anything", headroom, comfyBefore)
	}
	amount := pool - before.Held + headroom
	t.Logf("asking for %.2f GiB of a %.2f GiB pool, which is %.2f GiB more than is free",
		float64(amount)/(1<<30), float64(pool)/(1<<30), float64(headroom)/(1<<30))

	began = time.Now()
	granted, err := leases.AcquireContext(call, instrument.Card0, amount, liveLeaseWait)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("acquire read %s in %s", granted.Outcome, time.Since(began).Round(time.Millisecond))
	for _, ask := range granted.Asked {
		t.Logf("  asked %-16s freed %8.2f GiB  %s  in %s", ask.Holder, float64(ask.Amount)/(1<<30),
			ask.Answer, time.Duration(ask.TookMs)*time.Millisecond)
	}
	if granted.Outcome != rwire.AcquireOutcomeAcquired {
		t.Fatalf("an ask ComfyUI's own hold covers read %s: %+v", granted.Outcome, granted.Asked)
	}
	holder := claimedHostPrefix + "comfyui"
	var yielded bool
	for _, ask := range granted.Asked {
		if ask.Holder == holder && ask.Answer == resourceservice.AnswerYielded && ask.Amount > 0 {
			yielded = true
		}
	}
	if !yielded {
		t.Fatalf("host:comfyui was not asked, or did not yield: %+v", granted.Asked)
	}

	after, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	comfyAfter := liveHeldBy(after, "comfyui")
	t.Logf("ComfyUI's verified row read %.2f GiB after the yield, down from %.2f GiB",
		float64(comfyAfter)/(1<<30), float64(comfyBefore)/(1<<30))
	if comfyAfter >= comfyBefore {
		t.Fatalf("ComfyUI's verified row did not shrink: %.2f GiB before, %.2f GiB after", float64(comfyBefore)/(1<<30), float64(comfyAfter)/(1<<30))
	}

	if change, err := leases.ReleaseContext(call, granted.Lease.ID); err != nil || !change.Applied {
		t.Fatalf("release %+v %v", change, err)
	}
	liveAuditHas(t, options.stateDir, `"event":"ask"`, `"holder":"`+holder+`"`, `"answer":"yielded"`)
}

// comfyYieldWeights are the AMD bundle's own weights, read-only through
// extra_model_paths: the same smallest-diffusion-graph workflow
// research/resources/COMFY-CARD-2026-09-22.md section 1 used.
var comfyYieldWeights = comfyCardWeights

// comfyYield is one plain ComfyUI this test runs: no oa_card hook, no
// custom_nodes, no lease of its own. It holds the card the way any process
// that reads weights onto the GPU does, which is the only thing this proof
// needs of it.
type comfyYield struct {
	python, root, models string
	base, url            string
	process              *exec.Cmd
}

// comfyYieldSetup reads the same machine paths
// research/resources/COMFY-CARD-2026-09-22.md recorded and comfyCardSetup
// reads for the render-side proof: this machine's ComfyUI Python, the
// scratch copy of ComfyUI 0.33.0 (recreated by this task's own robocopy, per
// that document's "Commands" section, before this test runs) and the AMD
// bundle's own models directory.
func comfyYieldSetup(t *testing.T) *comfyYield {
	t.Helper()
	c := comfyCardSetup(t)
	return &comfyYield{python: c.python, root: c.root, models: c.models}
}

// start writes a fresh ComfyUI base directory and runs ComfyUI, plain, until
// the test ends.
func (c *comfyYield) start(t *testing.T, address string) {
	t.Helper()
	c.base = t.TempDir()
	// main.py's prestartup script lists base-directory/custom_nodes
	// unconditionally, even with none to load; without it ComfyUI never
	// gets as far as /system_stats.
	for _, dir := range []string{filepath.Join(c.base, "user"), filepath.Join(c.base, "output"), filepath.Join(c.base, "custom_nodes")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	extra := filepath.Join(c.base, "extra_model_paths.yaml")
	body := "comfyui:\n    base_path: " + c.models + "/\n    diffusion_models: models/diffusion_models/\n" +
		"    text_encoders: models/text_encoders/\n    vae: models/vae/\n"
	if err := os.WriteFile(extra, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	c.url = "http://" + address
	c.process = exec.Command(c.python, "main.py", "--listen", "127.0.0.1", "--port", port,
		"--base-directory", c.base, "--user-directory", filepath.Join(c.base, "user"),
		"--extra-model-paths-config", extra, "--disable-auto-launch")
	c.process.Dir = c.root
	c.process.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONUTF8=1")
	log := new(bytes.Buffer)
	c.process.Stdout, c.process.Stderr = log, log
	if err := c.process.Start(); err != nil {
		t.Fatalf("starting ComfyUI: %v", err)
	}
	t.Cleanup(func() {
		if c.process.Process != nil {
			// Only the PID this test started; never an image name.
			_ = c.process.Process.Kill()
			_, _ = c.process.Process.Wait()
		}
	})
	exited := make(chan error, 1)
	go func() { exited <- c.process.Wait() }()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		response, err := http.Get(c.url + "/system_stats")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case err := <-exited:
			t.Fatalf("ComfyUI exited (%v):\n%s", err, log.String())
		case <-time.After(2 * time.Second):
		}
	}
	t.Fatalf("ComfyUI did not answer within four minutes:\n%s", log.String())
}

// comfyYieldRender queues the same smallest-diffusion-graph workflow
// runtime_comfy_card_live_test.go's comfyCard.render does and waits for
// ComfyUI's own history to carry it, directly against base rather than
// through a *comfyCard (this ComfyUI carries no oa_card hook to route
// through).
func comfyYieldRender(t *testing.T, base string, seed int) comfyCardRun {
	t.Helper()
	prompt := map[string]any{
		"1": map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{
			"unet_name": comfyYieldWeights.unet, "weight_dtype": "default"}},
		"2": map[string]any{"class_type": "CLIPLoader", "inputs": map[string]any{
			"clip_name": comfyYieldWeights.clip, "type": "wan"}},
		"3": map[string]any{"class_type": "VAELoader", "inputs": map[string]any{"vae_name": comfyYieldWeights.vae}},
		"4": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{
			"clip": []any{"2", 0}, "text": "a red cube on a wooden table"}},
		"5": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{
			"clip": []any{"2", 0}, "text": ""}},
		"6": map[string]any{"class_type": "Wan22ImageToVideoLatent", "inputs": map[string]any{
			"vae": []any{"3", 0}, "width": 128, "height": 128, "length": 1, "batch_size": 1}},
		"7": map[string]any{"class_type": "KSampler", "inputs": map[string]any{
			"model": []any{"1", 0}, "positive": []any{"4", 0}, "negative": []any{"5", 0},
			"latent_image": []any{"6", 0}, "seed": seed, "steps": 1, "cfg": 1.0,
			"sampler_name": "euler", "scheduler": "simple", "denoise": 1.0}},
		"8": map[string]any{"class_type": "VAEDecode", "inputs": map[string]any{
			"samples": []any{"7", 0}, "vae": []any{"3", 0}}},
		"9": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{
			"images": []any{"8", 0}, "filename_prefix": "oa-yield"}},
	}
	raw, err := json.Marshal(map[string]any{"prompt": prompt, "client_id": "oa-yield-live"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Post(base+"/prompt", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /prompt: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode/100 != 2 {
		t.Fatalf("POST /prompt: %s %s", response.Status, body)
	}
	var queued struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.Unmarshal(body, &queued); err != nil {
		t.Fatalf("POST /prompt: %v: %s", err, body)
	}
	if queued.PromptID == "" {
		t.Fatal("ComfyUI accepted the workflow and named no prompt")
	}
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		response, err := http.Get(base + "/history/" + queued.PromptID)
		if err == nil {
			history := map[string]comfyCardRun{}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if json.Unmarshal(body, &history) == nil {
				if run, ok := history[queued.PromptID]; ok {
					return run
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("the render %s did not finish within fifteen minutes", queued.PromptID)
	return comfyCardRun{}
}
