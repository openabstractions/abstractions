package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	nas "github.com/openabstractions/abstraction-download/go/nas"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	job "github.com/openabstractions/abstraction-job/go"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

func TestManagedDownloadExecutorPinsNASSelectionAndRejectsReplacement(t *testing.T) {
	root := t.TempDir()
	nasRoot := filepath.Join(t.TempDir(), "shared-jobs")
	executor, err := managedDownloadExecutor(root, nil, "nas", nasRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonicalNASRoot, err := filepath.EvalSymlinks(nasRoot)
	if err != nil {
		t.Fatal(err)
	}
	if executor.Profile() != (downloadserve.DelegatedExecution{}).Profile() {
		t.Fatalf("profile = %q", executor.Profile())
	}
	data, err := os.ReadFile(filepath.Join(root, downloadProviderFile))
	if err != nil {
		t.Fatal(err)
	}
	var binding downloadProviderConfig
	if err := json.Unmarshal(data, &binding); err != nil || binding.Backend != "nas" || binding.Root != filepath.Clean(canonicalNASRoot) {
		t.Fatalf("provider binding = %q err=%v", data, err)
	}
	if _, err := managedDownloadExecutor(root, nil, "nas", filepath.Join(t.TempDir(), "replacement")); err == nil {
		t.Fatal("replacement NAS root was accepted")
	}
	if _, err := managedDownloadExecutor(root, nil, "native", ""); err == nil {
		t.Fatal("native provider replaced pinned NAS provider")
	}
}

func TestManagedDownloadExecutorRejectsIncompatibleSelectionBeforePin(t *testing.T) {
	root := t.TempDir()
	if _, err := managedDownloadExecutor(root, nil, "native", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := managedDownloadExecutor(root, nil, "nas", filepath.Join(t.TempDir(), "replacement")); err == nil {
		t.Fatal("incompatible provider was accepted")
	}
	if _, err := managedDownloadExecutor(root, nil, "native", ""); err != nil {
		t.Fatalf("original provider was poisoned by rejected selection: %v", err)
	}
	binding, found, err := readDownloadProviderPin(root)
	if err != nil || !found || binding.Backend != downloadBackendNative {
		t.Fatalf("binding after rejected selection = %+v found=%v err=%v", binding, found, err)
	}
}

func TestManagedDownloadExecutorConcurrentSelectionInstallsOneBinding(t *testing.T) {
	root := t.TempDir()
	roots := []string{filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")}
	var wg sync.WaitGroup
	errs := make(chan error, len(roots))
	for _, nasRoot := range roots {
		wg.Add(1)
		go func(nasRoot string) {
			defer wg.Done()
			_, err := managedDownloadExecutor(root, nil, "nas", nasRoot)
			errs <- err
		}(nasRoot)
	}
	wg.Wait()
	close(errs)
	var successes, failures int
	for err := range errs {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent selections successes=%d failures=%d", successes, failures)
	}
	if _, found, err := readDownloadProviderPin(root); err != nil || !found {
		t.Fatalf("binding missing after concurrent selection: found=%v err=%v", found, err)
	}
}

func TestManagedDownloadExecutorRefusesUnboundExistingDelegatedRoot(t *testing.T) {
	root := t.TempDir()
	provider, err := acceptanceprovider.OpenWithExecutor(root, "runtime-download", downloadserve.DelegatedExecution{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedDownloadExecutor(root, nil, "nas", filepath.Join(t.TempDir(), "shared")); err == nil {
		t.Fatal("existing delegated root silently rebound without a provider binding")
	}
	_ = provider
}

func TestNormalizeDownloadProviderResolvesExistingSymlinkAlias(t *testing.T) {
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, got, err := normalizeDownloadProvider("nas", alias)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("normalized symlink root = %q, want %q", got, filepath.Clean(want))
	}
}

func TestManagedDownloadExecutorRefusesInvalidNASSelection(t *testing.T) {
	if _, _, err := normalizeDownloadProvider("nas", "relative"); err == nil {
		t.Fatal("relative NAS root was accepted")
	}
	if _, _, err := normalizeDownloadProvider("native", filepath.Join(t.TempDir(), "unexpected")); err == nil {
		t.Fatal("native provider accepted a NAS root")
	}
	if _, _, err := normalizeDownloadProvider("motrix", ""); err == nil {
		t.Fatal("invalid backend was accepted")
	}
}

func TestManagedNASDownloadDeliversAfterExecutorRecreation(t *testing.T) {
	body := []byte("provider-selected NAS result")
	requests := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet {
			t.Fatalf("upstream method = %s", r.Method)
		}
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	oldTransport := http.DefaultTransport
	transport := oldTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // controlled httptest certificate
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = oldTransport }()
	localRoot := t.TempDir()
	nasRoot := t.TempDir()
	executor, err := managedDownloadExecutor(localRoot, nil, "nas", nasRoot)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := acceptanceprovider.OpenWithExecutor(localRoot, "runtime-download", executor)
	if err != nil {
		t.Fatal(err)
	}
	history, err := provider.Bind("caller").GetHistoryWindow()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	raw := request.Encode(&request.Request{Artifact: request.Artifact{Digest: fmt.Sprintf("sha256:%x", digest), Size: int64(len(body))}, Sources: []request.Source{{Scheme: "https", Locator: upstream.URL}}})
	identity := api.RequestIdentity{Key: "nas-selected", HistoryEpoch: history.HistoryEpoch}
	accepted, err := provider.Bind("caller").Submit(api.Submission{Identity: identity, Kind: download.Kind, Spec: raw, RequiredGuarantees: []string{acceptanceprovider.GuaranteeReconciliation, string(download.CapRecoverableSubmission)}})
	if err != nil || accepted.Receipt == nil {
		t.Fatalf("submit = %+v err=%v", accepted, err)
	}
	runDownloadExecutorPass(t, provider)
	provider, err = acceptanceprovider.OpenWithExecutor(localRoot, "runtime-download", executor)
	if err != nil {
		t.Fatal(err)
	}
	remoteStore, err := job.NewFileStore(nasRoot)
	if err != nil {
		t.Fatal(err)
	}
	remote := download.NewRunner(remoteStore, "nas-fixture")
	defer remote.Close()
	if _, err := remote.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	runDownloadExecutorPass(t, provider)
	observed, err := provider.BindOperations("caller").ObserveWork(identity)
	if err != nil || observed.Snapshot == nil || observed.Snapshot.State.String() != "complete" {
		if observed.Snapshot == nil {
			t.Fatalf("observed = %+v err=%v", observed, err)
		}
		t.Fatalf("observed = %+v failure=%+v err=%v", *observed.Snapshot, observed.Snapshot.Failure, err)
	}
	chunk, err := provider.BindOperations("caller").ReadResult(identity, 0, 1024)
	if err != nil || chunk.Chunk == nil || string(chunk.Chunk.Data) != string(body) || chunk.Chunk.Receipt.OperationID != accepted.Receipt.OperationID {
		t.Fatalf("result = %+v err=%v", chunk, err)
	}
	if requests != 1 {
		t.Fatalf("upstream requests = %d", requests)
	}
	localStore, err := job.NewFileStore(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	record, err := localStore.Load(accepted.Receipt.OperationID)
	if err != nil || record.Delegation == nil || record.Delegation.System != nas.System {
		t.Fatalf("provenance = %+v err=%v", record.Delegation, err)
	}
}

func runDownloadExecutorPass(t *testing.T, provider *acceptanceprovider.Provider) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := provider.Execute(ctx)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
