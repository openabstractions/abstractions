package model_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	download "github.com/openabstractions/abstraction-download/go"
	execution "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	model "github.com/openabstractions/abstraction-model/go"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type fixed struct{ url, digest string }

func (fixed) Registry() string { return "fixture" }
func (f fixed) Resolve(ctx context.Context, r model.Ref) (download.Spec, error) {
	if r.Repo == "missing" {
		return download.Spec{}, errors.New("unavailable")
	}
	s := download.Spec{Artifact: download.Artifact{Digest: f.digest, Size: 21}, Sources: []download.Source{{Scheme: "http", Locator: f.url}}}
	if r.Repo == "private" {
		s.Sink.Final = "private-destination"
	}
	return s, nil
}
func TestInstalledModel(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation")
	}
	dir := t.TempDir()
	body := []byte("portable model result")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer server.Close()
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-model-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), JobEndpoint: endpoint("jobs"), JobRoot: filepath.Join(dir, "private"), JobOwner: "model-fixture", JobExecutor: execution.HTTPExecution{}, ModelEndpoint: endpoint("model"), ModelRegistry: model.NewServiceRegistry(fixed{server.URL, fmt.Sprintf("sha256:%x", sha256.Sum256(body))})}
	h, err := host.Listen(o)
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
			t.Error("runtime did not drain")
		}
	}()
	for _, mode := range []struct{ env, endpoint string }{{"OA_CPP_MODEL_DIRECT", o.ModelEndpoint}, {"OA_CPP_MODEL_PROBE", o.Endpoint}, {"OA_CPP_MODEL_GENERIC", o.Endpoint}} {
		probe := os.Getenv(mode.env)
		if probe == "" {
			t.Fatal("required", mode.env)
		}
		out, err := exec.CommandContext(ctx, probe, mode.endpoint).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", mode.env, err, out)
		}
		t.Log(string(out))
	}
}
