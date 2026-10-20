package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// serviceRuntime runs a runtime inside this test executable and binds the
// example to it the way a developer does [FAC-B5]: the runtime's endpoint in
// the variable and --runtime-program naming this test executable, so the
// facade's development selection supplies the server expectation.
func serviceRuntime(t *testing.T, configure ...func(*host.Options)) *logging.FileSink {
	t.Helper()
	t.Setenv("OA_TEST_ALLOW_INSTALLED_RUNTIME", "")
	if runtime.GOOS == "darwin" {
		t.Skip("verified local fixture requires Program proof unavailable on macOS")
	}
	prefix := fmt.Sprintf("pretend-lemonade-%d", time.Now().UnixNano())
	sink, err := logging.OpenFileSink(filepath.Join(t.TempDir(), "history"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.Close() })
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		ConfigStore: casapi.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}, ConfigUserKey: filepath.Join(t.TempDir(), "config"), ConfigWithoutMachine: true,
		JobEndpoint: listen.Endpoint(prefix + "j"), JobRoot: filepath.Join(t.TempDir(), "provider"), JobOwner: "pretend-lemonade-owner", JobExecutor: downloadserve.HTTPExecution{}, Sink: sink}
	for _, change := range configure {
		change(&o)
	}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime failed to stop")
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(bootstrap.RuntimeEndpointVariable, o.Endpoint)
	selected, err := selectMachine(exe)
	if err != nil {
		t.Fatal(err)
	}
	previous := machine
	machine = func() *facade.Machine { return selected }
	t.Cleanup(func() { machine = previous })
	return sink
}

// The example is the adopter's shape of the facade. Building it proves nothing;
// a caller that compiles and then waits forever for bytes nobody started is the
// defect this catches.
func TestTheWholeIntegration(t *testing.T) {
	sink := serviceRuntime(t)
	payload := make([]byte, 1<<20)
	rand.New(rand.NewSource(1)).Read(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "weights.bin", time.Now(), bytes.NewReader(payload))
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	args := []string{srv.URL + "/weights.bin", strconv.Itoa(len(payload)), fmt.Sprintf("sha256:%x", sha256.Sum256(payload))}
	if err := run(args, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	final := strings.TrimSpace(strings.TrimPrefix(lastLine(out.String()), "delivered to "))
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("%s: %v\n%s", final, err, out.String())
	}
	if sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Fatalf("%s does not match what the server served", final)
	}
	page, err := sink.Read("", 16, 65536)
	if err != nil || len(page.Records) != 3 {
		t.Fatalf("expected three retained application events: %+v %v", page, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"download requested", "download accepted", "download delivered"} {
		record := page.Records[i]
		if record.Msg != want || record.Attrs["request.key"] == "" || record.Attrs["expected.bytes"] != strconv.Itoa(len(payload)) {
			t.Fatalf("event %d: %+v", i, record)
		}
		if i > 0 && record.Attrs["operation.id"] == "" {
			t.Fatalf("accepted event lacks operation identity: %+v", record)
		}
		if len(record.Identity) < 2 || record.Identity[0].Program != exe || !record.Identity[1].Verified ||
			identity.CanonicalProgramPath(record.Identity[1].Exe) != identity.CanonicalProgramPath(exe) {
			t.Fatalf("event lacks observed executable stamp: %+v", record.Identity)
		}
	}
}

func TestExplicitBindingRequiresNamedRuntime(t *testing.T) {
	t.Setenv(bootstrap.RuntimeEndpointVariable, "")
	if _, err := selectMachine(filepath.Join(t.TempDir(), "runtime.exe")); err == nil {
		t.Fatal("explicit program accepted without an explicit endpoint")
	}
	t.Setenv(bootstrap.RuntimeEndpointVariable, listen.Endpoint("example-binding"))
	if _, err := selectMachine("runtime.exe"); err == nil {
		t.Fatal("relative runtime program accepted")
	}
}

// The local fixture proves byte transfer and honours resumed range requests.
func TestSourceBytesAndRanges(t *testing.T) {
	data := sourcePayload()
	counts := &sourceCounts{}
	srv := httptest.NewServer(sourceHandler(data, counts))
	defer srv.Close()
	for _, tc := range []struct {
		path, byteRange string
		start           int
	}{{"/fixture.bin", "", 0}, {"/slow.bin", "bytes=900000-", 900000}} {
		request, err := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Range", tc.byteRange)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || !bytes.Equal(body, data[tc.start:]) {
			t.Fatalf("%s transferred unexpected bytes: %v", tc.path, err)
		}
	}
	if counts.requests.Load() != 2 || counts.bytes.Load() != int64(len(data)*2-900000) {
		t.Fatalf("transfer evidence is wrong: requests=%d bytes=%d", counts.requests.Load(), counts.bytes.Load())
	}
}

func TestSourceReviewModelIsDeterministicAndHasNoDownloadEffect(t *testing.T) {
	counts := &sourceCounts{}
	handler := sourceHandler(sourcePayload(), counts)
	for _, tc := range []struct {
		method, path, body, want string
		status                   int
	}{
		{"GET", "/v1/models", "", `"id":"pretend-review-model"`, 200},
		{"POST", "/v1/chat/completions", `{"model":"pretend-review-model","messages":[{"role":"user","content":"example"}]}`, "This is a deterministic review example.", 200},
		{"POST", "/v1/chat/completions", `{"model":"real-model"}`, "", 400},
		{"POST", "/v1/chat/completions", `{"model":"pretend-review-model","stream":true}`, "", 400},
		{"POST", "/v1/models", "", "", 405},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if counts.requests.Load() != 0 || counts.bytes.Load() != 0 {
		t.Fatal("model metadata/completion changed download counters")
	}
}

// Without a runtime the example reports it and creates no provider files.
func TestAbsenceHasNoLocalFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("ABSTRACTION_STORE", filepath.Join(dir, "must-not-create"))
	t.Setenv(bootstrap.RuntimeEndpointVariable, listen.Endpoint(fmt.Sprintf("absent-lemonade-%d", os.Getpid())))
	if err := run([]string{"http://127.0.0.1:1/x", "1", "sha256:00"}, &bytes.Buffer{}); err == nil {
		t.Fatal("absent runtime accepted work")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("absence created files: %v %v", entries, err)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
