package delegation_test

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
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
)

// The fake represents one external adapter; its state survives the host restart.
// All preparation, guarantee admission and reconciliation use production code.
type external struct {
	mu                          sync.Mutex
	capable, recovered          bool
	starts, locates, deliveries int
	request                     string
	started                     chan struct{}
	body                        []byte
}

type adapter struct{ *external }

func (*external) System() string    { return "conformance-external" }
func (*external) Close() error      { return nil }
func (*external) Schemes() []string { return []string{"http"} }
func (e *external) Capabilities() []download.Capability {
	caps := []download.Capability{download.CapSurvivesProcessExit, download.CapDelegates}
	if e.capable {
		caps = append(caps, download.Capability(request.DownstreamRecoveryGuarantees[0]))
	}
	return caps
}
func (e *external) Start(_ context.Context, s download.Spec, _ int64) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts++
	e.request = s.Request
	if e.starts == 1 {
		close(e.started)
	}
	return "", download.ErrOutcomeUnknown
}
func (e *external) Locate(_ context.Context, id string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.locates++
	if e.starts == 0 {
		return "", nil
	}
	if id != e.request {
		return "", fmt.Errorf("request identity changed")
	}
	if !e.recovered {
		return "", download.ErrOutcomeUnknown
	}
	return "external-1", nil
}
func (e *external) Poll(context.Context, string) (download.Status, error) {
	return download.Status{State: download.DelegateTransferred, Done: int64(len(e.body)), Total: int64(len(e.body))}, nil
}
func (e *external) Finalize(_ context.Context, id, dest string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id != "external-1" {
		return fmt.Errorf("external identity changed")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(dest, e.body, 0600); err != nil {
		return err
	}
	e.deliveries++
	return nil
}
func (*external) Abandon(context.Context, string) error { return fmt.Errorf("unexpected cancellation") }
func start(t *testing.T, o host.Options) func() {
	t.Helper()
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			h.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("host failed to drain")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}
func command(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, os.Getenv("OA_CPP_DELEGATION_PROBE"), args...)
	output, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("C++ %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
func TestCppDelegation(t *testing.T) {
	if os.Getenv("OA_CPP_DELEGATION_PROBE") == "" {
		t.Fatal("set OA_CPP_DELEGATION_PROBE to installed-header consumer")
	}
	if runtime.GOOS == "darwin" {
		t.Fatal("Program proof required; current macOS path cannot claim this conformance")
	}
	for _, mode := range []string{"recoverable", "incapable", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			capable := mode == "recoverable"
			dir, err := os.MkdirTemp("", "oa-del-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(dir); err != nil {
					t.Error(err)
				}
			})
			for _, name := range []string{"HOME", "APPDATA", "XDG_CONFIG_HOME", "ProgramData"} {
				t.Setenv(name, dir)
			}
			var httpCalls atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httpCalls.Add(1)
				w.Write([]byte("unexpected local fallback"))
			}))
			t.Cleanup(func() {
				source.Close()
				if httpCalls.Load() != 0 {
					t.Errorf("HTTP fallback effects: %d", httpCalls.Load())
				}
			})
			endpoint := func(name string) string {
				if runtime.GOOS == "windows" {
					return fmt.Sprintf(`\\.\pipe\oa-del-%d-%s`, time.Now().UnixNano(), name)
				}
				return filepath.Join(dir, name+".sock")
			}
			external := &external{capable: capable, started: make(chan struct{}), body: []byte("delegated\x00result\nfrom external")}
			var delegated download.Delegator = &adapter{external}
			if capable {
				delegated = remoteBackend(t, external)
			}
			refused := make(chan error, 1)
			report := func(err error) {
				t.Logf("execution: %v", err)
				select {
				case refused <- err:
				default:
				}
			}
			o := host.Options{Endpoint: endpoint("runtime"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), JobEndpoint: endpoint("jobs"), JobRoot: filepath.Join(dir, "provider"), JobOwner: "conformance-owner", JobExecutor: downloadserve.DelegatedExecution{Delegators: []download.Delegator{delegated}, HTTPExecution: downloadserve.HTTPExecution{OnError: report}}}
			if mode == "unsupported" {
				o.JobExecutor = downloadserve.HTTPExecution{}
			}
			stop := start(t, o)
			if mode == "unsupported" {
				out := command(t, "incapable", o.Endpoint, "incapable-request", source.URL)
				if !strings.HasPrefix(out, "REFUSED ") {
					t.Fatal(out)
				}
				stop()
				external.mu.Lock()
				defer external.mu.Unlock()
				if external.starts != 0 || external.deliveries != 0 {
					t.Fatal("incapable external effect")
				}
				return
			}
			receipt := strings.Fields(command(t, "submit", o.Endpoint, "durable-request", source.URL))
			if len(receipt) != 3 {
				t.Fatalf("receipt %q", receipt)
			}
			if mode == "incapable" {
				select {
				case err := <-refused:
					if !errors.Is(err, download.ErrRecoverableSubmissionUnavailable) {
						t.Fatalf("wrong refusal: %v", err)
					}
					t.Logf("runner refused: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("missing downstream refusal")
				}
				if out := command(t, "pending", o.Endpoint, "durable-request", receipt[0], receipt[1], receipt[2]); out != "PENDING" {
					t.Fatal(out)
				}
				stop()
				external.mu.Lock()
				defer external.mu.Unlock()
				if external.starts != 0 || external.deliveries != 0 {
					t.Fatal("incapable downstream effect")
				}
				return
			}
			select {
			case <-external.started:
			case <-time.After(5 * time.Second):
				t.Fatal("downstream start missing")
			}
			select {
			case <-delegated.(*remoteAdapter).unknown:
			case <-time.After(5 * time.Second):
				t.Fatal("service did not reconcile unknown remote outcome")
			}
			stop()
			external.mu.Lock()
			if external.starts != 1 || external.deliveries != 0 {
				t.Fatalf("before restart: starts=%d deliveries=%d", external.starts, external.deliveries)
			}
			external.recovered = true
			external.mu.Unlock()
			// Recreate the service-owned client while retaining the original backend.
			remote := delegated.(*remoteAdapter)
			remote.client.CloseIdleConnections()
			transport := remote.client.Transport.(*http.Transport).Clone()
			t.Cleanup(transport.CloseIdleConnections)
			delegated = &remoteAdapter{adapter: &adapter{external}, endpoint: remote.endpoint, token: remote.token, client: &http.Client{Transport: transport, Timeout: remote.client.Timeout}}
			o.JobExecutor = downloadserve.DelegatedExecution{Delegators: []download.Delegator{delegated}, HTTPExecution: downloadserve.HTTPExecution{OnError: report}}
			stop = start(t, o)
			out := command(t, "recover", o.Endpoint, "durable-request", receipt[0], receipt[1], receipt[2])
			got, err := hex.DecodeString(out)
			if err != nil || string(got) != string(external.body) {
				t.Fatalf("result %q: %v", out, err)
			}
			stop()
			external.mu.Lock()
			defer external.mu.Unlock()
			if external.starts != 1 || external.deliveries != 1 || external.locates == 0 {
				t.Fatalf("starts=%d deliveries=%d locates=%d", external.starts, external.deliveries, external.locates)
			}
		})
	}
}
