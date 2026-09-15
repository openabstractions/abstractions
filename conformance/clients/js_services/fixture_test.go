package jsservices_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	download "github.com/openabstractions/abstraction-download/go"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	logging "github.com/openabstractions/abstraction-logging/go"
	logservice "github.com/openabstractions/abstraction-logging/go/service"
	model "github.com/openabstractions/abstraction-model/go"
	modelservice "github.com/openabstractions/abstraction-model/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
	storage "github.com/openabstractions/abstraction-storage/go"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type forged struct{}

func (forged) Resolve(q wire.ResolveRequest) (wire.ResolveResult, error) {
	return wire.ResolveResult{Status: "resolved", Reference: &wire.ServiceReference{Provider: "test", Capability: "wrong", Contract: q.Contracts[0], Scope: "local", Transport: "oa-framed-local@1", Endpoint: "must-not-connect", Guarantees: []string{}}}, nil
}
func TestInstalledJavaScript(t *testing.T) {
	node, script := os.Getenv("OA_JS_NODE"), os.Getenv("OA_JS_CONSUMER")
	if node == "" || script == "" {
		t.Fatal("installed consumer required")
	}
	dir := t.TempDir()
	for _, key := range []string{"HOME", "APPDATA", "XDG_CONFIG_HOME", "ProgramData"} {
		t.Setenv(key, dir)
	}
	endpoint := func(name string) string { return fixture.Endpoint(dir, "js-"+name) }
	run := func(t *testing.T, mode, ep string, extra ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, node, append([]string{script, mode, ep}, extra...)...)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "ABSTRACTION_RUNTIME_ENDPOINT="+ep)
		out, err := fixture.Output(ctx, cmd)
		if err != nil {
			t.Fatalf("%s: %v %s", mode, err, out)
		}
		t.Log(string(out))
	}
	t.Run("runtime", func(t *testing.T) {
		sink, err := logging.OpenFileSink(filepath.Join(dir, "private-records"))
		if err != nil {
			t.Fatal(err)
		}
		defer sink.Close()
		body := bytes.Repeat([]byte("x"), 150000)
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
		}))
		defer source.Close()
		content := &fixtureContent{path: filepath.Join(dir, "private-content"), digest: digest}
		if err := os.WriteFile(content.path, body, 0600); err != nil {
			t.Fatal(err)
		}
		ep := endpoint("runtime")
		h, err := host.Listen(host.Options{Endpoint: ep, LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), Sink: sink,
			JobRoot: filepath.Join(dir, "jobs"), JobOwner: "js-fixture-owner", JobEndpoint: endpoint("jobs"), JobExecutor: downloadserve.HTTPExecution{},
			Storage: content, StorageEndpoint: endpoint("storage"), StoragePolicy: func(ctx context.Context, p *identity.Peer, d string) error {
				path, e := p.Path.AtLeast(listen.Program.Path)
				if e != nil || !fixture.SameExecutable(path, node) || d != digest {
					return errors.New("fixture content forbidden")
				}
				return ctx.Err()
			}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- h.Serve(ctx) }()
		defer func() {
			cancel()
			h.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(3 * time.Second):
				t.Error("runtime drain")
			}
		}()
		run(t, "roundtrip", ep)
		account, e := user.Current()
		if e != nil {
			t.Fatal(e)
		}
		program, e := os.Executable()
		if e != nil {
			t.Fatal(e)
		}
		run(t, "verified", ep, account.Uid, program)
		run(t, "untrusted", ep, account.Uid, program)
		run(t, "job-storage", ep, source.URL, digest)
	})
	for _, mode := range []string{"forged", "oversized", "truncated", "malformed", "cancel", "timeout", "queue"} {
		t.Run(mode, func(t *testing.T) {
			ep := endpoint(mode)
			listener, err := listen.Listen(ep)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
				if err != nil {
					return
				}
				defer call.Close()
				switch mode {
				case "forged":
					d := wire.ResolverDispatcher{Handler: forged{}}
					reply, e := d.ExchangeFrame(call.Frame)
					if e == nil {
						call.Reply(reply)
					}
				case "oversized":
					conn.Write([]byte{0x7f, 0xff, 0xff, 0xff})
				case "truncated":
					conn.Write([]byte{0, 0, 0, 9, 'a'})
				case "malformed":
					call.Reply([]byte("not-json"))
				default:
					<-call.WaitContext().Done()
				}
			}()
			run(t, mode, ep)
			cancel()
			listener.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("peer drain")
			}
		})
	}
	run(t, "absent", endpoint("absent"))
}

