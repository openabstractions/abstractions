package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	download "github.com/openabstractions/abstraction-download/go"
	archive "github.com/openabstractions/abstraction-download/go/archive"
	nas "github.com/openabstractions/abstraction-download/go/nas"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	job "github.com/openabstractions/abstraction-job/go"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

const (
	downloadBackendNative = "native"
	downloadBackendNAS    = "nas"
	downloadProviderFile  = "download-provider.json"
)

type downloadProviderConfig struct {
	Backend string `json:"backend"`
	Root    string `json:"root,omitempty"`
}

// runtimeNASStore reads the runtime's own config service. The flag is a run
// override for nas_store; no configuration file is read by this consumer.
func runtimeNASStore(ctx context.Context, options runtimeFlags) (string, error) {
	reader, err := runtimeConfigClient(options)
	if err != nil {
		return "", err
	}
	settings, err := reader.ReadTypedContext(ctx, []string{"nas_store"})
	if err != nil {
		return "", fmt.Errorf("runtime: read NAS store setting: %w", err)
	}
	if settings.Outcome != configwire.ReadOutcomeRead || len(settings.Refused) != 0 || len(settings.Entries) != 1 {
		return "", fmt.Errorf("runtime: NAS store setting unavailable: %s", settings.Outcome)
	}
	entries := settings.Entries
	if options.downloadNASRoot != "" {
		entries, err = configwire.OverlayConfigProcess(entries, []configwire.Change{{Key: "nas_store", Set: &configwire.Value{Kind: configwire.ValueKindText, Text: options.downloadNASRoot}}})
		if err != nil {
			return "", err
		}
	}
	if entries[0].Value == nil {
		return "", nil
	}
	return entries[0].Value.Text, nil
}

// configuredDownloadExecutor keeps native startup eager. NAS preparation waits
// in the existing executor lifetime until its co-hosted config service serves.
func configuredDownloadExecutor(options runtimeFlags, report func(error)) (acceptanceprovider.Executor, error) {
	if options.downloadBackend != downloadBackendNAS {
		return managedDownloadExecutor(options.jobRoot, report, options.downloadBackend, options.downloadNASRoot)
	}
	_, owned, err := acceptanceprovider.RecordedExecutionProfile(options.jobRoot)
	if err != nil {
		return nil, err
	}
	_, pinned, err := readDownloadProviderPin(options.jobRoot)
	if err != nil {
		return nil, err
	}
	e := &runtimeNASExecutor{
		DelegatedExecution: downloadserve.DelegatedExecution{
			HTTPExecution: downloadserve.HTTPExecution{OnError: report},
			// Only immutable capability inspection uses this empty adapter. Execution
			// uses the separately initialized, pinned adapter below.
			Delegators: []download.Delegator{new(nas.Delegator)},
		},
		options: options, initialUnbound: !owned && !pinned, initializeGate: make(chan struct{}, 1),
	}
	e.read = func(ctx context.Context) (string, error) { return runtimeNASStore(ctx, options) }
	if err := acceptanceprovider.CheckManaged(options.jobRoot, e); err != nil {
		return nil, err
	}
	return e, nil
}

type runtimeNASExecutor struct {
	downloadserve.DelegatedExecution
	options        runtimeFlags
	initialUnbound bool
	initializeGate chan struct{}
	read           func(context.Context) (string, error)
	admit          func(string) error
	mu             sync.RWMutex
	ready          *downloadserve.DelegatedExecution
	waiting        error
}

func (e *runtimeNASExecutor) configured() *downloadserve.DelegatedExecution {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ready
}

func (e *runtimeNASExecutor) CheckAdmission(scope, kind string, raw []byte, required []string) (jobapi.AcceptanceOutcome, string) {
	if ready := e.configured(); ready != nil {
		return ready.CheckAdmission(scope, kind, raw, required)
	}
	return jobapi.AcceptanceOutcomeUnavailable, "config:unavailable"
}

func (e *runtimeNASExecutor) CheckSubjectAdmission(binding acceptanceprovider.Binding, kind string, raw []byte, required []string) (jobapi.AcceptanceOutcome, string) {
	return e.CheckAdmission(binding.Scope, kind, raw, required)
}

