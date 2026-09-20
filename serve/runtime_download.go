package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	download "github.com/openabstractions/abstraction-download/go"
	nas "github.com/openabstractions/abstraction-download/go/nas"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
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

// managedDownloadExecutor pins the selected provider beside the acceptance
// root. The acceptance profile remains the authority for existing records;
// this file records the operator's provider choice and its binding.
func managedDownloadExecutor(root string, onError func(error), backend, nasRoot string) (acceptanceprovider.Executor, error) {
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
		return downloadserve.LegacySinkExecution{HTTPExecution: downloadserve.HTTPExecution{OnError: onError}}, nil
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
		if !found {
			return nil, errors.New("runtime: existing delegated download root has no provider binding")
		}
	}
	if err := pinDownloadProvider(root, downloadProviderConfig{Backend: backend, Root: nasRoot}); err != nil {
		return nil, err
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
			return "", "", errors.New("runtime: NAS download backend requires an absolute --jobs-download-nas-root")
		}
		nasRoot = filepath.Clean(nasRoot)
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
	execution := downloadserve.HTTPExecution{OnError: onError}
	if backend == downloadBackendNative {
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
