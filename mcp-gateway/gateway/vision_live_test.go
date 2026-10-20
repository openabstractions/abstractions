package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-provider-kit/go/providerhost"
	"github.com/openabstractions/abstraction-provider-modelhost/go/declaration"
	"github.com/openabstractions/abstraction-provider-modelhost/go/host"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/service"
	res "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	router "github.com/openabstractions/abstraction-router/go"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// grantedLeases is a lease book that grants every load; the live test's
// one engine is the only GPU holder it starts.
type grantedLeases struct{}

func (grantedLeases) Acquire(resource string, amount int64, _ time.Duration) (res.AcquireResult, error) {
	return res.AcquireResult{Outcome: res.AcquireOutcomeAcquired, Lease: &res.Lease{ID: "live-vision", Resource: resource, Amount: amount}, Asked: []res.YieldRecord{}}, nil
}
func (grantedLeases) Renew(string) (res.LeaseChange, error) {
	return res.LeaseChange{Applied: true}, nil
}
func (grantedLeases) BindProcess(string, int) (res.ProcessBindResult, error) {
	return res.ProcessBindResult{Outcome: res.ProcessBindOutcomeBound}, nil
}
func (grantedLeases) Release(string) (res.LeaseChange, error) {
	return res.LeaseChange{Applied: true}, nil
}
func (grantedLeases) Label(string, string) (res.LeaseChange, error) {
	return res.LeaseChange{Applied: true}, nil
}
func (grantedLeases) Observe(ctx context.Context, cursor string, wait time.Duration) (res.YieldPage, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	return res.YieldPage{Requests: []res.YieldRequest{}, Next: cursor}, nil
}
func (grantedLeases) Answer(context.Context, string, res.YieldAnswer, string) (res.AnswerResult, error) {
	return res.AnswerResult{Recorded: true}, nil
}

// chat3Mediator is the runtime's chat@3 owner as the gateway's port.
type chat3Mediator struct {
	p       *inference.Provider
	subject inference.Subject
}

func (m chat3Mediator) GetHistoryWindow(context.Context) (wire.ChatHistoryRead, error) {
	return m.p.GetChatHistoryWindow(m.subject), nil
}
func (m chat3Mediator) Start(ctx context.Context, r wire.RequestV3) (wire.ChatAdmission, error) {
	return m.p.StartV3(ctx, m.subject, r), nil
}
func (m chat3Mediator) Observe(ctx context.Context, id string, cursor, events, size, wait int64) (wire.ChatPage, error) {
	return m.p.ObserveV3(ctx, m.subject, id, cursor, events, size, wait), nil
}
func (m chat3Mediator) Cancel(_ context.Context, id string) (wire.ChatCancellation, error) {
	return m.p.CancelV3(m.subject, id), nil
}

