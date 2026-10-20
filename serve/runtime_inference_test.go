package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	fullwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	facaderuntime "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	acceptance "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	logging "github.com/openabstractions/abstraction-logging/go"
	bridge "github.com/openabstractions/abstraction-provider-modelbridge/go/bridge"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
	modelhostservice "github.com/openabstractions/abstraction-provider-modelhost/go/service"
	resclient "github.com/openabstractions/abstraction-resource/go/client"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
	mcpfixture "github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

func TestRemoteInferenceLogDisplayProjection(t *testing.T) {
	rec := inferenceLogRecord(inference.Record{Domain: "remote-key", Claim: "fixture\nclaim", Route: "remote"}, time.Unix(1, 0))
	if rec.Attrs["domain"] != "remote-key" || rec.Attrs["claim"] != "fixture claim" || rec.Attrs["route"] != "remote" {
		t.Fatalf("projection: %+v", rec.Attrs)
	}
}

func TestInferenceTimingLogKeepsClockFactsClaimsAndAbsence(t *testing.T) {
	queue, load, restore, prefill, decode, size, reused, evicted := int64(0), int64(11), int64(5), int64(14), int64(20), int64(4096), int64(40), int64(1)
	enginePrompt, engineDecode := 999.0, 777.0
	r := inference.Record{ChatVersion: 3, Timing: &inference.RequestTiming{QueueMS: &queue, LoadMS: &load, RestoreMS: &restore,
		PrefillMS: &prefill, DecodeMS: &decode, LoadBytes: &size, ReusedTokens: &reused, Load: "loaded", Restore: "hit",
		RestoreCause: "resident", CacheEvictions: &evicted,
		Engine: inference.EngineTiming{PromptMS: &enginePrompt, DecodeMS: &engineDecode}}}
	attrs := inferenceLogRecord(r, time.Unix(1, 0)).Attrs
	for key, want := range map[string]string{"oa.inference.queue_ms": "0", "oa.inference.load_ms": "11", "oa.inference.restore_ms": "5",
		"oa.inference.prefill_ms": "14", "oa.inference.decode_ms": "20", "oa.inference.load_bytes": "4096", "oa.inference.load": "loaded",
		"oa.inference.restore": "hit", "gen_ai.server.time_to_first_token": "0.014", "gen_ai.usage.cache_read.input_tokens": "40",
		"oa.engine.prompt_ms": "999", "oa.engine.decode_ms": "777"} {
		if attrs[key] != want {
			t.Fatalf("%s: %q want %q", key, attrs[key], want)
		}
	}
	if attrs["oa.inference.restore_cause"] != "resident" || attrs["oa.inference.cache_evictions"] != "1" {
		t.Fatal(attrs)
	}
	if _, present := attrs["oa.inference.load_source"]; present {
		t.Fatal("invented source")
	}
	for key := range inferenceLogRecord(inference.Record{}, time.Unix(1, 0)).Attrs {
		if strings.HasPrefix(key, "oa.inference.") || strings.HasPrefix(key, "oa.engine.") {
			t.Fatalf("invented timing %s", key)
		}
	}
}

func TestInferenceTimingLogSeparatesRelayClockAndDelegatedClaims(t *testing.T) {
	queue, relay, remoteQueue, remoteLoad := int64(12), int64(840), int64(4), int64(3100)
	r := inference.Record{ChatVersion: 3, WallMS: 900, Timing: &inference.RequestTiming{QueueMS: &queue, RelayMS: &relay},
		DelegatedTiming: &inference.DelegatedTiming{Timing: iwire.ChatTiming{QueueMs: &remoteQueue, LoadMs: &remoteLoad},
			ServedBy: &iwire.ServedBy{Place: iwire.ServedPlaceRemote, Server: "inner", Machine: "nas"}}}
	attrs := inferenceLogRecord(r, time.Unix(1, 0)).Attrs
	for key, want := range map[string]string{"oa.inference.queue_ms": "12", "oa.inference.relay_ms": "840", "oa.inference.end_to_end_ms": "900",
		"oa.remote.timing": "delegated", "oa.remote.queue_ms": "4", "oa.remote.load_ms": "3100", "oa.remote.machine": "nas", "oa.remote.server": "inner"} {
		if attrs[key] != want {
			t.Fatalf("%s: %q want %q", key, attrs[key], want)
		}
	}
	for _, key := range []string{"oa.inference.load_ms", "oa.inference.prefill_ms", "oa.inference.decode_ms", "oa.engine.prompt_ms"} {
		if _, present := attrs[key]; present {
			t.Fatalf("delegated claim became local fact: %s", key)
		}
	}
}

