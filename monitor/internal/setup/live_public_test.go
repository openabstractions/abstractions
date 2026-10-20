package setup

import (
	"context"
	"flag"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
)

var liveEndpoint = flag.String("oa-setup-live-endpoint", "", "isolated OA runtime endpoint for package qualification")
var liveRoot = flag.String("oa-setup-live-root", "", "disposable absolute root for package qualification")

// This qualification uses a separately started isolated OA runtime with
// --jobs-run-downloads. It downloads one real pinned release through the public
// job API, copies completed bytes, and installs into a disposable user cache.
func TestLiveLemonadePublicDownloadInstall(t *testing.T) {
	endpoint := *liveEndpoint
	root := *liveRoot
	if endpoint == "" || root == "" {
		t.Skip("pass -oa-setup-live-endpoint and -oa-setup-live-root for isolated public-job qualification")
	}
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("reviewed Lemonade package is Windows amd64 only")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("live root must be absolute")
	}
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "Local"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	runner := NewRunner(Journal{Path: filepath.Join(root, "plan.json")}, PublicTransfer{Machine: client.New(endpoint)})
	// This test qualifies the external package transfer and archive handler.
	// The separate core MSI feature is inspected in installed-package tests.
	runner.featureCheck = func() (FeatureStatus, error) { return FeatureStatus{Available: true, Installed: true}, nil }
	status, err := runner.Start(ctx, []AppID{Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Plan.Preparable {
		t.Fatal("reviewed Lemonade plan is unavailable")
	}
	for status.Components[0].Effect.Phase != Installed {
		status, err = runner.Advance(ctx, "lemonade-engine")
		if err != nil {
			t.Fatalf("public transfer/install: %v", err)
		}
		if status.Components[0].Effect.Phase == Requested {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	a := *status.Plan.Components[0].Artifact
	destination, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := ArchiveInstalled(a, destination)
	if err != nil || !installed || status.Ready {
		t.Fatalf("archive identity or configuration state: installed=%v ready=%v error=%v", installed, status.Ready, err)
	}
	t.Logf("public job installed pinned Lemonade archive at %s; model configuration remains pending", destination)
}