// TestLiveGemmaDescribesAPictureThroughOAInferenceComplete is the image
// input end-to-end check of the architect ruling 2026-11-12 (model
// capabilities): oa_inference_complete's image_path is stored as the
// caller's content and sent as a chat@3 image part; the runtime relays it to
// modelhost, staging the bytes in modelhost's content store; modelhost
// launches the real llama-server with the model's --mmproj; the OpenAI
// translation sends the image as image_url data; and the model names the
// picture's colours. The runtime's router reads image input from modelhost's
// description, and the models list line shows it.
//
// It runs only with OA_LIVE_VISION=1 and:
//   - OA_LIVE_VISION_HOME: a home whose Hugging Face cache holds one GGUF
//     model and its mmproj in one snapshot;
//   - OA_LIVE_VISION_ENGINE: the pinned llama-server executable;
//   - OA_LIVE_VISION_STATE: a state folder for the engine's file record.
func TestLiveGemmaDescribesAPictureThroughOAInferenceComplete(t *testing.T) {
	if os.Getenv("OA_LIVE_VISION") != "1" {
		t.Skip("set OA_LIVE_VISION=1 and OA_LIVE_VISION_* for one real vision chat")
	}
	if err := identity.CanEver(listen.Program); err != nil || runtime.GOOS != "windows" {
		t.Skip("native engine peer proof requires Windows")
	}
	need := func(name string) string {
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("%s is not set", name)
		}
		return value
	}
	home, program, state := need("OA_LIVE_VISION_HOME"), need("OA_LIVE_VISION_ENGINE"), need("OA_LIVE_VISION_STATE")
	snapshots, err := filepath.Glob(filepath.Join(home, ".cache", "huggingface", "hub", "models--*", "snapshots", "*", "*.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	locator := ""
	for _, file := range snapshots {
		if !strings.Contains(strings.ToLower(filepath.Base(file)), "mmproj") {
			rel, err := filepath.Rel(filepath.Join(home, ".cache", "huggingface", "hub"), file)
			if err != nil {
				t.Fatal(err)
			}
			locator = filepath.ToSlash(rel)
		}
	}
	if locator == "" {
		t.Fatalf("no model in %v", snapshots)
	}
	const name = "gemma-4-26b"
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	runtimeProgram, principal := identity.NormalizeSubjectProgram(self), identity.User{Kind: "windows", SID: current.Uid}
	account, err := principal.Account()
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := host.PinnedEngine(runtime.GOOS, runtime.GOARCH)
	if !ok {
		t.Fatal("no pinned engine for this platform")
	}
	sockets, err := os.MkdirTemp("", "oa-vis")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	engineLog, err := os.Create(filepath.Join(state, "engine.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer engineLog.Close()

	// The leaf: modelhostd's chat provider as modelhostd serve composes it.
	var leaf *inference.Provider
	model, err := host.New(host.Options{
		Models: []host.Model{{Name: name, Store: "huggingface", Object: locator}},
		Engine: host.Engine{Program: program, Pin: pin, Record: host.EngineRecord(state, pin), Sockets: filepath.Join(sockets, "s"),
			Arguments: []string{"--ctx-size", "8192", "--parallel", "1"}, Ready: 10 * time.Minute, MemoryLimit: 48 << 30, Stderr: engineLog},
		Source: host.NewLocalStores(source.Env{GOOS: runtime.GOOS, Home: home, Getenv: func(string) string { return "" }}),
		Leases: grantedLeases{}, Idle: time.Hour,
		Active:  func(name string) bool { return leaf != nil && leaf.ActiveForModel(name) },
		OnError: func(err error) { t.Logf("modelhost: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Close stops the engine this test started, by its own process handle.
	t.Cleanup(model.Close)
	facts, cancel := context.WithTimeout(context.Background(), time.Minute)
	model.RefreshFacts(facts)
	cancel()
	content, err := providerhost.NewDesignatedContent("modelhost-content", filepath.Join(state, "content"), filepath.Join(state, "uploads.json"), []string{runtimeProgram}, 20<<20)
	if err != nil {
		t.Fatal(err)
	}
	config := modelhost.ProviderConfig(model, modelhost.Routes(model, false), func(err error) { t.Logf("leaf: %v", err) })
	config.ResolveContent = content.ResolveContent
	leaf, err = inference.New(config)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := `\\.\pipe\oa-live-vision-` + hex.EncodeToString([]byte(time.Now().Format("150405.000")))
	served, err := modelhost.Listen(endpoint, leaf, runtimeProgram)
	if err != nil {
		t.Fatal(err)
	}
	served.Content = content.Component
	served.ModelCapabilities = modelhost.ModelCapabilities(model)
	serveCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- served.Serve(serveCtx) }()
	t.Cleanup(func() { stop(); <-done; leaf.Close() })

	// The runtime: its router reads modelhost's description, and its
	// inference service relays chat@3 to it.
	native, err := router.NewNative(declaration.ProviderName, endpoint, listen.ServerExpectation{Principal: principal, Program: runtimeProgram}, []string{name}, []string{router.ProfileChat})
	if err != nil {
		t.Fatal(err)
	}
	routes := router.New(native)
	surveyCtx, surveyed := context.WithTimeout(context.Background(), time.Minute)
	defer surveyed()
	if err := routes.SurveyContext(surveyCtx); err != nil {
		t.Fatal(err)
	}
	caps := routes.AliasCapabilities(declaration.ProviderName, name)
	line := capabilityLine(routerwire.Alias{Host: declaration.ProviderName, Name: name, Profiles: []string{"chat"}, Capabilities: caps})
	t.Logf("models list line: %s", line)
	if !strings.Contains(line, "in text+image") {
		t.Fatalf("image input not advertised: %s", line)
	}

	// The gateway stores the picture as the caller's content; the runtime
	// resolves it from there.
	stored := &recordingWriter{}
	picture := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for x := range 256 {
		for y := range 256 {
			c := color.RGBA{R: 230, G: 20, B: 20, A: 255}
			if x >= 128 {
				c = color.RGBA{R: 20, G: 40, B: 230, A: 255}
			}
			picture.Set(x, y, c)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "two-colours.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	mediator, err := inference.New(inference.Config{ChatLogicalOwner: "live-runtime-job-owner", Router: routes,
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "not_permitted"
		},
		ResolveContent: func(_ context.Context, _ inference.Subject, digest string, max int64) ([]byte, inference.ContentOutcome) {
			sum := sha256.Sum256(stored.data)
			if digest != "sha256:"+hex.EncodeToString(sum[:]) || int64(len(stored.data)) > max {
				return nil, inference.ContentUnknown
			}
			return stored.data, inference.ContentResolved
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mediator.Close() })
	o := &OA{stateDir: t.TempDir(), state: casapi.BoundedFileStore{MaxBytes: maxHandleRecordBytes}}
	o.imageWriter = func(context.Context) (contentWriter, error) { return stored, nil }
	port := chat3Mediator{p: mediator, subject: inference.Subject{Account: account, Program: runtimeProgram}}

	ctx, finish := context.WithTimeout(context.Background(), 12*time.Minute)
	defer finish()
	started := time.Now()
	in := CompleteInput{Model: name, Prompt: "What two colours does this picture show? Answer in one short sentence.", Hosting: "local",
		ImagePath: path, ReasoningEffort: "none", MaxOutput: 128}
	out, err := o.completeWithChat3(ctx, port, in)
	for err == nil && out.State == "working" && ctx.Err() == nil {
		cursor := out.Cursor
		var more CompleteOutput
		more, err = o.completeWithChat3(ctx, port, CompleteInput{OperationID: out.OperationID, Cursor: &cursor})
		more.Text = out.Text + more.Text
		out = more
	}
	t.Logf("%s in %s: %q (outcome %s, reason %s, reasoning %+v)", name, time.Since(started).Round(time.Second), out.Text, out.Outcome, out.Reason, out.ReasoningApplied)
	if err != nil || out.Outcome != "completed" {
		t.Fatalf("completion %+v %v", out, err)
	}
	answer := strings.ToLower(out.Text)
	if !strings.Contains(answer, "red") || !strings.Contains(answer, "blue") {
		t.Fatalf("the model did not describe the picture: %q", out.Text)
	}
}
