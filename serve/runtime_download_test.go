package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-identity/go/privatedir/privatetest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	nas "github.com/openabstractions/abstraction-download/go/nas"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	job "github.com/openabstractions/abstraction-job/go"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

var runtimeNASConfigSerial atomic.Uint64

func serveRuntimeNASConfig(t *testing.T, options *runtimeFlags, path string) {
	t.Helper()
	if options.configEndpoint == "" {
		options.configEndpoint = listen.Endpoint(fmt.Sprintf("nas-config-%d-%d", os.Getpid(), runtimeNASConfigSerial.Add(1)))
	}
	h, err := configservice.ListenWithStore(options.configEndpoint, casapi.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}, path)
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		t.Skip("Darwin Unix refused Program proof; this native Config fixture needs a Program-capable endpoint")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = h.OmitMachineRung(); err != nil {
		h.Close()
		t.Fatal(err)
	}
	if err = h.EnableTypedConfig(configservice.TypedOptions{ReadPolicy: func(context.Context, *identity.Peer, string) error { return nil }}); err != nil {
		h.Close()
		t.Fatal(err)
	}
	if err = h.EnableObservation(configservice.FileObservation(path)); err != nil {
		h.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(context.Background()) }()
	t.Cleanup(func() {
		h.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("config service did not drain")
		}
	})
}

func TestRuntimeNASStoreReadsServiceInsteadOfFiles(t *testing.T) {
	t.Setenv("ABSTRACTION_CONFIG_ENDPOINT", "must-not-select-profile-service")
	t.Setenv("ABSTRACTION_NAS_STORE", "")
	options := runtimeFlags{stateDir: privatetest.Dir(t), downloadBackend: downloadBackendNAS}
	_, decoy := isolatedConfigStore(options.stateDir)
	if err := os.MkdirAll(decoy, 0700); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(privatetest.Dir(t), "service.json")
	if err := config.Save(selected, config.Config{NASStore: "service-owned-value"}); err != nil {
		t.Fatal(err)
	}
	serveRuntimeNASConfig(t, &options, selected)
	got, err := runtimeNASStore(context.Background(), options)
	if err != nil || got != "service-owned-value" {
		t.Fatalf("service NAS setting = %q, %v", got, err)
	}
}

func TestRuntimeNASStoreDoesNotFallbackWhenServiceUnavailable(t *testing.T) {
	t.Setenv("ABSTRACTION_NAS_STORE", "")
	options := runtimeFlags{stateDir: privatetest.Dir(t), downloadBackend: downloadBackendNAS,
		configEndpoint: listen.Endpoint(fmt.Sprintf("nas-absent-%d-%d", os.Getpid(), runtimeNASConfigSerial.Add(1)))}
	_, decoy := isolatedConfigStore(options.stateDir)
	if err := config.Save(decoy, config.Config{NASStore: "must-not-read-files"}); err != nil {
		t.Fatal(err)
	}
	got, err := runtimeNASStore(context.Background(), options)
	if err == nil || got != "" {
		t.Fatalf("absent service returned %q, %v", got, err)
	}
}