// The fixture is byte-exact provider_data(distilled-bf16) from Python source
// c7688d00f, SHA256 e71f5dc126d130c9f64c0c43cbef1d806c69634eec53ed8f8dc4f0431fb9fcbb.
// Only this test contains product recipe data; runtime composition knows none.
func TestRuntimeComfyRegisteredHostDiscoversRecipeAndRunsGenericImage(t *testing.T) {
	raw, err := os.ReadFile("testdata/comfy_recipe_bf16.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "e71f5dc126d130c9f64c0c43cbef1d806c69634eec53ed8f8dc4f0431fb9fcbb" {
		t.Fatal("fixture changed")
	}
	var recipe map[string]any
	if err := json.Unmarshal(raw, &recipe); err != nil {
		t.Fatal(err)
	}
	assets := recipe["components"].([]any)
	for _, asset := range assets {
		asset.(map[string]any)["state"] = "ready"
	}
	manifest := map[string]any{"id": recipe["id"], "variant": recipe["variant"], "manifest_sha256": recipe["manifest_sha256"], "ready": true, "assets": assets}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int64
	var malformed atomic.Bool
	var missing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/models":
			fmt.Fprint(w, `["checkpoints","diffusion_models","text_encoders","vae"]`)
		case "/models/checkpoints":
			fmt.Fprint(w, `["stock.safetensors"]`)
		case "/system_stats":
			fmt.Fprint(w, `{"devices":[]}`)
		case "/abstraction/recipes":
			if missing.Load() {
				http.NotFound(w, request)
				return
			}
			if malformed.Load() {
				fmt.Fprint(w, `{"schema":"unknown","recipes":[]}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"schema": "oa.comfy.recipes@1", "recipes": []any{map[string]any{"recipe": json.RawMessage(raw), "manifest_path": "/fixture-manifest"}}})
		case "/fixture-manifest":
			_ = json.NewEncoder(w).Encode(manifest)
		case "/prompt":
			posts.Add(1)
			var body struct {
				Prompt map[string]struct {
					Class  string         `json:"class_type"`
					Inputs map[string]any `json:"inputs"`
				} `json:"prompt"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			found := false
			for _, node := range body.Prompt {
				if node.Class == "CLIPTextEncode" && node.Inputs["text"] == "generic runtime caller" {
					found = true
				}
			}
			if !found {
				t.Error("generic prompt did not reach the declared recipe slot")
			}
			fmt.Fprint(w, `{"prompt_id":"fixture"}`)
		case "/history/fixture":
			_ = json.NewEncoder(w).Encode(map[string]any{"fixture": map[string]any{"status": map[string]any{"completed": true}, "outputs": map[string]any{recipe["output_node"].(string): map[string]any{"images": []any{map[string]any{"filename": "out.png"}}}}}})
		case "/view":
			_, _ = w.Write(pngBytes.Bytes())
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	rightsState := testRights(t, t.TempDir())
	if err := rightsState.install([]string{ActionProviderManage, inference.ActionHostManage, inference.ActionComplete}, []installationRule{{inference.ActionHostManage, "account"}}); err != nil {
		t.Fatal(err)
	}
	providers := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}), report: func(err error) { t.Errorf("providers: %v", err) }}
	routes := router.New()
	ceilings, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "ceilings.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	operator := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: rightsState}, providers: providers, router: routes, ceilings: ceilings, report: func(err error) { t.Errorf("operator: %v", err) }}
	provider, err := inference.New(inference.Config{Router: routes, HTTP: server.Client(), ValidateHostBinding: providers.currentHostBinding, Decide: rightsState.decideInference,
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "applied"
		},
		PrepareContentWrite: func(context.Context, inference.Subject, string, string, int64) (inference.ContentCommitter, inference.ContentOutcome) {
			return func(context.Context, string, []byte) inference.ContentOutcome { return inference.ContentResolved }, inference.ContentResolved
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	bindHostDiscovery(providers, provider)
	providers.remotesChanged = operator.refreshHosts
	caller := inference.Subject{Account: rightsState.owner, Program: rightsState.operators[0]}
	_, revision, err := providers.read()
	if err != nil {
		t.Fatal(err)
	}
	entry := iwire.HostEntry{Name: "comfyui", Kind: "comfyui", Base: server.URL}
	change := operator.AddHost(context.Background(), caller, revision, entry)
	if change.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("ordinary AddHost: %+v", change)
	}
	files := providers.hostFiles()
	if len(files) != 1 {
		t.Fatalf("files: %+v", files)
	}
	oldBinding := files[0].bindingID()
	request := iwire.ImageRequest{Model: recipe["model_alias"].(string), Mode: iwire.ImageModeGenerate, Prompt: "generic runtime caller", Size: "512x512", Count: 1, Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeLocalOnly}}
	admission := provider.StartImage(context.Background(), caller, request)
	if admission.Outcome != iwire.StartOutcomeAccepted {
		t.Fatalf("generic image: %+v", admission)
	}
	finished := false
	for cursor, deadline := int64(0), time.Now().Add(5*time.Second); time.Now().Before(deadline) && !finished; {
		page := provider.ObserveImage(context.Background(), caller, admission.Operation, cursor, 256, 65536, 100)
		if page.Outcome != iwire.PageOutcomePage {
			t.Fatalf("observe: %+v", page)
		}
		for _, delta := range page.Deltas {
			cursor++
			if delta.ImageEnd != nil {
				if delta.ImageEnd.Outcome != iwire.ReplyOutcomeCompleted {
					t.Fatalf("image end: %+v", delta.ImageEnd)
				}
				finished = true
			}
		}
	}
	if !finished || posts.Load() != 1 {
		t.Fatalf("finished=%v posts=%d", finished, posts.Load())
	}
	malformed.Store(true)
	operator.refreshHosts()
	if next := provider.StartImage(context.Background(), caller, request); next.Outcome == iwire.StartOutcomeAccepted {
		t.Fatal("malformed discovery reused old route")
	}
	if posts.Load() != 1 {
		t.Fatal("malformed discovery posted")
	}
	malformed.Store(false)
	missing.Store(true)
	operator.refreshHosts()
	if next := provider.StartImage(context.Background(), caller, request); next.Outcome == iwire.StartOutcomeAccepted {
		t.Fatal("removed addon reused cached recipe alias")
	}
	if posts.Load() != 1 {
		t.Fatal("removed addon posted cached recipe")
	}
	if decision, _ := routes.Route(router.Request{Model: "stock.safetensors", Profile: router.ProfileImage}); decision.Verdict != router.WouldLoad || decision.Host != "comfyui" {
		t.Fatalf("absent optional addon hid stock checkpoint: %+v", decision)
	}
	missing.Store(false)
	_, revision, err = providers.read()
	if err != nil {
		t.Fatal(err)
	}
	if removed := operator.RemoveHost(context.Background(), caller, revision, "comfyui"); removed.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("remove: %+v", removed)
	}
	if providers.currentHostBinding(context.Background(), oldBinding, "comfyui", server.URL) {
		t.Fatal("removed binding retained")
	}
	_, revision, err = providers.read()
	if err != nil {
		t.Fatal(err)
	}
	if next := operator.AddHost(context.Background(), caller, revision, entry); next.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("redeclare: %+v", next)
	}
	if providers.currentHostBinding(context.Background(), oldBinding, "comfyui", server.URL) {
		t.Fatal("redeclared URL retained old generation")
	}
	fresh := providers.hostFiles()[0].bindingID()
	if !providers.currentHostBinding(context.Background(), fresh, "comfyui", server.URL) {
		t.Fatal("new declaration not current")
	}
	// Re-open the same registry directory as a restart; no recipe settings file.
	restarted := &runtimeProviders{dir: providers.dir, disabledPath: providers.disabledPath, report: providers.report}
	if !restarted.currentHostBinding(context.Background(), fresh, "comfyui", server.URL) {
		t.Fatal("restart lost existing registry binding")
	}
	restartRoutes := router.New()
	restartProvider, err := inference.New(inference.Config{Router: restartRoutes, HTTP: server.Client(), ValidateHostBinding: restarted.currentHostBinding, Decide: rightsState.decideInference,
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "applied"
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer restartProvider.Close()
	bindHostDiscovery(restarted, restartProvider)
	restartHosts, err := routerHosts(restarted)
	if err != nil {
		t.Fatal(err)
	}
	restartRoutes.SetHosts(restartHosts...)
	restartRoutes.Survey()
	if aliases, err := restartProvider.ReadyComfyRecipeAliases(context.Background(), fresh, server.URL); err != nil || len(aliases) != 1 || aliases[0] != request.Model {
		t.Fatalf("restart did not rediscover recipe from ordinary registry: %v %v", aliases, err)
	}
}

type failingInferenceDiagnosticSink struct {
	writes atomic.Int64
	called chan struct{}
}

func (s *failingInferenceDiagnosticSink) Write(logging.Record) error {
	s.writes.Add(1)
	select {
	case s.called <- struct{}{}:
	default:
	}
	return errors.New("diagnostic sink refused inference record")
}

// hostedUpstream is a fake OpenAI-compatible provider that lists one model and
// streams "Hello!" to a request carrying the bearer secret, recording the
// Authorization header of every chat request.
type hostedUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	chat []string
}

