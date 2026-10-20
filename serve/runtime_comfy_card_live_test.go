package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// The card goes to the render. Live: LM Studio holds a model, a real ComfyUI
// with the adapter's ABSTRACTION_COMFYUI_CARD=service hook queues a workflow
// that loads the AMD bundle's weights, the service asks LM Studio to yield,
// LM Studio unloads, the render completes and the audit carries the ask, the
// yield and the grant. The same workflow under a rule that denies yielding
// that host reads holders_refused in ComfyUI's own queue, with LM Studio still
// loaded and nothing allocated. This is PROPOSAL.md section 6's acceptance.
//
// It needs a ComfyUI this account may run and a Python with a working GPU
// backend, because a verified row on card:0 is what the instrument measures of
// the process. The defaults are the ones
// research/resources/COMFY-CARD-2026-09-22.md recorded; each is overridable.
func TestLiveComfyUIAsksForTheCardBeforeItRenders(t *testing.T) {
	if os.Getenv("OA_LIVE_CARD_COMFY") != "1" {
		t.Skip("set OA_LIVE_CARD_COMFY=1 to run a real ComfyUI render against this machine's LM Studio")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	comfy := comfyCardSetup(t)

	// What LM Studio holds before this test is what it holds after it.
	loadedBefore := map[string]bool{}
	for _, m := range lmStudioModels(t) {
		if m.State == "loaded" {
			loadedBefore[m.ID] = true
		}
	}
	t.Cleanup(func() {
		for _, m := range lmStudioModels(t) {
			switch {
			case m.State == "loaded" && !loadedBefore[m.ID]:
				lmStudioCall(t, http.MethodPost, "/api/v1/models/unload", map[string]string{"instance_id": m.ID}, nil)
			case m.State != "loaded" && loadedBefore[m.ID]:
				lmStudioCall(t, http.MethodPost, "/api/v1/models/load", map[string]string{"model": m.ID}, nil)
			}
		}
	})
	model := os.Getenv("OA_LIVE_CARD_MODEL")
	if model == "" {
		model = comfyCardModel
	}
	loadLive(t, model)

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	// The host the service has an unload mechanism for. A host the router has
	// not been told about claims nothing and is never asked (RES-L3).
	if out, err := runInference(t, "host", "add", "lmstudio", "--base", liveLMStudio,
		"--endpoint", options.endpoint, "--timeout", "60s"); err != nil &&
		!strings.Contains(out+err.Error(), "conflict: name") {
		t.Fatalf("host add lmstudio: %v\n%s", err, out)
	}
	call, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	machine := client.New(options.endpoint)
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	me := liveSubject(t)
	setLiveRule(t, call, operator, me, resourceservice.ActionTableRead, resourceservice.ResourceAccount, true)

	// The rule the hook's Acquire is decided against (RES-L1). The runtime
	// binds the caller to the image of the process that connected, which for a
	// virtual environment is that environment's own python.exe; the base
	// interpreter is written too, because a launcher that re-executes would
	// arrive as the base one.
	for _, program := range comfy.programs(t) {
		setLiveRule(t, call, operator, rights.Subject{Account: me.Account, Program: program},
			resourceservice.ActionHold, instrument.Card0, true)
	}

	table, err := machine.ResolveResourceTable(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	before, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	if liveHeldBy(before, "llama") <= 0 {
		t.Fatalf("LM Studio holds %s and the table reports no llama-server row: %+v", model, before.Holders)
	}
	pool := before.Capacity
	if pool == 0 {
		pool = instrument.MachineMemory()
	}
	if pool <= before.Held {
		t.Fatalf("the pool is %d and %d is held; there is nothing to ask for", pool, before.Held)
	}
	// What this ComfyUI reserves of the card. The weights are 16 GiB of a
	// 123 GiB pool, so their own size asks nobody; the reserve is what makes
	// the render need more of the card than is free, which is the case
	// section 6 is about.
	reserve := pool - before.Held + liveLeaseHeadroom
	t.Logf("card:0 held %.2f GiB of a %.2f GiB pool; ComfyUI will reserve %.2f GiB",
		float64(before.Held)/(1<<30), float64(pool)/(1<<30), float64(reserve)/(1<<30))

	comfy.start(t, options.endpoint, me.Account, reserve)
	status := comfy.status(t)
	if status.Card.Code != "holding" {
		t.Fatalf("the card hook read %s: %s", status.Card.Code, status.Card.Detail)
	}

	began := time.Now()
	run := comfy.render(t, 7)
	t.Logf("the render read %s in %s", run.Status.StatusStr, time.Since(began).Round(time.Millisecond))
	if run.Status.StatusStr != "success" || !run.Status.Completed {
		t.Fatalf("the render did not complete: %s %+v", run.Status.StatusStr, run.Errors())
	}
	if len(run.Images()) == 0 {
		t.Fatal("the render completed with no image")
	}
	t.Logf("the render saved %v", run.Images())

	for _, m := range lmStudioModels(t) {
		if m.State == "loaded" {
			t.Fatalf("LM Studio still holds %s after yielding to the render", m.ID)
		}
	}
	liveAuditHas(t, options.stateDir, `"event":"ask"`, `"holder":"host:lmstudio"`, `"answer":"yielded"`)
	liveAuditHas(t, options.stateDir, `"event":"acquired"`, `"answer":"acquired"`)

	// The render holds the card twice over: the bytes the instrument measured
	// of its process, and the grant it was given (RES-T5).
	during, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	var verified, leased bool
	programs := comfy.programs(t)
	for _, row := range during.Holders {
		matchesComfy := false
		for _, program := range programs {
			if strings.EqualFold(row.Program, program) {
				matchesComfy = true
				break
			}
		}
		if !matchesComfy {
			continue
		}
		if row.Evidence == rwire.EvidenceVerified && row.Amount > 0 {
			verified = true
			t.Logf("ComfyUI holds %.2f GiB of card:0, verified", float64(row.Amount)/(1<<30))
		}
		if row.Evidence == rwire.EvidenceClaimed && row.Lease != "" {
			leased = true
			t.Logf("ComfyUI holds lease %s for %.2f GiB", row.Lease, float64(row.Amount)/(1<<30))
		}
	}
	if !verified {
		t.Fatalf("the render loaded the weights and the table has no verified row for %s: %+v",
			comfy.python, during.Holders)
	}
	if !leased {
		t.Fatalf("the render holds a lease and the table has no claimed row carrying it: %+v", during.Holders)
	}

	// ComfyUI unloads; the card goes back.
	comfy.free(t)
	deadline := time.Now().Add(30 * time.Second)
	for comfy.status(t).Held.Lease != "" && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	if lease := comfy.status(t).Held.Lease; lease != "" {
		t.Fatalf("ComfyUI unloaded and still holds lease %s", lease)
	}
	liveAuditHas(t, options.stateDir, `"event":"released"`, `"answer":"released"`)

	// The refusal variant: the same render under a rule that refuses yields
	// for this host leaves it holding and the render with nothing.
	setLiveRule(t, call, operator, me, resourceservice.ActionYield, claimedHostPrefix+"lmstudio", false)
	loadLive(t, model)

	denied := comfy.render(t, 8)
	if denied.Status.StatusStr == "success" {
		t.Fatal("a render no holder would yield for completed anyway")
	}
	failure := denied.Errors()
	if !strings.Contains(failure, "holders_refused") || !strings.Contains(failure, "oa_card.CardRefused") ||
		!strings.Contains(failure, claimedHostPrefix+"lmstudio") {
		t.Fatalf("the refusal in ComfyUI's queue does not name the outcome, the type and the host: %s", failure)
	}
	t.Logf("ComfyUI's queue read: %s", failure)
	loaded := 0
	for _, m := range lmStudioModels(t) {
		if m.State == "loaded" {
			loaded++
		}
	}
	if loaded == 0 {
		t.Fatal("a host with a rule against yielding was unloaded anyway")
	}
	liveAuditHas(t, options.stateDir, `"event":"ask"`, `"holder":"host:lmstudio"`, `"answer":"refused`)
	liveAuditHas(t, options.stateDir, `"event":"refused"`, `"answer":"holders_refused"`)
}

// TestLiveComfyUIKleinYieldsTheLemonadeGPUModel exercises the same composed
// runtime, rights, resource Book, card adapter and real Comfy process as the
// LM Studio scenario above, using an official FLUX.2 Klein API workflow and
// Lemonade's native Vulkan-backed model host. It loads a cached model with
// temporary options into an isolated, initially empty Lemonade server, then
// returns that server to empty during cleanup.
func TestLiveComfyUIKleinYieldsTheLemonadeGPUModel(t *testing.T) {
	if os.Getenv("OA_LIVE_CARD_LEMONADE_COMFY") != "1" {
		t.Skip("set OA_LIVE_CARD_LEMONADE_COMFY=1 after preparing the isolated Lemonade Vulkan holder and Klein assets")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	workflowPath := os.Getenv("OA_LIVE_CARD_KLEIN_API_JSON")
	if workflowPath == "" {
		t.Fatal("OA_LIVE_CARD_KLEIN_API_JSON must name the API prompt generated from the pinned official Klein template")
	}
	workflowBody, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read official Klein API prompt %s: %v", workflowPath, err)
	}
	prompt, err := decodeComfyPrompt(workflowBody)
	if err != nil {
		t.Fatalf("decode official Klein API prompt %s: %v", workflowPath, err)
	}
	modelRoots := strings.Split(os.Getenv("OA_LIVE_CARD_MODEL_ROOTS"), string(os.PathListSeparator))
	if len(modelRoots) != 2 || modelRoots[0] == "" || modelRoots[1] == "" {
		t.Fatal("OA_LIVE_CARD_MODEL_ROOTS must list two base paths whose models/ children contain the E5 Klein assets")
	}
	for _, root := range modelRoots {
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("Comfy model root %s: %v", root, err)
		}
	}
	assets := []struct {
		root, relative string
		size           int64
	}{
		{modelRoots[0], filepath.Join("models", "text_encoders", "qwen_3_4b.safetensors"), 8044982048},
		{modelRoots[0], filepath.Join("models", "vae", "flux2-vae.safetensors"), 336211292},
		{modelRoots[1], filepath.Join("models", "diffusion_models", "flux-2-klein-4b-fp8.safetensors"), 4070624520},
	}
	for _, asset := range assets {
		path := filepath.Join(asset.root, asset.relative)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Klein workflow asset %s (model root must be its parent above models/): %v", path, err)
		}
		if info.Size() != asset.size {
			t.Fatalf("Klein workflow asset %s has size %d, want pinned size %d", path, info.Size(), asset.size)
		}
	}
	comfy := comfyCardSetup(t)
	device, err := comfy.gpuDevice(t)
	if err != nil {
		t.Fatalf("Comfy Python must use the isolated AMD GPU stack for E8: %v", err)
	}
	t.Logf("Comfy Python reports GPU device %s", device)

	base := os.Getenv("OA_LIVE_CARD_LEMONADE_BASE")
	if base == "" {
		t.Fatal("OA_LIVE_CARD_LEMONADE_BASE must name the isolated loopback Lemonade server owned by this test")
	}
	parsedBase, err := url.Parse(base)
	if err != nil || parsedBase.Scheme != "http" {
		t.Fatalf("OA_LIVE_CARD_LEMONADE_BASE must be an http loopback URL, got %q", base)
	}
	host, _, err := net.SplitHostPort(parsedBase.Host)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatalf("OA_LIVE_CARD_LEMONADE_BASE must be a loopback address, got %q", base)
	}
	health := lemonadeLiveHealth{}
	comfyCardHTTP(t, base, http.MethodGet, "/api/v1/health", nil, &health)
	if len(health.AllModelsLoaded) != 0 {
		t.Fatalf("Lemonade at %s must be empty before this owned fixture starts; leaving its %d pre-existing model(s) untouched", base, len(health.AllModelsLoaded))
	}
	const lemonadeFixtureModel = "Qwen3-0.6B-GGUF"
	var catalogue struct {
		Data []struct {
			ID         string `json:"id"`
			Downloaded bool   `json:"downloaded"`
		} `json:"data"`
	}
	comfyCardHTTP(t, base, http.MethodGet, "/api/v1/models?show_all=true", nil, &catalogue)
	fixtureDownloaded := false
	for _, model := range catalogue.Data {
		if model.ID == lemonadeFixtureModel && model.Downloaded {
			fixtureDownloaded = true
		}
	}
	if !fixtureDownloaded {
		t.Fatalf("the owned Lemonade fixture %s must already be downloaded; this test does not download models", lemonadeFixtureModel)
	}
	t.Cleanup(func() {
		// Never use Lemonade's unload-all if another model or active caller
		// appeared after this test's empty precondition.
		var beforeUnload lemonadeLiveHealth
		if err := comfyCardHTTPTry(base, http.MethodGet, "/api/v1/health", nil, &beforeUnload); err != nil {
			t.Errorf("inspect Lemonade before fixture cleanup: %v", err)
			return
		}
		for _, model := range beforeUnload.AllModelsLoaded {
			if model.ModelName != lemonadeFixtureModel || model.IsBusy || model.IsStreaming {
				t.Errorf("leaving Lemonade untouched during cleanup because model %q is not the idle owned fixture", model.ModelName)
				return
			}
		}
		if len(beforeUnload.AllModelsLoaded) == 0 {
			return
		}
		if err := comfyCardHTTPTry(base, http.MethodPost, "/api/v1/unload", nil, nil); err != nil {
			t.Errorf("unload owned Lemonade fixture model: %v", err)
			return
		}
		var after lemonadeLiveHealth
		if err := comfyCardHTTPTry(base, http.MethodGet, "/api/v1/health", nil, &after); err != nil {
			t.Errorf("verify Lemonade fixture cleanup: %v", err)
		} else if len(after.AllModelsLoaded) != 0 {
			t.Errorf("Lemonade retained %d model(s) after fixture cleanup", len(after.AllModelsLoaded))
		}
	})
	loadBody := map[string]any{"model_name": lemonadeFixtureModel, "ctx_size": 16384,
		"llamacpp_backend": "vulkan", "save_options": false}
	var loaded struct {
		ModelName string `json:"model_name"`
		Status    string `json:"status"`
	}
	comfyCardHTTP(t, base, http.MethodPost, "/api/v1/load", loadBody, &loaded)
	if loaded.ModelName != lemonadeFixtureModel || !strings.EqualFold(loaded.Status, "success") {
		t.Fatalf("Lemonade did not accept the temporary fixture load: %+v", loaded)
	}
	comfyCardHTTP(t, base, http.MethodGet, "/api/v1/health", nil, &health)
	if len(health.AllModelsLoaded) != 1 || health.AllModelsLoaded[0].ModelName != lemonadeFixtureModel {
		t.Fatalf("Lemonade did not load only the owned fixture model %s: %+v", lemonadeFixtureModel, health.AllModelsLoaded)
	}
	for _, model := range health.AllModelsLoaded {
		backend := model.Backend
		if backend == "" {
			backend, _ = model.RecipeOptions["llamacpp_backend"].(string)
		}
		if !strings.EqualFold(model.Device, "gpu") || !strings.EqualFold(backend, "vulkan") || model.PID <= 0 {
			t.Fatalf("Lemonade holder must be a health-confirmed Vulkan GPU process, got %+v", model)
		}
		t.Logf("Lemonade holds %s on Vulkan GPU in PID %d", model.ModelName, model.PID)
	}

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	if out, err := runInference(t, "host", "add", "lemonade", "--base", base,
		"--endpoint", options.endpoint, "--timeout", "60s"); err != nil &&
		!strings.Contains(out+err.Error(), "conflict: name") {
		t.Fatalf("host add lemonade: %v\n%s", err, out)
	}
	call, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	machine := client.New(options.endpoint)
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	me := liveSubject(t)
	setLiveRule(t, call, operator, me, resourceservice.ActionTableRead, resourceservice.ResourceAccount, true)
	for _, program := range comfy.programs(t) {
		subject := rights.Subject{Account: me.Account, Program: program}
		setLiveRule(t, call, operator, subject, resourceservice.ActionHold, instrument.Card0, true)
		setLiveRule(t, call, operator, subject, resourceservice.ActionYield, claimedHostPrefix+"lemonade", true)
	}
	comfy.models = modelRoots[0]
	comfy.startWithModelRoots(t, options.endpoint, me.Account, 0, modelRoots)
	comfy.cleanupHold(t)
	if status := comfy.status(t); status.Card.Code != "holding" {
		t.Fatalf("the card hook read %s: %s", status.Card.Code, status.Card.Detail)
	}
	table, err := machine.ResolveResourceTable(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	before, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	if liveHeldBy(before, "llama-server") <= 0 {
		t.Fatalf("the Vulkan backend is loaded, but the resource table has no verified llama-server row: %+v", before.Holders)
	}
	pool := before.Capacity
	if pool == 0 {
		pool = instrument.MachineMemory()
	}
	const kleinWeights = int64(4070624520 + 8044982048 + 336211292)
	free := pool - before.Held
	lemonadeHeld := liveHeldBy(before, "llama-server")
	const handoverMargin = int64(1 << 30)
	if lemonadeHeld <= handoverMargin {
		t.Fatalf("the owned Lemonade process holds only %.2f GiB, too little to safely back the configured %.2f GiB handover margin",
			float64(lemonadeHeld)/(1<<30), float64(handoverMargin)/(1<<30))
	}
	configuredBudget := free + handoverMargin
	if configuredBudget < kleinWeights {
		configuredBudget = kleinWeights
	}
	if configuredBudget > pool || configuredBudget > free+lemonadeHeld {
		t.Fatalf("configured OA admission budget %.2f GiB cannot be covered after yielding the measured Lemonade row (pool %.2f, free %.2f, Lemonade %.2f GiB)",
			float64(configuredBudget)/(1<<30), float64(pool)/(1<<30), float64(free)/(1<<30), float64(lemonadeHeld)/(1<<30))
	}
	t.Logf("measured capacity %.2f GiB, held %.2f GiB, free %.2f GiB; configured OA admission budget %.2f GiB is free capacity plus a %.2f GiB test margin (not an actual Comfy VRAM estimate); Klein's three files total %.2f GiB",
		float64(pool)/(1<<30), float64(before.Held)/(1<<30), float64(free)/(1<<30),
		float64(configuredBudget)/(1<<30), float64(handoverMargin)/(1<<30), float64(kleinWeights)/(1<<30))

	// The Comfy process is already running, so the free-space sample includes
	// its measured startup footprint. This explicit test admission budget is
	// not an estimate of the render's physical VRAM need.
	comfy.setReserve(t, configuredBudget)

	// Keep the resident holder genuinely busy while Comfy reaches its card
	// wait. Cancel the pending prompt through Comfy's own interrupt route, then
	// prove the holder is still active and the canceled loader acquired no
	// lease or file mapping. After that one request finishes, the same Klein
	// graph below exercises the idle native unload and the single image render.
	activeRequest := startLemonadeLongRequest(base, lemonadeFixtureModel)
	t.Cleanup(func() {
		// If the test ends early, cancel the waiting Comfy prompt before
		// waiting for the bounded fixture request or considering model cleanup.
		_ = comfyCardHTTPTry(comfy.url, http.MethodPost, "/interrupt", nil, nil)
		_ = comfyCardHTTPTry(comfy.url, http.MethodPost, "/free",
			map[string]bool{"unload_models": true, "free_memory": true}, nil)
		select {
		case <-activeRequest.done:
		case <-time.After(10 * time.Minute):
			activeRequest.cancel()
			select {
			case <-activeRequest.done:
				t.Errorf("Lemonade request exceeded ten minutes and needed cancellation")
			case <-time.After(30 * time.Second):
				t.Errorf("Lemonade request did not stop after its bounded client cancellation; leaving the active model untouched")
			}
		}
	})
	waitLemonadeBusy(t, base, lemonadeFixtureModel, activeRequest, 30*time.Second)
	canceledPrompt := comfy.queuePrompt(t, prompt)
	waitComfyProgress(t, comfy, "Waiting for card:0 capacity", activeRequest, 15*time.Second)
	comfy.call(t, http.MethodPost, "/interrupt", nil, nil)
	t.Logf("Comfy interrupt returned at %s; Lemonade request state=%s", time.Now().UTC().Format(time.RFC3339Nano), activeRequest.state())
	canceled := comfy.waitPrompt(t, canceledPrompt, 30*time.Second)
	if canceled.Status.StatusStr == "success" || len(canceled.Images()) != 0 {
		t.Fatalf("the interrupted card wait unexpectedly ran the workflow: %+v", canceled.Status)
	}
	if !canceled.Interrupted() {
		t.Fatalf("Comfy history has no execution_interrupted event: status=%+v messages=%s", canceled.Status, canceled.Errors())
	}
	if held := comfy.status(t).Held; held.Amount != 0 || held.Lease != "" || len(held.Files) != 0 {
		t.Fatalf("the canceled wait retained a lease or loaded-file mapping: %+v", held)
	}
	busyHealth := lemonadeHealth(t, base)
	if len(busyHealth.AllModelsLoaded) != 1 || !busyHealth.AllModelsLoaded[0].IsBusy {
		t.Fatalf("Comfy cancellation did not leave the owned Lemonade request active: %+v", busyHealth.AllModelsLoaded)
	}
	t.Logf("Comfy's visible wait was canceled while Lemonade remained active; no Comfy lease/files were retained")
	if err := activeRequest.wait(t, 10*time.Minute); err != nil {
		t.Fatalf("bounded Lemonade request: %v", err)
	}
	t.Logf("Lemonade request completed at %s", activeRequest.state())
	activeRequest.cancel()
	waitLemonadeIdle(t, base, lemonadeFixtureModel, 30*time.Second)

	run := comfy.renderPrompt(t, prompt)
	if run.Status.StatusStr != "success" || !run.Status.Completed || len(run.Images()) == 0 {
		t.Fatalf("the pinned Klein render did not complete with an image: status=%+v errors=%s", run.Status, run.Errors())
	}
	if !comfy.progressHas(t, "Waiting for card:0 capacity") {
		t.Fatal("Comfy accepted the render but its observable progress route recorded no card-wait explanation")
	}
	current := lemonadeHealth(t, base)
	for _, resident := range current.AllModelsLoaded {
		if resident.ModelName == lemonadeFixtureModel {
			t.Fatalf("Lemonade still reports %s resident after Comfy acquired card:0", resident.ModelName)
		}
	}
	liveAuditHas(t, options.stateDir, `"event":"ask"`, `"holder":"host:lemonade"`, `"answer":"yielded"`)
	liveAuditHas(t, options.stateDir, `"event":"acquired"`, `"answer":"acquired"`)
	during, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	var verified, leased bool
	for _, row := range during.Holders {
		if !strings.EqualFold(row.Program, comfy.python) {
			continue
		}
		verified = verified || row.Evidence == rwire.EvidenceVerified && row.Amount > 0
		leased = leased || row.Evidence == rwire.EvidenceClaimed && row.Lease != ""
	}
	if !verified || !leased {
		t.Fatalf("Klein rendered but Comfy lacks measured process use or its claimed lease (verified=%v leased=%v): %+v", verified, leased, during.Holders)
	}

	comfy.free(t)
	deadline := time.Now().Add(30 * time.Second)
	for comfy.status(t).Held.Amount > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Second)
	}
	if held := comfy.status(t).Held; held.Amount > 0 {
		t.Fatalf("Comfy unloaded and still holds %d bytes across %q", held.Amount, held.Lease)
	}
	liveAuditHas(t, options.stateDir, `"event":"released"`, `"answer":"released"`)
}

