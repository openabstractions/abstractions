package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	facaderuntime "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	job "github.com/openabstractions/abstraction-job/go"
	logging "github.com/openabstractions/abstraction-logging/go"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type discardSink struct{}

func (discardSink) Write(logging.Record) error { return nil }

type retainedExecutor struct {
	prepares *atomic.Int64
	enabled  *atomic.Bool
}

func (retainedExecutor) Profile() string { return "mcp-native-fixture-v1" }
func (e retainedExecutor) Prepare(id, kind string, spec []byte) ([]byte, error) {
	e.prepares.Add(1)
	return append([]byte(nil), spec...), nil
}
func (e retainedExecutor) ExecutionGuarantees() []string {
	return []string{inference.RecoverableUpstreamGuarantee}
}
func (e retainedExecutor) RecoveryGuarantees() []string {
	return []string{inference.RecoverableUpstreamGuarantee}
}
func (e retainedExecutor) PrepareWithGuarantees(id, kind string, spec []byte, required []string) ([]byte, []string, error) {
	work, err := e.Prepare(id, kind, spec)
	return work, append([]string(nil), required...), err
}
func (e retainedExecutor) Serve(ctx context.Context, store job.Store) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if e.enabled != nil && !e.enabled.Load() {
				continue
			}
			records, _ := store.Orphans()
			for _, record := range records {
				held, err := store.Claim(record.ID, "fixture-worker", time.Second)
				if err != nil {
					continue
				}
				_, _ = store.Update(held.ID, held.Lease.Epoch, func(current *job.Record) error {
					if current.Wants() == job.WantCancel {
						current.State = job.StateCancelled
					} else {
						current.State = job.StateComplete
					}
					return nil
				})
			}
		}
	}
}

// fixtureInference matches the shipped runtime's two fixed-placement endpoints.
type fixtureInference struct{ local, remote *inferenceservice.Host }

func (h *fixtureInference) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- h.local.Serve(ctx) }()
	go func() { done <- h.remote.Serve(ctx) }()
	err := <-done
	cancel()
	return errors.Join(err, <-done)
}
func (h *fixtureInference) Close() error { return errors.Join(h.local.Close(), h.remote.Close()) }

type fixtureRuntime struct {
	host     *facaderuntime.Host
	provider *inference.Provider
	cancel   context.CancelFunc
	done     chan error
}

type fixtureApplications struct {
	listener listen.Listener
	visible  map[string]string
	once     sync.Once
}

func newFixtureApplications(endpoint string, visible map[string]string) (*fixtureApplications, error) {
	l, err := listen.Listen(endpoint)
	if err != nil {
		return nil, err
	}
	return &fixtureApplications{listener: l, visible: visible}, nil
}

func (a *fixtureApplications) Close() (err error) {
	a.once.Do(func() { err = a.listener.Close() })
	return err
}

func (a *fixtureApplications) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = a.Close() })
	defer stop()
	for {
		conn, err := a.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			defer conn.Close()
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			call, err := listen.ReceiveFramed(callCtx, conn, listen.Program, 1<<20)
			if err != nil {
				return
			}
			defer call.Close()
			peer, err := call.Peer()
			if err != nil || call.Recheck() != nil {
				return
			}
			program, err := peer.Path.AtLeast(listen.Program.Path)
			if err != nil {
				return
			}
			reply, err := (&wire.ApplicationsDispatcher{Handler: fixtureApplicationsReceiver{application: a.visible[filepath.Clean(program)]}}).ExchangeFrame(call.Frame)
			if err == nil {
				_ = call.Reply(reply)
			}
		}()
	}
}

type fixtureApplicationsReceiver struct{ application string }

func (fixtureApplicationsReceiver) Register(wire.ApplicationDescriptor) (wire.ApplicationChange, error) {
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
}
func (fixtureApplicationsReceiver) Remove(string) (wire.ApplicationChange, error) {
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
}
func (fixtureApplicationsReceiver) Announce(wire.ApplicationPresence) (wire.ApplicationChange, error) {
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
}
func (fixtureApplicationsReceiver) Withdraw(string, string) (wire.ApplicationChange, error) {
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
}
func (r fixtureApplicationsReceiver) Observe(string, int64) (wire.ApplicationPage, error) {
	page := wire.ApplicationPage{Outcome: wire.ApplicationOutcomePage}
	if r.application != "" {
		page.Applications = []wire.ApplicationEntry{{
			Descriptor: wire.ApplicationDescriptor{Name: r.application, Title: "Visible " + r.application, Program: `C:\not-exposed\application.exe`},
			Scope:      wire.ScopeLocal,
			Instances:  []wire.ApplicationInstance{{Instance: "instance-" + r.application, ExpiresUnixMs: time.Now().Add(time.Minute).UnixMilli(), Contexts: []wire.ApplicationContext{{Name: "context-" + r.application, Revision: "r1"}}}},
		}}
	}
	return page, nil
}
func (fixtureApplicationsReceiver) Activate(string) (wire.ApplicationActivationResult, error) {
	return wire.ApplicationActivationResult{Outcome: wire.ApplicationActivationOutcomeForbidden}, nil
}