// TestInstalledJavaScriptCapabilities runs the installed generated clients for
// model, job cancellation, log observation, config, rights and asks against one
// production runtime. OA_JS_CONNECTOR selects the native or test pipe connector.
func TestInstalledJavaScriptCapabilities(t *testing.T) {
	node, script := os.Getenv("OA_JS_NODE"), os.Getenv("OA_JS_SERVICES")
	if node == "" || script == "" {
		t.Skip("installed capability consumer not configured")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation")
	}
	dir := t.TempDir()
	for _, key := range []string{"HOME", "APPDATA", "XDG_CONFIG_HOME", "ProgramData"} {
		t.Setenv(key, dir)
	}
	endpoint := func(name string) string { return fixture.Endpoint(dir, "js-capabilities-"+name) }
	sink, err := logging.OpenFileSink(filepath.Join(dir, "records"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	body := bytes.Repeat([]byte("m"), 70000)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	}))
	defer source.Close()
	release := make(chan struct{})
	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("x"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer blocking.Close()
	defer close(release)
	isNode := func(p *identity.Peer) bool {
		path, e := p.Path.AtLeast(listen.Program.Path)
		return e == nil && fixture.SameExecutable(path, node)
	}
	const readAction, writeAction, observeAction = "abstraction.storage/content.read", "abstraction.storage/content.write", "abstraction.storage/content.observe"
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "policy.json"), []string{"fixture.read", readAction, writeAction, observeAction})
	if err != nil {
		t.Fatal(err)
	}
	content, err := storage.NewContentStore("js-writer", filepath.Join(dir, "content"))
	if err != nil {
		t.Fatal(err)
	}
	rightsEndpoint := endpoint("rights")
	decisions := rightsclient.New(rightsEndpoint)
	book, err := asks.LoadApplicationBook(filepath.Join(dir, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The selected store has no native notifier; this fixture invalidates periodically.
	changes := make(chan struct{}, 1)
	go func() {
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				select {
				case changes <- struct{}{}:
				default:
				}
			}
		}
	}()
	// The fake model hosts every router proof reads: a resident Lemonade model,
	// cold LM Studio models and an unreachable Ollama host.
	lemonade, studio, closeHosts := fixture.ModelHosts(nil)
	defer closeHosts()
	liveRouter := router.New(router.Lemonade(lemonade), router.LMStudio(studio), router.Ollama(fixture.UnreachableOllama))
	liveRouter.Survey()
	ep := endpoint("runtime")
	// services.mjs writes the edit-policy mode; an absent file permits.
	editPolicy := filepath.Join(dir, "edit-policy")
	// services.mjs writes "unavailable" to simulate a job decision outage [JOB-A9].
	jobPolicy := filepath.Join(dir, "job-policy")
	h, err := host.Listen(host.Options{Endpoint: ep, LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), Sink: sink,
		JobMethodPolicy: func(ctx context.Context, p *identity.Peer, service, method string) error {
			mode, _ := os.ReadFile(jobPolicy)
			if strings.TrimSpace(string(mode)) == "unavailable" {
				return fmt.Errorf("fixture job decision: %w", acceptanceprovider.ErrPolicyUnavailable)
			}
			return ctx.Err()
		},
		ConfigStore: casapi.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}, ConfigUserKey: filepath.Join(dir, "user-config"),
		ConfigEditPolicy: func(ctx context.Context, p *identity.Peer) error {
			mode, _ := os.ReadFile(editPolicy)
			switch strings.TrimSpace(string(mode)) {
			case "forbidden":
				return errors.New("fixture edit refused")
			case "unavailable":
				return fmt.Errorf("fixture decision lookup: %w", configservice.ErrEditPolicyUnavailable)
			}
			return ctx.Err()
		},
		// The same mode file drives history and model refusals for services.mjs.
		LogHistoryPolicy: func(ctx context.Context, p *identity.Peer) error {
			mode, _ := os.ReadFile(editPolicy)
			switch strings.TrimSpace(string(mode)) {
			case "history-forbidden":
				return errors.New("fixture history refused")
			case "history-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", logservice.ErrHistoryPolicyUnavailable)
			}
			return ctx.Err()
		},
		ModelPolicy: func(ctx context.Context, p *identity.Peer, registry string) error {
			mode, _ := os.ReadFile(editPolicy)
			switch strings.TrimSpace(string(mode)) {
			case "model-forbidden":
				return errors.New("fixture lookup refused")
			case "model-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", modelservice.ErrPolicyUnavailable)
			}
			return ctx.Err()
		},
		ConfigObservationSource: func(context.Context) (<-chan struct{}, func(), error) { return changes, func() {}, nil },
		JobRoot:                 filepath.Join(dir, "jobs"), JobOwner: "js-capability-owner", JobEndpoint: endpoint("jobs"), JobExecutor: downloadserve.HTTPExecution{},
		ModelRegistry: model.NewServiceRegistry(fixtureModel{source.URL, digest, int64(len(body))}), ModelEndpoint: endpoint("model"),
		Router: liveRouter, RouterEndpoint: endpoint("router"),
		RouterPolicy: func(ctx context.Context, p *identity.Peer, action, resource string) error {
			mode, _ := os.ReadFile(editPolicy)
			switch strings.TrimSpace(string(mode)) {
			case "router-forbidden":
				return errors.New("fixture route refused")
			case "router-unavailable":
				return fmt.Errorf("fixture decision lookup: %w", routerservice.ErrPolicyUnavailable)
			}
			return ctx.Err()
		},
		QuestionBook: book, QuestionEndpoint: endpoint("asks"),
		QuestionOperator: func(ctx context.Context, p *identity.Peer) error {
			if !isNode(p) {
				return asksservice.ErrOperatorForbidden
			}
			return ctx.Err()
		},
		Storage: content, StorageEndpoint: endpoint("storage"), StorageWriteLimit: 1 << 20,
		StorageWriteRecordPath: filepath.Join(dir, "writer-records.json"),
		StoragePolicy:          host.ContentPolicyFromRights(decisions, readAction),
		StorageWritePolicy:     host.ContentPolicyFromRights(decisions, writeAction),
		// A four-entry journal lets services.mjs overflow a stale cursor with a short burst.
		StorageChangesPolicy:   host.ContentPolicyFromRights(decisions, observeAction),
		StorageChangesInterval: 25 * time.Millisecond, StorageChangesCapacity: 4,
		RightsPolicy: policy, RightsEndpoint: rightsEndpoint,
		// Node may relay fixture.read decisions; only this runtime process enforces storage actions.
		RightsEnforcer: func(ctx context.Context, p *identity.Peer, action, resource string) bool {
			if ctx.Err() != nil {
				return false
			}
			if action == readAction || action == writeAction || action == observeAction {
				process, e := p.Process.AtLeast(listen.Program.Process)
				return e == nil && process.PID == os.Getpid()
			}
			return isNode(p) && action == "fixture.read"
		},
		RightsOperator: func(ctx context.Context, p *identity.Peer) error {
			if !isNode(p) {
				return rightsservice.ErrOperatorForbidden
			}
			return ctx.Err()
		}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() {
		cancel()
		h.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("runtime drain")
		}
	}()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	run, stop := context.WithTimeout(context.Background(), 180*time.Second)
	defer stop()
	cmd := exec.CommandContext(run, node, script, ep, source.URL, blocking.URL, digest, fmt.Sprint(len(body)), account.Uid, filepath.Clean(node), editPolicy, jobPolicy)
	cmd.Dir = filepath.Dir(script)
	// A waiting Observe occupies one native worker while Write proceeds; the
	// single-worker queue proof applies only to TestInstalledJavaScript.
	cmd.Env = append(os.Environ(), "UV_THREADPOOL_SIZE=4")
	out, err := fixture.Output(run, cmd)
	if err != nil {
		t.Fatalf("capabilities: %v %s", err, out)
	}
	t.Log(string(out))
}

type fixtureModel struct {
	url, digest string
	size        int64
}

func (fixtureModel) Registry() string { return "fixture" }
func (m fixtureModel) Resolve(ctx context.Context, ref model.Ref) (download.Spec, error) {
	spec := download.Spec{Artifact: download.Artifact{Digest: m.digest, Size: m.size}, Sources: []download.Source{{Scheme: "http", Locator: m.url}}}
	if ref.Repo == "private" {
		spec.Sink.Final = "provider-private-result"
	}
	return spec, nil
}

// Provider-private fixture allocation stays outside the installed application.
type fixtureContent struct{ path, digest string }

func (*fixtureContent) Name() string { return "js-fixture" }
func (p *fixtureContent) Find(d string) (storage.Ref, bool) {
	return storage.Ref{Store: p.Name(), Digest: d, Size: 150000}, d == p.digest
}
func (*fixtureContent) Place(string, int64) (storage.Ref, error) {
	return storage.Ref{}, storage.ErrReadOnly
}
func (p *fixtureContent) Path(storage.Ref) string { return p.path }
