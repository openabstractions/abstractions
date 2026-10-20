package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
)

func qualificationFixture(t *testing.T) (Catalog, PinnedDownload, QualificationMirror) {
	t.Helper()
	_, artifact, _, _ := nativeArchiveFixture(t)
	pin := artifact.DownloadPin()
	return Catalog{Components: []Component{{ID: "native-fixture", Artifact: &artifact}}}, pin,
		QualificationMirror{CanonicalURL: pin.URL, SHA256: pin.SHA256, Bytes: pin.Bytes,
			MirrorURL: "http://127.0.0.1:32123/native-qualification/" + strings.Repeat("a", 32) + "/" + strings.Repeat("b", 64)}
}

func desktopQualificationFixture(t *testing.T, body []byte) (Catalog, PinnedDownload, QualificationMirror) {
	t.Helper()
	data := fmt.Sprintf(`{"schema":2,"release_tag":"v0.8.0","apps":[],"extensions":[{"id":"opencode","file":"opencode-0.8.0.msi","sha256":"%x","bytes":%d,"version":"0.8.0","product_code":"{01234567-89AB-CDEF-0123-456789ABCDEF}","upgrade_code":%q}]}`,
		sha256.Sum256(body), len(body), openCodeWatcherFixup.RepairUpgradeCode)
	catalog := catalogForTarget("windows", "amd64")
	if err := projectAdopterAssets(&catalog, "windows", "amd64", []byte(data)); err != nil {
		t.Fatal(err)
	}
	pin := catalog.DesktopIntegration.downloadPin()
	return catalog, pin, QualificationMirror{CanonicalURL: pin.URL, SHA256: pin.SHA256, Bytes: pin.Bytes,
		MirrorURL: "http://127.0.0.1:32123/native-qualification/" + strings.Repeat("a", 32) + "/" + strings.Repeat("b", 64)}
}

func TestQualificationDesktopIntegrationPin(t *testing.T) {
	catalog, pin, mirror := desktopQualificationFixture(t, []byte("signed integration fixture"))
	machine := client.New("unused-fixture-endpoint")
	expires := time.Now().Add(time.Minute)
	transfer, err := qualificationTransfer(machine, catalog, []QualificationMirror{mirror}, expires)
	if err != nil || transfer.Machine != machine || !transfer.LocalOnly {
		t.Fatalf("explicit machine/local selection lost: %v", err)
	}
	sources, err := transfer.downloadSources(pin, "https")
	if err != nil || len(sources) != 2 || sources[0].Locator != mirror.MirrorURL || sources[1].Locator != pin.URL {
		t.Fatalf("desktop pin sources: %+v (%v)", sources, err)
	}
	for _, wrong := range []QualificationMirror{
		{CanonicalURL: pin.URL, SHA256: strings.Repeat("f", 64), Bytes: pin.Bytes, MirrorURL: mirror.MirrorURL},
		{CanonicalURL: pin.URL, SHA256: pin.SHA256, Bytes: pin.Bytes + 1, MirrorURL: mirror.MirrorURL},
		{CanonicalURL: pin.URL + "-other", SHA256: pin.SHA256, Bytes: pin.Bytes, MirrorURL: mirror.MirrorURL},
	} {
		if _, err := qualificationTransfer(machine, catalog, []QualificationMirror{wrong}, expires); err == nil {
			t.Fatalf("mismatched mirror accepted: %+v", wrong)
		}
	}
	other, _, native := qualificationFixture(t)
	catalog.Components = other.Components
	if _, err := installedQualificationTransfer(machine, catalog, []QualificationMirror{mirror}, expires); err != nil {
		t.Fatal(err)
	}
	if _, err := installedQualificationTransfer(machine, catalog, []QualificationMirror{native}, expires); err == nil {
		t.Fatal("installed qualification admitted a native archive mirror")
	}
}

func TestQualificationInputStrictJSON(t *testing.T) {
	_, _, mirror := qualificationFixture(t)
	input := QualificationInput{RuntimeEndpoint: "fixture", RuntimeProgram: "program", RuntimeStateDir: "state", Expires: time.Now().Add(time.Minute), Mirrors: []QualificationMirror{mirror}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseQualificationInput(data); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		string(data) + " {}",
		strings.Replace(string(data), `"expires":`, `"foreign":1,"expires":`, 1),
		strings.Replace(string(data), `"expires":`, `"expires":"2020-01-01T00:00:00Z","expires":`, 1),
		strings.Replace(string(data), `"runtime_endpoint":`, `"RUNTIME_ENDPOINT":`, 1),
		strings.Replace(string(data), `"mirror_url":`, `"MIRROR_URL":`, 1),
		strings.Replace(string(data), `"bytes":`, `"bytes":1,"bytes":`, 1),
		strings.Repeat(" ", QualificationInputMaxBytes+1),
	} {
		if _, err := ParseQualificationInput([]byte(bad)); err == nil {
			t.Fatal("non-exact startup JSON accepted")
		}
	}
}