type chat3CompositionSink struct{}

func (chat3CompositionSink) Write(logging.Record) error { return nil }

// The older facade shape intentionally exposes no logical-owner binder.
type legacyChat3Composition struct{ runtime *runtimeInference }

func (l legacyChat3Composition) Serve(ctx context.Context) error  { return l.runtime.Serve(ctx) }
func (l legacyChat3Composition) Close() error                     { return l.runtime.Close() }
func (legacyChat3Composition) ChatV2Available() bool              { return true }
func (legacyChat3Composition) SelectionOperatorV2Available() bool { return true }

type chat3ComposedFixture struct {
	options   runtimeFlags
	rights    *runtimeRights
	asks      *runtimeAsks
	inference *runtimeInference
	facade    *facaderuntime.Host
}

func composeChat3Fixture(t *testing.T, legacy bool) *chat3ComposedFixture {
	t.Helper()
	if !statusTransportProvesProgram(t) {
		t.Skip("native Program proof unavailable")
	}
	options, _ := isolatedRuntime(t)
	writeStateFile(t, filepath.Join(options.stateDir, "inference", inferenceHostsFile), `{"local":[],"declared":false}`)
	rights, err := composeRights(options)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := composeCredentials(options, rights, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credentials.close)
	questions, err := composeAsks(options, rights, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	inferred, err := composeInference(options, credentials, nil, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	o := facaderuntime.Options{Endpoint: options.endpoint, LogEndpoint: options.logEndpoint, ConfigEndpoint: options.configEndpoint,
		JobEndpoint: options.jobEndpoint, JobRoot: filepath.Join(options.stateDir, "jobs"), JobOwner: rights.owner, Sink: chat3CompositionSink{}}
	o.ConfigStore, o.ConfigUserKey = isolatedConfigStore(options.stateDir)
	o.ConfigWithoutMachine = true
	routerEndpoint, err := runtimeRouterEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	resourceEndpoint, err := resourceTableEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	inferred.configure(&o, routerEndpoint, resourceEndpoint)
	questions.configure(&o)
	rights.configure(&o, acceptance.ResourceActions)
	if err := rights.install(o.RightsActions, installationRules(o, nil)); err != nil {
		t.Fatal(err)
	}
	if legacy {
		o.Inference = legacyChat3Composition{inferred}
	}
	facade, err := facaderuntime.Listen(o)
	if err != nil {
		inferred.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- facade.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := facade.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("composed facade did not stop")
		}
	})
	return &chat3ComposedFixture{options: options, rights: rights, asks: questions, inference: inferred, facade: facade}
}

func (f *chat3ComposedFixture) chat(t *testing.T, contract string, scope fwire.Scope) (*listen.FrameClient, fwire.ResolveResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resolved, err := resolution.NewUnverifiedClient(f.options.endpoint, time.Second).Resolve(ctx, fwire.ResolveRequest{Capability: "abstraction.inference", Contracts: []string{contract}, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != fwire.ResolutionStatusResolved {
		return nil, resolved
	}
	binding, err := resolution.BindLocal(t.Context(), *resolved.Reference, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &binding, resolved
}

func TestChat3ActualRuntimeCompositionBindsOnlyLocalAndKeepsOlderFacadeUnready(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			f := composeChat3Fixture(t, legacy)
			binding, resolved := f.chat(t, "abstraction.inference/chat@3", fwire.ScopeLocal)
			// Production composition serves and binds chat@3 for local
			// applications; only an older facade without the binder withholds it.
			_, productionBinder := any(f.inference).(interface{ BindChatV3LogicalOwner(string) error })
			if !productionBinder || !f.inference.host.ApplicationChatV3 {
				t.Fatal("production composition does not enable application chat3")
			}
			if legacy {
				if _, legacyBinder := any(legacyChat3Composition{f.inference}).(interface{ BindChatV3LogicalOwner(string) error }); legacyBinder {
					t.Fatal("older facade shape gained a binder")
				}
				if binding != nil || resolved.Status == fwire.ResolutionStatusResolved {
					t.Fatal("unbound older facade advertised chat3", resolved)
				}
				if old, _ := f.chat(t, "abstraction.inference/chat@2", fwire.ScopeLocal); old == nil {
					t.Fatal("older chat2 disappeared")
				}
				return
			}
			if binding == nil || resolved.Reference.Endpoint != f.inference.endpoint {
				t.Fatal(resolved)
			}
			window, err := iwire.NewChatV3Client(binding.WithDefaults(5*time.Second, 1<<20)).GetHistoryWindow()
			if err != nil || window.Window == nil || window.Window.LogicalOwner != f.facade.JobLogicalOwner() || window.Window.LogicalOwner == "" {
				t.Fatal(window, err)
			}
			if remote, _ := f.chat(t, "abstraction.inference/chat@3", fwire.ScopeRemote); remote != nil {
				t.Fatal("remote chat3 advertised")
			}
			_, err = iwire.NewChatV3Client(listen.FrameClient{Endpoint: f.inference.remoteEndpoint, Timeout: time.Second}).GetHistoryWindow()
			if err == nil || !strings.Contains(err.Error(), "unknown_service") {
				t.Fatal("remote endpoint served chat3", err)
			}
		})
	}
}

