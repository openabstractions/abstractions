// Package fixture composes an in-process OA runtime for gateway tests: the
// facade runtime host, a router to one hosted OpenAI-compatible upstream, an
// inference provider on its local and remote placements, an applications
// service and a job executor that completes or cancels each job. A program is
// admitted by its canonical path. Nothing here reaches an installed runtime.
//
// The gateway package's native test and the OpenCode adopter harness
// (adopters/opencode/test/opencode_gateway_test.go) both use it.
package fixture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	facaderuntime "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	job "github.com/openabstractions/abstraction-job/go"
	acceptance "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	logging "github.com/openabstractions/abstraction-logging/go"
	router "github.com/openabstractions/abstraction-router/go"
	storage "github.com/openabstractions/abstraction-storage/go"
)

// Secret is the credential the router sends upstream. A test asserts that it
// never reaches an adopter.
const Secret = "fixture-secret"

// Config names the fixture's endpoints and its admitted programs.
type Config struct {
	RuntimeEndpoint, LogEndpoint, ConfigEndpoint   string
	RouterEndpoint, InferenceEndpoint, JobEndpoint string
	ApplicationsEndpoint                           string
	// JobRoot is the job store's folder.
	JobRoot string
	// JobMethodPolicy supplies an optional native admission fault seam.
	JobMethodPolicy acceptanceprovider.MethodPolicy
	// RouterPolicy decides each router operation of an admitted program when
	// set, as the runtime's rights decide it; nil permits them.
	RouterPolicy func(ctx context.Context, peer *identity.Peer, action, resource string) error
	// Allowed lists the program paths the runtime admits.
	Allowed []string
	// Prepares counts executor preparations when set.
	Prepares *atomic.Int64
	// ExecutorEnabled holds the executor back while false; nil runs it.
	ExecutorEnabled *atomic.Bool
	// Upstream is the base URL of the OpenAI-compatible model server.
	Upstream string
	// LocalUpstream serves Upstream as a model server on this machine (the
	// router's Ollama host) in place of the hosted "fixture" server.
	LocalUpstream bool
	// Hosts are further model servers the router routes to beside
	// Upstream, such as a native provider's declared set; the inference
	// provider may not complete on them.
	Hosts []*router.Host
	// ChatV3 publishes the local chat@3 registry entry when set.
	ChatV3 bool
	// CompletePolicy is an optional additional host permission for native fixture callers.
	CompletePolicy func(inference.Subject, string) bool
	// Storage supplies a private fixture store for gateway image staging.
	Storage *storage.ContentStore
	// Record receives each inference record when set.
	Record func(inference.Record)
	// Applications maps a program path to the one application it sees.
	Applications map[string]string
	// RouteJobs admits a job only when the fixture's router routes its model
	// for its profile, and refuses it otherwise with invalid
	// host:router:<verdict>, the inference job executor's refusal
	// (abstraction-inference provider.pick).
	RouteJobs bool
}

var endpointCount atomic.Uint64

// Endpoint names a private local endpoint from the process id and a counter.
func Endpoint(name string) string {
	return listen.Endpoint(fmt.Sprintf("mcp-fixture-%d-%d-%s", os.Getpid(), endpointCount.Add(1), name))
}

// NewConfig gives every endpoint of a Config a fresh private name.
func NewConfig(name string) Config {
	return Config{
		RuntimeEndpoint: Endpoint(name + "-runtime"), LogEndpoint: Endpoint(name + "-log"),
		ConfigEndpoint: Endpoint(name + "-config"), RouterEndpoint: Endpoint(name + "-router"),
		InferenceEndpoint: Endpoint(name + "-inference"), JobEndpoint: Endpoint(name + "-jobs"),
		ApplicationsEndpoint: Endpoint(name + "-applications"),
	}
}

// Program is the canonical form of a program path, as the runtime sees a peer.
func Program(path string) string {
	return identity.CanonicalProgramPath(filepath.Clean(path))
}

