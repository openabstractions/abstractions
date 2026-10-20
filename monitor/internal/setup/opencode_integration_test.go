package setup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestOpenCodeIntegrationAppliesOnlyPinnedSource(t *testing.T) {
	body := []byte("MSI fixture")
	pin := DesktopIntegration{File: "opencode-0.8.0.msi", SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Bytes: int64(len(body)), ProductCode: "{01234567-89AB-CDEF-0123-456789ABCDEF}"}
	path := filepath.Join(t.TempDir(), pin.File)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	run := func(_ context.Context, file, action string) error {
		calls++
		if file != path || action != "repair" {
			t.Fatalf("installer inputs: %q %q", file, action)
		}
		return nil
	}
	if err := pin.applyPackage(context.Background(), path, "repair", run); err != nil || calls != 1 {
		t.Fatalf("exact pin: %d calls (%v)", calls, err)
	}
	body[0] = 'N'
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pin.applyPackage(context.Background(), path, "repair", run); err == nil || calls != 1 {
		t.Fatalf("changed bytes: %d calls (%v)", calls, err)
	}
}

type desktopDownloadFixture struct {
	fakeTransfer
	reserved, submitted PinnedDownload
	observed, copied    []WorkIdentity
	states              []string
}

func (f *desktopDownloadFixture) Reserve(ctx context.Context, pin PinnedDownload, key string) (WorkIdentity, error) {
	f.reserved = pin
	return f.fakeTransfer.Reserve(ctx, pin, key)
}

func (f *desktopDownloadFixture) Submit(ctx context.Context, pin PinnedDownload, work WorkIdentity) error {
	f.submitted = pin
	return f.fakeTransfer.Submit(ctx, pin, work)
}

func (f *desktopDownloadFixture) Observe(_ context.Context, work WorkIdentity) (TransferObservation, error) {
	f.observed = append(f.observed, work)
	if len(f.states) == 0 {
		return TransferObservation{}, errors.New("unexpected observation")
	}
	state := f.states[0]
	f.states = f.states[1:]
	return TransferObservation{State: state}, nil
}

func (f *desktopDownloadFixture) Copy(ctx context.Context, work WorkIdentity, dst io.Writer) (int64, error) {
	f.copied = append(f.copied, work)
	return f.fakeTransfer.Copy(ctx, work, dst)
}

func TestOpenCodeIntegrationDownloadsPinnedSource(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	old := LocalPackageFolder
	LocalPackageFolder = ""
	t.Cleanup(func() { LocalPackageFolder = old })
	body := []byte("MSI fixture")
	pin := DesktopIntegration{App: OpenCode, Title: "OpenCode", File: "opencode-0.8.0.msi", URL: releaseAssetURL("v0.8.0", "opencode-0.8.0.msi"), SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Bytes: int64(len(body))}
	for _, tt := range []struct {
		name          string
		data          []byte
		states        []string
		attempt       int64
		submits, runs int
	}{
		{"new", body, []string{"unknown", "complete"}, 0, 1, 1},
		{"completed", body, []string{"complete"}, 0, 0, 1},
		{"retry", body, []string{"failed", "definitely_not_accepted", "unknown", "complete"}, 2, 1, 1},
		{"fails after submit", body, []string{"unknown", "failed"}, 0, 1, 0},
		{"changed digest", []byte("MSI changed"), []string{"complete"}, 0, 0, 0},
		{"oversize", append(slices.Clone(body), 0), []string{"complete"}, 0, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transfer := &desktopDownloadFixture{fakeTransfer: fakeTransfer{bytes: tt.data}, states: tt.states}
			runs := 0
			var source string
			run := func(_ context.Context, path, action string) error {
				runs++
				source = path
				if rel, err := filepath.Rel(tmp, path); err != nil || !filepath.IsLocal(rel) {
					t.Fatalf("staged MSI lies outside the fixture temp root: %v", err)
				}
				got, err := os.ReadFile(path)
				if err != nil || !slices.Equal(got, body) || filepath.Base(path) != pin.File || action != "repair" {
					t.Errorf("MSI source %q (%v), path %q, action %q", got, err, path, action)
				}
				return nil
			}
			err := pin.applySource(context.Background(), "repair", transfer, run)
			if runs != tt.runs || (err == nil) != (tt.runs == 1) {
				t.Fatalf("apply returned %v after %d installer calls", err, runs)
			}
			if transfer.reserve != 1 || transfer.reserved != pin.downloadPin() || transfer.submit != tt.submits ||
				(tt.submits != 0 && transfer.submitted != pin.downloadPin()) || len(transfer.states) != 0 {
				t.Fatalf("transfer calls: %+v", transfer)
			}
			if len(transfer.copied) > 0 {
				work := transfer.last
				work.Attempt = tt.attempt
				if len(transfer.copied) != 1 || !reflect.DeepEqual(transfer.copied[0], work) {
					t.Fatalf("copy used a different request: %+v", transfer.copied)
				}
			}
			if source != "" {
				if _, err := os.Stat(filepath.Dir(source)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("staging folder was left behind: %v", err)
				}
			}
		})
	}
	t.Run("local preview", func(t *testing.T) {
		LocalPackageFolder = t.TempDir()
		path := filepath.Join(LocalPackageFolder, pin.File)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		run := func(_ context.Context, got, _ string) error {
			if got != path {
				t.Fatalf("preview source is %q, expected %q", got, path)
			}
			return nil
		}
		if err := pin.applySource(context.Background(), "repair", nil, run); err != nil {
			t.Fatal(err)
		}
	})
}
