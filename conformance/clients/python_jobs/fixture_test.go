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
	"sync/atomic"
	"testing"
	"time"

	execution "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
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
	options := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"), ModelEndpoint: listen.Endpoint(prefix + "m"), JobEndpoint: listen.Endpoint(prefix + "j"), JobRoot: filepath.Join(home, "private"), JobOwner: "python-jobs-owner", JobExecutor: execution.HTTPExecution{}}
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
	out, err := cmd.CombinedOutput()
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
