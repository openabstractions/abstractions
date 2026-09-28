package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	for _, row := range during.Holders {
		if !strings.EqualFold(row.Program, comfy.python) {
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
        from abstraction.ipc import ServerExpectation
        expected = ServerExpectation(1 if sys.platform == "win32" else 2,
                                     os.environ["OA_CARD_UID"], os.environ["OA_CARD_IMAGE"])
        endpoint = os.environ["OA_CARD_ENDPOINT"]
        card.configure(lambda deadline: Machine(endpoint, server=expected, deadline=deadline))
        card.install()


@PromptServer.instance.routes.get("/oa-card/status")
async def status(request):
    body = dict(node_loaded=node is not None)
    if card is not None:
        body["card"] = card.status()._asdict()
        holding = card.held()
        body["held"] = dict(lease=holding.lease or "", amount=holding.amount, files=holding.files)
    return web.json_response(body)
`

// comfyCard is one ComfyUI this test runs and the scratch directories it uses.
type comfyCard struct {
	python, root, models, pythonPath, ipcPrefix string
	base, url                                   string
	process                                     *exec.Cmd
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
		python:    comfyCardEnv("OA_LIVE_CARD_PYTHON", filepath.Join(bundle, "venv", "Scripts", "python.exe")),
		root:      comfyCardEnv("OA_LIVE_CARD_COMFYUI", filepath.Join(forks, "scratch-comfyui-card")),
		models:    comfyCardEnv("OA_LIVE_CARD_MODELS", filepath.Join(bundle, "ComfyUI")),
		ipcPrefix: comfyCardEnv("ABSTRACTION_IPC_PREFIX", filepath.Join(forks, "scratch-comfyui-oa", "prefix")),
	}
	layers := []string{"identity", "facade", "job", "download", "model", "storage", "logging", "resource"}
	var paths []string
	for _, layer := range layers {
		paths = append(paths, filepath.Join(repo, "openabstractions-flat", "abstraction-"+layer, "py"))
	}
	paths = append(paths, filepath.Join(forks, "scratch-comfyui-oa", "python"))
	c.pythonPath = comfyCardEnv("OA_LIVE_CARD_PYTHONPATH", strings.Join(paths, string(os.PathListSeparator)))
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

// start writes a fresh ComfyUI base directory, installs the adapter node and
// the harness beside it, and runs ComfyUI until the test ends.
func (c *comfyCard) start(t *testing.T, endpoint, account string, reserve int64) {
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
	body := fmt.Sprintf("comfyui:\n    base_path: %s/\n    diffusion_models: models/diffusion_models/\n"+
		"    text_encoders: models/text_encoders/\n    vae: models/vae/\n", c.models)
	if err := os.WriteFile(extra, []byte(body), 0o600); err != nil {
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
	log := new(bytes.Buffer)
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
		Lease  string `json:"lease"`
		Amount int64  `json:"amount"`
	} `json:"held"`
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
		t.Fatalf("ComfyUI %s %s: %s %s", method, path, response.Status, raw)
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
	var queued struct {
		PromptID string `json:"prompt_id"`
	}
	c.call(t, http.MethodPost, "/prompt", map[string]any{"prompt": prompt, "client_id": "oa-card-live"}, &queued)
	if queued.PromptID == "" {
		t.Fatal("ComfyUI accepted the workflow and named no prompt")
	}
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		history := map[string]comfyCardRun{}
		c.call(t, http.MethodGet, "/history/"+queued.PromptID, nil, &history)
		if run, ok := history[queued.PromptID]; ok {
			return run
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("the render %s did not finish within fifteen minutes", queued.PromptID)
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