func (r *fixtureRuntime) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	_ = r.host.Close()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("fixture runtime stopped: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fixture runtime did not stop")
	}
	if err := r.provider.Close(); err != nil {
		t.Fatalf("fixture inference provider stopped: %v", err)
	}
}

type fixtureConfig struct {
	runtimeEndpoint, logEndpoint, configEndpoint   string
	routerEndpoint, inferenceEndpoint, jobEndpoint string
	applicationsEndpoint                           string
	jobRoot                                        string
	allowed                                        map[string]bool
	prepares                                       *atomic.Int64
	executorEnabled                                *atomic.Bool
	upstream                                       string
	record                                         func(inference.Record)
	applications                                   map[string]string
}

func startFixture(t *testing.T, cfg fixtureConfig) *fixtureRuntime {
	t.Helper()
	r := router.New(router.NewHosted("fixture", cfg.upstream+"/v1", router.WireOpenAICompatible, "fixture"))
	r.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer fixture-secret"}, nil
	})
	p, err := inference.New(inference.Config{
		Router: r,
		Decide: func(_ context.Context, subject inference.Subject, action, resource string) (string, error) {
			if cfg.allowed[filepath.Clean(subject.Program)] && action == inference.ActionComplete && resource == "host:fixture" {
				return "permitted", nil
			}
			return "not_granted", nil
		},
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "applied"
		},
		Record: cfg.record,
	})
	if err != nil {
		t.Fatal(err)
	}
	localHost, err := inferenceservice.ListenForPlacement(cfg.inferenceEndpoint, p, inference.ExecutionLocal)
	if err != nil {
		p.Close()
		t.Fatal(err)
	}
	remoteEndpoint := cfg.inferenceEndpoint + "-remote"
	remoteHost, err := inferenceservice.ListenForPlacement(remoteEndpoint, p, inference.ExecutionRemote)
	if err != nil {
		localHost.Close()
		p.Close()
		t.Fatal(err)
	}
	inferenceHost := &fixtureInference{local: localHost, remote: remoteHost}
	applicationsHost, err := newFixtureApplications(cfg.applicationsEndpoint, cfg.applications)
	if err != nil {
		inferenceHost.Close()
		p.Close()
		t.Fatal(err)
	}
	policy := func(peer *identity.Peer) bool {
		path, err := peer.Path.AtLeast(listen.Program.Path)
		return err == nil && cfg.allowed[filepath.Clean(path)]
	}
	h, err := facaderuntime.Listen(facaderuntime.Options{
		Endpoint: cfg.runtimeEndpoint, LogEndpoint: cfg.logEndpoint, ConfigEndpoint: cfg.configEndpoint,
		ConfigWithoutMachine: true, Sink: discardSink{},
		Router: r, RouterEndpoint: cfg.routerEndpoint,
		RouterPolicy: func(_ context.Context, peer *identity.Peer, _, _ string) error {
			if policy(peer) {
				return nil
			}
			return errors.New("fixture principal refused")
		},
		Inference: inferenceHost, InferenceEndpoint: cfg.inferenceEndpoint, InferenceRemoteEndpoint: remoteEndpoint,
		Applications: applicationsHost, ApplicationsEndpoint: cfg.applicationsEndpoint,
		JobRoot: cfg.jobRoot, JobOwner: "mcp-native-fixture-owner", JobEndpoint: cfg.jobEndpoint,
		JobExecutor: retainedExecutor{prepares: cfg.prepares, enabled: cfg.executorEnabled}, JobPolicy: policy,
	})
	if err != nil {
		applicationsHost.Close()
		inferenceHost.Close()
		p.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	return &fixtureRuntime{host: h, provider: p, cancel: cancel, done: done}
}

