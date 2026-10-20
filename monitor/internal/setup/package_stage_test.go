package setup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

func TestExampleAppPackageStagePublishesOnlyTheVerifiedBytes(t *testing.T) {
	saved := LocalPackageFolder
	t.Cleanup(func() { LocalPackageFolder = saved })
	LocalPackageFolder = t.TempDir()
	const file, prior, candidate = "example.msi", "earlier package", "reviewed package"
	base := t.TempDir()
	current := filepath.Join(base, file)
	target := packageTarget{catalogID: "example-app", registration: "example-runtime", connector: filepath.Join(base, "connector.exe")}
	spec := product.PackageSpec{ID: "example-app", Title: "Example App", Registration: "example-runtime", Connector: "connector.exe"}
	if err := target.match(spec); err != nil {
		t.Fatal(err)
	}
	spec.Registration = "other-runtime"
	if err := target.match(spec); err == nil {
		t.Fatal("a different registration matched the package target")
	}
	writeComfyFixture(t, current, prior)
	source := filepath.Join(LocalPackageFolder, file)
	writeComfyFixture(t, source, "foreign package!")
	pin := PinnedDownload{URL: releaseAssetURL("v0.8.0", file), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(candidate))), Bytes: int64(len(candidate))}
	ctx := context.Background()
	calls := 0
	install := func(_ context.Context, staged string, _ func(context.Context) error) error {
		calls++
		raw, err := os.ReadFile(staged)
		if err != nil || string(raw) != candidate {
			t.Fatalf("installer read %q, %v", raw, err)
		}
		return nil
	}
	if _, err := runStagedPackage(ctx, pin, current, install); err == nil || calls != 0 {
		t.Fatalf("unverified package reached install, calls=%d, error=%v", calls, err)
	}
	writeComfyFixture(t, source, candidate)
	publish, err := runStagedPackage(ctx, pin, current, install)
	if err != nil || publish == nil || calls != 1 {
		t.Fatalf("reviewed stage, calls=%d, error=%v", calls, err)
	}
	if raw, err := os.ReadFile(current); err != nil || string(raw) != prior {
		t.Fatalf("prior package before publication: %q, %v", raw, err)
	}
	if err := publish(ctx); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(current); err != nil || string(raw) != candidate {
		t.Fatalf("published package: %q, %v", raw, err)
	}
}