func (e *runtimeNASExecutor) OperationWaiting(record *job.Record) string {
	if e.configured() == nil {
		return "config:unavailable"
	}
	return e.DelegatedExecution.OperationWaiting(record)
}

func (e *runtimeNASExecutor) Serve(ctx context.Context, store job.Store) error {
	return e.ServeWithSubjectLookup(ctx, store, nil)
}

func (e *runtimeNASExecutor) ServeWithSubjectLookup(ctx context.Context, store job.Store, lookup acceptanceprovider.SubjectLookup) error {
	defer func() { e.mu.Lock(); e.ready = nil; e.mu.Unlock() }()
	ready, err := e.initialize(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return err
	}
	return ready.ServeWithSubjectLookup(ctx, store, lookup)
}

func (e *runtimeNASExecutor) initialize(ctx context.Context) (*downloadserve.DelegatedExecution, error) {
	// One initialization owns the first-binding witness. A concurrent caller
	// remains cancellable while that lifetime waits for configuration.
	select {
	case e.initializeGate <- struct{}{}:
		defer func() { <-e.initializeGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if ready := e.configured(); ready != nil {
		return ready, nil
	}
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		selected, err := e.read(ctx)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			var execution acceptanceprovider.Executor
			execution, err = prepareManagedDownloadExecutor(e.options.jobRoot, e.OnError, downloadBackendNAS, selected, &e.initialUnbound)
			if err == nil {
				if e.admit != nil {
					if err := e.admit(selected); err != nil {
						return nil, err
					}
				}
				ready := execution.(downloadserve.DelegatedExecution)
				e.mu.Lock()
				e.ready, e.waiting = &ready, nil
				e.mu.Unlock()
				return &ready, nil
			}
		}
		e.mu.Lock()
		changed := e.waiting == nil || e.waiting.Error() != err.Error()
		e.waiting = err
		e.mu.Unlock()
		if changed && e.OnError != nil {
			e.OnError(err)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// managedDownloadExecutor pins the selected provider beside the acceptance
// root. The acceptance profile remains the authority for existing records;
// this file records the operator's provider choice and its binding.
func managedDownloadExecutor(root string, onError func(error), backend, nasRoot string) (acceptanceprovider.Executor, error) {
	return prepareManagedDownloadExecutor(root, onError, backend, nasRoot, nil)
}

// initialUnbound records that this runtime observed no execution profile before
// its job provider created the new profile. Existing roots still require a pin.
func prepareManagedDownloadExecutor(root string, onError func(error), backend, nasRoot string, initialUnbound *bool) (acceptanceprovider.Executor, error) {
	if root == "" {
		return nil, errors.New("runtime: managed download root required")
	}
	backend, nasRoot, err := normalizeDownloadProvider(backend, nasRoot)
	if err != nil {
		return nil, err
	}
	profile, owned, err := acceptanceprovider.RecordedExecutionProfile(root)
	if err != nil {
		return nil, err
	}
	if owned && backend == downloadBackendNative && profile == downloadserve.LegacySinkProfile {
		// Legacy roots predate provider binding. Keep them usable without
		// mutating their metadata from this selection path.
		return downloadserve.LegacySinkExecution{HTTPExecution: downloadserve.HTTPExecution{OnError: onError, Archive: archive.Service{}}}, nil
	}
	expected := downloadserve.HTTPExecution{}.Profile()
	if backend == downloadBackendNAS {
		expected = downloadserve.DelegatedExecution{}.Profile()
	}
	if owned && profile != expected {
		return nil, fmt.Errorf("runtime: download provider %q is incompatible with recorded execution profile %q", backend, profile)
	}
	if owned && profile == (downloadserve.DelegatedExecution{}).Profile() {
		_, found, err := readDownloadProviderPin(root)
		if err != nil {
			return nil, err
		}
		if !found && (initialUnbound == nil || !*initialUnbound) {
			return nil, errors.New("runtime: existing delegated download root has no provider binding")
		}
	}
	if err := pinDownloadProvider(root, downloadProviderConfig{Backend: backend, Root: nasRoot}); err != nil {
		return nil, err
	}
	// Retire a fresh-root witness at the binding boundary, including a later
	// adapter-availability error. Removing the pin cannot renew this authority.
	if initialUnbound != nil {
		*initialUnbound = false
	}
	return newDownloadExecutor(backend, nasRoot, onError)
}

func normalizeDownloadProvider(backend, nasRoot string) (string, string, error) {
	if backend == "" {
		backend = downloadBackendNative
	}
	switch backend {
	case downloadBackendNative:
		if nasRoot != "" {
			return "", "", errors.New("runtime: --jobs-download-nas-root requires --jobs-download-backend=nas")
		}
		return backend, "", nil
	case downloadBackendNAS:
		if nasRoot == "" || !filepath.IsAbs(nasRoot) {
			return "", "", errors.New("runtime: NAS download backend requires an absolute nas_store setting or --jobs-download-nas-root override")
		}
		nasRoot = filepath.Clean(nasRoot)
		// The store is a job file store, whose records take the cas lock, and
		// the lock holds within one host (cas CAS-L2). A root on a network
		// share is refused before anything is created there or pinned.
		if err := cas.LocalPath(nasRoot); err != nil {
			return "", "", fmt.Errorf("runtime: NAS download root: %w", err)
		}
		// Establish the trusted directory before recording its identity. This
		// resolves symlinks in an existing parent as well as a final alias to a
		// newly created directory, so restart cannot canonicalize it differently.
		if err := os.MkdirAll(nasRoot, 0o700); err != nil {
			return "", "", fmt.Errorf("runtime: create NAS download root: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(nasRoot)
		if err != nil {
			return "", "", fmt.Errorf("runtime: resolve NAS download root: %w", err)
		}
		return backend, filepath.Clean(resolved), nil
	default:
		return "", "", fmt.Errorf("runtime: unsupported download backend %q", backend)
	}
}

func pinDownloadProvider(root string, want downloadProviderConfig) error {
	store := casapi.BoundedFileStore{MaxBytes: 4096}
	path := filepath.Join(root, downloadProviderFile)
	got, found, err := readDownloadProviderPin(root)
	if err != nil {
		return err
	}
	if found {
		if got != want {
			return fmt.Errorf("runtime: download provider binding changed from %s to %s", formatDownloadProvider(got), formatDownloadProvider(want))
		}
		return nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		return err
	}
	if err := store.WriteContext(context.Background(), path, casapi.Value{}, append(encoded, '\n')); err != nil {
		if errors.Is(err, cas.ErrMoved) {
			got, found, readErr := readDownloadProviderPin(root)
			if readErr != nil {
				return readErr
			}
			if found && got == want {
				return nil
			}
			if found {
				return fmt.Errorf("runtime: download provider binding changed from %s to %s", formatDownloadProvider(got), formatDownloadProvider(want))
			}
		}
		return err
	}
	return nil
}

func readDownloadProviderPin(root string) (downloadProviderConfig, bool, error) {
	data, err := (casapi.BoundedFileStore{MaxBytes: 4096}).Read(filepath.Join(root, downloadProviderFile))
	if err != nil {
		return downloadProviderConfig{}, false, err
	}
	if data.Data == nil {
		return downloadProviderConfig{}, false, nil
	}
	var got downloadProviderConfig
	if err := json.Unmarshal(data.Data, &got); err != nil {
		return downloadProviderConfig{}, false, fmt.Errorf("runtime: unreadable download provider binding: %w", err)
	}
	return got, true, nil
}

func formatDownloadProvider(c downloadProviderConfig) string {
	if c.Root == "" {
		return c.Backend
	}
	return c.Backend + "@" + c.Root
}

func newDownloadExecutor(backend, nasRoot string, onError func(error)) (acceptanceprovider.Executor, error) {
	execution := downloadserve.HTTPExecution{OnError: onError, Archive: archive.Service{}}
	if backend == downloadBackendNative {
		execution.CheckWriteSpace = checkLocalDownloadSpace
		return execution, nil
	}
	delegate, err := nas.New(nasRoot)
	if err != nil {
		return nil, err
	}
	if err := delegate.Available(); err != nil {
		return nil, err
	}
	return downloadserve.DelegatedExecution{HTTPExecution: execution, Delegators: []download.Delegator{delegate}}, nil
}