func TestRuntimeNASStoreUsesConfigAndKeepsPinnedJobs(t *testing.T) {
	t.Setenv("ABSTRACTION_NAS_STORE", "")
	state := privatetest.Dir(t)
	options, err := parseTestRuntime(t, []string{"--isolated", "nas-config-fixture", "--state-dir", state, "--jobs-download-backend", "nas"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_, path := isolatedConfigStore(state)
	first := filepath.Join(privatetest.Dir(t), "first")
	if err := config.Save(path, config.Config{NASStore: first}); err != nil {
		t.Fatal(err)
	}
	serveRuntimeNASConfig(t, &options, path)
	selected, err := runtimeNASStore(context.Background(), options)
	if err != nil || selected != first {
		t.Fatalf("configured NAS store = %q, %v", selected, err)
	}
	root := filepath.Join(state, "jobs")
	if _, err := managedDownloadExecutor(root, nil, options.downloadBackend, selected); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(privatetest.Dir(t), "second")
	if err := config.Save(path, config.Config{NASStore: second}); err != nil {
		t.Fatal(err)
	}
	selected, err = runtimeNASStore(context.Background(), options)
	if err != nil || selected != second {
		t.Fatalf("edited NAS store = %q, %v", selected, err)
	}
	if _, err := managedDownloadExecutor(root, nil, options.downloadBackend, selected); err == nil {
		t.Fatal("settings edit silently moved the pinned job store")
	}
	// The flag is the same key's run override, and can keep an existing root
	// bound while the shared setting is changed for another runtime.
	options.downloadNASRoot = first
	selected, err = runtimeNASStore(context.Background(), options)
	if err != nil || selected != first {
		t.Fatalf("flag override = %q, %v", selected, err)
	}
	if _, err := managedDownloadExecutor(root, nil, options.downloadBackend, selected); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeNASStoreOverridesAndIsolation(t *testing.T) {
	state := privatetest.Dir(t)
	options := runtimeFlags{stateDir: state, downloadBackend: downloadBackendNAS}
	t.Setenv("ABSTRACTION_NAS_STORE", "")
	// An isolated runtime reads its own settings when the user's profile
	// names a different NAS store.
	profile := privatetest.Dir(t)
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(key, profile)
	}
	t.Setenv("ProgramData", filepath.Join(profile, "machine"))
	if err := config.Save(config.UserPath(), config.Config{NASStore: "profile-sentinel"}); err != nil {
		t.Fatal(err)
	}
	_, selectedPath := isolatedConfigStore(state)
	serveRuntimeNASConfig(t, &options, selectedPath)
	if got, err := runtimeNASStore(context.Background(), options); err != nil || got != "" {
		t.Fatalf("isolated store read profile: %q, %v", got, err)
	}
	t.Setenv("ABSTRACTION_NAS_STORE", "environment-override")
	if got, err := runtimeNASStore(context.Background(), options); err != nil || got != "" {
		t.Fatalf("legacy environment leaked into typed setting: %q, %v", got, err)
	}
	options.downloadNASRoot = "flag-override"
	if got, err := runtimeNASStore(context.Background(), options); err != nil || got != "flag-override" {
		t.Fatalf("flag precedence: %q, %v", got, err)
	}
	_, path := isolatedConfigStore(state)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeNASStore(context.Background(), options); err == nil {
		t.Fatal("unreadable selected configuration was ignored")
	}
}

func TestManagedDownloadExecutorPinsNASSelectionAndRejectsReplacement(t *testing.T) {
	root := privatetest.Dir(t)
	nasRoot := filepath.Join(privatetest.Dir(t), "shared-jobs")
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
	if _, err := managedDownloadExecutor(root, nil, "nas", filepath.Join(privatetest.Dir(t), "replacement")); err == nil {
		t.Fatal("replacement NAS root was accepted")
	}
	if _, err := managedDownloadExecutor(root, nil, "native", ""); err == nil {
		t.Fatal("native provider replaced pinned NAS provider")
	}
}

func TestManagedDownloadExecutorRejectsIncompatibleSelectionBeforePin(t *testing.T) {
	root := privatetest.Dir(t)
	if _, err := managedDownloadExecutor(root, nil, "native", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := managedDownloadExecutor(root, nil, "nas", filepath.Join(privatetest.Dir(t), "replacement")); err == nil {
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
	root := privatetest.Dir(t)
	roots := []string{filepath.Join(privatetest.Dir(t), "one"), filepath.Join(privatetest.Dir(t), "two")}
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
	root := privatetest.Dir(t)
	provider, err := acceptanceprovider.OpenWithExecutor(root, "runtime-download", downloadserve.DelegatedExecution{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managedDownloadExecutor(root, nil, "nas", filepath.Join(privatetest.Dir(t), "shared")); err == nil {
		t.Fatal("existing delegated root silently rebound without a provider binding")
	}
	_ = provider
}

func TestNormalizeDownloadProviderResolvesExistingSymlinkAlias(t *testing.T) {
	target := privatetest.Dir(t)
	alias := filepath.Join(privatetest.Dir(t), "alias")
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
	if _, _, err := normalizeDownloadProvider("native", filepath.Join(privatetest.Dir(t), "unexpected")); err == nil {
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
	localRoot := privatetest.Dir(t)
	nasRoot := privatetest.Dir(t)
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

func runtimeNASSubmission(t *testing.T, provider *acceptanceprovider.Provider, key string) api.Submission {
	t.Helper()
	history, err := provider.Bind("caller").GetHistoryWindow()
	if err != nil {
		t.Fatal(err)
	}
	raw := request.Encode(&request.Request{
		Artifact: request.Artifact{Digest: "sha256:" + strings.Repeat("a", 64), Size: 1},
		Sources:  []request.Source{{Scheme: "https", Locator: "https://example.invalid/inert"}},
	})
	return api.Submission{Identity: api.RequestIdentity{Key: key, HistoryEpoch: history.HistoryEpoch},
		Kind: download.Kind, Spec: raw, RequiredGuarantees: []string{acceptanceprovider.GuaranteeReconciliation, string(download.CapRecoverableSubmission)}}
}

func configuredNASFixture(t *testing.T, options runtimeFlags) *runtimeNASExecutor {
	t.Helper()
	executor, err := configuredDownloadExecutor(options, nil)
	if err != nil {
		t.Fatal(err)
	}
	return executor.(*runtimeNASExecutor)
}

func TestRuntimeNASPreparationPinsBeforeAdmissionAndKeepsGuarantees(t *testing.T) {
	t.Setenv("ABSTRACTION_NAS_STORE", "")
	options := runtimeFlags{stateDir: privatetest.Dir(t), jobRoot: privatetest.Dir(t), downloadBackend: downloadBackendNAS}
	configPath := filepath.Join(privatetest.Dir(t), "config.json")
	nasRoot := filepath.Join(privatetest.Dir(t), "delegate")
	if err := config.Save(configPath, config.Config{NASStore: nasRoot}); err != nil {
		t.Fatal(err)
	}
	serveRuntimeNASConfig(t, &options, configPath)
	executor := configuredNASFixture(t, options)
	var reads atomic.Int32
	var admissions atomic.Int32
	executor.admit = func(selected string) error {
		if selected != nasRoot {
			t.Fatalf("admitted %q, selected %q", selected, nasRoot)
		}
		if executor.configured() != nil {
			t.Fatal("published before admission snapshot")
		}
		admissions.Add(1)
		return nil
	}
	read := executor.read
	executor.read = func(ctx context.Context) (string, error) { reads.Add(1); return read(ctx) }
	provider, err := acceptanceprovider.OpenWithExecutor(options.jobRoot, "runtime-download", executor)
	if err != nil {
		t.Fatal(err)
	}
	submission := runtimeNASSubmission(t, provider, "wait-for-config")
	got, err := provider.Bind("caller").Submit(submission)
	if err != nil || got.Outcome != api.AcceptanceOutcomeUnavailable || got.Receipt != nil {
		t.Fatalf("early admission: %+v, %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Join(options.jobRoot, "acceptance", "requests"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("unavailable admission journaled: %v, %v", entries, err)
	}
	if _, found, err := readDownloadProviderPin(options.jobRoot); err != nil || found {
		t.Fatalf("early pin: %v %v", found, err)
	}
	if _, err := os.Stat(nasRoot); !os.IsNotExist(err) {
		t.Fatalf("early delegate effect: %v", err)
	}

	// Capability queries run concurrently with publication of the ready executor.
	done := make(chan struct{})
	queries, stopQueries := context.WithCancel(context.Background())
	t.Cleanup(func() { stopQueries(); <-done })
	startedQueries := make(chan struct{})
	go func() {
		defer close(done)
		close(startedQueries)
		for {
			select {
			case <-queries.Done():
				return
			default:
			}
			if !slices.Contains(provider.SupportedGuarantees(), string(download.CapRecoverableSubmission)) {
				t.Error("NAS recovery guarantee changed during preparation")
				return
			}
		}
	}()
	<-startedQueries
	if _, err := executor.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopQueries()
	<-done
	pinBefore, err := os.ReadFile(filepath.Join(options.jobRoot, downloadProviderFile))
	if err != nil {
		t.Fatal(err)
	}
	got, err = provider.Bind("caller").Submit(submission)
	if err != nil || got.Outcome != api.AcceptanceOutcomeAccepted || got.Receipt == nil {
		t.Fatalf("same key after readiness: %+v, %v", got, err)
	}
	if err := config.Save(configPath, config.Config{NASStore: filepath.Join(privatetest.Dir(t), "replacement")}); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	pinAfter, err := os.ReadFile(filepath.Join(options.jobRoot, downloadProviderFile))
	if err != nil || !bytes.Equal(pinBefore, pinAfter) || reads.Load() != 1 || admissions.Load() != 1 {
		t.Fatalf("binding was redirected or reread: %q %v reads=%d", pinAfter, err, reads.Load())
	}
}

func TestRuntimeNASUnavailableRecoveryWaitsAndCancelsWithoutEffects(t *testing.T) {
	root, nasRoot := privatetest.Dir(t), privatetest.Dir(t)
	old, err := managedDownloadExecutor(root, nil, downloadBackendNAS, nasRoot)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := acceptanceprovider.OpenWithExecutor(root, "runtime-download", old)
	if err != nil {
		t.Fatal(err)
	}
	submission := runtimeNASSubmission(t, provider, "retained")
	accepted, err := provider.Bind("caller").Submit(submission)
	if err != nil || accepted.Receipt == nil {
		t.Fatal(accepted, err)
	}
	store, err := job.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(accepted.Receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, _ := json.Marshal(before)
	pinBefore, err := os.ReadFile(filepath.Join(root, downloadProviderFile))
	if err != nil {
		t.Fatal(err)
	}
	executor := configuredNASFixture(t, runtimeFlags{jobRoot: root, downloadBackend: downloadBackendNAS})
	started := make(chan struct{})
	executor.read = func(ctx context.Context) (string, error) { close(started); <-ctx.Done(); return "", ctx.Err() }
	reopened, err := acceptanceprovider.OpenWithExecutor(root, "runtime-download", executor)
	if err != nil {
		t.Fatal("recovery depended on live config", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.Serve(ctx, store) }()
	<-started
	duplicate, err := reopened.Bind("caller").Submit(submission)
	if err != nil || duplicate.Outcome != api.AcceptanceOutcomeAccepted || duplicate.Receipt == nil || duplicate.Receipt.OperationID != accepted.Receipt.OperationID {
		t.Fatalf("accepted work lost: %+v %v", duplicate, err)
	}
	if reason := executor.OperationWaiting(before); reason != "config:unavailable" {
		t.Fatal(reason)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("config wait outlived root context")
	}
	after, err := store.Load(accepted.Receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, _ := json.Marshal(after)
	pinAfter, err := os.ReadFile(filepath.Join(root, downloadProviderFile))
	if err != nil || !bytes.Equal(beforeBytes, afterBytes) || !bytes.Equal(pinBefore, pinAfter) {
		t.Fatal("unavailable recovery changed accepted work or binding", err)
	}
	remote, err := job.NewFileStore(nasRoot)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := remote.List()
	if err != nil || len(jobs) != 0 {
		t.Fatal("unavailable config delegated work", jobs, err)
	}
}

func TestRuntimeNASCancellationBeforePinHasNoEffect(t *testing.T) {
	options := runtimeFlags{jobRoot: privatetest.Dir(t), downloadBackend: downloadBackendNAS}
	executor := configuredNASFixture(t, options)
	nasRoot := filepath.Join(privatetest.Dir(t), "not-created")
	ctx, cancel := context.WithCancel(context.Background())
	executor.read = func(context.Context) (string, error) { cancel(); return nasRoot, nil }
	if err := executor.Serve(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := readDownloadProviderPin(options.jobRoot); err != nil || found {
		t.Fatal("cancelled preparation pinned", found, err)
	}
	if _, err := os.Stat(nasRoot); !os.IsNotExist(err) {
		t.Fatal("cancelled preparation created delegate root", err)
	}
}

func TestRuntimeNASFreshWitnessNeverRebindsExistingRoots(t *testing.T) {
	for _, state := range []string{"missing-pin", "conflicting-pin", "wrong-profile", "unowned-jobs"} {
		t.Run(state, func(t *testing.T) {
			root := privatetest.Dir(t)
			selected := filepath.Join(privatetest.Dir(t), "selected")
			var before []byte
			switch state {
			case "missing-pin":
				if _, err := acceptanceprovider.OpenWithExecutor(root, "runtime-download", downloadserve.DelegatedExecution{}); err != nil {
					t.Fatal(err)
				}
			case "conflicting-pin":
				old, err := managedDownloadExecutor(root, nil, downloadBackendNAS, privatetest.Dir(t))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = acceptanceprovider.OpenWithExecutor(root, "runtime-download", old); err != nil {
					t.Fatal(err)
				}
				before, err = os.ReadFile(filepath.Join(root, downloadProviderFile))
				if err != nil {
					t.Fatal(err)
				}
			case "wrong-profile":
				if _, err := acceptanceprovider.OpenWithExecutor(root, "runtime-download", downloadserve.HTTPExecution{}); err != nil {
					t.Fatal(err)
				}
			case "unowned-jobs":
				if err := os.Mkdir(filepath.Join(root, "jobs"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "jobs", "legacy.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			value, err := configuredDownloadExecutor(runtimeFlags{jobRoot: root, downloadBackend: downloadBackendNAS}, nil)
			if state == "wrong-profile" || state == "unowned-jobs" {
				if err == nil {
					t.Fatal("incompatible storage admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			executor := value.(*runtimeNASExecutor)
			if executor.initialUnbound {
				t.Fatal("existing root received fresh witness")
			}
			executor.read = func(context.Context) (string, error) { return selected, nil }
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
			defer cancel()
			if _, err := executor.initialize(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if executor.configured() != nil {
				t.Fatal("conflicting root became ready")
			}
			after, found, err := readDownloadProviderPin(root)
			if err != nil {
				t.Fatal(err)
			}
			if state == "missing-pin" && found {
				t.Fatal("existing unbound root was pinned")
			}
			if state == "conflicting-pin" {
				raw, err := os.ReadFile(filepath.Join(root, downloadProviderFile))
				if err != nil || !bytes.Equal(before, raw) || after.Root == selected {
					t.Fatal("persisted binding changed", after, err)
				}
			}
		})
	}
}

func TestRuntimeNASMissingSettingsNeverAdmitsOrPins(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			options := runtimeFlags{jobRoot: privatetest.Dir(t), downloadBackend: downloadBackendNAS}
			executor := configuredNASFixture(t, options)
			executor.read = func(context.Context) (string, error) {
				if missing {
					return "", errors.New("config service unavailable")
				}
				return "", nil
			}
			provider, err := acceptanceprovider.OpenWithExecutor(options.jobRoot, "runtime-download", executor)
			if err != nil {
				t.Fatal(err)
			}
			submission := runtimeNASSubmission(t, provider, "same-unavailable-key")
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
			defer cancel()
			if _, err := executor.initialize(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			got, err := provider.Bind("caller").Submit(submission)
			if err != nil || got.Outcome != api.AcceptanceOutcomeUnavailable || got.Receipt != nil {
				t.Fatalf("missing configuration accepted: %+v %v", got, err)
			}
			if _, found, err := readDownloadProviderPin(options.jobRoot); err != nil || found {
				t.Fatal("missing setting pinned provider", found, err)
			}
			entries, err := os.ReadDir(filepath.Join(options.jobRoot, "acceptance", "requests"))
			if err != nil || len(entries) != 0 {
				t.Fatal("missing setting journaled work", entries, err)
			}
			store, err := job.NewFileStore(options.jobRoot)
			if err != nil {
				t.Fatal(err)
			}
			records, err := store.List()
			if err != nil || len(records) != 0 {
				t.Fatal("missing setting created work", records, err)
			}
		})
	}
}

func TestRuntimeNASServeCanPrecedeConfigService(t *testing.T) {
	t.Setenv("ABSTRACTION_NAS_STORE", "")
	options := runtimeFlags{jobRoot: privatetest.Dir(t), downloadBackend: downloadBackendNAS,
		configEndpoint: listen.Endpoint(fmt.Sprintf("nas-later-%d-%d", os.Getpid(), runtimeNASConfigSerial.Add(1)))}
	configPath := filepath.Join(privatetest.Dir(t), "config.json")
	nasRoot := filepath.Join(privatetest.Dir(t), "delegate")
	if err := config.Save(configPath, config.Config{NASStore: nasRoot}); err != nil {
		t.Fatal(err)
	}
	executor := configuredNASFixture(t, options)
	provider, err := acceptanceprovider.OpenWithExecutor(options.jobRoot, "runtime-download", executor)
	if err != nil {
		t.Fatal(err)
	}
	submission := runtimeNASSubmission(t, provider, "not-yet-serving")
	store, err := job.NewFileStore(options.jobRoot)
	if err != nil {
		t.Fatal(err)
	}
	attempted := make(chan struct{})
	var once sync.Once
	read := executor.read
	executor.read = func(ctx context.Context) (string, error) {
		selected, err := read(ctx)
		once.Do(func() { close(attempted) })
		return selected, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() { defer close(done); serveErr = executor.Serve(ctx, store) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			if serveErr != nil {
				t.Error(serveErr)
			}
		case <-time.After(time.Second):
			t.Error("NAS executor did not drain")
		}
	})
	select {
	case <-attempted:
	case <-time.After(3 * time.Second):
		t.Fatal("config attempt did not finish")
	}
	got, err := provider.Bind("caller").Submit(submission)
	if err != nil || got.Outcome != api.AcceptanceOutcomeUnavailable {
		t.Fatal("config-not-serving admission", got, err)
	}
	if _, found, err := readDownloadProviderPin(options.jobRoot); err != nil || found {
		t.Fatal("early provider pin", found, err)
	}
	serveRuntimeNASConfig(t, &options, configPath)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for executor.configured() == nil {
		select {
		case <-deadline.C:
			t.Fatal("config service was starved by recovery")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
		if serveErr != nil {
			t.Fatal(serveErr)
		}
	case <-time.After(time.Second):
		t.Fatal("NAS executor cancellation did not join")
	}
}

func TestRuntimeNASFirstWitnessEndsWithItsEstablishedBinding(t *testing.T) {
	options := runtimeFlags{jobRoot: privatetest.Dir(t), downloadBackend: downloadBackendNAS}
	executor := configuredNASFixture(t, options)
	first, second := privatetest.Dir(t), filepath.Join(privatetest.Dir(t), "different")
	executor.read = func(context.Context) (string, error) { return first, nil }
	provider, err := acceptanceprovider.OpenWithExecutor(options.jobRoot, "runtime-download", executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	submission := runtimeNASSubmission(t, provider, "retained-first-binding")
	accepted, err := provider.Bind("caller").Submit(submission)
	if err != nil || accepted.Receipt == nil {
		t.Fatal(accepted, err)
	}
	store, err := job.NewFileStore(options.jobRoot)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Load(accepted.Receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, _ := json.Marshal(before)
	// End the old lifetime without running accepted work. Even the cancelled
	// entry path must retire in-memory readiness.
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if err := executor.Serve(ended, store); err != nil {
		t.Fatal(err)
	}
	if executor.configured() != nil {
		t.Fatal("ended lifetime kept readiness")
	}
	if err := os.Remove(filepath.Join(options.jobRoot, downloadProviderFile)); err != nil {
		t.Fatal(err)
	}
	executor.read = func(context.Context) (string, error) { return second, nil }
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer stop()
	if _, err := executor.initialize(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("owned root reinitialized after its pin was removed: %v", err)
	}
	if _, found, err := readDownloadProviderPin(options.jobRoot); err != nil || found {
		t.Fatal("owned root rebound with expired witness", found, err)
	}
	after, err := store.Load(accepted.Receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, _ := json.Marshal(after)
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("reinitialization changed accepted work")
	}
}