func TestPublishedConnectorPackageUsesTheStagedCatalogTransfer(t *testing.T) {
	saved := LocalPackageFolder
	t.Cleanup(func() { LocalPackageFolder = saved })
	LocalPackageFolder = ""
	body := []byte("exact downloaded connector package")
	current := filepath.Join(t.TempDir(), "example.msi")
	pin := PinnedDownload{URL: releaseAssetURL("v0.8.0", "example.msi"), SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Bytes: int64(len(body)), Label: "Download the Example integration"}
	bad := &desktopDownloadFixture{fakeTransfer: fakeTransfer{bytes: make([]byte, len(body))}, states: []string{"complete"}}
	if err := acquireConnectorPackage(t.Context(), pin, current, bad); err == nil {
		t.Fatal("published bytes with the wrong digest reached staging")
	}
	transfer := &desktopDownloadFixture{fakeTransfer: fakeTransfer{bytes: body}, states: []string{"unknown", "complete"}}
	if err := acquireConnectorPackage(t.Context(), pin, current, transfer); err != nil {
		t.Fatal(err)
	}
	if transfer.submitted != pin || len(transfer.copied) != 1 {
		t.Fatalf("wrong public Catalog transfer: %+v", transfer)
	}
	publish, err := runStagedPackage(t.Context(), pin, current, func(_ context.Context, stage string, _ func(context.Context) error) error {
		got, err := os.ReadFile(stage)
		if err != nil || string(got) != string(body) {
			t.Fatalf("staged installer received %q (%v)", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(current); err != nil || string(got) != string(body) {
		t.Fatalf("published %q (%v)", got, err)
	}
}

func TestExampleAppPackageCopyFollowsTheDeclaredInstallFolder(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	spec := product.PackageSpec{ID: "example-app", InstallFolder: "example-integration"}
	files := extensionPackageFiles{compatibility: spec}
	current, err := files.current()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(current) != "example-integration.msi" {
		t.Fatalf("unexpected saved package path: %s", current)
	}
	const content = "Example App fixture package"
	writeComfyFixture(t, current, content)
	ctx := context.Background()
	previous, digest, err := files.keepPrevious(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(previous) != filepath.Dir(current) ||
		filepath.Base(previous) != "example-integration-previous-"+digest+".msi" ||
		digest != fmt.Sprintf("%x", sha256.Sum256([]byte(content))) {
		t.Fatalf("unexpected retained package: %s (%s)", previous, digest)
	}
	const candidate = "later acknowledged package"
	writeComfyFixture(t, current, candidate)
	if release, publish, err := files.holdPredecessor(ctx, previous, fmt.Sprintf("%x", sha256.Sum256([]byte(candidate)))); err == nil || release != nil || publish != nil {
		t.Fatalf("wrong predecessor digest got a hold or publication: %v", err)
	}
	release, publish, err := files.holdPredecessor(ctx, previous, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	if raw, err := os.ReadFile(current); err != nil || string(raw) != candidate {
		t.Fatalf("hold changed saved-current before acknowledgement: %q, %v", raw, err)
	}
	if err := publish(ctx); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(current); err != nil || string(raw) != content {
		t.Fatalf("publication did not use the held bytes: %q, %v", raw, err)
	}
}

func TestExampleAppPackageLifecycleCommitsAfterAcknowledgedReplacement(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	saved := LocalPackageFolder
	t.Cleanup(func() { LocalPackageFolder = saved })
	LocalPackageFolder = t.TempDir()
	const earlier, candidate, oldImage, newImage = "earlier MSI", "reviewed MSI", "original image", "reviewed image"
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	program, previous, current := filepath.Join(base, "example.exe"), filepath.Join(base, "previous.msi"), filepath.Join(base, "example.msi")
	writeComfyFixture(t, program, oldImage)
	writeComfyFixture(t, previous, earlier)
	writeComfyFixture(t, current, earlier)
	writeComfyFixture(t, filepath.Join(LocalPackageFolder, "example.msi"), candidate)
	pin := PinnedDownload{URL: releaseAssetURL("v0.8.0", "example.msi"), SHA256: digest(candidate), Bytes: int64(len(candidate))}
	events := []string{}
	revision := "old"
	receiptFile := filepath.Join(base, "receipt.json")
	readReceipt := func() PackageReplacement {
		t.Helper()
		raw, err := os.ReadFile(receiptFile)
		if err != nil {
			t.Fatal(err)
		}
		var receipt PackageReplacement
		if err := json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	err := runKeptPackageTransaction(ctx, KeptPackageTransaction{
		Program: program, PackageSHA256: pin.SHA256, ConnectorSHA256: digest(newImage), ConnectorBytes: int64(len(newImage)),
		Timeout: time.Minute,
		Image: func() (string, int64, error) {
			raw, err := os.ReadFile(program)
			return digest(string(raw)), int64(len(raw)), err
		},
		Keep:  func(context.Context) (string, string, error) { return previous, digest(earlier), nil },
		Ready: func() error { return nil },
		Install: func(ctx context.Context, mark func() error) (func(context.Context) error, error) {
			return runStagedPackage(ctx, pin, current, func(_ context.Context, staged string, _ func(context.Context) error) error {
				if err := mark(); err != nil {
					return err
				}
				if proof := readReceipt(); proof.InstallComplete || proof.BeforeDispatch {
					t.Fatalf("dispatch proof: %+v", proof)
				}
				if raw, err := os.ReadFile(staged); err != nil || string(raw) != candidate {
					t.Fatalf("staged package: %q %v", raw, err)
				}
				events = append(events, "install")
				return os.WriteFile(program, []byte(newImage), 0600)
			})
		},
		Restore: func(context.Context, string) error { t.Fatal("successful update restored the prior"); return nil },
		Hold: func(ctx context.Context) (HeldProgram, error) {
			return holdPinnedProgram(ctx, sevenZipPin{Path: program, SHA256: digest(newImage), Bytes: int64(len(newImage))})
		},
		Observe: func(context.Context) (string, func(context.Context, string) (fwire.ApplicationChange, string, error), error) {
			observed := revision
			return observed, func(_ context.Context, path string) (fwire.ApplicationChange, string, error) {
				if path != program || observed != revision || !readReceipt().InstallComplete {
					t.Fatal("replacement lacked exact prior Registry or MSI proof")
				}
				events = append(events, "replace")
				revision = "new"
				return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, revision, nil
			}, nil
		},
		Expect: func(context.Context) { events = append(events, "expect") },
		Save: func(ctx context.Context, receipt *PackageReplacement) error {
			raw, err := json.Marshal(receipt)
			if err != nil {
				return err
			}
			return writePinnedPackage(ctx, receiptFile, raw, 4096)
		},
		MarkChanged: func(context.Context) { t.Fatal("verified target was marked changed") },
		Commit: func(_ context.Context, receipt PackageReplacement) error {
			if raw, err := os.ReadFile(current); err != nil || string(raw) != candidate || readReceipt() != receipt || receipt.After != "new" {
				t.Fatalf("publication or final proof: %q %v %+v", raw, err, receipt)
			}
			events = append(events, "commit")
			return nil
		},
	})
	if err != nil || !reflect.DeepEqual(events, []string{"install", "expect", "replace", "commit"}) {
		t.Fatalf("lifecycle events %v, error %v", events, err)
	}
}