func TestChat3ActualCompositionFirstUseAllowsOneEffectAndRefusesBeforeEffect(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			f := composeChat3Fixture(t, false)
			upstream, posts := fakeOllama(t)
			f.inference.router.SetHosts(router.OpenAICompatible("fixture", upstream.URL, "/v1", []string{"fixture-chat:1b"}))
			f.inference.router.Survey()
			binding, _ := f.chat(t, "abstraction.inference/chat@3", fwire.ScopeLocal)
			chat := iwire.NewChatV3Client(binding.WithDefaults(10*time.Second, 1<<20))
			window, err := chat.GetHistoryWindow()
			if err != nil || window.Window == nil {
				t.Fatal(window, err)
			}
			request := iwire.RequestV3{Identity: acceptance.RequestIdentity{Key: "one-first-use", HistoryEpoch: window.Window.HistoryEpoch}, Target: iwire.ChatTarget{Model: "fixture-chat:1b"}, Content: iwire.ChatContent{Messages: []iwire.ChatMessage{{Role: iwire.RoleUser, Parts: []iwire.ChatInputPart{{Kind: iwire.PartKindText, Text: "inert"}}}}, Guarantees: []iwire.ChatGuarantee{iwire.ChatGuaranteeLocalOnly}}}
			type response struct {
				out iwire.ChatAdmission
				err error
			}
			returned := make(chan response, 1)
			go func() { out, err := chat.Start(request); returned <- response{out, err} }()
			var id string
			deadline := time.Now().Add(5 * time.Second)
			for id == "" && time.Now().Before(deadline) {
				records, _, err := f.asks.book.ApplicationHistory()
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range records {
					if !r.Pending() {
						continue
					}
					id = r.ID
					break
				}
				if id == "" {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if id == "" || posts.Load() != 0 {
				select {
				case got := <-returned:
					t.Fatalf("first use returned before question: %+v %v", got.out, got.err)
				default:
				}
				t.Fatal("first use escaped or missing", id, posts.Load())
			}
			option := "never"
			if allow {
				option = "allow"
			}
			if _, _, err := f.asks.book.AnswerApplication(id, option); err != nil {
				t.Fatal(err)
			}
			if allow {
				snapshot, err := f.rights.policy.OperatorSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				edit, err := f.asks.applyOperatorAllow(t.Context(), f.rights.self(), snapshot.Revision, id, func() error { return nil })
				if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
					t.Fatal(edit, err)
				}
			}
			select {
			case got := <-returned:
				if got.err != nil {
					t.Fatal(got.err)
				}
				if !allow {
					if got.out.Outcome != iwire.ChatStartOutcomeNotPermitted || posts.Load() != 0 {
						t.Fatal(got.out, posts.Load())
					}
					return
				}
				if got.out.Outcome != iwire.ChatStartOutcomeAccepted {
					t.Fatal(got.out)
				}
				cursor := int64(0)
				for {
					page, err := chat.Observe(got.out.Request, cursor, 256, 65536, 1000)
					if err != nil || page.Outcome != iwire.PageOutcomePage {
						t.Fatal(page, err)
					}
					cursor = page.Next
					if page.AtEnd {
						break
					}
				}
				if posts.Load() != 1 {
					t.Fatal("upstream effects", posts.Load())
				}
			case <-time.After(8 * time.Second):
				t.Fatal("native admission did not settle")
			}
		})
	}
}

type chat3FixtureACL struct{}

func (chat3FixtureACL) Restrict([]string) error                  { return nil }
func (chat3FixtureACL) Check(string, *programfiles.Handle) error { return nil }

type chat3FixtureObjects struct{ path string }

func (s chat3FixtureObjects) Resolve(store, id string, _ bridge.Accepts) (modelhost.Object, error) {
	if store != "fixture" || (id != "tiny" && id != "main") {
		return modelhost.Object{}, fmt.Errorf("unknown fixture object")
	}
	return modelhost.Object{Store: store, ID: id, Path: s.path, File: "model.gguf", Size: 2048}, nil
}
func (chat3FixtureObjects) Stores() []string   { return []string{"fixture"} }
func (chat3FixtureObjects) Root(string) string { return "" }

type chat3FixtureLeases struct{ normal, free, released atomic.Int32 }

func (b *chat3FixtureLeases) Acquire(resource string, amount int64, _ time.Duration) (resclient.AcquireResult, error) {
	n := b.normal.Add(1)
	return resclient.AcquireResult{Outcome: resclient.AcquireOutcomeAcquired, Lease: &resclient.Lease{ID: fmt.Sprintf("normal-%d", n), Resource: resource, Amount: amount}, Asked: []resclient.YieldRecord{}}, nil
}
func (b *chat3FixtureLeases) AcquireFree(resource string, amount int64) (resclient.AcquireFreeResult, error) {
	n := b.free.Add(1)
	return resclient.AcquireFreeResult{Outcome: resclient.AcquireFreeOutcomeAcquired, Lease: &resclient.Lease{ID: fmt.Sprintf("free-%d", n), Resource: resource, Amount: amount}, Asked: []resclient.YieldRecord{}}, nil
}
func (*chat3FixtureLeases) Renew(string) (resclient.LeaseChange, error) {
	return resclient.LeaseChange{Applied: true}, nil
}
func (*chat3FixtureLeases) BindProcess(string, int) (resclient.ProcessBindResult, error) {
	return resclient.ProcessBindResult{Outcome: resclient.ProcessBindOutcomeBound}, nil
}
func (*chat3FixtureLeases) Label(string, string) (resclient.LeaseChange, error) {
	return resclient.LeaseChange{Applied: true}, nil
}

func (b *chat3FixtureLeases) Release(string) (resclient.LeaseChange, error) {
	b.released.Add(1)
	return resclient.LeaseChange{Applied: true}, nil
}
func (*chat3FixtureLeases) Observe(ctx context.Context, cursor string, wait time.Duration) (resclient.YieldPage, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	return resclient.YieldPage{Requests: []resclient.YieldRequest{}, Next: cursor}, nil
}
func (*chat3FixtureLeases) Answer(context.Context, string, resclient.YieldAnswer, string) (resclient.AnswerResult, error) {
	return resclient.AnswerResult{Recorded: true}, nil
}

type chat3ObservedScope struct {
	scope             string
	affinity, persist bool
	use               iwire.ChatUse
	hasUse            bool
}