func TestQualificationTransferAdmission(t *testing.T) {
	catalog, pin, mirror := qualificationFixture(t)
	machine := client.New("unused-fixture-endpoint")
	expires := time.Now().Add(time.Minute)
	transfer, err := qualificationTransfer(machine, catalog, []QualificationMirror{mirror}, expires)
	if err != nil || transfer.Machine != machine || !transfer.LocalOnly {
		t.Fatalf("explicit machine/local selection lost: %v", err)
	}
	sources, err := transfer.downloadSources(pin, "https")
	if err != nil || len(sources) != 2 || sources[0].Locator != mirror.MirrorURL || sources[1].Locator != pin.URL {
		t.Fatalf("transport order/pin: %+v %v", sources, err)
	}
	for _, source := range sources {
		if source.Credential != "" {
			t.Fatal("qualification supplied credentials or source policy")
		}
	}
	for name, mutate := range map[string]func(*QualificationMirror){
		"URL":      func(m *QualificationMirror) { m.CanonicalURL += "-other" },
		"digest":   func(m *QualificationMirror) { m.SHA256 = strings.Repeat("0", 64) },
		"size":     func(m *QualificationMirror) { m.Bytes++ },
		"DNS":      func(m *QualificationMirror) { m.MirrorURL = strings.Replace(m.MirrorURL, "127.0.0.1", "localhost", 1) },
		"foreign":  func(m *QualificationMirror) { m.MirrorURL = strings.Replace(m.MirrorURL, "127.0.0.1", "127.0.0.2", 1) },
		"query":    func(m *QualificationMirror) { m.MirrorURL += "?token=x" },
		"fragment": func(m *QualificationMirror) { m.MirrorURL += "#x" },
		"userinfo": func(m *QualificationMirror) { m.MirrorURL = strings.Replace(m.MirrorURL, "http://", "http://user@", 1) },
		"encoded":  func(m *QualificationMirror) { m.MirrorURL = strings.Replace(m.MirrorURL, "/native-", "/%6eative-", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := mirror
			mutate(&bad)
			if _, err := qualificationTransfer(machine, catalog, []QualificationMirror{bad}, expires); err == nil {
				t.Fatal("foreign transport accepted")
			}
		})
	}
	if _, err := qualificationTransfer(machine, catalog, []QualificationMirror{mirror, mirror}, expires); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := qualificationTransfer(nil, catalog, []QualificationMirror{mirror}, expires); err == nil {
		t.Fatal("missing explicit machine accepted")
	}
	part, err := nativeResourceDownload(*catalog.Components[0].Artifact, 1)
	if err != nil {
		t.Fatal(err)
	}
	resource := QualificationMirror{CanonicalURL: part.URL, SHA256: part.SHA256, Bytes: part.Bytes, MirrorURL: strings.Replace(mirror.MirrorURL, strings.Repeat("b", 64), strings.Repeat("c", 64), 1)}
	if _, err := qualificationTransfer(machine, catalog, []QualificationMirror{mirror, resource}, expires); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{
		strings.Replace(resource.MirrorURL, ":32123", ":32124", 1),
		strings.Replace(resource.MirrorURL, strings.Repeat("a", 32), strings.Repeat("d", 32), 1),
	} {
		bad := resource
		bad.MirrorURL = wrong
		if _, err := qualificationTransfer(machine, catalog, []QualificationMirror{mirror, bad}, expires); err == nil {
			t.Fatal("mixed mirror owner/attempt accepted")
		}
	}
	for _, invalidExpiry := range []time.Time{time.Now().Add(-time.Second), time.Now().Add(time.Hour)} {
		if _, err := qualificationTransfer(machine, catalog, []QualificationMirror{mirror}, invalidExpiry); err == nil {
			t.Fatal("unbounded/expired qualification accepted")
		}
	}
	other := pin
	other.URL += "-different"
	if sources, err := transfer.downloadSources(other, "https"); err != nil || len(sources) != 1 || sources[0].Locator != other.URL {
		t.Fatal("mirror escaped its exact pin")
	}
	transfer.qualification.expires = time.Now().Add(-time.Second)
	if _, err := transfer.downloadSources(pin, "https"); err == nil {
		t.Fatal("expired attempt silently continued")
	}
	ordinary, err := (PublicTransfer{}).downloadSources(pin, "https")
	if err != nil || len(ordinary) != 1 || ordinary[0].Locator != pin.URL {
		t.Fatal("ordinary factory changed")
	}
}