// comfyCardModel is the largest model this machine's LM Studio will load. The
// 2026-09-22 measurement found which of the six installed files load at all.
const comfyCardModel = "cygnal/qwen3.8-27b-heretic-ara-q4_k_m-mtp-gguf/qwen3.8-27b-heretic-ara-q4_k_m-mtp.gguf"

// comfyCardWeights are the AMD bundle's own weights, loaded read-only through
// extra_model_paths. The workflow is a WAN 2.2 TI2V text-to-video graph asked
// for one 128x128 frame: the smallest thing that loads a diffusion model, a
// text encoder and a VAE and saves an image.
var comfyCardWeights = struct{ unet, clip, vae string }{
	unet: "wan2.2_ti2v_5B_fp16.safetensors",
	clip: "umt5_xxl_fp8_e4m3fn_scaled.safetensors",
	vae:  "wan2.2_vae.safetensors",
}

// comfyCardHarness gives oa_card the isolated runtime's identity through the
// module's own configure() seam. An isolated runtime carries no Windows MSI
// product record, so Machine()'s default discovery cannot select it
// (adopter-comfyui/SERVICE.md, "Runtime selection"); this is the same
// independently configured trust every other ComfyUI proof here uses. It is a
// test fixture and no part of the adapter.
const comfyCardHarness = `"""Test-only harness for serve/runtime_comfy_card_live_test.go."""
import importlib
import os
import sys

from aiohttp import web
from server import PromptServer

NODE_CLASS_MAPPINGS = {}


def _node():
    suffix = os.path.normcase(os.path.join("abstraction_downloads", "__init__.py"))
    for module in list(sys.modules.values()):
        if os.path.normcase(getattr(module, "__file__", None) or "").endswith(suffix):
            return module
    return None


node = _node()
card = None
if node is not None:
    card = importlib.import_module(node.__name__ + ".oa_card")
    if os.environ.get("OA_CARD_TRUST"):
        from abstraction.facade.client import Machine
        from abstraction.ipc import Library, ServerExpectation
        expected = ServerExpectation(1 if sys.platform == "win32" else 2,
                                     os.environ["OA_CARD_UID"], os.environ["OA_CARD_IMAGE"])
        endpoint = os.environ["OA_CARD_ENDPOINT"]
        library = Library()
        card.configure(lambda deadline, cancellation=None: Machine(
            endpoint, server=expected, deadline=deadline, cancellation=cancellation),
            cancellation_factory=library.cancellation)
        card.install()


@PromptServer.instance.routes.get("/oa-card/status")
async def status(request):
    body = dict(node_loaded=node is not None)
    if card is not None:
        body["card"] = card.status()._asdict()
        holding = card.held()
        body["held"] = dict(lease=holding.lease or "", amount=holding.amount, files=holding.files)
    body["progress"] = list(_progress)
    return web.json_response(body)


@PromptServer.instance.routes.post("/oa-card/reserve")
async def set_reserve(request):
    value = int((await request.json())["bytes"])
    if value < 0:
        return web.json_response({"error": "negative reserve"}, status=400)
    os.environ["ABSTRACTION_COMFYUI_CARD_RESERVE"] = str(value)
    return web.json_response({"bytes": value})


_progress = []
_prompt_server = PromptServer.instance
_send_progress_text = getattr(_prompt_server, "send_progress_text", None)
if callable(_send_progress_text):
    def _capture_progress(text, node_id=None):
        _progress.append(dict(text=str(text), node_id=node_id))
        return _send_progress_text(text, node_id)
    _prompt_server.send_progress_text = _capture_progress
`

