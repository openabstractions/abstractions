package setup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// notFoundTransfer answers like the download service for a release asset the
// release has not published yet: GitHub's 404 is the not_found cause.
type notFoundTransfer struct{ fakeTransfer }

func (f *notFoundTransfer) Observe(context.Context, WorkIdentity) (TransferObservation, error) {
	f.observe++
	if f.observe == 1 {
		return TransferObservation{State: "unknown"}, nil
	}
	return TransferObservation{State: "failed", Failure: "permanent: download attempt failed: not found", Cause: "not_found"}, nil
}

// CATALOG-UNPUBLISHED: before v0.8.0 was public, the Catalog's OpenCode
// install read as a broken download. It now says the download is not
// published yet, and a pre-release check installs from the release folder.
func TestCatalogInstallOfAnUnpublishedReleaseSaysSo(t *testing.T) {
	old := LocalPackageFolder
	LocalPackageFolder = ""
	t.Cleanup(func() { LocalPackageFolder = old })
	t.Setenv(ReleaseFolderVariable, "")
	body := []byte("MSI fixture")
	pin := DesktopIntegration{App: OpenCode, Title: "OpenCode", File: "opencode-0.8.0.msi", URL: releaseAssetURL("v0.8.0", "opencode-0.8.0.msi"),
		SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Bytes: int64(len(body))}
	run := func(context.Context, string, string) error {
		t.Fatal("no installer runs without the package")
		return nil
	}

	t.Run("not published", func(t *testing.T) {
		transfer := &notFoundTransfer{}
		err := pin.applySource(context.Background(), "install", transfer, run)
		var failure *DesktopInstallerFailure
		if !errors.As(err, &failure) || failure.Reason != "OpenCode's download isn't published yet. Try again later." {
			t.Fatalf("failure: %#v (%v)", failure, err)
		}
		if !errors.Is(err, ErrNotPublished) || !strings.Contains(err.Error(), "not found") || transfer.submit != 1 {
			t.Fatalf("diagnostic %q after %d submits", err, transfer.submit)
		}
	})

	t.Run("another failure keeps its own text", func(t *testing.T) {
		transfer := &desktopDownloadFixture{states: []string{"unknown", "failed"}}
		err := pin.applySource(context.Background(), "install", transfer, run)
		var failure *DesktopInstallerFailure
		if err == nil || errors.Is(err, ErrNotPublished) || errors.As(err, &failure) {
			t.Fatalf("a failure without not_found: %v", err)
		}
	})

	t.Run("pre-release check installs from the release folder", func(t *testing.T) {
		folder := t.TempDir()
		path := filepath.Join(folder, pin.File)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(ReleaseFolderVariable, folder)
		runs := 0
		install := func(_ context.Context, got, action string) error {
			runs++
			if got != path || action != "install" {
				t.Fatalf("installer inputs %q %q", got, action)
			}
			return nil
		}
		if err := pin.applySource(context.Background(), "install", &notFoundTransfer{}, install); err != nil || runs != 1 {
			t.Fatalf("release folder: %d runs (%v)", runs, err)
		}
		// The folder's bytes still meet the build's pin.
		if err := os.WriteFile(path, []byte("MSI changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := pin.applySource(context.Background(), "install", &notFoundTransfer{}, install); err == nil || runs != 1 {
			t.Fatalf("changed release folder bytes: %d runs (%v)", runs, err)
		}
		t.Setenv(ReleaseFolderVariable, "relative")
		if got := localPackage(pin.downloadPin()); got != "" {
			t.Fatalf("a relative release folder was used: %q", got)
		}
	})
}