func fixtureEndpoint(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\mcp-native-%d-%s`, time.Now().UnixNano(), name)
	}
	return filepath.Join(t.TempDir(), name+".sock")
}

func buildFixture(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", path, "./internal/nativefixture")
	cmd.Dir = filepath.Clean("..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
}

func jobCount(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			n++
		}
	}
	return n
}

func callFixture[Out any](t *testing.T, binary, endpoint, state, claimedClient, tool string, args any) (*mcp.CallToolResult, Out) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.Command(binary, "--runtime", endpoint, "--state-dir", state)
	client := mcp.NewClient(&mcp.Implementation{Name: claimedClient, Version: "forged"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		var zero Out
		t.Fatalf("connect fixture %s: %v", filepath.Base(binary), err)
		return nil, zero
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		var zero Out
		t.Fatalf("call %s: %v", tool, err)
		return nil, zero
	}
	var out Out
	raw, _ := json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v (%s)", tool, err, raw)
	}
	return result, out
}

func dropSubmitReply(t *testing.T, binary, endpoint, state string, args any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.Command(binary, "--runtime", endpoint, "--state-dir", state)
	cmd.Env = append(os.Environ(), "OA_MCP_FIXTURE_DROP_AFTER_SUBMIT=1")
	client := mcp.NewClient(&mcp.Implementation{Name: "forged-after-drop", Version: "forged"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolJobSubmit, Arguments: args}); err == nil {
		t.Fatal("fixture returned the MCP reply that it was instructed to drop")
	}
}

func TestNativePrincipalsAndDurableRecoveryThroughMCP(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof is deliberately unavailable on Darwin")
	}
	if err := identity.CanEver(listen.Program); err != nil {
		t.Skipf("native Program proof unavailable: %v", err)
	}
	dir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	a, b, denied := filepath.Join(dir, "principal-a"+suffix), filepath.Join(dir, "principal-b"+suffix), filepath.Join(dir, "denied"+suffix)
	buildFixture(t, a)
	buildFixture(t, b)
	buildFixture(t, denied)
	allowed := map[string]bool{filepath.Clean(a): true, filepath.Clean(b): true}
	var gets, posts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"fixture/chat-small"}]}`)
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"native-ok\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	var prepares atomic.Int64
	var executorEnabled atomic.Bool
	var recordMu sync.Mutex
	var records []inference.Record
	cfg := fixtureConfig{
		runtimeEndpoint: fixtureEndpoint(t, "runtime"), logEndpoint: fixtureEndpoint(t, "log"), configEndpoint: fixtureEndpoint(t, "config"),
		routerEndpoint: fixtureEndpoint(t, "router"), inferenceEndpoint: fixtureEndpoint(t, "inference"), jobEndpoint: fixtureEndpoint(t, "jobs"),
		applicationsEndpoint: fixtureEndpoint(t, "applications"),
		jobRoot:              filepath.Join(dir, "jobs"), allowed: allowed, prepares: &prepares, executorEnabled: &executorEnabled, upstream: upstream.URL,
		applications: map[string]string{filepath.Clean(a): "visible-a", filepath.Clean(b): "visible-b"},
		record: func(record inference.Record) {
			recordMu.Lock()
			defer recordMu.Unlock()
			records = append(records, record)
		},
	}
	state := filepath.Join(dir, "gateway-state")
	first := startFixture(t, cfg)
	_, applicationsA := callFixture[gateway.ApplicationsOutput](t, a, cfg.runtimeEndpoint, state, "principal-b", gateway.ToolApplications, map[string]any{})
	_, applicationsB := callFixture[gateway.ApplicationsOutput](t, b, cfg.runtimeEndpoint, filepath.Join(dir, "state-b"), "principal-a", gateway.ToolApplications, map[string]any{})
	_, deniedApplications := callFixture[gateway.ApplicationsOutput](t, denied, cfg.runtimeEndpoint, filepath.Join(dir, "denied-applications"), "principal-a", gateway.ToolApplications, map[string]any{})
	if len(applicationsA.Applications) != 1 || applicationsA.Applications[0].Name != "visible-a" ||
		len(applicationsB.Applications) != 1 || applicationsB.Applications[0].Name != "visible-b" ||
		len(deniedApplications.Applications) != 0 {
		first.stop(t)
		t.Fatalf("native application filtering crossed principals: a=%+v b=%+v denied=%+v", applicationsA, applicationsB, deniedApplications)
	}
	applicationsRaw, _ := json.Marshal([]any{applicationsA, applicationsB})
	if strings.Contains(string(applicationsRaw), "not-exposed") {
		first.stop(t)
		t.Fatalf("application executable leaked through gateway: %s", applicationsRaw)
	}
	_, models := callFixture[gateway.ModelsOutput](t, a, cfg.runtimeEndpoint, state, "principal-b", gateway.ToolModels, map[string]any{"fresh": true})
	if len(models.Models) != 1 || !strings.Contains(strings.Join(models.Models[0].Aliases, ","), "fixture/chat-small") {
		first.stop(t)
		t.Fatalf("curated native inventory: %+v", models)
	}
	listed := gets.Load()
	deniedModels, _ := callFixture[gateway.ModelsOutput](t, denied, cfg.runtimeEndpoint, filepath.Join(dir, "denied-models"), "principal-a", gateway.ToolModels, map[string]any{"fresh": true})
	if !deniedModels.IsError || gets.Load() != listed {
		first.stop(t)
		t.Fatalf("denied inventory caused provider effect: isError=%v gets=%d->%d", deniedModels.IsError, listed, gets.Load())
	}
	_, completed := callFixture[gateway.CompleteOutput](t, a, cfg.runtimeEndpoint, state, "principal-b", gateway.ToolComplete, map[string]any{"model": "fixture/chat-small", "prompt": "hello", "max_output": 8, "hosting": "hosted", "credential": "fixture"})
	if completed.Outcome != "completed" || completed.Text != "native-ok" || posts.Load() != 1 {
		first.stop(t)
		t.Fatalf("native completion: %+v posts=%d", completed, posts.Load())
	}
	deniedResult, deniedCompletion := callFixture[gateway.CompleteOutput](t, denied, cfg.runtimeEndpoint, filepath.Join(dir, "denied-state"), "principal-a", gateway.ToolComplete, map[string]any{"model": "fixture/chat-small", "prompt": "must refuse", "hosting": "hosted", "credential": "fixture"})
	if (!deniedResult.IsError && deniedCompletion.Outcome != "not_permitted") || posts.Load() != 1 {
		first.stop(t)
		t.Fatalf("denied inference reached provider: isError=%v posts=%d", deniedResult.IsError, posts.Load())
	}
	visible, _ := json.Marshal([]any{models, completed, deniedCompletion, deniedResult.StructuredContent})
	if strings.Contains(string(visible), "fixture-secret") {
		first.stop(t)
		t.Fatalf("credential leaked into MCP response: %s", visible)
	}
	recordMu.Lock()
	observedRecords := append([]inference.Record(nil), records...)
	recordMu.Unlock()
	var permittedAttributed, refusedAttributed bool
	for _, record := range observedRecords {
		if filepath.Clean(record.Program) == filepath.Clean(a) && record.Outcome == "completed" {
			permittedAttributed = true
		}
		if filepath.Clean(record.Program) == filepath.Clean(denied) && record.Outcome != "completed" {
			refusedAttributed = true
		}
		raw, _ := json.Marshal(record)
		if strings.Contains(string(raw), "fixture-secret") {
			first.stop(t)
			t.Fatalf("credential leaked into attributed audit record: %s", raw)
		}
	}
	if !permittedAttributed || !refusedAttributed {
		first.stop(t)
		t.Fatalf("missing native attribution in audit records: permitted=%v refused=%v records=%+v", permittedAttributed, refusedAttributed, observedRecords)
	}
	cancelArgs := map[string]any{"request_key": "native-cancel-1", "profile": "image_batch", "model": "fixture/image", "prompt": "cancel pending", "hosting": "hosted", "credential": "fixture", "count": 1}
	_, cancelSubmitted := callFixture[gateway.JobSubmitOutput](t, a, cfg.runtimeEndpoint, state, "cancel-client", gateway.ToolJobSubmit, cancelArgs)
	if cancelSubmitted.Outcome != "accepted" || cancelSubmitted.Handle == "" {
		first.stop(t)
		t.Fatalf("pending cancel submission: %+v", cancelSubmitted)
	}
	_, cancelResult := callFixture[gateway.JobCancelOutput](t, a, cfg.runtimeEndpoint, state, "cancel-client", gateway.ToolJobCancel, map[string]any{"handle": cancelSubmitted.Handle})
	if cancelResult.Outcome != "requested" {
		first.stop(t)
		t.Fatalf("pending cancel outcome: %+v", cancelResult)
	}
	executorEnabled.Store(true)
	var cancelStatus gateway.JobStatusOutput
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		_, cancelStatus = callFixture[gateway.JobStatusOutput](t, a, cfg.runtimeEndpoint, state, "cancel-client", gateway.ToolJobStatus, map[string]any{"handle": cancelSubmitted.Handle})
		if cancelStatus.State == "cancelled" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cancelStatus.State != "cancelled" {
		first.stop(t)
		t.Fatalf("pending job was not cancelled by the fixture worker: %+v", cancelStatus)
	}
	cancelJobs := jobCount(t, cfg.jobRoot)
	jobArgs := map[string]any{"request_key": "native-restart-1", "profile": "image_batch", "model": "fixture/image", "prompt": "retain", "hosting": "hosted", "credential": "fixture", "count": 1}
	dropSubmitReply(t, a, cfg.runtimeEndpoint, state, jobArgs)
	if jobCount(t, cfg.jobRoot) != cancelJobs+1 {
		first.stop(t)
		t.Fatal("dropped reply did not leave exactly one additional accepted job")
	}
	_, submitted := callFixture[gateway.JobSubmitOutput](t, a, cfg.runtimeEndpoint, state, "principal-b", gateway.ToolJobSubmit, jobArgs)
	if submitted.Outcome != "accepted" || submitted.Handle == "" || prepares.Load() == 0 {
		first.stop(t)
		t.Fatalf("native submit: %+v prepares=%d", submitted, prepares.Load())
	}
	prepared := prepares.Load()
	jobs := jobCount(t, cfg.jobRoot)
	// A further fresh MCP process still reconciles the same operation.
	_, retried := callFixture[gateway.JobSubmitOutput](t, a, cfg.runtimeEndpoint, state, "different-client-metadata", gateway.ToolJobSubmit, map[string]any{"request_key": "native-restart-1", "profile": "image_batch", "model": "fixture/image", "prompt": "retain", "hosting": "hosted", "credential": "fixture", "count": 1})
	if retried.Outcome != "accepted" || retried.Handle != submitted.Handle || retried.OperationID != submitted.OperationID || jobCount(t, cfg.jobRoot) != jobs {
		first.stop(t)
		t.Fatalf("idempotent retry: first=%+v retry=%+v prepares=%d->%d", submitted, retried, prepared, prepares.Load())
	}
	mismatch, _ := callFixture[gateway.JobSubmitOutput](t, a, cfg.runtimeEndpoint, state, "principal-a", gateway.ToolJobSubmit, map[string]any{"request_key": "native-restart-1", "profile": "image_batch", "model": "fixture/image", "prompt": "different", "hosting": "hosted", "credential": "fixture", "count": 1})
	if !mismatch.IsError || jobCount(t, cfg.jobRoot) != jobs {
		first.stop(t)
		t.Fatalf("request-key spec mismatch was not effect-free: isError=%v prepares=%d->%d", mismatch.IsError, prepared, prepares.Load())
	}
	first.stop(t)

	second := startFixture(t, cfg)
	defer second.stop(t)
	var status gateway.JobStatusOutput
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		_, status = callFixture[gateway.JobStatusOutput](t, a, cfg.runtimeEndpoint, state, "changed-after-restart", gateway.ToolJobStatus, map[string]any{"handle": submitted.Handle})
		if status.State == "complete" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.Outcome != "observed" || status.State != "complete" || status.ResultOutcome != "unsupported" || status.Result != nil {
		t.Fatalf("persisted status: %+v", status)
	}
	cross, crossStatus := callFixture[gateway.JobStatusOutput](t, b, cfg.runtimeEndpoint, state, "principal-a", gateway.ToolJobStatus, map[string]any{"handle": submitted.Handle})
	if !cross.IsError && crossStatus.Outcome == "observed" {
		t.Fatalf("different native principal observed handle: %+v", crossStatus)
	}
	before := prepares.Load()
	deniedSubmit, _ := callFixture[gateway.JobSubmitOutput](t, denied, cfg.runtimeEndpoint, filepath.Join(dir, "denied-jobs"), "principal-a", gateway.ToolJobSubmit, map[string]any{"request_key": "native-denied-1", "profile": "image_batch", "model": "fixture/image", "prompt": "must refuse", "hosting": "hosted", "credential": "fixture", "count": 1})
	if !deniedSubmit.IsError || prepares.Load() != before {
		t.Fatalf("denied submit caused preparation: isError=%v prepares=%d->%d", deniedSubmit.IsError, before, prepares.Load())
	}
}