// comfyCard is one ComfyUI this test runs and the scratch directories it uses.
type comfyCard struct {
	python, root, models, pythonPath, ipcPrefix, dependencySite, nativeRuntimeDir string
	base, url                                                                     string
	process                                                                       *exec.Cmd
	logs                                                                          *comfyCardLog
}

// comfyCardLog lets the test include a bounded tail of server output when an
// API request fails, without racing Comfy's stdout writer.
type comfyCardLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *comfyCardLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *comfyCardLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func decodeComfyPrompt(raw []byte) (map[string]any, error) {
	var prompt map[string]any
	if err := json.Unmarshal(raw, &prompt); err != nil {
		return nil, err
	}
	if workflow, ok := prompt["workflow"].(map[string]any); ok {
		// Converter output retains save_node metadata; /prompt accepts only
		// the node map under "workflow".
		prompt = workflow
	}
	if len(prompt) == 0 {
		return nil, fmt.Errorf("empty prompt graph")
	}
	return prompt, nil
}

func TestDecodeComfyPromptUnwrapsCanonicalEnvelope(t *testing.T) {
	got, err := decodeComfyPrompt([]byte(`{"workflow":{"1":{"class_type":"SaveImage","inputs":{}}},"save_node":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["1"].(map[string]any); !ok || len(got) != 1 {
		t.Fatalf("decoded prompt should contain only its node map: %#v", got)
	}
	if _, ok := got["save_node"]; ok {
		t.Fatalf("converter envelope metadata leaked into prompt graph: %#v", got)
	}
}

type lemonadeLiveModel struct {
	ModelName     string         `json:"model_name"`
	Device        string         `json:"device"`
	Backend       string         `json:"backend"`
	PID           int            `json:"pid"`
	IsBusy        bool           `json:"is_busy"`
	IsStreaming   bool           `json:"is_streaming"`
	RecipeOptions map[string]any `json:"recipe_options"`
}

type lemonadeLiveHealth struct {
	AllModelsLoaded []lemonadeLiveModel `json:"all_models_loaded"`
}

type lemonadeLiveRequest struct {
	done       chan struct{}
	cancel     context.CancelFunc
	err        error
	finishedAt time.Time
}

func (r *lemonadeLiveRequest) state() string {
	select {
	case <-r.done:
		return "finished at " + r.finishedAt.UTC().Format(time.RFC3339Nano)
	default:
		return "still running"
	}
}

func comfyCardEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// comfyCardSetup reads where this machine's ComfyUI, its Python and the OA
// Python packages are. Every default is what COMFY-CARD-2026-09-22.md used.
func comfyCardSetup(t *testing.T) *comfyCard {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	forks := comfyCardEnv("OA_COMFYUI_FORKS", filepath.Join(repo, ".forks"))
	bundle := filepath.Join(os.Getenv("LOCALAPPDATA"), "AMD", "AI_Bundle", "ComfyUI")
	c := &comfyCard{
		python:           comfyCardEnv("OA_LIVE_CARD_PYTHON", filepath.Join(bundle, "venv", "Scripts", "python.exe")),
		root:             comfyCardEnv("OA_LIVE_CARD_COMFYUI", filepath.Join(forks, "scratch-comfyui-card")),
		models:           comfyCardEnv("OA_LIVE_CARD_MODELS", filepath.Join(bundle, "ComfyUI")),
		ipcPrefix:        comfyCardEnv("ABSTRACTION_IPC_PREFIX", filepath.Join(forks, "scratch-comfyui-oa", "prefix")),
		nativeRuntimeDir: os.Getenv("OA_COMFYUI_REAL_NATIVE_RUNTIME_DIR"),
	}
	layers := []string{"identity", "asks", "facade", "job", "download", "model", "storage", "logging", "resource"}
	var paths []string
	for _, layer := range layers {
		paths = append(paths, filepath.Join(repo, "openabstractions-flat", "abstraction-"+layer, "py"))
	}
	paths = append(paths, filepath.Join(forks, "scratch-comfyui-oa", "python"))
	c.pythonPath = comfyCardEnv("OA_LIVE_CARD_PYTHONPATH", strings.Join(paths, string(os.PathListSeparator)))
	dependencySite := os.Getenv("OA_LIVE_CARD_PYTHON_DEPENDENCY_SITE")
	if dependencySite == "" && runtime.GOOS == "windows" {
		candidate := filepath.Join(forks, "scratch-comfyui-venv", "Lib", "site-packages")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			dependencySite = candidate
		}
	}
	if dependencySite != "" || c.nativeRuntimeDir != "" {
		if dependencySite != "" {
			info, err := os.Stat(dependencySite)
			if err != nil {
				t.Fatalf("Comfy dependency site-packages %s: %v", dependencySite, err)
			}
			if !info.IsDir() {
				t.Fatalf("Comfy dependency site-packages %s is not a directory", dependencySite)
			}
		}
		bootstrap := t.TempDir()
		bootstrapSource := "import os, sys\n" +
			"_NATIVE_RUNTIME_DLL_DIRECTORY = None\n" +
			"runtime = os.environ.get('OA_COMFYUI_REAL_NATIVE_RUNTIME_DIR')\n" +
			"if os.name == 'nt' and runtime:\n    _NATIVE_RUNTIME_DLL_DIRECTORY = os.add_dll_directory(runtime)\n" +
			"dependencies = os.environ.get('OA_LIVE_CARD_PYTHON_DEPENDENCY_SITE')\n" +
			"if dependencies and dependencies not in sys.path:\n    sys.path.append(dependencies)\n"
		if err := os.WriteFile(filepath.Join(bootstrap, "sitecustomize.py"), []byte(bootstrapSource), 0o600); err != nil {
			t.Fatalf("prepare isolated Comfy Python dependency bootstrap: %v", err)
		}
		c.pythonPath = bootstrap + string(os.PathListSeparator) + c.pythonPath
		c.dependencySite = dependencySite
	}
	for name, path := range map[string]string{"python": c.python, "ComfyUI": c.root, "models": c.models} {
		if _, err := os.Stat(path); err != nil {
			t.Skipf("no %s at %s: %v", name, path, err)
		}
	}
	return c
}

// programs are the images the runtime might bind this ComfyUI to: the Python
// that is started, and the base interpreter it reports.
func (c *comfyCard) programs(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command(c.python, "-c",
		"import os,sys;print(os.path.realpath(getattr(sys,'_base_executable',None) or sys.executable))").Output()
	if err != nil {
		t.Fatalf("asking %s for its base executable: %v", c.python, err)
	}
	base := strings.TrimSpace(string(out))
	if base == "" || strings.EqualFold(base, c.python) {
		return []string{c.python}
	}
	return []string{c.python, base}
}

func (c *comfyCard) gpuDevice(t *testing.T) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" && c.nativeRuntimeDir == "" {
		return "", fmt.Errorf("set OA_COMFYUI_REAL_NATIVE_RUNTIME_DIR to the native IPC compiler runtime directory before starting this Windows ComfyUI")
	}
	if c.nativeRuntimeDir != "" {
		info, err := os.Stat(c.nativeRuntimeDir)
		if err != nil {
			return "", fmt.Errorf("native IPC compiler runtime directory %s: %w", c.nativeRuntimeDir, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("native IPC compiler runtime path %s is not a directory", c.nativeRuntimeDir)
		}
	}
	preflight := `import hashlib, importlib.util, os, torch, alembic, sqlalchemy
import abstraction.identity.api as identity
import abstraction.asks.api as asks
import abstraction.facade as facade_wire
import abstraction.facade.client as facade
import abstraction.job.acceptance as job
import abstraction.download.request as download
import abstraction.storage.content as storage
import abstraction.storage.content.client as storage_client
import abstraction.resource as resource
import abstraction.ipc as ipc
modules = [identity, asks, facade_wire, facade, job, download, storage, storage_client, resource, ipc]
for module in modules:
    print("module", module.__name__, module.__file__)
assert hasattr(facade.Machine, "resolve_resource_leases"), "facade Machine lacks resolve_resource_leases"
assert hasattr(facade_wire, "Scope") and hasattr(resource, "LeasesClient")
assert hasattr(resource, "LeaseChangeResult") and hasattr(job, "RecoverableAcceptanceClient")
assert hasattr(download, "Request") and hasattr(storage, "ContentReaderClient")
assert torch.cuda.is_available(), "PyTorch has no available GPU"
print("torch", torch.__version__, torch.__file__, "device", torch.cuda.get_device_name(0))
print("alembic", alembic.__file__)
print("sqlalchemy", sqlalchemy.__file__)
library = ipc.Library()
print("native IPC ABI", library._version())
assert library._version() == 1
token = library.cancellation()
token.close()
source = os.environ["OA_LIVE_CARD_ADOPTER_CARD_SOURCE"]
spec = importlib.util.spec_from_file_location("oa_card_preflight", source)
card = importlib.util.module_from_spec(spec)
spec.loader.exec_module(card)
assert callable(card.install) and callable(card.configure)
print("oa_card", source, hashlib.sha256(open(source, "rb").read()).hexdigest())
print("GPU_DEVICE", torch.cuda.get_device_name(0))`
	preflightContext, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(preflightContext, c.python, "-c", preflight)
	command.Dir = c.root
	cardSource, err := filepath.Abs(filepath.Join("..", "openabstractions-flat", "adopter-comfyui", "oa_card.py"))
	if err != nil {
		return "", fmt.Errorf("resolve the staged Comfy card adapter source: %w", err)
	}
	command.Env = append(os.Environ(), "PYTHONPATH="+c.pythonPath,
		"ABSTRACTION_IPC_PREFIX="+c.ipcPrefix,
		"OA_LIVE_CARD_ADOPTER_CARD_SOURCE="+cardSource)
	if c.nativeRuntimeDir != "" {
		command.Env = append(command.Env, "OA_COMFYUI_REAL_NATIVE_RUNTIME_DIR="+c.nativeRuntimeDir)
	}
	if c.dependencySite != "" {
		command.Env = append(command.Env, "OA_LIVE_CARD_PYTHON_DEPENDENCY_SITE="+c.dependencySite)
	}
	out, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", c.python, err, strings.TrimSpace(string(out)))
	}
	t.Logf("Comfy Python preflight:\n%s", strings.TrimSpace(string(out)))
	device := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "GPU_DEVICE ") {
			device = strings.TrimSpace(strings.TrimPrefix(line, "GPU_DEVICE "))
		}
	}
	if device == "" {
		return "", fmt.Errorf("%s preflight returned no GPU name", c.python)
	}
	return device, nil
}

// start writes a fresh ComfyUI base directory, installs the adapter node and
// the harness beside it, and runs ComfyUI until the test ends.
func (c *comfyCard) start(t *testing.T, endpoint, account string, reserve int64) {
	c.startWithModelRoots(t, endpoint, account, reserve, []string{c.models})
}

func (c *comfyCard) startWithModelRoots(t *testing.T, endpoint, account string, reserve int64, modelRoots []string) {
	t.Helper()
	c.base = t.TempDir()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	nodes := filepath.Join(c.base, "custom_nodes")
	if err := copyNodeTree(filepath.Join(repo, "openabstractions-flat", "adopter-comfyui"),
		filepath.Join(nodes, "abstraction_downloads")); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(nodes, "zz_oa_card_harness")
	for _, dir := range []string{harness, filepath.Join(c.base, "user"), filepath.Join(c.base, "output")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(harness, "__init__.py"), []byte(comfyCardHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(c.base, "extra_model_paths.yaml")
	var body strings.Builder
	for i, root := range modelRoots {
		name := "comfyui"
		if i > 0 {
			name = fmt.Sprintf("comfyui_%d", i+1)
		}
		fmt.Fprintf(&body, "%s:\n    base_path: %q\n    diffusion_models: models/diffusion_models/\n"+
			"    text_encoders: models/text_encoders/\n    vae: models/vae/\n", name, filepath.ToSlash(root))
	}
	if err := os.WriteFile(extra, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	address := freeLoopbackPort(t)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	c.url = "http://" + address
	c.process = exec.Command(c.python, "main.py", "--listen", "127.0.0.1", "--port", port,
		"--base-directory", c.base, "--user-directory", filepath.Join(c.base, "user"),
		"--extra-model-paths-config", extra, "--disable-auto-launch")
	c.process.Dir = c.root
	c.process.Env = append(os.Environ(),
		"PYTHONPATH="+c.pythonPath,
		"ABSTRACTION_IPC_PREFIX="+c.ipcPrefix,
		"PYTHONDONTWRITEBYTECODE=1", "PYTHONUTF8=1",
		"ABSTRACTION_COMFYUI_CARD=service",
		"ABSTRACTION_COMFYUI_CARD_RESERVE="+strconv.FormatInt(reserve, 10),
		"ABSTRACTION_COMFYUI_CARD_WAIT_SECONDS=90",
		"OA_CARD_TRUST=1", "OA_CARD_ENDPOINT="+endpoint,
		"OA_CARD_UID="+account, "OA_CARD_IMAGE="+self)
	if c.dependencySite != "" {
		c.process.Env = append(c.process.Env, "OA_LIVE_CARD_PYTHON_DEPENDENCY_SITE="+c.dependencySite)
	}
	if c.nativeRuntimeDir != "" {
		c.process.Env = append(c.process.Env, "OA_COMFYUI_REAL_NATIVE_RUNTIME_DIR="+c.nativeRuntimeDir)
	}
	log := new(comfyCardLog)
	c.logs = log
	c.process.Stdout, c.process.Stderr = log, log
	if err := c.process.Start(); err != nil {
		t.Fatalf("starting ComfyUI: %v", err)
	}
	t.Cleanup(func() {
		if c.process.Process != nil {
			// Only the PID this test started; never an image name.
			_ = c.process.Process.Kill()
		}
		for _, line := range strings.Split(log.String(), "\n") {
			if strings.Contains(line, "[abstraction]") {
				t.Logf("ComfyUI: %s", strings.TrimSpace(line))
			}
		}
	})
	exited := make(chan error, 1)
	go func() { exited <- c.process.Wait() }()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		response, err := http.Get(c.url + "/oa-card/status")
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

// comfyCardStatus is what the harness route reports.
type comfyCardStatus struct {
	Card struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	} `json:"card"`
	Held struct {
		Lease  string           `json:"lease"`
		Amount int64            `json:"amount"`
		Files  map[string]int64 `json:"files"`
	} `json:"held"`
	Progress []struct {
		Text   string `json:"text"`
		NodeID string `json:"node_id"`
	} `json:"progress"`
}

func (c *comfyCard) status(t *testing.T) comfyCardStatus {
	t.Helper()
	var status comfyCardStatus
	c.call(t, http.MethodGet, "/oa-card/status", nil, &status)
	return status
}

func (c *comfyCard) free(t *testing.T) {
	t.Helper()
	c.call(t, http.MethodPost, "/free", map[string]bool{"unload_models": true, "free_memory": true}, nil)
}

func (c *comfyCard) setReserve(t *testing.T, amount int64) {
	t.Helper()
	var applied struct {
		Bytes int64 `json:"bytes"`
	}
	c.call(t, http.MethodPost, "/oa-card/reserve", map[string]int64{"bytes": amount}, &applied)
	if applied.Bytes != amount {
		t.Fatalf("Comfy configured reserve %d bytes, requested %d", applied.Bytes, amount)
	}
}

func (c *comfyCard) cleanupHold(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if c.url == "" {
			return
		}
		if err := comfyCardHTTPTry(c.url, http.MethodPost, "/free",
			map[string]bool{"unload_models": true, "free_memory": true}, nil); err != nil {
			t.Errorf("release Comfy card hold during cleanup: %v", err)
		}
	})
}

func (c *comfyCard) progressHas(t *testing.T, fragment string) bool {
	t.Helper()
	for _, entry := range c.status(t).Progress {
		if strings.Contains(entry.Text, fragment) {
			return true
		}
	}
	return false
}

func comfyCardHTTP(t *testing.T, base, method, path string, body, into any) {
	t.Helper()
	if err := comfyCardHTTPTry(base, method, path, body, into); err != nil {
		t.Fatalf("Lemonade %s %s: %v", method, path, err)
	}
}

func comfyCardHTTPTry(base, method, path string, body, into any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s", response.Status, raw)
	}
	if into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			return fmt.Errorf("decode response: %w: %s", err, raw)
		}
	}
	return nil
}

func startLemonadeLongRequest(base, model string) *lemonadeLiveRequest {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "Write a detailed, multi-section guide about designing a small greenhouse. Continue with useful, concrete detail until you reach the output limit."}},
		"max_tokens": 4096, "stream": true, "thinking": false,
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/api/v1/chat/completions", bytes.NewReader(body))
	result := &lemonadeLiveRequest{done: make(chan struct{}), cancel: cancel}
	if err != nil {
		result.err = err
		close(result.done)
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	go func() {
		defer func() {
			result.finishedAt = time.Now()
			close(result.done)
		}()
		response, err := (&http.Client{Timeout: 0}).Do(request)
		if err != nil {
			result.err = err
			return
		}
		defer response.Body.Close()
		if response.StatusCode/100 != 2 {
			raw, _ := io.ReadAll(response.Body)
			result.err = fmt.Errorf("chat completion returned %s: %s", response.Status, raw)
			return
		}
		_, result.err = io.Copy(io.Discard, response.Body)
	}()
	return result
}

func (r *lemonadeLiveRequest) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(timeout):
		return fmt.Errorf("request did not finish within %s", timeout)
	}
}

func lemonadeHealth(t *testing.T, base string) lemonadeLiveHealth {
	t.Helper()
	var health lemonadeLiveHealth
	comfyCardHTTP(t, base, http.MethodGet, "/api/v1/health", nil, &health)
	return health
}

func waitLemonadeBusy(t *testing.T, base, model string, request *lemonadeLiveRequest, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-request.done:
			t.Fatalf("bounded request ended before Comfy reached its wait: %v", request.err)
		default:
		}
		for _, loaded := range lemonadeHealth(t, base).AllModelsLoaded {
			if loaded.ModelName == model && loaded.IsBusy && loaded.IsStreaming {
				t.Logf("Lemonade health observed busy+streaming at %s; request state=%s",
					time.Now().UTC().Format(time.RFC3339Nano), request.state())
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Lemonade did not report %s busy and streaming within %s", model, timeout)
}

func waitLemonadeIdle(t *testing.T, base, model string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		health := lemonadeHealth(t, base)
		for _, loaded := range health.AllModelsLoaded {
			if loaded.ModelName == model && !loaded.IsBusy && !loaded.IsStreaming {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("Lemonade did not report %s idle within %s", model, timeout)
}

func waitComfyProgress(t *testing.T, comfy *comfyCard, fragment string, active *lemonadeLiveRequest, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-active.done:
			t.Fatalf("Lemonade request ended before the card wait was visible: %v", active.err)
		default:
		}
		if comfy.progressHas(t, fragment) {
			t.Logf("Comfy card-wait progress observed at %s; Lemonade request state=%s",
				time.Now().UTC().Format(time.RFC3339Nano), active.state())
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Comfy did not report the card-wait reason %q within %s", fragment, timeout)
}

func (c *comfyCard) call(t *testing.T, method, path string, body, into any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequest(method, c.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Do(request)
	if err != nil {
		t.Fatalf("ComfyUI %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode/100 != 2 {
		serverLog := ""
		if c.logs != nil {
			serverLog = c.logs.String()
			if len(serverLog) > 12000 {
				serverLog = serverLog[len(serverLog)-12000:]
			}
		}
		t.Fatalf("ComfyUI %s %s: %s %s\nrecent ComfyUI log:\n%s", method, path, response.Status, raw, serverLog)
	}
	if into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("ComfyUI %s %s: %v: %s", method, path, err, raw)
		}
	}
}

// comfyCardRun is one entry of ComfyUI's history.
type comfyCardRun struct {
	Status struct {
		StatusStr string              `json:"status_str"`
		Completed bool                `json:"completed"`
		Messages  [][]json.RawMessage `json:"messages"`
	} `json:"status"`
	Outputs map[string]struct {
		Images []struct {
			Filename string `json:"filename"`
		} `json:"images"`
	} `json:"outputs"`
}

func (r comfyCardRun) Images() []string {
	var names []string
	for _, output := range r.Outputs {
		for _, image := range output.Images {
			names = append(names, image.Filename)
		}
	}
	return names
}

// Errors is every message ComfyUI recorded for a run that did not succeed,
// which is where a node's exception type and message land.
func (r comfyCardRun) Errors() string {
	var parts []string
	for _, message := range r.Status.Messages {
		if len(message) != 2 {
			continue
		}
		var kind string
		if json.Unmarshal(message[0], &kind) != nil || !strings.Contains(kind, "error") {
			continue
		}
		parts = append(parts, string(message[1]))
	}
	return strings.Join(parts, "\n")
}

func (r comfyCardRun) Interrupted() bool {
	for _, message := range r.Status.Messages {
		if len(message) != 2 {
			continue
		}
		var kind string
		if json.Unmarshal(message[0], &kind) == nil && kind == "execution_interrupted" {
			return true
		}
	}
	return false
}

func TestComfyCardRunRecognizesExecutionInterrupted(t *testing.T) {
	var run comfyCardRun
	if err := json.Unmarshal([]byte(`{"status":{"status_str":"error","completed":false,"messages":[["execution_interrupted",{"prompt_id":"p"}]]}}`), &run); err != nil {
		t.Fatal(err)
	}
	if !run.Interrupted() {
		t.Fatalf("Comfy interruption event was not recognized: %+v", run.Status)
	}
	if run.Errors() != "" {
		t.Fatalf("execution_interrupted should not be classified as an execution error: %s", run.Errors())
	}
}

// render queues the workflow and waits for ComfyUI's own history to carry it.
func (c *comfyCard) render(t *testing.T, seed int) comfyCardRun {
	t.Helper()
	prompt := map[string]any{
		"1": map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{
			"unet_name": comfyCardWeights.unet, "weight_dtype": "default"}},
		"2": map[string]any{"class_type": "CLIPLoader", "inputs": map[string]any{
			"clip_name": comfyCardWeights.clip, "type": "wan"}},
		"3": map[string]any{"class_type": "VAELoader", "inputs": map[string]any{"vae_name": comfyCardWeights.vae}},
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
			"images": []any{"8", 0}, "filename_prefix": "oa-card"}},
	}
	return c.renderPrompt(t, prompt)
}

func (c *comfyCard) renderPrompt(t *testing.T, prompt map[string]any) comfyCardRun {
	t.Helper()
	return c.waitPrompt(t, c.queuePrompt(t, prompt), 15*time.Minute)
}

func (c *comfyCard) queuePrompt(t *testing.T, prompt map[string]any) string {
	t.Helper()
	var queued struct {
		PromptID   string         `json:"prompt_id"`
		NodeErrors map[string]any `json:"node_errors"`
	}
	c.call(t, http.MethodPost, "/prompt", map[string]any{"prompt": prompt, "client_id": "oa-card-live"}, &queued)
	if len(queued.NodeErrors) > 0 {
		t.Fatalf("ComfyUI rejected the workflow: %+v", queued.NodeErrors)
	}
	if queued.PromptID == "" {
		t.Fatal("ComfyUI accepted the workflow and named no prompt")
	}
	return queued.PromptID
}

func (c *comfyCard) waitPrompt(t *testing.T, promptID string, timeout time.Duration) comfyCardRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		history := map[string]comfyCardRun{}
		c.call(t, http.MethodGet, "/history/"+promptID, nil, &history)
		if run, ok := history[promptID]; ok {
			return run
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the render %s did not finish within %s", promptID, timeout)
	return comfyCardRun{}
}

// copyNodeTree copies one directory, leaving __pycache__ behind.
func copyNodeTree(from, to string) error {
	return filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(to, relative), 0o700)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, relative), body, 0o600)
	})
}