// BuildGateway builds the gateway's native fixture program
// (mcp-gateway/internal/nativefixture) into dir as name plus the platform's
// executable suffix, and returns its path. Each distinct path is a distinct
// native Program principal.
func BuildGateway(t testing.TB, dir, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the fixture source")
	}
	module := filepath.Dir(filepath.Dir(thisFile))
	suffix, err := exec.Command("go", "env", "GOEXE").Output()
	if err != nil {
		t.Fatalf("go env GOEXE: %v", err)
	}
	path := filepath.Join(dir, name+strings.TrimSpace(string(suffix)))
	cmd := exec.Command("go", "build", "-C", module, "-o", path, "./internal/nativefixture")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build gateway fixture: %v\n%s", err, out)
	}
	return path
}

// Runtime is a started fixture.
type Runtime struct {
	host     *facaderuntime.Host
	provider *inference.Provider
	cancel   context.CancelFunc
	done     chan error
	stopOnce sync.Once
}

// Start serves the fixture until Stop.
func Start(t testing.TB, cfg Config) *Runtime {
	t.Helper()
	r, err := start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func start(cfg Config) (*Runtime, error) {
	allowed := map[string]bool{}
	for _, path := range cfg.Allowed {
		allowed[Program(path)] = true
	}
	visible := map[string]string{}
	for path, application := range cfg.Applications {
		visible[Program(path)] = application
	}
	prepares := cfg.Prepares
	if prepares == nil {
		prepares = new(atomic.Int64)
	}
	upstream := router.NewHosted("fixture", cfg.Upstream+"/v1", router.WireOpenAICompatible, "fixture")
	if cfg.LocalUpstream {
		upstream = router.Ollama(cfg.Upstream)
	}
	r := router.New(append([]*router.Host{upstream}, cfg.Hosts...)...)
	if len(cfg.Hosts) > 0 {
		// The shipped runtime surveys at start; a declared set is routable
		// from the first call.
		r.Survey()
	}
	r.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer " + Secret}, nil
	})
	p, err := inference.New(inference.Config{
		Router: r,
		Decide: func(_ context.Context, subject inference.Subject, action, resource string) (string, error) {
			if allowed[Program(subject.Program)] && action == inference.ActionComplete {
				permitted := resource == inference.ResourceHost(upstream.Name)
				if cfg.CompletePolicy != nil {
					permitted = cfg.CompletePolicy(subject, resource)
				}
				if permitted {
					return "permitted", nil
				}
			}
			return "not_granted", nil
		},
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "applied"
		},
		Record: cfg.Record,
	})
	if err != nil {
		return nil, err
	}
	localHost, err := inferenceservice.ListenForPlacement(cfg.InferenceEndpoint, p, inference.ExecutionLocal)
	if err != nil {
		p.Close()
		return nil, err
	}
	remoteEndpoint := cfg.InferenceEndpoint + "-remote"
	remoteHost, err := inferenceservice.ListenForPlacement(remoteEndpoint, p, inference.ExecutionRemote)
	if err != nil {
		localHost.Close()
		p.Close()
		return nil, err
	}
	inferenceHost := &placements{local: localHost, remote: remoteHost}
	applicationsHost, err := newApplications(cfg.ApplicationsEndpoint, visible)
	if err != nil {
		inferenceHost.Close()
		p.Close()
		return nil, err
	}
	policy := func(peer *identity.Peer) bool {
		path, err := peer.Path.AtLeast(listen.Program.Path)
		return err == nil && allowed[Program(path)]
	}
	var servedInference facaderuntime.InferenceService = inferenceHost
	if cfg.ChatV3 {
		localHost.ApplicationChatV3 = true
		servedInference = chat3Placements{placements: inferenceHost, provider: p}
	}
	options := facaderuntime.Options{
		Endpoint: cfg.RuntimeEndpoint, LogEndpoint: cfg.LogEndpoint, ConfigEndpoint: cfg.ConfigEndpoint,
		ConfigWithoutMachine: true, Sink: discardSink{},
		Router: r, RouterEndpoint: cfg.RouterEndpoint,
		RouterPolicy: func(ctx context.Context, peer *identity.Peer, action, resource string) error {
			if !policy(peer) {
				return errors.New("fixture principal refused")
			}
			if cfg.RouterPolicy != nil {
				return cfg.RouterPolicy(ctx, peer, action, resource)
			}
			return nil
		},
		Inference: servedInference, InferenceEndpoint: cfg.InferenceEndpoint, InferenceRemoteEndpoint: remoteEndpoint,
		Applications: applicationsHost, ApplicationsEndpoint: cfg.ApplicationsEndpoint,
		JobRoot: cfg.JobRoot, JobOwner: "mcp-native-fixture-owner", JobEndpoint: cfg.JobEndpoint,
		JobExecutor: jobExecutor(executor{prepares: prepares, enabled: cfg.ExecutorEnabled}, r, cfg.RouteJobs), JobPolicy: policy, JobMethodPolicy: cfg.JobMethodPolicy,
	}
	if cfg.Storage != nil {
		options.Storage, options.StorageEndpoint = cfg.Storage, Endpoint("storage")
		options.StoragePolicy = func(ctx context.Context, peer *identity.Peer, _ string) error {
			if !policy(peer) {
				return errors.New("fixture principal refused")
			}
			return ctx.Err()
		}
		options.StorageWritePolicy, options.StorageWriteLimit = options.StoragePolicy, 20<<20
		options.StorageWriteRecordPath = filepath.Join(filepath.Dir(cfg.JobRoot), "writer-records.json")
	}
	h, err := facaderuntime.Listen(options)
	if err != nil {
		applicationsHost.Close()
		inferenceHost.Close()
		p.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	return &Runtime{host: h, provider: p, cancel: cancel, done: done}, nil
}

