package python_jobs_test

import (
	"context"
	"crypto/sha256"
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

	download "github.com/openabstractions/abstraction-download/go"
	"github.com/openabstractions/abstraction-download/go/netcost"
	execution "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	job "github.com/openabstractions/abstraction-job/go"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
)

func TestInstalledPythonJobs(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation")
	}
	python := os.Getenv("OA_PYTHON_JOBS")
	if python == "" {
		t.Fatal("run the installed Python fixture runner")
	}
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	t.Setenv("ProgramData", filepath.Join(home, "machine"))
	for _, key := range []string{"ABSTRACTION_STORE", "ABSTRACTION_NAS_STORE", "ABSTRACTION_LOG", "ABSTRACTION_LOG_SERVICE"} {
		t.Setenv(key, "")
	}
	body := make([]byte, 256*1025)
	for i := range body {
		body[i] = byte(i)
	}
	var gets atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gets.Add(1)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body)
	}))
	defer source.Close()
	prefix := fmt.Sprintf("python-jobs-%d-%d", os.Getpid(), time.Now().UnixNano())
	options := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"), ModelEndpoint: listen.Endpoint(prefix + "m"), JobEndpoint: listen.Endpoint(prefix + "j"), JobRoot: filepath.Join(home, "private"), JobOwner: "python-jobs-owner", JobExecutor: meteredExecution()}
	h, err := host.Listen(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime cleanup timeout")
		}
	}()
	clientHome := filepath.Join(home, "client")
	if err := os.Mkdir(clientHome, 0700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("consumer.py")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, python, "-I", script, os.Getenv("OA_PYTHON_JOBS_PACKAGES"), options.Endpoint, source.URL, fmt.Sprintf("sha256:%x", sha256.Sum256(body)), fmt.Sprint(len(body)))
	cmd.Dir = clientHome
	cmd.Env = append(os.Environ(), "HOME="+clientHome, "USERPROFILE="+clientHome, "APPDATA="+clientHome, "XDG_CONFIG_HOME="+clientHome, "ProgramData="+clientHome, "PYTHONDONTWRITEBYTECODE=1")
	out, err := fixture.Output(ctx, cmd)
	if err != nil {
		t.Fatalf("Python: %v %s", err, out)
	}
	t.Log(string(out))
	if gets.Load() != 1 {
		t.Fatalf("external effect repeated: %d", gets.Load())
	}
	entries, err := os.ReadDir(clientHome)
	if err != nil || len(entries) != 0 {
		t.Fatal("client touched provider storage", entries, err)
	}
}

func TestPythonServicePartialRangeResume(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation")
	}
	python := os.Getenv("OA_PYTHON_JOBS")
	if python == "" {
		t.Fatal("run the installed Python fixture runner")
	}
	const (
		size       = 32 << 20
		holdAt     = 4 << 20
		checkpoint = 20 << 20
	)
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i)
	}
	origin := fixture.NewHeldRangeOrigin(body, holdAt)
	source := httptest.NewServer(origin)
	defer source.Close()
	defer origin.Unblock()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	t.Setenv("ProgramData", filepath.Join(home, "machine"))
	prefix := fmt.Sprintf("python-range-%d-%d", os.Getpid(), time.Now().UnixNano())
	options := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"), JobEndpoint: listen.Endpoint(prefix + "j"), JobRoot: filepath.Join(home, "private"), JobOwner: "python-range-owner", JobExecutor: meteredExecution()}
	start := func() func() {
		h, err := host.Listen(options)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- h.Serve(ctx) }()
		var once sync.Once
		return func() {
			once.Do(func() {
				cancel()
				h.Close()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(10 * time.Second):
					t.Error("runtime did not drain")
				}
			})
		}
	}
	stop := start()
	t.Cleanup(func() { stop() })
	clientHome := filepath.Join(home, "client")
	if err := os.Mkdir(clientHome, 0700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("range_client.py")
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []string {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, python, append([]string{"-I", script, os.Getenv("OA_PYTHON_JOBS_PACKAGES"), options.Endpoint}, args...)...)
		cmd.Dir = clientHome
		cmd.Env = append(os.Environ(), "HOME="+clientHome, "USERPROFILE="+clientHome, "APPDATA="+clientHome, "XDG_CONFIG_HOME="+clientHome, "ProgramData="+clientHome, "PYTHONDONTWRITEBYTECODE=1")
		out, err := fixture.Output(ctx, cmd)
		if err != nil {
			t.Fatalf("Python range client: %v\n%s", err, out)
		}
		return strings.Fields(string(out))
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	accepted := run("submit", source.URL+"/artifact", fmt.Sprint(size), digest, "python-range-key")
	if len(accepted) != 4 || accepted[0] != "ACCEPTED" {
		t.Fatalf("submission: %v", accepted)
	}
	select {
	case <-origin.Held():
	case <-time.After(20 * time.Second):
		t.Fatal("first range did not hold")
	}
	store, err := job.NewFileStore(options.JobRoot)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		record, err := store.Load(accepted[2])
		if err == nil && record.Progress.Done == checkpoint {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("partial checkpoint not durable: record=%+v error=%v", record, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	restarted := time.Now()
	stop = start()
	result := run("result", "python-range-key", accepted[1], accepted[2])
	if len(result) != 3 || result[0] != "RESULT" || result[1] != fmt.Sprint(size) || result[2] != fmt.Sprintf("%x", sha256.Sum256(body)) {
		t.Fatalf("result: %v", result)
	}
	origin.CheckResume(t, restarted, 10*time.Second)
}

// meteredExecution is real HTTP execution whose network cost source reports a
// metered path, so a request with network unmetered waits and never fetches.
// Unconstrained work is unaffected by it.
func meteredExecution() execution.HTTPExecution {
	metered := netcost.NewFake(netcost.Metered)
	return execution.HTTPExecution{NetworkCost: func() (netcost.Source, error) { return metered, nil }, Credentials: fixtureCredentials{}}
}

// fixtureCredentials admits the credential hf and refuses any other name as
// unknown at admission; applying hf is refused as revoked, so work naming it
// ends with cause credential and never reaches the origin (JOB-A16, DL-K1).
type fixtureCredentials struct{}

func (fixtureCredentials) CheckCredential(_ context.Context, _, name, _ string) error {
	if name == "hf" {
		return nil
	}
	return download.CredentialRefusal(name, "unknown")
}

func (fixtureCredentials) ApplyCredential(_ context.Context, _, name, _ string) (map[string]string, error) {
	return nil, download.CredentialRefusal(name, "revoked")
}