func nextChat3Scope(t *testing.T, seen <-chan chat3ObservedScope) chat3ObservedScope {
	t.Helper()
	select {
	case value := <-seen:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("real leaf did not receive execution scope")
		return chat3ObservedScope{}
	}
}

func TestRuntimeInferencePrivateModelLeafFixture(t *testing.T) {
	module := privateInput(t, "..", "openabstractions-flat", "abstraction-provider-modelhost", "go")
	if _, err := os.Stat(filepath.Join(module, "host", "testdata", "fakeengine", "main.go")); err != nil {
		t.Fatal(err)
	}
	reached := false
	t.Run("published-checkout", func(t *testing.T) {
		t.Chdir(t.TempDir())
		privateInput(t, "..", "openabstractions-flat", "abstraction-provider-modelhost", "go")
		reached = true
	})
	if reached {
		t.Fatal("published checkout continued without the private model-leaf fixture")
	}
}

// This reuses modelhost's existing fakeengine source and real pinned process,
// native leaf, admission, EngineClient and holder owners. No model or GPU runs.
func (f *chat3ComposedFixture) modelLeaf(t *testing.T) (*modelhost.Host, *chat3FixtureLeases, chan chat3ObservedScope, string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("existing engine peer proof requires Windows")
	}
	engine := filepath.Join(t.TempDir(), "fakeengine.exe")
	build := exec.Command("go", "-C", privateInput(t, "..", "openabstractions-flat", "abstraction-provider-modelhost", "go"), "build", "-o", engine, "./host/testdata/fakeengine")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("existing fakeengine build: %v %s", err, out)
	}
	bytes, err := os.ReadFile(engine)
	if err != nil {
		t.Fatal(err)
	}
	weights := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(weights, make([]byte, 2048), 0600); err != nil {
		t.Fatal(err)
	}
	sockets, err := os.MkdirTemp("", "oa-c3-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	book := &chat3FixtureLeases{}
	model, err := modelhost.New(modelhost.Options{Models: []modelhost.Model{{Name: "tiny:1b", Store: "fixture", Object: "tiny"}, {Name: "main:2b", Store: "fixture", Object: "main"}},
		Engine: modelhost.Engine{Program: engine, Pin: modelhost.EnginePin{Release: "test", Program: filepath.Base(engine), Files: []programfiles.File{{Path: filepath.Base(engine), Size: int64(len(bytes)), SHA256: fmt.Sprintf("%x", sha256.Sum256(bytes))}}},
			Record: filepath.Join(t.TempDir(), "engine.files"), Sockets: filepath.Join(sockets, "s"), Files: programfiles.NewVerifier(programfiles.Options{ACL: chat3FixtureACL{}})},
		Source: chat3FixtureObjects{weights}, Leases: book, Idle: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Close)
	leafRoutes := router.New(router.OpenAICompatible("modelhost-engine", model.Base("tiny:1b"), "/v1", model.Models()))
	cfg := modelhostservice.ProviderConfig(model, leafRoutes, func(err error) { t.Log(err) })
	seen := make(chan chat3ObservedScope, 16)
	wait := cfg.WaitExecution
	cfg.WaitExecution = func(ctx context.Context, h *router.Host, name string) inference.ExecutionPreparation {
		scope, affinity, persist := inference.ExecutionCacheScope(ctx)
		use, hasUse := inference.ExecutionUse(ctx)
		seen <- chat3ObservedScope{scope: scope, affinity: affinity, persist: persist, use: use, hasUse: hasUse}
		return wait(ctx, h, name)
	}
	provider, err := inference.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self = identity.NormalizeSubjectProgram(self)
	name := fmt.Sprintf("chat3-leaf-%d", time.Now().UnixNano())
	endpoint := mustProviderEndpoint(t, name)
	leaf, err := modelhostservice.Listen(endpoint, provider, self)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- leaf.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	})
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	file := providerFile{Declaration: providerDeclaration{Name: name, Program: self, Endpoint: name, Transport: transportNative, Contracts: []string{inference.Contract}, Models: []string{"tiny:1b"}, Activation: fullwire.ActivationOnDemand.String()}}
	supervisor := newProviderSupervisor(f.inference.providers, file)
	supervisor.set(fullwire.DeclarationReadinessReady, "")
	f.inference.providers.mu.Lock()
	f.inference.providers.supervisors[name] = supervisor
	f.inference.providers.mu.Unlock()
	native, err := router.NewNative(name, endpoint, listen.ServerExpectation{Principal: principal, Program: self}, file.Declaration.Models, []string{router.ProfileChat})
	if err != nil {
		t.Fatal(err)
	}
	native.BindingID = file.bindingID()
	f.inference.router.SetHosts(native)
	f.inference.router.Survey()
	return model, book, seen, name
}

func (f *chat3ComposedFixture) nodePolicy(t *testing.T, name, model string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	node = identity.NormalizeSubjectProgram(node)
	change := f.inference.operator.putSelectionScopeV2(t.Context(), inference.Subject{Account: f.rights.owner, Program: node}, "", iwire.SelectionPolicyV2{Name: name, Candidates: []iwire.SelectionCandidateV2{{Model: model, Placement: iwire.CandidatePlacementLocal}}})
	if change.Outcome != iwire.EditOutcomeApplied {
		t.Fatal(change)
	}
	return node
}

// requireIPCAddon skips a native composition test on a machine without the
// built IPC Node addon; scripts/build_node_addon.py builds it and names it in
// ABSTRACTION_IPC_NODE.
func requireIPCAddon(t *testing.T) {
	t.Helper()
	if os.Getenv("ABSTRACTION_IPC_NODE") == "" {
		t.Skip("native composition needs the built IPC Node addon (ABSTRACTION_IPC_NODE is unset)")
	}
}