type qualificationSink struct{}

func (qualificationSink) Write(logging.Record) error { return nil }

type qualificationRoundTrip func(*http.Request) (*http.Response, error)

func (f qualificationRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQualificationTransferRealPublicJob(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("ordinary local Program-bound transport remains unsupported")
	}
	body := []byte("tiny desktop integration qualification transport fixture")
	var corrupt atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		if r.Method != http.MethodHead {
			if corrupt.Load() {
				_, _ = w.Write(bytes.Repeat([]byte("x"), len(body)))
			} else {
				_, _ = w.Write(body)
			}
		}
	}))
	defer server.Close()
	// The inert receiving never reaches a public origin, even on a failing
	// mirror. The submitted canonical HTTPS source remains unchanged.
	previousClient := http.DefaultClient
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	http.DefaultClient = &http.Client{Transport: qualificationRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "http" || "http://"+r.URL.Host != server.URL {
			return nil, errors.New("fixture refuses every non-owned HTTP origin")
		}
		return transport.RoundTrip(r)
	})}
	defer func() { http.DefaultClient = previousClient; transport.CloseIdleConnections() }()
	catalog, pin, mirror := desktopQualificationFixture(t, body)
	mirror.MirrorURL = server.URL + "/native-qualification/" + strings.Repeat("a", 32) + "/" + strings.Repeat("b", 64)
	root := t.TempDir()
	var endpointNumber atomic.Uint64
	endpoint := func(name string) string {
		return listen.Endpoint(fmt.Sprintf("oa-mirror-%d-%d-%s", os.Getpid(), endpointNumber.Add(1), name))
	}
	options := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), JobEndpoint: endpoint("job"), JobRoot: filepath.Join(root, "jobs"), JobOwner: "qualification-fixture", JobExecutor: downloadserve.HTTPExecution{}, Sink: qualificationSink{}}
	h, err := host.Listen(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() {
		cancel()
		_ = h.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	self, err := identity.Self()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	machine := client.NewVerified(options.Endpoint, listen.ServerExpectation{Principal: self, Program: identity.CanonicalProgramPath(program)})
	transfer, err := qualificationTransfer(machine, catalog, []QualificationMirror{mirror}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	work, err := transfer.Reserve(ctx, pin, "qualification-real-job")
	if err != nil {
		t.Fatal(err)
	}
	if err := transfer.Submit(ctx, pin, work); err != nil {
		t.Fatal(err)
	}
	// The identical saved identity reconciles acceptance rather than duplicating work.
	if err := transfer.Submit(ctx, pin, work); err != nil {
		t.Fatal(err)
	}
	for {
		observed, err := transfer.Observe(ctx, work)
		if err != nil {
			t.Fatal(err)
		}
		if observed.State == "complete" {
			break
		}
		if observed.State == "failed" || observed.State == "cancelled" {
			t.Fatalf("real public work: %+v", observed)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	var got bytes.Buffer
	if _, err := transfer.Copy(ctx, work, &got); err != nil || !bytes.Equal(got.Bytes(), body) {
		t.Fatalf("same-subject public result: %q %v", got.Bytes(), err)
	}
	corrupt.Store(true)
	bad, err := transfer.Reserve(ctx, pin, "qualification-wrong-bytes")
	if err != nil {
		t.Fatal(err)
	}
	if err := transfer.Submit(ctx, pin, bad); err != nil {
		t.Fatal(err)
	}
	for {
		observed, err := transfer.Observe(ctx, bad)
		if err != nil {
			t.Fatal(err)
		}
		if observed.State == "failed" {
			break
		}
		if observed.State == "complete" {
			t.Fatal("wrong mirror bytes became a verified result")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := transfer.Copy(ctx, bad, &bytes.Buffer{}); err == nil {
		t.Fatal("failed work exposed result bytes")
	}
}