// Stop closes the runtime and its inference provider. It runs once, so a test
// can defer it and still stop early before a failure; it reports with Errorf
// and closes the provider even when the runtime does not stop.
func (r *Runtime) Stop(t testing.TB) {
	t.Helper()
	r.stopOnce.Do(func() {
		r.cancel()
		_ = r.host.Close()
		defer func() {
			if err := r.provider.Close(); err != nil {
				t.Errorf("fixture inference provider stopped: %v", err)
			}
		}()
		select {
		case err := <-r.done:
			if err != nil {
				t.Errorf("fixture runtime stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("fixture runtime did not stop")
		}
	})
}

type discardSink struct{}

func (discardSink) Write(logging.Record) error { return nil }

// executor completes each orphaned job, or cancels it when cancellation was
// asked, while enabled.
type executor struct {
	prepares *atomic.Int64
	enabled  *atomic.Bool
}

func (executor) Profile() string { return "mcp-native-fixture-v1" }
func (e executor) Prepare(id, kind string, spec []byte) ([]byte, error) {
	e.prepares.Add(1)
	return append([]byte(nil), spec...), nil
}
func (e executor) ExecutionGuarantees() []string {
	return []string{inference.RecoverableUpstreamGuarantee}
}
func (e executor) RecoveryGuarantees() []string {
	return []string{inference.RecoverableUpstreamGuarantee}
}
func (e executor) PrepareWithGuarantees(id, kind string, spec []byte, required []string) ([]byte, []string, error) {
	work, err := e.Prepare(id, kind, spec)
	return work, append([]string(nil), required...), err
}
func (e executor) Serve(ctx context.Context, store job.Store) error {
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

// routedExecutor is executor with the inference job executor's admission:
// a job whose model the router does not route for its profile is invalid
// host:router:<verdict>.
type routedExecutor struct {
	executor
	router *router.Router
}

func jobExecutor(e executor, r *router.Router, routed bool) acceptanceprovider.Executor {
	if routed {
		return routedExecutor{executor: e, router: r}
	}
	return e
}

func (e routedExecutor) CheckAdmission(_, _ string, spec []byte, _ []string) (acceptance.AcceptanceOutcome, string) {
	document, err := jobwire.Decode(spec)
	if err != nil || document.Request == nil {
		return acceptance.AcceptanceOutcomeInvalid, "invalid inference job request"
	}
	profile := router.ProfileVideo
	if document.Request.Profile == jobwire.ProfileImageBatch {
		profile = router.ProfileImage
	}
	d, _, _ := e.router.RouteContext(context.Background(), router.Request{Model: document.Request.Image.Model, Profile: profile})
	if d == nil {
		return acceptance.AcceptanceOutcomeUnavailable, "host:unavailable"
	}
	switch d.Verdict {
	case router.Resident, router.WouldLoad, router.Hosted:
		return acceptance.AcceptanceOutcomeAccepted, ""
	}
	return acceptance.AcceptanceOutcomeInvalid, "host:router:" + d.Verdict
}

// placements serves the shipped runtime's two fixed-placement endpoints.
type placements struct{ local, remote *inferenceservice.Host }

// chat3Placements adds the chat@3 binding used by the shipped runtime.
type chat3Placements struct {
	*placements
	provider *inference.Provider
}

func (h chat3Placements) BindChatV3LogicalOwner(owner string) error {
	return h.provider.BindChatV3LogicalOwner(owner)
}

// Both fixed-placement hosts dispatch the existing chat@2 profile.
func (h *placements) ChatV2Available() bool { return true }

func (h *placements) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- h.local.Serve(ctx) }()
	go func() { done <- h.remote.Serve(ctx) }()
	err := <-done
	cancel()
	return errors.Join(err, <-done)
}
func (h *placements) Close() error { return errors.Join(h.local.Close(), h.remote.Close()) }

// applications answers Observe with the one application a program sees and
// refuses every change.
type applications struct {
	listener listen.Listener
	visible  map[string]string
	once     sync.Once
}

func newApplications(endpoint string, visible map[string]string) (*applications, error) {
	l, err := listen.Listen(endpoint)
	if err != nil {
		return nil, err
	}
	return &applications{listener: l, visible: visible}, nil
}

func (a *applications) Close() (err error) {
	a.once.Do(func() { err = a.listener.Close() })
	return err
}

func (a *applications) Serve(ctx context.Context) error {
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
			reply, err := (&wire.ApplicationsDispatcher{Handler: applicationsReceiver{application: a.visible[Program(program)]}}).ExchangeFrame(call.Frame)
			if err == nil {
				_ = call.Reply(reply)
			}
		}()
	}
}

type applicationsReceiver struct{ application string }

func (applicationsReceiver) Register(wire.ApplicationDescriptor) (wire.ApplicationChange, error) {
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
}
func (applicationsReceiver) Remove(string) (wire.ApplicationChange, error) {
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
}
func (applicationsReceiver) Announce(wire.ApplicationPresence) (wire.ApplicationChange, error) {
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
}
func (applicationsReceiver) Withdraw(string, string) (wire.ApplicationChange, error) {
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
}
func (r applicationsReceiver) Observe(string, int64) (wire.ApplicationPage, error) {
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
func (applicationsReceiver) Activate(string) (wire.ApplicationActivationResult, error) {
	return wire.ApplicationActivationResult{Outcome: wire.ApplicationActivationOutcomeForbidden}, nil
}
func (applicationsReceiver) AskConfirmation(string) (wire.ApplicationChange, error) {
	return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeForbidden}, nil
}
func (applicationsReceiver) ListStarts(string, int64) (wire.ApplicationStartPage, error) {
	return wire.ApplicationStartPage{Outcome: wire.ApplicationOutcomeForbidden, Starts: []wire.ApplicationStart{}}, nil
}