func (f *chat3ComposedFixture) openCode(t *testing.T, mode, model string) {
	t.Helper()
	requireIPCAddon(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	provider := stageOpenCodeProvider(t)
	mcpfixture.CopyTree(t, filepath.Join(os.Getenv("OA_AI_SDK_PROVIDER_MODULES"), "jsonc-parser"), filepath.Join(filepath.Dir(provider), "node_modules", "jsonc-parser"))
	fixture, err := json.Marshal(map[string]any{"endpoint": f.options.endpoint, "server": map[string]any{"principalKind": 1, "principal": account.Uid, "program": identity.NormalizeSubjectProgram(self)}, "config": filepath.Join(t.TempDir(), "opencode.json"), "mode": mode, "model": model})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--test", "--test-name-pattern", "^native runtime composition uses the actual OpenCode adapter$", filepath.Join(filepath.Dir(provider), "test", "chat3.test.mjs"))
	command.Env = append(os.Environ(), "OA_CHAT3_NATIVE_COMPOSITION="+string(fixture), "ABSTRACTION_RUNTIME_ENDPOINT="+f.options.endpoint)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual OpenCode adapter %s: %v\n%s", mode, err, out)
	} else {
		t.Log(string(out))
	}
}

func TestChat3OpenCodeAdapterSessionMapping(t *testing.T) {
	provider := stageOpenCodeProvider(t)
	for _, file := range []string{"chat3.test.mjs", "v2.test.mjs", "reply-place.test.mjs"} {
		command := exec.Command("node", "--test", filepath.Join(filepath.Dir(provider), "test", file))
		if out, err := command.CombinedOutput(); err != nil {
			t.Errorf("OpenCode adapter %s: %v\n%s", file, err, out)
		} else {
			t.Log(string(out))
		}
	}
}

func TestChat3OpenCodeRealLeafTitleAndSessionScopes(t *testing.T) {
	requireIPCAddon(t)
	f := composeChat3Fixture(t, false)
	model, leases, seen, name := f.modelLeaf(t)
	if _, err := model.Ensure(t.Context(), "main:2b"); err != nil {
		t.Fatal(err)
	}
	main := model.Lease("main:2b")
	node := f.nodePolicy(t, "title", "tiny:1b")
	setCompleteRule(t, f.options.endpoint, node, name, true)
	f.openCode(t, "title", "tiny:1b")
	title := nextChat3Scope(t, seen)
	if title.scope == "" || title.affinity || !title.hasUse || title.use != iwire.ChatUseTitle || leases.free.Load() != 1 || leases.normal.Load() != 1 || leases.released.Load() != 0 || model.Lease("main:2b") != main {
		t.Fatal("title disturbed main lease or carried conversation affinity", title, leases.free.Load(), leases.normal.Load(), leases.released.Load())
	}
	f.openCode(t, "stream", "tiny:1b")
	turn := nextChat3Scope(t, seen)
	if turn.scope == "" || !turn.affinity || turn.scope == title.scope || turn.hasUse {
		t.Fatal("session label did not reach leaf", turn, title)
	}
	f.openCode(t, "lost-start", "tiny:1b")
	replayed := nextChat3Scope(t, seen)
	if replayed.scope != turn.scope || !replayed.affinity {
		t.Fatal("frozen replay changed session scope", replayed, turn)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	setCompleteRule(t, f.options.endpoint, identity.NormalizeSubjectProgram(self), name, true)
	binding, _ := f.chat(t, "abstraction.inference/chat@3", fwire.ScopeLocal)
	chat := iwire.NewChatV3Client(binding.WithDefaults(10*time.Second, 1<<20))
	window, err := chat.GetHistoryWindow()
	if err != nil || window.Window == nil {
		t.Fatal(window, err)
	}
	accepted, err := chat.Start(iwire.RequestV3{Identity: acceptance.RequestIdentity{Key: "same-label-other-program", HistoryEpoch: window.Window.HistoryEpoch}, Conversation: "shared-session", Target: iwire.ChatTarget{Model: "tiny:1b"}, Content: iwire.ChatContent{Messages: []iwire.ChatMessage{{Role: iwire.RoleUser, Parts: []iwire.ChatInputPart{{Kind: iwire.PartKindText, Text: "inert"}}}}, Guarantees: []iwire.ChatGuarantee{iwire.ChatGuaranteeLocalOnly}}})
	if err != nil || accepted.Outcome != iwire.ChatStartOutcomeAccepted {
		t.Fatal(accepted, err)
	}
	other := nextChat3Scope(t, seen)
	if other.scope == turn.scope || !other.affinity {
		t.Fatal("two actual programs shared a conversation scope", other, turn)
	}
}

func TestChat3OpenCodeActualOlderFacadeFallsBackAndKeepsTitleDisabled(t *testing.T) {
	requireIPCAddon(t)
	f := composeChat3Fixture(t, true)
	upstream, posts := fakeOllama(t)
	f.inference.router.SetHosts(router.OpenAICompatible("fixture", upstream.URL, "/v1", []string{"fixture-chat:1b"}))
	f.inference.router.Survey()
	node := f.nodePolicy(t, "local", "fixture-chat:1b")
	f.nodePolicy(t, "title", "fixture-chat:1b")
	setCompleteRule(t, f.options.endpoint, node, "fixture", true)
	f.openCode(t, "legacy", "fixture-chat:1b")
	if posts.Load() != 1 {
		t.Fatal("chat2 fallback effects", posts.Load())
	}
	f.openCode(t, "unready-title", "fixture-chat:1b")
	if posts.Load() != 1 {
		t.Fatal("unready title executed", posts.Load())
	}
}

func TestChat3OpenCodeRemoteScopeNegotiatesOnlyChat2(t *testing.T) {
	requireIPCAddon(t)
	f := composeChat3Fixture(t, false)
	f.nodePolicy(t, "local", "fixture-chat:1b")
	f.openCode(t, "remote", "fixture-chat:1b")
}

func newHostedUpstream(t *testing.T) *hostedUpstream {
	t.Helper()
	u := &hostedUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorized := r.Header.Get("Authorization") == "Bearer "+testCredentialSecret
		if r.Method == http.MethodPost {
			u.mu.Lock()
			u.chat = append(u.chat, r.Header.Get("Authorization"))
			u.mu.Unlock()
		}
		if !authorized {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"data":[{"id":"anthropic/claude-sonnet-5"}]}`))
			return
		}
		for _, c := range []string{"Hel", "lo", "!"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *hostedUpstream) authorizations() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.chat...)
}

func TestRuntimeInferenceDiagnosticSinkFailureDoesNotDuplicateWork(t *testing.T) {
	upstream, chats := fakeOllama(t)
	options, _ := isolatedRuntime(t)
	saved := declarationEnv
	declarationEnv = fixtureDeclarations(nil, map[string]string{"OLLAMA_HOST": upstream.URL})
	t.Cleanup(func() { declarationEnv = saved })
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"declared":true}`
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	rightsState, err := composeRights(options)
	if err != nil {
		t.Fatal(err)
	}
	program := rightsState.operators[0]
	if err := rightsState.policy.RegisterAction(inference.ActionComplete); err != nil {
		t.Fatal(err)
	}
	if err := rightsState.policy.Set(rwire.Subject{Account: rightsState.owner, Program: program}, inference.ActionComplete, inference.ResourceHost("ollama"), true); err != nil {
		t.Fatal(err)
	}
	var reports chan error = make(chan error, 8)
	report := func(err error) {
		select {
		case reports <- err:
		default:
		}
	}
	credentials, err := composeCredentials(options, rightsState, report)
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		if chats.Load() != 0 {
			t.Fatal("unproven runtime reached the upstream host")
		}
		assertListenersReleased(t, options)
		t.Skip("inference diagnostic path requires Program proof; Unix startup refusal is checked separately")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer credentials.close()
	sink := &failingInferenceDiagnosticSink{called: make(chan struct{}, 1)}
	runtime, err := composeInference(options, credentials, sink, report)
	if err != nil {
		t.Fatal(err)
	}
	if runtime == nil {
		t.Fatal("composeInference returned nil")
	}
	defer runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	subject := inference.Subject{Account: rightsState.owner, Program: program}
	admission := runtime.provider.Start(ctx, subject, iwire.Request{Model: "fixture-chat:1b", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeLocalOnly}, Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "hello"}}}}})
	if admission.Outcome != iwire.StartOutcomeAccepted {
		t.Fatalf("admission = %+v", admission)
	}
	var end *iwire.Reply
	cursor := int64(0)
	for {
		page := runtime.provider.Observe(ctx, subject, admission.Operation, cursor, 256, 65536, 500)
		if page.Outcome != iwire.PageOutcomePage {
			t.Fatalf("observe = %+v", page)
		}
		for _, delta := range page.Deltas {
			if delta.End != nil {
				end = delta.End
			}
		}
		if page.AtEnd {
			break
		}
		cursor = page.Next
	}
	if end == nil || end.Outcome != iwire.ReplyOutcomeCompleted {
		t.Fatalf("end = %+v", end)
	}
	if got := chats.Load(); got != 1 {
		t.Fatalf("upstream chats = %d, want one accepted operation", got)
	}
	select {
	case <-sink.called:
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostic sink was not called")
	}
	if sink.writes.Load() != 1 {
		t.Fatalf("diagnostic writes = %d, want one", sink.writes.Load())
	}
	select {
	case reportErr := <-reports:
		if !strings.Contains(reportErr.Error(), "diagnostic sink refused") {
			t.Fatalf("reported error = %v", reportErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostic sink failure was not reported")
	}
}

// setCompleteRule grants or denies abstraction.inference/complete on host:<name>
// to program through the runtime's rights operator.
func setCompleteRule(t *testing.T, endpoint, program, host string, permit bool) {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator, err := client.New(endpoint).ResolveRightsOperator(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("list policy: %+v %v", page, err)
	}
	rule := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: program}, Action: inference.ActionComplete, Resource: inference.ResourceHost(host), Permit: permit}
	edit, err := operator.SetRuleContext(ctx, page.Revision, rule)
	if err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("set rule: %+v %v", edit, err)
	}
}

// The credential journey starts with a settled hosted registration. Listener
// initialization is not a barrier for the separate legacy-adoption owner.
func seedHostedCredentialRegistration(t *testing.T, options runtimeFlags) {
	t.Helper()
	if err := migrateProviderState(options.stateDir, func(err error) { t.Error(err) }); err != nil {
		t.Fatal(err)
	}
	config, err := loadInferenceHosts(filepath.Join(options.stateDir, "inference", inferenceHostsFile))
	if err != nil {
		t.Fatal(err)
	}
	options.privateState = true
	p, err := openFixtureRuntimeProviders(t, options, options.stateDir, config, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	directory := registration1459Directory(t, options.stateDir)
	p.registrationDirectory = func() *applicationDirectory { return directory }
	before, err := routerHosts(p)
	if err != nil || len(before) != 1 || before[0].Name != "openrouter" {
		t.Fatal("initial hosted declaration", before, err)
	}
	if why, ok := p.registrationAdmit(t.Context(), before[0], ""); !ok {
		t.Fatal("initial hosted binding refused", why)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := routerHosts(p)
	if err != nil || len(after) != 1 || after[0].BindingID == before[0].BindingID {
		t.Fatal("adoption did not replace the legacy binding", after, err)
	}
	if why, ok := p.registrationAdmit(t.Context(), before[0], ""); ok || why != "registration:changed" {
		t.Fatal("legacy binding survived canonical publication", why, ok)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if why, ok := p.registrationAdmit(t.Context(), after[0], ""); !ok {
		t.Fatal("unchanged canonical binding refused after replay", why)
	}
}

// The runtime publishes chat@1. A hosted host lists its models with the
// registered credential; a program with a complete Block is not permitted
// and nothing reaches the upstream; with the rule the reply streams, the
// upstream sees the credential once, and the secret is in no reply, record,
// log or state file. Revocation refuses another call before provider I/O.
func TestRuntimeInferenceAppliesAHostedCredential(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	upstream := newHostedUpstream(t)
	options, _ := isolatedRuntime(t)
	config := fmt.Sprintf(`{"local":[],"hosted":[{"name":"openrouter","base":%q,"wire":"openai-compatible","credential":"openrouter"}],"ceilings":{"openrouter":{"tokens_per_day":100000}}}`, upstream.URL+"/api/v1")
	for _, file := range []struct{ path, body string }{
		{filepath.Join(options.stateDir, "credentials", credentialsBackendFile), "file-0600\n"},
		{filepath.Join(options.stateDir, "inference", inferenceHostsFile), config},
	} {
		writeStateFile(t, file.path, file.body)
	}
	seedHostedCredentialRegistration(t, options)
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		cancel()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	t.Cleanup(func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe = filepath.Clean(exe)
	var transcript strings.Builder
	out, diag, err := runCredentials(t, testCredentialSecret+"\n", "add", "openrouter", "--target", "127.0.0.1",
		"--for", inference.Contract, "--for", "abstraction.router/router@1", "--from-stdin", "--use-by", exe, "--endpoint", options.endpoint, "--timeout", "30s")
	transcript.WriteString(out + diag)
	requireUserScopeAdd(t, err, transcript.String())

	call, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	resolver := resolution.NewUnverifiedClient(options.endpoint, 2*time.Second)
	result, err := resolver.Resolve(call, fwire.ResolveRequest{Capability: "abstraction.inference", Contracts: []string{inference.Contract}, Guarantees: []string{inference.GuaranteeHosted}, Scope: fwire.ScopeRemote})
	if err != nil || result.Status != fwire.ResolutionStatusResolved {
		t.Fatalf("resolve chat@1: %+v %v", result, err)
	}
	bound, err := resolution.BindLocal(call, *result.Reference, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	chat := iwire.NewChatClient(bound.WithDefaults(35*time.Second, 1<<20))
	request := iwire.Request{Model: "anthropic/claude-sonnet-5", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Credential: "openrouter",
		Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "hi"}}}}}

	setCompleteRule(t, options.endpoint, exe, "openrouter", false)
	refused, err := chat.Start(request)
	if err != nil || refused.Outcome != iwire.StartOutcomeNotPermitted || refused.Reason != "rights:denied" {
		t.Fatalf("start with a complete Block: %+v %v", refused, err)
	}
	if got := upstream.authorizations(); len(got) != 0 {
		t.Fatalf("a refused start reached the upstream: %q", got)
	}

	setCompleteRule(t, options.endpoint, exe, "openrouter", true)
	local, err := resolver.Resolve(call, fwire.ResolveRequest{Capability: "abstraction.inference", Contracts: []string{inference.Contract}, Guarantees: []string{inference.GuaranteeLocalOnly}, Scope: fwire.ScopeLocal})
	if err != nil || local.Status != fwire.ResolutionStatusResolved || local.Reference == nil || local.Reference.Endpoint == result.Reference.Endpoint {
		t.Fatalf("resolve local chat@1: %+v %v", local, err)
	}
	localTransport, err := resolution.BindLocal(call, *local.Reference, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	localAdmission, err := iwire.NewChatClient(localTransport.WithDefaults(35*time.Second, 1<<20)).Start(request)
	if err != nil || localAdmission.Outcome != iwire.StartOutcomeNoHost {
		t.Fatalf("hosted request through local binding: %+v %v", localAdmission, err)
	}
	if got := upstream.authorizations(); len(got) != 0 {
		t.Fatalf("local binding reached hosted upstream: %q", got)
	}
	remoteLocalRequest := request
	remoteLocalRequest.Guarantees = []iwire.RequestGuarantee{iwire.RequestGuaranteeLocalOnly}
	remoteLocalRequest.Credential = ""
	remoteLocal, err := chat.Start(remoteLocalRequest)
	if err != nil || remoteLocal.Outcome != iwire.StartOutcomeNoHost {
		t.Fatalf("local-only request through remote binding: %+v %v", remoteLocal, err)
	}
	if got := upstream.authorizations(); len(got) != 0 {
		t.Fatalf("remote binding widened local-only request: %q", got)
	}
	admission, err := chat.Start(request)
	if err != nil || admission.Outcome != iwire.StartOutcomeAccepted || admission.Host != "openrouter" {
		t.Fatalf("start with a complete rule: %+v %v", admission, err)
	}
	var text strings.Builder
	var pages []iwire.DeltaPage
	cursor := int64(0)
	for i := 0; i < 100; i++ {
		page, err := chat.Observe(admission.Operation, cursor, 256, 65536, 2000)
		if err != nil || page.Outcome != iwire.PageOutcomePage {
			t.Fatalf("observe: %+v %v", page, err)
		}
		pages = append(pages, page)
		for _, d := range page.Deltas {
			if d.Kind == iwire.DeltaKindPart {
				text.WriteString(d.Part.Text)
			}
		}
		cursor = page.Next
		if page.AtEnd {
			break
		}
	}
	end := pages[len(pages)-1].Deltas[len(pages[len(pages)-1].Deltas)-1].End
	if text.String() != "Hello!" || end == nil || end.Outcome != iwire.ReplyOutcomeCompleted || end.Usage.Input != 5 {
		t.Fatalf("reply %q %+v", text.String(), end)
	}
	if got := upstream.authorizations(); len(got) != 1 || got[0] != "Bearer "+testCredentialSecret {
		t.Fatalf("upstream chat Authorization %q", got)
	}
	revokedOut, revokedDiag, err := runCredentials(t, "", "revoke", "openrouter", "--endpoint", options.endpoint, "--timeout", "30s")
	transcript.WriteString(revokedOut + revokedDiag)
	if err != nil {
		t.Fatalf("revoke test credential: %v\n%s%s", err, revokedOut, revokedDiag)
	}
	postRevoke, err := chat.Start(request)
	if err != nil || postRevoke.Outcome == iwire.StartOutcomeAccepted ||
		(postRevoke.Reason != "credential:revoked:openrouter" && postRevoke.Reason != "router:not-here") {
		t.Fatalf("revoked credential admission: %+v %v", postRevoke, err)
	}
	if got := upstream.authorizations(); len(got) != 1 {
		t.Fatalf("revoked credential caused a provider request: %q", got)
	}
	replies, _ := json.Marshal([]any{refused, admission, pages, postRevoke})
	if bytes.Contains(replies, []byte(testCredentialSecret)) || strings.Contains(transcript.String(), testCredentialSecret) {
		t.Fatal("a reply or command output carries the secret")
	}
	deadline := time.Now().Add(10 * time.Second)
	var log []byte
	for time.Now().Before(deadline) {
		log, _ = os.ReadFile(options.out)
		if bytes.Contains(log, []byte(`"outcome":"completed"`)) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !bytes.Contains(log, []byte("abstraction.inference/chat@1")) || !bytes.Contains(log, []byte(`"outcome":"not_permitted"`)) || !bytes.Contains(log, []byte(`"outcome":"completed"`)) {
		t.Fatalf("runtime log lacks the inference records:\n%s", log)
	}
	if bytes.Contains(log, []byte(testCredentialSecret)) {
		t.Fatal("the runtime log carries the secret")
	}
	err = filepath.WalkDir(options.stateDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.Contains(path, string(filepath.Separator)+"items"+string(filepath.Separator)) {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, []byte(testCredentialSecret)) {
			t.Errorf("%s carries the secret", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
